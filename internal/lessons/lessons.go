// Package lessons holds the lesson days mirrored from HolliHop and answers
// "which lessons does this teacher have on this date" (spec 8.1).
package lessons

import (
	"context"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nazacd/makonbook-api/internal/lessons/lessondb"
	"github.com/nazacd/makonbook-api/internal/platform/scope"
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
	// The lesson's attendance session, once a teacher opened one.
	Session *SessionRef `json:"session"`
}

// SessionRef points at a lesson's attendance session.
type SessionRef struct {
	ID     string `json:"id"`
	Status string `json:"status"` // "open" or "finished"
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
			Session:      sessionRef(r.SessionID, r.SessionFinished),
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

// NewGroupDays: a group whose schedule began this recently, and that nobody
// has attended yet, is newly opened — an empty list is expected there.
const NewGroupDays = 30

// OfficeLesson is a lesson as the admin panel lists it: any teacher's, with
// the attendance counts once a session exists.
type OfficeLesson struct {
	Lesson
	OfficeID *int32      `json:"office_id"`
	Teachers []string    `json:"teachers"`
	Counts   *MarkCounts `json:"counts"` // null until attendance is opened
	// NewGroup: opened recently and nobody attended yet. Only such groups
	// are listed with no students; other empty groups are finished in
	// practice and appear under GroupIssues instead.
	NewGroup bool `json:"new_group"`
}

// MarkCounts counts a session's marks.
type MarkCounts struct {
	Present int32 `json:"present"`
	Late    int32 `json:"late"`
	Absent  int32 `json:"absent"`
	Pending int32 `json:"pending"`
}

// OfficeLessons lists every lesson on day in the given branches, by branch
// and start time.
func (s *Service) OfficeLessons(ctx context.Context, offices scope.Offices, day time.Time) ([]OfficeLesson, error) {
	rows, err := s.q.OfficeLessons(ctx, lessondb.OfficeLessonsParams{
		Date: date(day), AllOffices: offices.All, OfficeIds: offices.List(), NewGroupDays: NewGroupDays,
	})
	if err != nil {
		return nil, fmt.Errorf("office lessons: %w", err)
	}
	out := make([]OfficeLesson, 0, len(rows))
	for _, r := range rows {
		if r.Students == 0 && !r.NewGroup && !r.SessionID.Valid {
			continue // nobody on the list of an old group: see GroupIssues
		}
		l := OfficeLesson{
			Lesson: Lesson{
				Session:      sessionRef(r.SessionID, r.SessionFinished),
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
			},
			Teachers: r.Teachers,
			NewGroup: r.NewGroup,
		}
		if r.OfficeID.Valid {
			id := r.OfficeID.Int32
			l.OfficeID = &id
		}
		if l.Teachers == nil {
			l.Teachers = []string{}
		}
		if l.Session != nil {
			l.Counts = &MarkCounts{Present: r.Present, Late: r.Late, Absent: r.Absent, Pending: r.Pending}
		}
		out = append(out, l)
	}
	return out, nil
}

// Group problems admins should fix in HolliHop.
const (
	// IssueNoTeacher: lessons are scheduled, but no teacher on the schedule
	// can take attendance (none, or only fired or unknown ones).
	IssueNoTeacher = "no_teacher"
	// IssueNoStudents: lessons are scheduled for a group that isn't new, and
	// nobody is on the list — in practice a finished group whose schedule
	// was never closed.
	IssueNoStudents = "no_students"
)

// GroupIssue is a group whose HolliHop entry needs an admin.
type GroupIssue struct {
	Kind          string   `json:"kind"` // IssueNoTeacher or IssueNoStudents
	GroupID       int32    `json:"group_id"`
	GroupName     string   `json:"group_name"`
	OfficeID      *int32   `json:"office_id"`
	Office        string   `json:"office"`
	Individual    bool     `json:"individual"`    // one-to-one or pair, not a group lesson
	Teachers      []string `json:"teachers"`      // who can take attendance (empty for no_teacher)
	FiredTeacher  bool     `json:"fired_teacher"` // no_teacher: the schedule names only fired or unknown teachers
	Students      int32    `json:"students"`      // on the list of the next lesson
	NextLesson    string   `json:"next_lesson"`
	ScheduleSince *string  `json:"schedule_since"` // the group's earliest schedule start
	LastAttended  *string  `json:"last_attended"`  // last lesson anyone attended (HolliHop, last 120 days)
}

// GroupIssues lists groups with lessons from today on that need an admin in
// HolliHop, by branch and name.
func (s *Service) GroupIssues(ctx context.Context, offices scope.Offices, today time.Time) ([]GroupIssue, error) {
	rows, err := s.q.GroupIssues(ctx, lessondb.GroupIssuesParams{Today: date(today), AllOffices: offices.All, OfficeIds: offices.List()})
	if err != nil {
		return nil, fmt.Errorf("group issues: %w", err)
	}
	out := []GroupIssue{}
	for _, r := range rows {
		since, last := dateString(r.ScheduleSince), dateString(r.LastAttended)
		newGroup := last == nil && since != nil && !r.ScheduleSince.Time.Before(today.AddDate(0, 0, -NewGroupDays))
		kind := ""
		switch {
		case len(r.Teachers) == 0:
			kind = IssueNoTeacher
		case r.Students == 0 && !newGroup:
			kind = IssueNoStudents
		default:
			continue
		}
		is := GroupIssue{
			Kind: kind, GroupID: r.GroupID, GroupName: r.GroupName, Office: r.Office,
			Individual: r.LearningType != "GROUP", Teachers: r.Teachers, FiredTeacher: len(r.Teachers) == 0 && r.ScheduledTeachers > 0,
			Students: r.Students, NextLesson: r.NextLesson.Time.Format(time.DateOnly), ScheduleSince: since, LastAttended: last,
		}
		if is.Teachers == nil {
			is.Teachers = []string{}
		}
		if r.OfficeID.Valid {
			id := r.OfficeID.Int32
			is.OfficeID = &id
		}
		out = append(out, is)
	}
	return out, nil
}

func dateString(d pgtype.Date) *string {
	if !d.Valid {
		return nil
	}
	s := d.Time.Format(time.DateOnly)
	return &s
}

func sessionRef(id pgtype.UUID, finished bool) *SessionRef {
	if !id.Valid {
		return nil
	}
	ref := &SessionRef{ID: uuidString(id.Bytes), Status: "open"}
	if finished {
		ref.Status = "finished"
	}
	return ref
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

func uuidString(b [16]byte) string {
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
