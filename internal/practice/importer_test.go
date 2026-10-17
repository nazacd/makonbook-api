package practice

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nazacd/makonbook-api/internal/platform/testdb"
)

// review builds a test's questions.jsonl: every question's text is unique to
// the test unless edit changes it.
func review(t *testing.T, code string, edit func(subject string, module, number int, rec map[string]any)) string {
	t.Helper()
	var lines []string
	for _, subject := range []string{"english", "math"} {
		for module := 1; module <= 2; module++ {
			for n := 1; n <= moduleSize[subject]; n++ {
				qid := fmt.Sprintf("%s-m%d-%02d", subject, module, n)
				rec := map[string]any{
					"qid":    qid,
					"source": map[string]any{"subject": subject, "csv_id": n, "test_id": code, "module": fmt.Sprintf("module_%d", module), "number": n},
					"type":   "mcq", "passage": nil,
					"stem":    fmt.Sprintf("Question %s of %s?", qid, code),
					"choices": map[string]string{"A": "a", "B": "b", "C": "c", "D": "d"},
					"figures": []any{}, "key": "B", "accepted": []string{},
					"verdict": "key_correct", "risk": "green",
					"explanation": map[string]any{"text": "Because."},
					"tags":        map[string]any{"domain": "Algebra", "skill": "Linear functions", "difficulty": "easy"},
				}
				if edit != nil {
					edit(subject, module, n, rec)
				}
				b, err := json.Marshal(rec)
				if err != nil {
					t.Fatal(err)
				}
				lines = append(lines, string(b))
			}
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

func load(t *testing.T, jsonl string) Test {
	t.Helper()
	test, err := parse(strings.NewReader(jsonl), "test")
	if err != nil {
		t.Fatal(err)
	}
	return test
}

func TestImport(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	im := Importer{Pool: pool}
	count := func(sql string) int {
		var n int
		if err := pool.QueryRow(ctx, sql).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// DAY 2 (the bank writes some ids with a space) has a red question with
	// a figure and a grid-in.
	day2 := review(t, "DAY 2", func(subject string, module, n int, rec map[string]any) {
		switch {
		case subject == "english" && module == 1 && n == 5:
			rec["stem"] = "Shared question, curly ‘quotes’?"
			rec["risk"] = "yellow"
		case subject == "english" && module == 1 && n == 6:
			rec["passage"] = "The guide, however; was late."
			rec["stem"] = "Which choice conforms?"
		case subject == "math" && module == 2 && n == 3:
			rec["stem"] = "The graph:\n\n![A line through (0, 2)](r2:sat/g.png)\n\nWhat is the slope?"
			rec["figures"] = []map[string]string{{"key": "sat/g.png", "kind": "graph", "alt": "A line through (0, 2)"}}
		case subject == "math" && module == 2 && n == 4:
			rec["type"], rec["choices"], rec["key"], rec["accepted"] = "grid_in", nil, "17/2", []string{"17/2", "8.5"}
		}
	})
	s, err := im.Import(ctx, load(t, day2), false)
	if err != nil {
		t.Fatal(err)
	}
	if s.Code != "DAY2" || s.Questions != 98 || s.New != 98 || s.Risk["yellow"] != 1 {
		t.Errorf("DAY2: %+v", s)
	}

	// DAY3 repeats two DAY2 questions, written with other spacing and quotes
	// (its risk is raised to this review's red) or another figure description:
	// each is stored once. A third differs by a comma, so it is a different
	// grammar question and is stored apart.
	day3 := review(t, "DAY3", func(subject string, module, n int, rec map[string]any) {
		switch {
		case subject == "english" && module == 2 && n == 1:
			rec["stem"] = "Shared  question, curly 'quotes'?"
			rec["risk"], rec["verdict"] = "red", "ambiguous"
		case subject == "english" && module == 2 && n == 2:
			rec["passage"] = "The guide, however, was late."
			rec["stem"] = "Which choice conforms?"
		case subject == "math" && module == 1 && n == 9:
			rec["stem"] = "The graph:\n\n![a straight line](r2:sat/g.png)\n\nWhat is the slope?"
			rec["figures"] = []map[string]string{{"key": "sat/g.png", "kind": "graph", "alt": "a straight line"}}
		}
	})
	s, err = im.Import(ctx, load(t, day3), false)
	if err != nil {
		t.Fatal(err)
	}
	if s.New != 96 || s.Repeats != 2 {
		t.Errorf("DAY3: %+v", s)
	}
	if n := count(`SELECT count(*) FROM questions`); n != 98+96 {
		t.Errorf("questions: %d", n)
	}
	if n := count(`SELECT count(*) FROM questions WHERE ai_risk = 'red' AND stem LIKE 'Shared%'`); n != 1 {
		t.Error("the shared question's risk wasn't raised to red")
	}
	if n := count(`SELECT count(*) FROM question_ai_reviews r JOIN questions q ON q.id = r.question_id WHERE q.stem LIKE 'Shared%'`); n != 2 {
		t.Errorf("the shared question has %d reviews, want both", n)
	}
	if n := count(`SELECT count(*) FROM questions WHERE type = 'grid_in' AND accepted = '{17/2,8.5}' AND correct IS NULL`); n != 1 {
		t.Error("grid-in not stored")
	}
	if n := count(`SELECT count(*) FROM question_versions WHERE version = 1 AND source = 'import'`); n != 98+96 {
		t.Errorf("versions: %d", n)
	}

	// Loading DAY2 again is skipped; a reload replaces it while nobody has
	// worked on it, and drops the questions only it used.
	if _, err := im.Import(ctx, load(t, day2), false); !errors.Is(err, ErrImported) {
		t.Errorf("second import: %v", err)
	}
	changed := review(t, "DAY2", func(subject string, module, n int, rec map[string]any) {
		rec["stem"] = fmt.Sprintf("Fixed %s?", rec["qid"])
	})
	s, err = im.Import(ctx, load(t, changed), true)
	if err != nil || !s.Replaced || s.New != 98 {
		t.Fatalf("reload: %+v %v", s, err)
	}
	// DAY2's old questions are gone, except the two DAY3 still uses.
	if n := count(`SELECT count(*) FROM questions`); n != 98+96+2 {
		t.Errorf("questions after reload: %d", n)
	}

	// Once a teacher approves a question, the test can't be reloaded.
	if _, err := pool.Exec(ctx, `UPDATE questions SET status = 'approved', approved_at = now()
		WHERE id = (SELECT question_id FROM practice_test_questions tq JOIN practice_tests t ON t.id = tq.test_id
		            WHERE t.code = 'DAY2' LIMIT 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := im.Import(ctx, load(t, changed), true); !errors.Is(err, ErrTouched) {
		t.Errorf("reload after approval: %v", err)
	}

	tests, err := NewService(pool).Tests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tests) != 2 || tests[0].Code != "DAY2" || tests[0].Approved != 1 || tests[0].Questions != 98 ||
		tests[1].Red != 1 || tests[1].Shared != 0 {
		t.Errorf("progress: %+v", tests)
	}
}

func TestParseRejects(t *testing.T) {
	good := review(t, "DAY4", nil)
	lines := strings.Split(strings.TrimSpace(good), "\n")
	cases := map[string]string{
		"old pilot format": `{"source": {"subject": "math"}, "explanation": "text"}`,
		"missing question": strings.Join(lines[1:], "\n"),
		"two tests":        strings.Replace(good, `"test_id":"DAY4"`, `"test_id":"DAY5"`, 1),
		"repeated qid":     good + lines[0],
		"unknown risk":     strings.Replace(good, `"risk":"green"`, `"risk":"blue"`, 1),
	}
	for name, input := range cases {
		if _, err := parse(strings.NewReader(input), "x"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := parse(strings.NewReader(cases["old pilot format"]), "x"); err == nil || !strings.Contains(err.Error(), "old format") {
		t.Errorf("old format message: %v", err)
	}
}
