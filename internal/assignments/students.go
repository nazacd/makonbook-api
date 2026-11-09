package assignments

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nazacd/makonbook-api/internal/assignments/assignmentsdb"
	"github.com/nazacd/makonbook-api/internal/roadmap"
)

// Students' side (spec 23.3).

// StudentAssignment is an assignment as a student of the group sees it.
type StudentAssignment struct {
	ID           string     `json:"id"`
	Kind         string     `json:"kind"`
	Status       string     `json:"status"` // open, overdue or closed
	Title        string     `json:"title"`
	Instructions string     `json:"instructions"`
	Subject      string     `json:"subject"`
	GroupID      int32      `json:"group_id"`
	GroupName    string     `json:"group_name"`
	Lesson       *Lesson    `json:"lesson"`
	Test         *Test      `json:"test"`
	Questions    int        `json:"questions"`
	OpensAt      time.Time  `json:"opens_at"`
	DueAt        time.Time  `json:"due_at"`
	ClosedAt     *time.Time `json:"closed_at"`
	Mine         Standing   `json:"mine"`
}

// Mine lists the assignments of the student's current groups once open,
// soonest deadline first: open and overdue ones, and closed ones the
// student has work on.
func (s *Service) Mine(ctx context.Context, student int32, day time.Time) ([]StudentAssignment, error) {
	out := []StudentAssignment{}
	q := assignmentsdb.New(s.pool)
	groups, err := q.StudentGroupIDs(ctx, assignmentsdb.StudentGroupIDsParams{Student: student, Today: date(day)})
	if err != nil || len(groups) == 0 {
		return out, err
	}
	rows, err := q.OpenedAssignments(ctx, groups)
	if err != nil {
		return nil, err
	}
	f, err := s.gather(ctx, rows, day, []int32{student})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		mine := standing(r, f, student)
		if r.ClosedAt.Valid && !mine.hasWork {
			continue
		}
		a, err := s.view(ctx, r, f)
		if err != nil {
			return nil, err
		}
		out = append(out, StudentAssignment{
			ID: a.ID, Kind: a.Kind, Status: a.Status, Title: a.Title, Instructions: a.Instructions, Subject: a.Subject,
			GroupID: a.GroupID, GroupName: a.GroupName, Lesson: a.Lesson, Test: a.Test, Questions: a.Questions,
			OpensAt: a.OpensAt, DueAt: a.DueAt, ClosedAt: a.ClosedAt, Mine: mine,
		})
	}
	return out, nil
}

// enrolled reads an assignment of one of the student's current groups.
func (s *Service) enrolled(ctx context.Context, student int32, id string, day time.Time) (assignmentsdb.Assignment, error) {
	u, ok := parseUUID(id)
	if !ok {
		return assignmentsdb.Assignment{}, ErrNotFound
	}
	q := assignmentsdb.New(s.pool)
	r, err := q.GetAssignment(ctx, u)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	groups, err := q.StudentGroupIDs(ctx, assignmentsdb.StudentGroupIDsParams{Student: student, Today: date(day)})
	if err != nil {
		return r, err
	}
	for _, g := range groups {
		if g == r.GroupID {
			return r, nil
		}
	}
	return r, ErrNotFound
}

// Start returns the student's open assessment for an assigned homework or
// level check, or a new one with its questions. Work in progress can still
// be finished after the assignment closes; a new start can't.
func (s *Service) Start(ctx context.Context, student int32, id string, day time.Time) (roadmap.Assessment, error) {
	r, err := s.enrolled(ctx, student, id, day)
	if err != nil {
		return roadmap.Assessment{}, err
	}
	if r.Kind == KindExam {
		return roadmap.Assessment{}, ErrNotAnAssessment
	}
	f, err := s.gather(ctx, []assignmentsdb.Assignment{r}, day, []int32{student})
	if err != nil {
		return roadmap.Assessment{}, err
	}
	open := false
	for _, w := range f.work[uuidString(r.ID)][student] {
		open = open || w.finished == nil
	}
	if !open {
		switch {
		case s.now().Before(r.OpensAt.Time):
			return roadmap.Assessment{}, ErrNotOpen
		case r.ClosedAt.Valid:
			return roadmap.Assessment{}, ErrClosed
		}
		ids := idStrings(r.QuestionIds)
		bank, err := s.practice.BankQuestions(ctx, ids)
		if err != nil {
			return roadmap.Assessment{}, err
		}
		if len(bank) == 0 {
			return roadmap.Assessment{}, roadmap.ErrNoQuestions
		}
	}
	set := roadmap.Assigned{ID: uuidString(r.ID), Kind: r.Kind, Course: textPtr(r.Course), LessonID: textPtr(r.LessonID),
		QuestionIDs: idStrings(r.QuestionIds)}
	if r.HomeworkIndex.Valid {
		i := int(r.HomeworkIndex.Int16)
		set.HomeworkIndex = &i
	}
	return s.roadmap.StartAssigned(ctx, student, set)
}
