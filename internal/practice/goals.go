package practice

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nazacd/makonbook-api/internal/practice/practicedb"
)

// Goals (spec 9.6): a student's target score per section and the exam date,
// for the web app's dashboard.

const dateLayout = "2006-01-02"

// GoalsError is a goal that can't be stored.
type GoalsError struct{ Reason string }

func (e *GoalsError) Error() string { return e.Reason }

// Goals is a student's goals; every field is null until set.
type Goals struct {
	TargetMath *int `json:"target_math"`
	TargetRW   *int `json:"target_rw"`
	// TargetTotal is the two targets' sum, when both are set.
	TargetTotal *int       `json:"target_total"`
	ExamDate    *string    `json:"exam_date"` // YYYY-MM-DD
	UpdatedAt   *time.Time `json:"updated_at"`
}

// GoalsInput is what the student sets; each may be null (none).
type GoalsInput struct {
	TargetMath *int    `json:"target_math"`
	TargetRW   *int    `json:"target_rw"`
	ExamDate   *string `json:"exam_date"`
}

func goalsOf(r practicedb.StudentGoal) Goals {
	g := Goals{}
	if r.TargetMath.Valid {
		v := int(r.TargetMath.Int16)
		g.TargetMath = &v
	}
	if r.TargetRw.Valid {
		v := int(r.TargetRw.Int16)
		g.TargetRW = &v
	}
	if g.TargetMath != nil && g.TargetRW != nil {
		total := *g.TargetMath + *g.TargetRW
		g.TargetTotal = &total
	}
	if r.ExamDate.Valid {
		d := r.ExamDate.Time.Format(dateLayout)
		g.ExamDate = &d
	}
	if r.UpdatedAt.Valid {
		t := r.UpdatedAt.Time.UTC()
		g.UpdatedAt = &t
	}
	return g
}

// Goals returns the student's goals (all null if never set).
func (s *Service) Goals(ctx context.Context, student int32) (Goals, error) {
	r, err := practicedb.New(s.pool).Goals(ctx, student)
	if errors.Is(err, pgx.ErrNoRows) {
		return Goals{}, nil
	}
	if err != nil {
		return Goals{}, err
	}
	return goalsOf(r), nil
}

// SetGoals replaces the student's goals. Targets are 200–800 in steps of 10;
// the exam date can't be before today (the school's date), unless it is the
// one already stored (the exam has passed, and the student changes a target).
func (s *Service) SetGoals(ctx context.Context, student int32, in GoalsInput, today time.Time) (Goals, error) {
	target := func(name string, v *int) (pgtype.Int2, error) {
		if v == nil {
			return pgtype.Int2{}, nil
		}
		if *v < 200 || *v > 800 || *v%10 != 0 {
			return pgtype.Int2{}, &GoalsError{fmt.Sprintf("%s must be 200–800, a multiple of 10, or null", name)}
		}
		return pgtype.Int2{Int16: int16(*v), Valid: true}, nil
	}
	math, err := target("target_math", in.TargetMath)
	if err != nil {
		return Goals{}, err
	}
	rw, err := target("target_rw", in.TargetRW)
	if err != nil {
		return Goals{}, err
	}
	var exam pgtype.Date
	if in.ExamDate != nil {
		d, err := time.Parse(dateLayout, *in.ExamDate)
		if err != nil {
			return Goals{}, &GoalsError{"exam_date must be a date (YYYY-MM-DD) or null"}
		}
		if d.Before(today) {
			stored, err := s.Goals(ctx, student)
			if err != nil {
				return Goals{}, err
			}
			if stored.ExamDate == nil || *stored.ExamDate != d.Format(dateLayout) {
				return Goals{}, &GoalsError{"exam_date is in the past"}
			}
		}
		exam = pgtype.Date{Time: d, Valid: true}
	}
	r, err := practicedb.New(s.pool).SetGoals(ctx, practicedb.SetGoalsParams{
		StudentClientID: student, TargetMath: math, TargetRw: rw, ExamDate: exam,
	})
	if err != nil {
		return Goals{}, err
	}
	return goalsOf(r), nil
}
