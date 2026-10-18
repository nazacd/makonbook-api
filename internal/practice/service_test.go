package practice

import (
	"errors"
	"testing"

	"github.com/nazacd/makonbook-api/internal/platform/testdb"
)

func TestEditing(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	svc := NewService(pool, nil)

	day := review(t, "DAY8", func(subject string, module, n int, rec map[string]any) {
		switch {
		case subject == "english" && module == 1 && n == 3:
			rec["risk"] = "red"
		case subject == "math" && module == 1 && n == 1:
			rec["risk"] = "yellow"
			rec["stem"] = "The graph:\n\n![a line](r2:sat/g.png)\n\nWhat is the slope?"
			rec["figures"] = []map[string]string{{"key": "sat/g.png", "kind": "graph", "alt": "a line"}}
		case subject == "math" && module == 2 && n == 2:
			rec["type"], rec["choices"], rec["key"], rec["accepted"] = "grid_in", nil, "17/2", []string{"17/2", "8.5"}
		}
	})
	if _, err := (Importer{Pool: pool}).Import(ctx, load(t, day), false); err != nil {
		t.Fatal(err)
	}

	// Red first, then yellow; filters narrow the list.
	list, total, err := svc.Questions(ctx, Filter{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if total != 98 || len(list) != 3 || *list[0].AIRisk != "red" || *list[1].AIRisk != "yellow" || list[0].Places[0].Test != "DAY8" {
		t.Fatalf("list: %d %+v", total, list)
	}
	if p := preview(text(ptr("*Butterfly* by \\(b - 49 = \\frac{x}{y}\\) and **bold**")), ""); p != "Butterfly by b - 49 = x/y and bold" {
		t.Errorf("preview: %q", p)
	}
	if list[1].Preview != "The graph: [figure] What is the slope?" {
		t.Errorf("preview: %q", list[1].Preview)
	}
	if _, total, _ := svc.Questions(ctx, Filter{Risk: "red", Subject: "english", Limit: 10}); total != 1 {
		t.Errorf("red English: %d", total)
	}

	d, err := svc.Test(ctx, "DAY8")
	if err != nil {
		t.Fatal(err)
	}
	if d.Slots[0].Subject != "english" || d.Slots[0].Position.Position != 1 || len(d.Slots) != 98 || d.Questions != 98 || d.Red != 1 || d.Open {
		t.Errorf("test: %+v", d.TestSummary)
	}
	if _, err := svc.Test(ctx, "DAY99"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown test: %v", err)
	}

	// The figure question, as the editor opens it.
	q, err := svc.Question(ctx, list[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if q.Version != 1 || len(q.Reviews) != 1 || q.Reviews[0].QID != "math-m1-01" || len(q.Versions) != 1 || q.Figures[0].Key != "sat/g.png" {
		t.Fatalf("question: %+v", q)
	}
	edit := Edit{
		BaseVersion: 1, Stem: q.Stem, Choices: q.Choices, Figures: q.Figures, Correct: "C",
		Explanation: "Rise over run.", Domain: "Algebra", Skill: "Linear functions", Difficulty: "medium",
	}

	// Mistakes come back field by field.
	bad := edit
	bad.Stem = `Solve \(x + 1 = 2`
	bad.Choices = map[string]string{"A": "1", "B": "<b>2</b>", "C": "3", "D": ""}
	bad.Correct = "E"
	bad.Skill = "Circles"
	_, err = svc.Update(ctx, q.ID, "Editor", bad)
	var inv *InvalidError
	if !errors.As(err, &inv) {
		t.Fatalf("invalid edit: %v", err)
	}
	for _, f := range []string{"stem", "choices.B", "choices.D", "correct", "skill", "figures"} {
		if inv.Fields[f] == "" {
			t.Errorf("no error for %s: %v", f, inv.Fields)
		}
	}

	// A good edit is a new version, by its editor.
	q2, err := svc.Update(ctx, q.ID, "Editor", edit)
	if err != nil {
		t.Fatal(err)
	}
	if q2.Version != 2 || *q2.Correct != "C" || q2.Explanation != "Rise over run." || len(q2.Versions) != 2 ||
		*q2.Versions[0].CreatedBy != "Editor" || q2.Versions[0].Source != "editor" {
		t.Errorf("after edit: %+v", q2)
	}
	// Saving over someone else's newer version is refused.
	if _, err := svc.Update(ctx, q.ID, "Other", edit); !errors.Is(err, ErrConflict) {
		t.Errorf("stale edit: %v", err)
	}

	// Changing an approved question sends it back to review.
	if _, err := pool.Exec(ctx, `UPDATE questions SET status = 'approved', approved_at = now() WHERE id = $1`, q.ID); err != nil {
		t.Fatal(err)
	}
	edit.BaseVersion = 2
	edit.Explanation = "The slope is rise over run."
	q3, err := svc.Update(ctx, q.ID, "Editor", edit)
	if err != nil || q3.Status != "review" || q3.ApprovedAt != nil {
		t.Errorf("approved then edited: %+v %v", q3, err)
	}

	// Grid-in answers must be numbers.
	gl, _, _ := svc.Questions(ctx, Filter{Subject: "math", Limit: 98})
	for _, s := range gl {
		if s.Type != "grid_in" {
			continue
		}
		g, err := svc.Question(ctx, s.ID)
		if err != nil {
			t.Fatal(err)
		}
		ge := Edit{BaseVersion: 1, Stem: g.Stem, Accepted: []string{"17/2", "eight"}, Explanation: "x",
			Domain: "Algebra", Skill: "Linear functions"}
		if _, err := svc.Update(ctx, g.ID, "Editor", ge); !errors.As(err, &inv) || inv.Fields["accepted"] == "" {
			t.Errorf("grid-in with a word: %v", err)
		}
		ge.Accepted = []string{" 17/2 ", "8.5", ""}
		if !g.AutoApproved || g.Status != "approved" {
			t.Errorf("a green question isn't approved automatically: %+v", g)
		}
		// A teacher checks a change, even to a question the AI approved.
		if g2, err := svc.Update(ctx, g.ID, "Editor", ge); err != nil || len(g2.Accepted) != 2 || g2.Choices != nil ||
			g2.Status != "review" || g2.AutoApproved || g2.ApprovedAt != nil {
			t.Errorf("grid-in edit: %+v %v", g2, err)
		}
	}
	if _, err := svc.Question(ctx, "not-a-uuid"); !errors.Is(err, ErrNotFound) {
		t.Errorf("bad id: %v", err)
	}
}

func TestCheckText(t *testing.T) {
	figures := map[string]bool{"sat/g.png": true}
	for _, ok := range []string{
		`If \(x^2 = 4\), then \[x = \pm 2\]`,
		"**Text 1**\n\n<u>under</u> and *it*",
		"| a | b |\n|---|---|\n| 1 | 2 |",
		"I and II are | and ||",
		"![a line](r2:sat/g.png)",
		"***bold italic*** and *italic with **bold** inside*",
	} {
		if msg := checkText(ok, figures); msg != "" {
			t.Errorf("%q: %s", ok, msg)
		}
	}
	for _, bad := range []string{`\(x`, `x\)`, "<b>x</b>", "<u>x", "**x", "![g](r2:other.png)", "a�b", "| a | b |\n| 1 | 2 |"} {
		if checkText(bad, figures) == "" {
			t.Errorf("%q accepted", bad)
		}
	}
	for s, ok := range map[string]bool{"17/2": true, "-8.5": true, ".125": true, "−3": true, "1,200": false, "x": false, "2/": false} {
		if _, got := ParseNumber(s); got != ok {
			t.Errorf("ParseNumber(%q) = %v", s, got)
		}
	}
}

func ptr(s string) *string { return &s }

func TestUploadsOff(t *testing.T) {
	svc := NewService(nil, nil)
	if _, err := svc.UploadImage(t.Context(), []byte("x"), "Editor"); !errors.Is(err, ErrUploadsOff) {
		t.Errorf("upload without a bucket: %v", err)
	}
	if _, err := svc.GetImage(t.Context(), "mb/x.png"); !errors.Is(err, ErrUploadsOff) {
		t.Errorf("get without a bucket: %v", err)
	}
}
