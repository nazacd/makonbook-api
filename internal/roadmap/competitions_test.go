package roadmap

import (
	"errors"
	"testing"
	"time"

	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/testdb"
	"github.com/nazacd/makonbook-api/internal/practice"
)

func TestCompetitions(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	ali, err := people.CreateSelfStudent(ctx, pool, "Ali", "Valiyev", "+998935550021")
	if err != nil {
		t.Fatal(err)
	}
	bek, _ := people.CreateSelfStudent(ctx, pool, "Bek", "Aliyev", "+998935550022")
	dana, _ := people.CreateSelfStudent(ctx, pool, "Dana", "Karimova", "+998935550023")
	svc := NewService(pool, practice.NewService(pool, nil), people.NewDirectory(pool))
	qs := addQuestions(t, pool, "math", "Linear functions", "easy", 6)
	now := time.Now()
	b := "B" // the right answer (addQuestions)

	// An editor's draft: checked, then published.
	bad := CompetitionInput{Title: "Weekly", Subject: "math", Source: "bank", StartsAt: now.Add(-time.Minute), EndsAt: now.Add(time.Hour), Minutes: 0}
	if _, err := svc.SaveCompetition(ctx, "", bad, "Employee:1", now); !errors.As(err, new(*CompetitionError)) {
		t.Errorf("no minutes: %v", err)
	}
	drawn, err := svc.DrawCompetition(ctx, "math", []string{"Linear functions"}, map[string]int{"easy": 3})
	if err != nil || len(drawn) != 3 {
		t.Fatalf("draw: %v %v", drawn, err)
	}
	in := CompetitionInput{Title: "Weekly", Prize: "A book", Subject: "math", Source: "bank", QuestionIDs: drawn, StartsAt: now.Add(-time.Minute), EndsAt: now.Add(time.Hour), Minutes: 10}
	c, err := svc.SaveCompetition(ctx, "", in, "Employee:1", now)
	if err != nil || c.Status != "draft" {
		t.Fatalf("save: %+v %v", c, err)
	}
	if list, _ := svc.Competitions(ctx, ali, false, now); len(list) != 0 {
		t.Errorf("a draft is listed for students: %+v", list)
	}
	if _, err := svc.StartCompetition(ctx, ali, c.ID, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("start a draft: %v", err)
	}
	if err := svc.PublishCompetition(ctx, c.ID, true, now); err != nil {
		t.Fatal(err)
	}

	// While live, its questions are out of the bank.
	if _, err := svc.StartPractice(ctx, ali, PracticeRequest{Subject: "math", QuestionIDs: drawn, Mode: ModeOwnPace}); !errors.Is(err, ErrNoQuestions) {
		t.Errorf("practise a held question: %v", err)
	}
	if err := svc.SaveQuestion(ctx, ali, drawn[0], true); !errors.Is(err, ErrNotFound) {
		t.Errorf("save a held question: %v", err)
	}
	cands, _ := practice.NewService(pool, nil).Candidates(ctx, "math", []string{"linear functions"})
	if len(cands) != 3 {
		t.Errorf("draw while held: %d candidates", len(cands))
	}

	// Ali takes part: the same questions, no keys; one attempt.
	a, err := svc.StartCompetition(ctx, ali, c.ID, now)
	if err != nil || a.Kind != "competition" || len(a.Questions) != 3 || a.Competition == nil || a.Competition.Ended || a.Questions[0].Correct != nil {
		t.Fatalf("start: %+v %v", a.Competition, err)
	}
	again, err := svc.StartCompetition(ctx, ali, c.ID, now)
	if err != nil || again.ID != a.ID {
		t.Errorf("open attempt again: %v", err)
	}
	done, err := svc.Submit(ctx, Who{ID: ali}, a.ID, Answers{Answers: map[string]*string{drawn[0]: &b, drawn[1]: &b}})
	if err != nil || done.Result == nil || done.Result.Correct != 2 || len(done.Result.Items) != 0 || done.Questions[0].Correct != nil || done.Questions[0].Explanation != nil {
		t.Fatalf("submit hides keys until the end: %+v %v", done.Result, err)
	}
	if _, err := svc.StartCompetition(ctx, ali, c.ID, now); !errors.Is(err, ErrAttempted) {
		t.Errorf("second attempt: %v", err)
	}

	// Bek's time runs out: a late answer is refused, and his attempt reaches
	// the board on its own with what he had saved.
	bk, err := svc.StartCompetition(ctx, bek, c.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SaveAnswers(ctx, Who{ID: bek}, bk.ID, Answers{Answers: map[string]*string{drawn[0]: &b}}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE roadmap_assessments SET created_at = now() - interval '11 minutes' WHERE id = $1`, mustUUID(bk.ID)); err != nil {
		t.Fatal(err)
	}
	if err := svc.SaveAnswers(ctx, Who{ID: bek}, bk.ID, Answers{Answers: map[string]*string{drawn[1]: &b}}); !errors.Is(err, ErrTimeUp) {
		t.Errorf("late answer: %v", err)
	}
	board, err := svc.CompetitionBoard(ctx, c.ID, bek, false)
	if err != nil || board.Participants != 2 || board.Entries[0].Name != "Ali V." || board.Entries[0].Correct != 2 ||
		board.Me == nil || board.Me.Correct != 1 || board.Me.Seconds > 10*60 {
		t.Fatalf("board: %+v %v", board, err)
	}

	// An unpublish after the start is refused; changes are words only.
	if err := svc.PublishCompetition(ctx, c.ID, false, now); !errors.Is(err, ErrCompetitionLocked) {
		t.Errorf("unpublish after start: %v", err)
	}
	in.Minutes, in.Title = 99, "Weekly sprint"
	saved, err := svc.SaveCompetition(ctx, c.ID, in, "Employee:1", now)
	if err != nil || saved.Minutes != 10 || saved.Title != "Weekly sprint" {
		t.Errorf("locked save: %+v %v", saved, err)
	}

	// When it ends, the answers open, and the questions return to the bank.
	if _, err := pool.Exec(ctx, `UPDATE competitions SET starts_at = now() - interval '2 hours', ends_at = now() - interval '1 second' WHERE id = $1`, mustUUID(c.ID)); err != nil {
		t.Fatal(err)
	}
	after, err := svc.Assessment(ctx, Who{ID: ali}, a.ID)
	if err != nil || !after.Competition.Ended || after.Questions[0].Correct == nil || len(after.Result.Items) != 3 {
		t.Fatalf("after the end: %+v %v", after.Competition, err)
	}
	if err := svc.SaveQuestion(ctx, ali, drawn[0], true); err != nil {
		t.Errorf("save after the end: %v", err)
	}

	// A reserved competition: its questions are held while still a draft;
	// a question in a mock test can't be published in one.
	reserved := addQuestions(t, pool, "math", "Percentages", "hard", 2)
	rc, err := svc.SaveCompetition(ctx, "", CompetitionInput{Title: "Cup", Subject: "math", Source: "reserved", QuestionIDs: reserved, StartsAt: now.Add(time.Hour), EndsAt: now.Add(2 * time.Hour), Minutes: 20}, "Employee:1", now)
	if err != nil {
		t.Fatal(err)
	}
	if held, _ := practice.NewService(pool, nil).Held(ctx, reserved); len(held) != 2 {
		t.Errorf("reserved draft not held: %v", held)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO practice_tests (code, title) VALUES ('T1', 'T1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO practice_test_questions (test_id, subject, module, position, question_id) SELECT id, 'math', 1, 1, $1 FROM practice_tests WHERE code = 'T1'`, mustUUID(reserved[0])); err != nil {
		t.Fatal(err)
	}
	if err := svc.PublishCompetition(ctx, rc.ID, true, now); !errors.As(err, new(*CompetitionError)) {
		t.Errorf("publish with a mock-test question: %v", err)
	}
	if err := svc.DeleteCompetition(ctx, rc.ID); err != nil {
		t.Errorf("delete an untaken one: %v", err)
	}
	if err := svc.DeleteCompetition(ctx, c.ID); !errors.Is(err, ErrCompetitionLocked) {
		t.Errorf("delete a taken one: %v", err)
	}

	// Merging: Dana also took it; merging Dana into Ali keeps Ali's attempt.
	live, _ := svc.SaveCompetition(ctx, "", CompetitionInput{Title: "Live", Subject: "math", Source: "bank", QuestionIDs: qs[3:5], StartsAt: now.Add(-time.Minute), EndsAt: now.Add(time.Hour), Minutes: 5}, "Employee:1", now)
	if err := svc.PublishCompetition(ctx, live.ID, true, now); err != nil {
		t.Fatal(err)
	}
	for _, s := range []int32{ali, dana} {
		if _, err := svc.StartCompetition(ctx, s, live.ID, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := people.MergeSelfStudent(ctx, pool, dana, ali); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if n, _ := svc.CompetitionBoard(ctx, live.ID, ali, false); n.Participants != 0 {
		t.Errorf("unfinished attempts on the board: %+v", n)
	}
}
