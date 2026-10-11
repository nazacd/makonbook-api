// Package people owns the mirror of HolliHop people and groups: students,
// teachers, groups, schedules, enrollments, and the data-quality issues that
// stop someone from logging in. The sync feeds it snapshots; other features
// read from it.
package people

import (
	"time"
)

// StatusStudying is HolliHop's student status "Занимается" — the only status
// that grants access (spec 5).
const StatusStudying = 2

// Teacher activity window around "today" (spec 5): a teacher counts as
// teaching while on a schedule item that has started (or starts within
// TeacherLeadDays) and hasn't ended more than TeacherGraceDays ago. The grace
// keeps teachers logged in across short gaps between a group's end and the
// next one's start.
const (
	TeacherLeadDays  = 14
	TeacherGraceDays = 7
)

// Snapshot is what one sync run read from HolliHop, already parsed.
// Dates are midnight UTC; a zero Date means "open-ended".
type Snapshot struct {
	Today       time.Time // in the school's time zone, truncated to the date
	Offices     []Office
	Groups      []Group
	Teachers    []Teacher
	Students    []Student
	Enrollments []Enrollment

	// StudentsComplete is true when Students is the full list (a full pull),
	// so a stored student missing from it has left HolliHop. Incremental
	// pulls only contain students that changed.
	StudentsComplete bool
}

type Office struct {
	ID        int32
	Name      string
	UTCOffset string
}

type Group struct {
	ID           int32
	Name         string
	Discipline   string
	Level        string
	LearningType string
	OfficeID     int32 // 0 = none
	Schedule     []ScheduleItem
}

type ScheduleItem struct {
	ID         int32
	BeginDate  time.Time
	EndDate    time.Time
	BeginTime  time.Duration // since midnight
	EndTime    time.Duration
	Weekdays   int16
	Classroom  string
	TeacherIDs []int32
}

type Teacher struct {
	ID        int32
	FirstName string
	LastName  string
	PhoneRaw  string
	Fired     bool
}

type Student struct {
	ClientID    int32
	HHStudentID int32
	FirstName   string
	LastName    string
	PhoneRaw    string
	StatusID    int32 // 0 = HolliHop sent none
	OfficeIDs   []int32
}

type Enrollment struct {
	GroupID         int32
	StudentClientID int32
	BeginDate       time.Time
	EndDate         time.Time
	Weekdays        int16
}

// ActiveTeacherIDs returns the teachers who may use the app: not fired and on
// a current schedule item (see TeacherLeadDays / TeacherGraceDays).
func ActiveTeacherIDs(s Snapshot) map[int32]bool {
	scheduled := make(map[int32]bool)
	latestStart := s.Today.AddDate(0, 0, TeacherLeadDays)
	earliestEnd := s.Today.AddDate(0, 0, -TeacherGraceDays)
	for _, g := range s.Groups {
		for _, it := range g.Schedule {
			if !it.BeginDate.IsZero() && it.BeginDate.After(latestStart) {
				continue
			}
			if !it.EndDate.IsZero() && it.EndDate.Before(earliestEnd) {
				continue
			}
			for _, id := range it.TeacherIDs {
				scheduled[id] = true
			}
		}
	}

	active := make(map[int32]bool)
	for _, t := range s.Teachers {
		if !t.Fired && scheduled[t.ID] {
			active[t.ID] = true
		}
	}
	return active
}
