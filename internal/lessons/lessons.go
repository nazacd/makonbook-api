// Package lessons holds the lesson days mirrored from HolliHop and answers
// "which lessons does this teacher have on this date" (spec 8.1).
package lessons

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nazacd/makonbook-api/internal/lessons/lessondb"
)

// Window is how far around today the sync mirrors lesson days: back far
// enough for a teacher's 24-hour correction window (spec D13) and a missed
// sync or two, forward far enough to show the coming weeks.
const (
	WindowBack    = 7  // days
	WindowForward = 14 // days
)

// Attendance timing (spec 8.1, D13).
const (
	// OpensBefore: a teacher may start attendance this long before the lesson.
	OpensBefore = 15 * time.Minute
	// ClosesAfter: an open session finishes on its own this long after the end.
	ClosesAfter = 15 * time.Minute
)

// excludedOffices are offices whose lessons never use app attendance:
// online lessons (spec D4).
var excludedOffices = map[string]bool{"ONLINE": true}

// AttendanceEnabled says whether a group's lessons take attendance in the app:
// group lessons only, not one-to-one or online (spec 5, D4).
func AttendanceEnabled(learningType, office string) bool {
	return learningType == "GROUP" && !excludedOffices[strings.ToUpper(strings.TrimSpace(office))]
}

// Day is one lesson day of a group, as HolliHop lists it.
type Day struct {
	GroupID         int32
	Date            time.Time // midnight UTC, the calendar date
	Minutes         int32
	ScheduleItemIDs []int32
}

// ApplyDays replaces the stored lesson days between from and to (inclusive)
// with days. It runs inside the sync's transaction, after people.Apply has
// stored the groups.
func ApplyDays(ctx context.Context, db lessondb.DBTX, from, to time.Time, days []Day) error {
	q := lessondb.New(db)
	if err := q.DeleteDaysBetween(ctx, lessondb.DeleteDaysBetweenParams{DateFrom: date(from), DateTo: date(to)}); err != nil {
		return fmt.Errorf("delete lesson days: %w", err)
	}
	params := make([]lessondb.InsertDayParams, 0, len(days))
	for _, d := range days {
		if d.Date.Before(from) || d.Date.After(to) {
			continue
		}
		ids := d.ScheduleItemIDs
		if ids == nil {
			ids = []int32{}
		}
		params = append(params, lessondb.InsertDayParams{GroupID: d.GroupID, Date: date(d.Date), Minutes: d.Minutes, ScheduleItemIds: ids})
	}
	var err error
	q.InsertDay(ctx, params).Exec(func(_ int, e error) {
		if e != nil && err == nil {
			err = e
		}
	})
	if err != nil {
		return fmt.Errorf("insert lesson days: %w", err)
	}
	return nil
}

// Lesson is one of a teacher's lessons on a date.
type Lesson struct {
	GroupID      int32     `json:"group_id"`
	GroupName    string    `json:"group_name"`
	Discipline   string    `json:"discipline"`
	Office       string    `json:"office"`
	Classroom    string    `json:"classroom"`
	BeginTime    string    `json:"begin_time"` // "14:00", school time
	EndTime      string    `json:"end_time"`
	StartsAt     time.Time `json:"starts_at"`
	EndsAt       time.Time `json:"ends_at"`
	Students     int32     `json:"students"` // roster size on this date
	Attendance   bool      `json:"attendance_enabled"`
	LearningType string    `json:"learning_type"`
}

// Service answers lesson queries.
type Service struct {
	q        *lessondb.Queries
	location *time.Location
}

func NewService(db lessondb.DBTX, location *time.Location) *Service {
	return &Service{q: lessondb.New(db), location: location}
}

// TeacherLessons lists a teacher's lessons on day (a calendar date, midnight
// UTC), earliest first.
func (s *Service) TeacherLessons(ctx context.Context, teacherID int32, day time.Time) ([]Lesson, error) {
	rows, err := s.q.TeacherLessons(ctx, lessondb.TeacherLessonsParams{Date: date(day), TeacherID: teacherID})
	if err != nil {
		return nil, fmt.Errorf("teacher lessons: %w", err)
	}
	out := make([]Lesson, 0, len(rows))
	for _, r := range rows {
		out = append(out, Lesson{
			GroupID:      r.GroupID,
			GroupName:    r.GroupName,
			Discipline:   r.Discipline,
			Office:       r.Office,
			Classroom:    r.Classroom,
			BeginTime:    clock(r.BeginTime),
			EndTime:      clock(r.EndTime),
			StartsAt:     s.at(day, r.BeginTime),
			EndsAt:       s.at(day, r.EndTime),
			Students:     r.Students,
			Attendance:   AttendanceEnabled(r.LearningType, r.Office),
			LearningType: r.LearningType,
		})
	}
	return out, nil
}

// at is the instant of a school-time clock reading on a calendar date.
func (s *Service) at(day time.Time, t pgtype.Time) time.Time {
	m := int(t.Microseconds / 60_000_000)
	y, mo, d := day.Date()
	return time.Date(y, mo, d, m/60, m%60, 0, 0, s.location)
}

// MinutesOf converts HolliHop's lesson length to whole minutes.
func MinutesOf(f float64) int32 {
	if f <= 0 || f > 24*60 {
		return 0
	}
	return int32(math.Round(f))
}

func clock(t pgtype.Time) string {
	m := t.Microseconds / 60_000_000
	return fmt.Sprintf("%02d:%02d", m/60, m%60)
}

func date(t time.Time) pgtype.Date { return pgtype.Date{Time: t, Valid: true} }
