package practice

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nazacd/makonbook-api/internal/platform/testdb"
	"github.com/nazacd/makonbook-api/internal/universities"
)

func TestGoals(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `
		INSERT INTO students (client_id, hh_student_id, first_name, last_name, phone_raw, office_ids)
		VALUES (11, 1, 'Ali', 'Valiyev', '', '{}')`); err != nil {
		t.Fatal(err)
	}
	svc := NewService(pool, nil)
	today := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	n := func(v int) *int { return &v }
	date := func(s string) *string { return &s }

	if g, err := svc.Goals(ctx, 11); err != nil || g != (Goals{}) {
		t.Errorf("never set: %+v %v", g, err)
	}
	g, err := svc.SetGoals(ctx, 11, GoalsInput{TargetMath: n(750), TargetRW: n(700), ExamDate: date("2026-12-05")}, today)
	if err != nil || *g.TargetMath != 750 || *g.TargetRW != 700 || *g.TargetTotal != 1450 || *g.ExamDate != "2026-12-05" || g.UpdatedAt == nil {
		t.Fatalf("set: %+v %v", g, err)
	}
	if got, err := svc.Goals(ctx, 11); err != nil || *got.TargetTotal != 1450 || *got.ExamDate != "2026-12-05" {
		t.Errorf("get: %+v %v", got, err)
	}
	// Each may be null; the total needs both.
	if g, err := svc.SetGoals(ctx, 11, GoalsInput{TargetMath: n(600)}, today); err != nil || *g.TargetMath != 600 || g.TargetRW != nil ||
		g.TargetTotal != nil || g.ExamDate != nil {
		t.Errorf("one target: %+v %v", g, err)
	}

	var bad *GoalsError
	for name, in := range map[string]GoalsInput{
		"too low":     {TargetMath: n(190)},
		"too high":    {TargetRW: n(810)},
		"not by 10":   {TargetMath: n(655)},
		"not a date":  {ExamDate: date("5 December")},
		"in the past": {ExamDate: date("2026-11-03")},
	} {
		if _, err := svc.SetGoals(ctx, 11, in, today); !errors.As(err, &bad) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := svc.SetGoals(ctx, 11, GoalsInput{ExamDate: date("2026-11-04")}, today); err != nil {
		t.Errorf("today: %v", err)
	}
	// Once the exam has passed, the stored date can be sent back unchanged.
	later := today.AddDate(0, 0, 10)
	if g, err := svc.SetGoals(ctx, 11, GoalsInput{TargetMath: n(800), ExamDate: date("2026-11-04")}, later); err != nil || *g.TargetMath != 800 {
		t.Errorf("the stored date after the exam: %+v %v", g, err)
	}
	if _, err := svc.SetGoals(ctx, 11, GoalsInput{ExamDate: date("2026-11-05")}, later); !errors.As(err, &bad) {
		t.Errorf("another past date: %v", err)
	}
}

func TestGoalsUniversity(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `
		INSERT INTO students (client_id, hh_student_id, first_name, last_name, phone_raw, office_ids)
		VALUES (11, 1, 'Ali', 'Valiyev', '', '{}');
		INSERT INTO universities (id, name, country, list_reason) VALUES
			('northbridge-university', 'Northbridge University', 'GB', 'qs');
		INSERT INTO universities (id, name, country, list_reason, listed) VALUES
			('gone-university', 'Gone University', 'GB', 'qs', false)`); err != nil {
		t.Fatal(err)
	}
	svc := NewService(pool, nil).WithUniversities(func(_ context.Context, id string) (universities.Summary, error) {
		return universities.Summary{ID: id, Name: "Northbridge University"}, nil
	})
	today := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	n := func(v int) *int { return &v }
	id := func(s string) Optional { return Optional{Set: true, Value: &s} }

	g, err := svc.SetGoals(ctx, 11, GoalsInput{TargetMath: n(700), UniversityID: id("northbridge-university")}, today)
	if err != nil || *g.UniversityID != "northbridge-university" || g.University == nil || g.University.Name != "Northbridge University" {
		t.Fatalf("set: %+v %v", g, err)
	}
	// Left out (as the phone sends goals), it stays; null clears it.
	if g, err := svc.SetGoals(ctx, 11, GoalsInput{TargetMath: n(750)}, today); err != nil || g.UniversityID == nil || *g.TargetMath != 750 {
		t.Errorf("left out: %+v %v", g, err)
	}
	var in GoalsInput
	if err := json.Unmarshal([]byte(`{"target_math":750,"university_id":null}`), &in); err != nil || !in.UniversityID.Set {
		t.Fatalf("null decoded: %+v %v", in, err)
	}
	if g, err := svc.SetGoals(ctx, 11, in, today); err != nil || g.UniversityID != nil || g.University != nil {
		t.Errorf("null: %+v %v", g, err)
	}
	var bad *GoalsError
	for _, u := range []string{"nowhere", "gone-university"} {
		if _, err := svc.SetGoals(ctx, 11, GoalsInput{UniversityID: id(u)}, today); !errors.As(err, &bad) {
			t.Errorf("%s: %v", u, err)
		}
	}
}
