package roadmap

import (
	"errors"
	"testing"
	"time"

	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/testdb"
	"github.com/nazacd/makonbook-api/internal/practice"
)

func TestAnswersChecksAndStats(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	tashkent, _ := time.LoadLocation("Asia/Tashkent")
	student, err := people.CreateSelfStudent(ctx, pool, "Web", "Student", "+998935550012")
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(pool, practice.NewService(pool, nil), people.NewDirectory(pool))
	easy := addQuestions(t, pool, "math", "Linear functions", "easy", 4)
	addQuestions(t, pool, "math", "Linear functions", "hard", 2)
	unrated := addQuestions(t, pool, "math", "Linear functions", "", 1)
	a, b := "A", "B" // B is right (addQuestions)

	// At one's own pace: checked one by one, each once; never on a rush.
	set, err := svc.StartPractice(ctx, student, PracticeRequest{Subject: "math", QuestionIDs: []string{easy[0], easy[1], easy[2], unrated[0]}, Mode: ModeOwnPace, SecondsPerQuestion: 60})
	if err != nil || set.Practice.Mode != ModeOwnPace || set.Practice.SecondsPerQuestion != 0 {
		t.Fatalf("own pace: %+v %v", set.Practice, err)
	}
	c, err := svc.Check(ctx, Who{ID: student}, set.ID, easy[0], "A", 40)
	if err != nil || c.Correct || c.Key == nil || *c.Key != "B" || c.Explanation == "" {
		t.Fatalf("check wrong: %+v %v", c, err)
	}
	if _, err := svc.Check(ctx, Who{ID: student}, set.ID, easy[0], "B", 5); !errors.Is(err, ErrAlreadyChecked) {
		t.Errorf("checked twice: %v", err)
	}
	if c, err := svc.Check(ctx, Who{ID: student}, set.ID, unrated[0], "B", 20); err != nil || !c.Correct {
		t.Errorf("check right: %+v %v", c, err)
	}
	if _, err := svc.Check(ctx, Who{ID: student}, set.ID, easy[3], "B", 5); !errors.As(err, new(*AnswersError)) {
		t.Errorf("a question not in the set: %v", err)
	}
	// The view shows what was checked, with its key, before the set is done.
	view, err := svc.Assessment(ctx, Who{ID: student}, set.ID)
	if err != nil || len(view.Checked) != 2 || view.Checked[easy[0]] || view.Questions[0].Correct == nil || view.Questions[1].Correct != nil {
		t.Fatalf("view: %+v %v", view.Checked, err)
	}
	// Submitting can't change a checked answer; it records the rest.
	done, err := svc.Submit(ctx, Who{ID: student}, set.ID, Answers{Answers: map[string]*string{easy[0]: &b, easy[1]: &b}})
	if err != nil || done.Result.Correct != 2 || *done.Answers[easy[0]] != "A" {
		t.Fatalf("submit: %+v %v", done.Result, err)
	}
	rush, err := svc.StartPractice(ctx, student, PracticeRequest{Subject: "math", QuestionIDs: []string{easy[3]}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Check(ctx, Who{ID: student}, rush.ID, easy[3], "B", 5); !errors.Is(err, ErrNotCheckable) {
		t.Errorf("check on a rush: %v", err)
	}
	// Wrong then right on easy[0] later: solved once. Discarding keeps answers.
	again, _ := svc.Submit(ctx, Who{ID: student}, rush.ID, Answers{Answers: map[string]*string{easy[3]: &a}})
	if again.Result == nil {
		t.Fatal("rush not submitted")
	}
	redo, err := svc.StartPractice(ctx, student, PracticeRequest{Subject: "math", QuestionIDs: []string{easy[0]}, Mode: ModeOwnPace})
	if err != nil {
		t.Fatal(err)
	}
	if c, err := svc.Check(ctx, Who{ID: student}, redo.ID, easy[0], "B", 30); err != nil || !c.Correct {
		t.Fatalf("redo: %+v %v", c, err)
	}
	if err := svc.DiscardPractice(ctx, student, redo.ID); err != nil {
		t.Fatal(err)
	}
	// Unsolved only leaves out the solved ones.
	fresh, err := svc.StartPractice(ctx, student, PracticeRequest{Subject: "math", Skills: []string{"Linear functions"}, Count: 10, Unsolved: true, Shuffle: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range fresh.Questions {
		if q.ID == easy[0] || q.ID == easy[1] || q.ID == unrated[0] {
			t.Errorf("a solved question came back: %s", q.ID)
		}
	}
	if err := svc.DiscardPractice(ctx, student, fresh.ID); err != nil {
		t.Fatal(err)
	}
	// A chosen difficulty is kept to: two hard questions exist, so two come.
	hardOnly, err := svc.StartPractice(ctx, student, PracticeRequest{Subject: "math", Skills: []string{"Linear functions"}, Difficulty: "hard", Count: 10})
	if err != nil || len(hardOnly.Questions) != 2 || *hardOnly.Questions[0].Difficulty != "hard" {
		t.Fatalf("hard only: %d %v", len(hardOnly.Questions), err)
	}
	if err := svc.DiscardPractice(ctx, student, hardOnly.ID); err != nil {
		t.Fatal(err)
	}

	// An answer last Sunday at 23:30 Tashkent time: last week, not this one.
	now := time.Now().In(tashkent)
	monday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, tashkent).AddDate(0, 0, -((int(now.Weekday()) + 6) % 7))
	if _, err := pool.Exec(ctx, `INSERT INTO question_answers (student_client_id, question_id, answer, correct, answered_at) VALUES ($1, $2, 'A', false, $3)`,
		student, easy[2], monday.Add(-30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := svc.SaveQuestion(ctx, student, easy[3], true); err != nil {
		t.Fatal(err)
	}
	if err := svc.SaveQuestion(ctx, student, "00000000-0000-4000-8000-000000000000", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("save an unknown question: %v", err)
	}

	// On a Monday the Sunday-night answer is yesterday: two days in a row.
	wantStreak := 1
	if now.Weekday() == time.Monday {
		wantStreak = 2
	}
	st, err := svc.Stats(ctx, student, time.Now(), tashkent)
	if err != nil {
		t.Fatal(err)
	}
	m := st.Subjects["math"]
	// Answers: easy0 A (wrong), unrated B, easy1 B, easy3 A, easy0 B, easy2 A (last week) = 6.
	switch {
	case st.Answers != 6 || st.Correct != 3 || st.Attempted != 5:
		t.Errorf("totals: %+v", st)
	case m.Total != 7 || m.Solved != 3 || m.Attempted != 5 || m.Skills["Linear functions"] != 3:
		t.Errorf("math: %+v", m)
	case st.Saved != 1 || st.Streak != wantStreak:
		t.Errorf("saved %d streak %d, want %d", st.Saved, st.Streak, wantStreak)
	case len(st.Weeks) != WeeksShown:
		t.Errorf("weeks: %d", len(st.Weeks))
	}
	this, last := st.Weeks[WeeksShown-1], st.Weeks[WeeksShown-2]
	if this.Correct["easy"] != 2 || this.Correct["unrated"] != 1 || this.Wrong["easy"] != 2 || last.Wrong["easy"] != 1 {
		t.Errorf("week boundary: this %+v last %+v", this, last)
	}
	if len(m.Difficulty) != 3 || m.Difficulty[0].Answers != 5 || m.Difficulty[0].AvgSeconds == nil || m.Difficulty[0].SiteAccuracy != nil {
		t.Errorf("difficulty: %+v", m.Difficulty)
	}
	if len(m.Domains) != 1 || m.Domains[0].Answers != 6 {
		t.Errorf("domains: %+v", m.Domains)
	}

	// The backfill adds nothing twice.
	if n, err := svc.BackfillAnswers(ctx); err != nil || n != 0 {
		t.Errorf("backfill: %d %v", n, err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM question_answers WHERE assessment_id = $1`, mustUUID(rush.ID)); err != nil {
		t.Fatal(err)
	}
	if n, err := svc.BackfillAnswers(ctx); err != nil || n != 1 {
		t.Errorf("backfill after a gap: %d %v", n, err)
	}
}
