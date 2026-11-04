package practice

import (
	"errors"
	"testing"
	"time"

	"github.com/nazacd/makonbook-api/internal/platform/testdb"
)

func TestQuestionStats(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	day1 := review(t, "DAY1", func(subject string, module, n int, rec map[string]any) {
		if subject == "math" && module == 2 && n == 3 {
			rec["type"], rec["choices"], rec["key"], rec["accepted"] = "grid_in", nil, nil, []string{"1/2"}
		}
	})
	// DAY2 repeats DAY1's first Reading and Writing question: one question.
	day2 := review(t, "DAY2", func(subject string, module, n int, rec map[string]any) {
		if subject == "english" && module == 1 && n == 1 {
			rec["stem"] = "Question english-m1-01 of DAY1?"
		}
	})
	for _, jsonl := range []string{day1, day2} {
		if _, err := (Importer{Pool: pool}).Import(ctx, load(t, jsonl), false); err != nil {
			t.Fatal(err)
		}
	}
	svc := NewService(pool, signingStore{memStore{}})
	if _, err := svc.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO students (client_id, hh_student_id, first_name, last_name, phone_raw, office_ids)
		VALUES (11, 1, 'Ali', 'Valiyev', '', '{}'), (12, 2, 'Bek', 'Aliyev', '', '{}'), (-1, -1, 'Demo', 'Student', '', '{}')`); err != nil {
		t.Fatal(err)
	}
	a, b, half := "A", "b", ".5"
	B := "B"
	start := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	attempt := func(student int32, id, test string, sections []string, answers ...Answer) {
		t.Helper()
		in := AttemptInput{ID: id, Test: test, Version: 1, Sections: sections, StartedAt: start, FinishedAt: start.Add(time.Hour), Answers: answers}
		if _, err := svc.SaveAttempt(ctx, student, in); err != nil {
			t.Fatal(err)
		}
	}
	attempt(11, "3f2b8a6e-1c4d-4e5f-9a0b-1c2d3e4f5a6b", "DAY1", []string{"math"},
		Answer{Subject: "math", Module: 1, Position: 1, Answer: &B},
		Answer{Subject: "math", Module: 1, Position: 2},
		Answer{Subject: "math", Module: 2, Position: 3, Answer: &half})
	attempt(12, "7d1e2f3a-4b5c-4d6e-8f70-8192a3b4c5d6", "DAY1", []string{"english", "math"},
		Answer{Subject: "math", Module: 1, Position: 1, Answer: &a},
		Answer{Subject: "english", Module: 1, Position: 1, Answer: &b})
	attempt(12, "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d", "DAY2", []string{"english"},
		Answer{Subject: "english", Module: 1, Position: 1, Answer: &B})
	// The demo account's made-up results don't count.
	attempt(-1, "9a8b7c6d-5e4f-4a3b-9c2d-1e0f9a8b7c6d", "DAY1", []string{"math"},
		Answer{Subject: "math", Module: 1, Position: 1, Answer: &B})

	pkg, err := svc.LatestPackage(ctx, "DAY1")
	if err != nil {
		t.Fatal(err)
	}
	id := func(subject string, module, position int) string {
		for _, sec := range pkg.Sections {
			for _, m := range sec.Modules {
				for _, q := range m.Questions {
					if sec.Subject == subject && int(m.Module) == module && int(q.Position) == position {
						return q.ID
					}
				}
			}
		}
		t.Fatalf("no %s %d %d", subject, module, position)
		return ""
	}
	stats, err := svc.QuestionStats(ctx, "DAY1")
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 98 || stats[0].ID != id("english", 1, 1) || stats[97].ID != id("math", 2, 22) {
		t.Fatalf("one per question, in the package's order: %d %+v", len(stats), stats[0])
	}
	got := map[string]QuestionStat{}
	for _, s := range stats {
		got[s.ID] = s
	}
	for _, c := range []struct {
		subject           string
		module, position  int
		answered, correct int32
	}{
		{"math", 1, 1, 2, 1},    // B right, A wrong (the demo's left out)
		{"math", 1, 2, 2, 0},    // blank, and not sent: answered, wrong
		{"math", 2, 3, 2, 1},    // the grid-in: .5 is 1/2
		{"english", 1, 1, 2, 2}, // in DAY1 and in DAY2; "b" is B
		{"english", 1, 2, 1, 0},
	} {
		if s := got[id(c.subject, c.module, c.position)]; s.Answered != c.answered || s.Correct != c.correct {
			t.Errorf("%s m%d q%d: %+v, want %d answered, %d correct", c.subject, c.module, c.position, s, c.answered, c.correct)
		}
	}

	// Counted again only after StatsTTL.
	attempt(11, "1b2c3d4e-5f6a-4b7c-8d9e-0f1a2b3c4d5e", "DAY1", []string{"math"},
		Answer{Subject: "math", Module: 1, Position: 1, Answer: &B})
	if again, _ := svc.QuestionStats(ctx, "DAY1"); again[54] != stats[54] {
		t.Errorf("counted again within the TTL: %+v", again[54])
	}
	svc.stats.at = time.Now().Add(-StatsTTL)
	if again, _ := svc.QuestionStats(ctx, "DAY1"); again[54].Answered != 3 || again[54].Correct != 2 {
		t.Errorf("after the TTL: %+v", again[54])
	}

	if _, err := svc.QuestionStats(ctx, "DAY9"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown test: %v", err)
	}
}
