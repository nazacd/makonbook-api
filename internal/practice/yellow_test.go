package practice

import (
	"testing"

	"github.com/nazacd/makonbook-api/internal/platform/testdb"
)

func TestApproveYellow(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	im := Importer{Pool: pool}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	fix := func(kinds []string, changes map[string]any) map[string]any {
		return map[string]any{"kinds": kinds, "changes": changes, "note": "x"}
	}
	corrected := []string{"corrected"}
	shared, repeat := "Shared yellow question?", "Repeated yellow question?"

	day5 := review(t, "DAY 5", func(subject string, module, n int, rec map[string]any) {
		if subject != "math" || module != 1 {
			return
		}
		switch n {
		case 1: // qualifies: the stem and the explanation are corrected
			rec["risk"], rec["fix"] = "yellow", fix(corrected, map[string]any{"stem": "Fixed yellow?", "explanation": "Fixed because."})
		case 2:
			rec["risk"] = "yellow" // the fix step hasn't run
		case 3:
			rec["risk"], rec["fix"] = "yellow", fix(corrected, map[string]any{"key": "C"})
		case 4: // the same key restated and a choice corrected: qualifies
			rec["risk"], rec["fix"] = "yellow", fix([]string{"corrected", "restored"}, map[string]any{"key": "B", "choices.D": "fixed d"})
		case 5:
			rec["risk"], rec["fix"] = "yellow", map[string]any{"kinds": []string{}, "changes": map[string]any{}, "needs_person": "Replace the photo.", "note": "x"}
		case 6:
			rec["risk"], rec["fix"] = "yellow", fix(corrected, map[string]any{"stem": " "})
		case 7:
			rec["risk"], rec["fix"], rec["stem"] = "yellow", fix(corrected, map[string]any{"stem": "One fix?"}), shared
		case 8:
			rec["risk"], rec["fix"] = "red", fix(corrected, map[string]any{"stem": "Red fix?"})
		case 9:
			rec["risk"], rec["fix"] = "yellow", fix(corrected, map[string]any{"stem": "Edited meanwhile?"})
		case 10: // a grid-in whose fix changes the accepted answers
			rec["type"], rec["choices"], rec["key"], rec["accepted"] = "grid_in", nil, "3", []string{"3"}
			rec["risk"], rec["fix"] = "yellow", fix(corrected, map[string]any{"accepted": []string{"4"}})
		case 11:
			rec["risk"], rec["fix"] = "yellow", fix([]string{"invented"}, map[string]any{"choices.A": "a new distractor"})
		case 12: // the fix step found nothing to change: qualifies, stays version 1
			rec["risk"], rec["fix"] = "yellow", fix([]string{}, map[string]any{})
		case 13: // DAY6 repeats it with a green review and no fix: qualifies
			rec["risk"], rec["fix"], rec["stem"] = "yellow", fix(corrected, map[string]any{"stem": "Repeat fixed?"}), repeat
		}
	})
	day6 := review(t, "DAY6", func(subject string, module, n int, rec map[string]any) {
		switch {
		case subject == "math" && module == 1 && n == 1: // question 7 with another fix
			rec["risk"], rec["fix"], rec["stem"] = "yellow", fix(corrected, map[string]any{"stem": "Another fix?"}), shared
		case subject == "math" && module == 1 && n == 2:
			rec["risk"], rec["fix"] = "yellow", fix(corrected, map[string]any{"stem": "DAY6 fixed?"})
		case subject == "math" && module == 1 && n == 3:
			rec["stem"] = repeat
		}
	})
	for _, jsonl := range []string{day5, day6} {
		if _, err := im.Import(ctx, load(t, jsonl), false); err != nil {
			t.Fatal(err)
		}
	}
	// An editor changed question 9 before the bulk approval.
	if _, err := pool.Exec(ctx, `UPDATE questions SET version = 2 WHERE stem LIKE 'Question math-m1-09 of DAY 5%'`); err != nil {
		t.Fatal(err)
	}

	results := func(rs []YellowResult) map[string]string {
		m := map[string]string{}
		for _, r := range rs {
			m[r.Place] = r.Result
		}
		return m
	}
	want := map[string]string{
		"DAY5 math-m1-01": YellowWouldApprove,
		"DAY5 math-m1-02": YellowNoFix,
		"DAY5 math-m1-03": YellowAnswer,
		"DAY5 math-m1-04": YellowWouldApprove,
		"DAY5 math-m1-05": YellowPerson,
		"DAY5 math-m1-06": YellowCantApply + ": stem",
		"DAY5 math-m1-07": YellowDisagree,
		"DAY5 math-m1-10": YellowAnswer,
		"DAY5 math-m1-11": YellowMadeUp,
		"DAY5 math-m1-12": YellowWouldApprove,
		"DAY5 math-m1-13": YellowWouldApprove,
	}

	// Without --apply nothing changes; only DAY5's questions when asked for DAY5.
	dry, err := im.ApproveYellow(ctx, []string{"day 5"}, false)
	if err != nil {
		t.Fatal(err)
	}
	got := results(dry)
	if len(got) != len(want) {
		t.Errorf("dry run: %v", got)
	}
	for place, w := range want {
		if got[place] != w {
			t.Errorf("dry run %s: %q, want %q", place, got[place], w)
		}
	}
	if n := count(`SELECT count(*) FROM questions WHERE ai_risk <> 'green' AND (status = 'approved' OR version > 1)`); n != 1 {
		t.Errorf("the dry run changed %d questions", n-1)
	}

	// Applied to every test: DAY5's four and DAY6's own.
	done, err := im.ApproveYellow(ctx, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	approved := 0
	for _, r := range done {
		if r.Result == YellowApproved {
			approved++
		}
	}
	if got := results(done); approved != 5 || got["DAY6 math-m1-02"] != YellowApproved {
		t.Errorf("applied: %v", got)
	}
	if n := count(`SELECT count(*) FROM questions q JOIN question_versions v ON v.question_id = q.id AND v.version = 2
		WHERE q.stem = 'Fixed yellow?' AND q.explanation = 'Fixed because.' AND q.status = 'approved' AND NOT q.auto_approved
		  AND q.approved_by = $1 AND v.source = 'ai_fix' AND v.created_by = 'AI review'`, YellowApprover); n != 1 {
		t.Error("the fix wasn't applied and approved as the AI review's version")
	}
	if n := count(`SELECT count(*) FROM questions WHERE choices->>'D' = 'fixed d' AND correct = 'B' AND status = 'approved'`); n != 1 {
		t.Error("a fix restating the same key wasn't approved")
	}
	if n := count(`SELECT count(*) FROM questions WHERE stem LIKE 'Question math-m1-12 of DAY 5%' AND status = 'approved' AND version = 1`); n != 1 {
		t.Error("a fix changing nothing didn't just approve")
	}
	if n := count(`SELECT count(*) FROM questions WHERE stem = 'Repeat fixed?' AND status = 'approved'`); n != 1 {
		t.Error("a question repeated with a green review wasn't approved")
	}
	if n := count(`SELECT count(*) FROM questions WHERE status = 'approved' AND NOT auto_approved
		AND (ai_risk = 'red' OR approved_by IS DISTINCT FROM $1)`, YellowApprover); n != 0 {
		t.Errorf("%d red or other questions approved", n)
	}
	if n := count(`SELECT count(*) FROM questions WHERE stem LIKE 'Question math-m1-09 of DAY 5%' AND status = 'review'`); n != 1 {
		t.Error("the question an editor changed was approved")
	}

	// Again: nothing more to approve; the rest are still listed.
	again, err := im.ApproveYellow(ctx, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range again {
		if r.Approved() {
			t.Errorf("approved twice: %+v", r)
		}
	}
	if len(again) != len(want)-4 {
		t.Errorf("left: %v", results(again))
	}
}
