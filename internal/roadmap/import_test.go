package roadmap

import (
	"errors"
	"strings"
	"testing"

	"github.com/nazacd/makonbook-api/internal/platform/testdb"
	"github.com/nazacd/makonbook-api/internal/practice"
)

func TestImportAndCoverage(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	svc := NewService(pool, practice.NewService(pool, nil), nil)

	math, english := fixture("math", "foundation", "m-"), fixture("english", "foundation", "e-")
	got, err := svc.Import(ctx, []Course{math, english})
	if err != nil || len(got) != 2 || !got[0].Changed || got[0].Version != 1 || got[1].ID != "english-foundation" {
		t.Fatalf("import: %+v %v", got, err)
	}

	// The same again: nothing new. A change: a new version, live at once.
	if got, err := svc.Import(ctx, []Course{math}); err != nil || got[0].Changed || got[0].Version != 1 {
		t.Errorf("same again: %+v %v", got, err)
	}
	math.Units[0].Lessons[0].Title = "Renamed"
	if got, err := svc.Import(ctx, []Course{math}); err != nil || !got[0].Changed || got[0].Version != 2 {
		t.Errorf("changed: %+v %v", got, err)
	}
	if c, err := svc.course(ctx, "math-foundation"); err != nil || c.Units[0].Lessons[0].Title != "Renamed" {
		t.Errorf("live version: %v", err)
	}

	// Invalid courses, a course twice, a lesson id another course has: nothing is stored.
	bad := fixture("math", "pre-sat", "p-")
	bad.Units[0].Lessons[0].LevelCheck.Mix.Easy = 9
	var invalid *InvalidError
	if _, err := svc.Import(ctx, []Course{fixture("math", "advanced", "a-"), bad}); !errors.As(err, &invalid) ||
		!strings.Contains(err.Error(), "math-pre-sat") {
		t.Errorf("invalid: %v", err)
	}
	if _, err := svc.Import(ctx, []Course{fixture("math", "advanced", "a-"), fixture("math", "advanced", "b-")}); !errors.As(err, &invalid) ||
		!strings.Contains(err.Error(), "twice") {
		t.Errorf("twice: %v", err)
	}
	if _, err := svc.Import(ctx, []Course{fixture("math", "advanced", "m-")}); !errors.As(err, &invalid) ||
		!strings.Contains(err.Error(), "already in math-foundation") {
		t.Errorf("a lesson of another course: %v", err)
	}
	if _, err := svc.course(ctx, "math-advanced"); !errors.Is(err, ErrNotFound) {
		t.Errorf("stored after a failed import: %v", err)
	}

	// Coverage: 3 easy, 1 medium, no hard of the math skill.
	addQuestions(t, pool, "math", "Linear functions", "easy", 3)
	addQuestions(t, pool, "math", "linear FUNCTIONS", "medium", 1)
	addQuestions(t, pool, "english", "Linear functions", "hard", 5) // another subject
	report, err := svc.Coverage(ctx)
	if err != nil || len(report) != 2 || report[0].ID != "math-foundation" || report[0].Version != 2 {
		t.Fatalf("coverage: %+v %v", report, err)
	}
	m := report[0]
	// 4 lessons: 4 level checks and 5 homework sets; every level check is short of hard.
	if len(m.Sets) != 9 || m.Short != 4 || len(m.Unknown) != 0 {
		t.Errorf("math coverage: %+v", m)
	}
	lc := m.Sets[0]
	if lc.Set != "level check" || lc.Any != 4 || !lc.Short || len(lc.Need) != 3 ||
		lc.Need[0] != (Need{Difficulty: "easy", Want: 2, Have: 3}) || lc.Need[2] != (Need{Difficulty: "hard", Want: 1, Have: 0}) {
		t.Errorf("a level check: %+v", lc)
	}
	if hw := m.Sets[1]; hw.Set != "homework 1" || hw.Short || hw.Need[0] != (Need{Difficulty: "easy", Want: 2, Have: 3}) {
		t.Errorf("a homework set: %+v", hw)
	}
	if e := report[1]; e.ID != "english-foundation" || e.Short != 9 || len(e.Unknown) != 1 || e.Unknown[0] != "Words in Context" {
		t.Errorf("english coverage: %+v", e)
	}
}
