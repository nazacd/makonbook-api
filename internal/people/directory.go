package people

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nazacd/makonbook-api/internal/people/peopledb"
)

// ErrNotFound means no stored person matches.
var ErrNotFound = errors.New("person not found")

// Person is the view of a student or teacher that other features use.
type Person struct {
	Kind        string // KindStudent or KindTeacher
	ID          int32  // students.client_id or teachers.id
	FirstName   string
	LastName    string
	LoginPhone  string // E.164; "" = cannot log in by phone (see data_quality_issues)
	Active      bool
	ActivatedAt time.Time // start of the current active period
}

// Directory is the read API of this feature. Other features (auth,
// attendance) use it instead of querying people tables.
type Directory struct {
	q *peopledb.Queries
}

func NewDirectory(db peopledb.DBTX) *Directory {
	return &Directory{q: peopledb.New(db)}
}

// ByLoginPhone finds the person who may log in with an E.164 number.
func (d *Directory) ByLoginPhone(ctx context.Context, phone string) (Person, error) {
	rows, err := d.q.FindByLoginPhone(ctx, phone)
	if err != nil {
		return Person{}, fmt.Errorf("find by login phone: %w", err)
	}
	// The sync never gives one number to two people; treat a violation of
	// that as "nobody" rather than guessing.
	if len(rows) != 1 {
		return Person{}, ErrNotFound
	}
	r := rows[0]
	return person(r.Kind, r.ID, r.FirstName, r.LastName, r.LoginPhone, r.DeactivatedAt, r.ActivatedAt), nil
}

// Get returns a stored person, active or not.
func (d *Directory) Get(ctx context.Context, kind string, id int32) (Person, error) {
	switch kind {
	case KindStudent:
		r, err := d.q.GetStudent(ctx, id)
		if err != nil {
			return Person{}, notFound(err)
		}
		return person(kind, r.ClientID, r.FirstName, r.LastName, r.LoginPhone, r.DeactivatedAt, r.ActivatedAt), nil
	case KindTeacher:
		r, err := d.q.GetTeacher(ctx, id)
		if err != nil {
			return Person{}, notFound(err)
		}
		return person(kind, r.ID, r.FirstName, r.LastName, r.LoginPhone, r.DeactivatedAt, r.ActivatedAt), nil
	default:
		return Person{}, ErrNotFound
	}
}

func person(kind string, id int32, first, last string, phone pgtype.Text, deactivated, activated pgtype.Timestamptz) Person {
	return Person{
		Kind:        kind,
		ID:          id,
		FirstName:   first,
		LastName:    last,
		LoginPhone:  phone.String,
		Active:      !deactivated.Valid,
		ActivatedAt: activated.Time,
	}
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// GroupInfo is a group as shown to its students and teachers.
type GroupInfo struct {
	ID           int32  `json:"id"`
	Name         string `json:"name"`
	Discipline   string `json:"discipline"`
	LearningType string `json:"learning_type"`
	Office       string `json:"office"`
	Schedule     []Slot `json:"schedule"`
}

// Slot is a recurring lesson time of a group.
type Slot struct {
	Weekdays  []string `json:"weekdays"`   // "mon" … "sun"
	BeginTime string   `json:"begin_time"` // "14:00", school time
	EndTime   string   `json:"end_time"`
	Classroom string   `json:"classroom"`
	BeginDate string   `json:"begin_date,omitempty"` // "2026-09-01"; absent = open-ended
	EndDate   string   `json:"end_date,omitempty"`
	Teachers  []string `json:"teachers"`
}

// Groups returns the current and upcoming groups of a student (enrollments)
// or teacher (schedule items), with their schedules.
func (d *Directory) Groups(ctx context.Context, kind string, id int32, today time.Time) ([]GroupInfo, error) {
	day := pgtype.Date{Time: today, Valid: true}
	var groups []GroupInfo
	switch kind {
	case KindStudent:
		rows, err := d.q.StudentGroups(ctx, peopledb.StudentGroupsParams{ClientID: id, Today: day})
		if err != nil {
			return nil, fmt.Errorf("student groups: %w", err)
		}
		for _, r := range rows {
			groups = append(groups, GroupInfo{ID: r.ID, Name: r.Name, Discipline: r.Discipline, LearningType: r.LearningType, Office: r.Office})
		}
	case KindTeacher:
		rows, err := d.q.TeacherGroups(ctx, peopledb.TeacherGroupsParams{TeacherID: id, Today: day})
		if err != nil {
			return nil, fmt.Errorf("teacher groups: %w", err)
		}
		for _, r := range rows {
			groups = append(groups, GroupInfo{ID: r.ID, Name: r.Name, Discipline: r.Discipline, LearningType: r.LearningType, Office: r.Office})
		}
	default:
		return nil, ErrNotFound
	}
	if len(groups) == 0 {
		return []GroupInfo{}, nil
	}

	ids := make([]int32, len(groups))
	index := make(map[int32]int, len(groups))
	for i, g := range groups {
		ids[i], index[g.ID] = g.ID, i
		groups[i].Schedule = []Slot{}
	}
	items, err := d.q.GroupSchedules(ctx, peopledb.GroupSchedulesParams{GroupIds: ids, Today: day})
	if err != nil {
		return nil, fmt.Errorf("group schedules: %w", err)
	}

	var teacherIDs []int32
	for _, it := range items {
		teacherIDs = append(teacherIDs, it.TeacherIds...)
	}
	names := map[int32]string{}
	if len(teacherIDs) > 0 {
		rows, err := d.q.TeacherNames(ctx, teacherIDs)
		if err != nil {
			return nil, fmt.Errorf("teacher names: %w", err)
		}
		for _, r := range rows {
			names[r.ID] = strings.TrimSpace(r.FirstName + " " + r.LastName)
		}
	}

	for _, it := range items {
		slot := Slot{
			Weekdays:  WeekdayNames(it.Weekdays),
			BeginTime: clock(it.BeginTime),
			EndTime:   clock(it.EndTime),
			Classroom: it.Classroom,
			BeginDate: dateString(it.BeginDate),
			EndDate:   dateString(it.EndDate),
			Teachers:  []string{},
		}
		for _, tid := range it.TeacherIds {
			if n, ok := names[tid]; ok {
				slot.Teachers = append(slot.Teachers, n)
			}
		}
		g := &groups[index[it.GroupID]]
		g.Schedule = append(g.Schedule, slot)
	}
	return groups, nil
}

var weekdayNames = [...]string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// WeekdayNames decodes HolliHop's weekday bit set (Mon=1 … Sun=64).
func WeekdayNames(mask int16) []string {
	out := []string{}
	for i, name := range weekdayNames {
		if mask&(1<<i) != 0 {
			out = append(out, name)
		}
	}
	return out
}

func clock(t pgtype.Time) string {
	m := t.Microseconds / 60_000_000
	return fmt.Sprintf("%02d:%02d", m/60, m%60)
}

func dateString(d pgtype.Date) string {
	if !d.Valid {
		return ""
	}
	return d.Time.Format(time.DateOnly)
}
