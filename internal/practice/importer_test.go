package practice

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nazacd/makonbook-api/internal/platform/r2"
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
		case subject == "math" && module == 1 && n == 7:
			rec["proposed_fix"] = map[string]string{"choices.A": "fixed a", "stem": "Fixed stem?"}
		case subject == "math" && module == 1 && n == 8:
			rec["proposed_fix"] = map[string]string{"question": "?"} // nothing it can apply
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
	// Green questions are approved at once, marked as done by the AI review.
	if n := count(`SELECT count(*) FROM questions WHERE status = 'approved' AND auto_approved AND approved_at IS NOT NULL`); n != 97 {
		t.Errorf("auto-approved: %d, want the 97 green", n)
	}
	if n := count(`SELECT count(*) FROM questions WHERE ai_risk = 'yellow' AND status = 'review'`); n != 1 {
		t.Error("the yellow question was approved")
	}

	// Their proposed fixes are applied as a version of their own; one that
	// can't be applied sends its question to review.
	fixed, err := im.ApplyAIFixes(ctx)
	if err != nil || fixed.Applied != 1 || fixed.Review != 1 {
		t.Fatalf("fixes: %+v %v", fixed, err)
	}
	if n := count(`SELECT count(*) FROM questions q JOIN question_versions v ON v.question_id = q.id AND v.version = 2
		WHERE q.stem = 'Fixed stem?' AND q.choices->>'A' = 'fixed a' AND q.auto_approved AND v.source = 'ai_fix' AND v.created_by = 'AI review'`); n != 1 {
		t.Error("the fix wasn't applied as an AI version")
	}
	if n := count(`SELECT count(*) FROM questions WHERE stem LIKE 'Question math-m1-08%' AND status = 'review' AND NOT auto_approved`); n != 1 {
		t.Error("the question with a fix that can't be applied stayed approved")
	}
	if again, err := im.ApplyAIFixes(ctx); err != nil || again.Applied+again.Review != 0 {
		t.Errorf("fixes run twice: %+v %v", again, err)
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
			rec["risk"] = "yellow"
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
	// The figure question, green in DAY2, is yellow in DAY3's review: it
	// loses its automatic approval.
	if n := count(`SELECT count(*) FROM questions WHERE stem LIKE 'The graph%' AND ai_risk = 'yellow' AND status = 'review' AND NOT auto_approved AND approved_at IS NULL`); n != 1 {
		t.Error("the figure question kept its automatic approval after a yellow review")
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
	// worked on it (automatic approvals don't count), and drops the
	// questions only it used.
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
	if _, err := pool.Exec(ctx, `UPDATE questions SET status = 'approved', approved_at = now(), auto_approved = false
		WHERE id = (SELECT question_id FROM practice_test_questions tq JOIN practice_tests t ON t.id = tq.test_id
		            WHERE t.code = 'DAY2' LIMIT 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := im.Import(ctx, load(t, changed), true); !errors.Is(err, ErrTouched) {
		t.Errorf("reload after approval: %v", err)
	}

	tests, err := NewService(pool, nil).Tests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tests) != 2 || tests[0].Code != "DAY2" || tests[0].Approved != 98 || tests[0].Questions != 98 ||
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

// memStore is a bucket in memory.
type memStore map[string][]byte

func (m memStore) Get(_ context.Context, key string) (r2.Object, error) {
	b, ok := m[key]
	if !ok {
		return r2.Object{}, r2.ErrNotFound
	}
	return r2.Object{Body: b, ContentType: "image/png"}, nil
}

func (m memStore) Put(_ context.Context, key string, body []byte, _ string) error {
	m[key] = body
	return nil
}

func TestFixStep(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	root := t.TempDir()
	file := filepath.Join(root, "DAY6", "questions.jsonl")
	write := func(content string) Test {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		test, err := Read(file)
		if err != nil {
			t.Fatal(err)
		}
		return test
	}
	store := memStore{}
	im := Importer{Pool: pool, Uploads: store}
	if _, err := im.Import(ctx, write(review(t, "DAY6", nil)), false); err != nil {
		t.Fatal(err)
	}

	// The fix step redrew a figure: a PNG in figures/, named by its hash.
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 40, 30))); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	key := "mb/" + hex.EncodeToString(sum[:20]) + ".png"
	if err := os.MkdirAll(filepath.Join(root, "figures"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "figures", key[3:]), buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	fixed := review(t, "DAY6", func(subject string, module, n int, rec map[string]any) {
		if subject == "math" && module == 1 && n == 2 {
			rec["fix"] = map[string]any{"kinds": []string{"redrawn"}, "changes": map[string]string{"stem": "Redrawn?"},
				"figures": []map[string]string{{"key": key, "kind": "graph", "alt": "a line"}}, "note": "Redrawn."}
		}
	})

	// Loading the test again brings in the fix and uploads its figure.
	if _, err := (Importer{Pool: pool}).Import(ctx, write(fixed), false); err == nil {
		t.Error("figures loaded without an uploads bucket")
	}
	s, err := im.Import(ctx, write(fixed), false)
	if err != nil || s.Refreshed != 1 {
		t.Fatalf("refresh: %+v %v", s, err)
	}
	if !bytes.Equal(store[key], buf.Bytes()) {
		t.Error("the figure isn't in the bucket")
	}
	var by string
	if err := pool.QueryRow(ctx, `SELECT uploaded_by FROM practice_images WHERE key = $1`, key).Scan(&by); err != nil || by != AIFixer {
		t.Errorf("figure record: %q %v", by, err)
	}
	var note string
	if err := pool.QueryRow(ctx, `SELECT record->'fix'->>'note' FROM question_ai_reviews WHERE qid = 'math-m1-02'`).Scan(&note); err != nil || note != "Redrawn." {
		t.Errorf("the review has no fix: %q %v", note, err)
	}
	if _, err := im.Import(ctx, write(fixed), false); !errors.Is(err, ErrImported) {
		t.Errorf("nothing new: %v", err)
	}

	// A figure whose bytes don't match its name is refused.
	if err := os.WriteFile(filepath.Join(root, "figures", key[3:]), append(buf.Bytes(), 0), 0o600); err != nil {
		t.Fatal(err)
	}
	delete(store, key)
	if _, err := pool.Exec(ctx, `DELETE FROM practice_images`); err != nil {
		t.Fatal(err)
	}
	if _, err := im.Import(ctx, write(fixed), false); err == nil || !strings.Contains(err.Error(), "doesn't match its name") {
		t.Errorf("a changed figure: %v", err)
	}
}
