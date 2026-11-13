package roadmap

import (
	"errors"
	"testing"

	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/testdb"
	"github.com/nazacd/makonbook-api/internal/practice"
)

func TestPracticeSets(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	student, err := people.CreateSelfStudent(ctx, pool, "Web", "Student", "+998935550011")
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(pool, practice.NewService(pool, nil), people.NewDirectory(pool))
	addQuestions(t, pool, "math", "Linear functions", "easy", 6)
	addQuestions(t, pool, "math", "Linear functions", "medium", 6)
	hard := addQuestions(t, pool, "math", "Linear functions", "hard", 6)
	addQuestions(t, pool, "math", "Percentages", "medium", 30)

	var bad *PracticeError
	for name, r := range map[string]PracticeRequest{
		"subject":    {Subject: "art", Count: 5},
		"count":      {Subject: "math", Count: 0},
		"difficulty": {Subject: "math", Count: 5, Difficulty: "brutal"},
		"seconds":    {Subject: "math", Count: 5, SecondsPerQuestion: 5},
	} {
		if _, err := svc.StartPractice(ctx, student, r); !errors.As(err, &bad) {
			t.Errorf("%s: %v", name, err)
		}
	}

	// A mixed set of one skill: easy, medium and hard, timed.
	a, err := svc.StartPractice(ctx, student, PracticeRequest{Subject: "math", Skills: []string{"Linear functions"}, Difficulty: "mixed", Count: 9, SecondsPerQuestion: 60})
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != "practice" || len(a.Questions) != 9 || a.Practice == nil || a.Practice.SecondsPerQuestion != 60 || a.Title != "Linear functions" || a.Subject != "math" || a.Questions[0].Correct != nil {
		t.Fatalf("set: %+v", a)
	}
	// One open at a time: asking again gives it back.
	again, err := svc.StartPractice(ctx, student, PracticeRequest{Subject: "math", Count: 5})
	if !errors.Is(err, ErrPracticeOpen) || again.ID != a.ID {
		t.Errorf("open: %v %s", err, again.ID)
	}
	answers := map[string]*string{}
	b := "B"
	for _, q := range a.Questions[:6] {
		answers[q.ID] = &b
	}
	done, err := svc.Submit(ctx, Who{ID: student}, a.ID, Answers{Answers: answers})
	if err != nil || done.Result == nil || done.Result.Correct != 6 || done.Result.Total != 9 || done.Questions[0].Correct == nil {
		t.Fatalf("submit: %+v %v", done.Result, err)
	}
	finished, err := svc.FinishedQuestions(ctx, student)
	if err != nil || len(finished) != 9 {
		t.Errorf("finished: %d %v", len(finished), err)
	}

	// A chosen question, then discarded.
	one, err := svc.StartPractice(ctx, student, PracticeRequest{Subject: "math", QuestionIDs: []string{hard[0], "not-an-id"}})
	if err != nil || len(one.Questions) != 1 || one.Questions[0].ID != hard[0] {
		t.Fatalf("one: %+v %v", one, err)
	}
	if err := svc.DiscardPractice(ctx, student, one.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.DiscardPractice(ctx, student, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("discard a finished set: %v", err)
	}

	// Every skill of the subject when none is named.
	all, err := svc.StartPractice(ctx, student, PracticeRequest{Subject: "math", Count: 15})
	if err != nil || len(all.Questions) != 15 {
		t.Fatalf("every skill: %d %v", len(all.Questions), err)
	}
	if err := svc.DiscardPractice(ctx, student, all.ID); err != nil {
		t.Fatal(err)
	}
	thirty, err := svc.StartPractice(ctx, student, PracticeRequest{Subject: "math", Skills: []string{"Percentages"}, Count: 30})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Submit(ctx, Who{ID: student}, thirty.ID, Answers{}); err != nil {
		t.Fatal(err)
	}
	sets, err := svc.PracticeSets(ctx, student, 10)
	if err != nil || len(sets) != 2 || sets[1].Percent == nil || *sets[1].Percent != 66 || sets[0].FinishedAt == nil {
		t.Errorf("sets: %+v %v", sets, err)
	}
}
