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
	"github.com/nazacd/makonbook-api/internal/platform/scope"
)

// ErrNotFound means no stored person matches.
var ErrNotFound = errors.New("person not found")

// Person is the view of a student or teacher that other features use.
type Person struct {
	Kind string // KindStudent or KindTeacher
	ID   int32  // students.client_id or teachers.id
	// FirstName and LastName are the names as the person sees their own:
	// the ones they chose (SetNames), else HolliHop's.
	FirstName   string
	LastName    string
	LoginPhone  string // E.164; "" = cannot log in by phone (see data_quality_issues)
	Active      bool
	ActivatedAt time.Time // start of the current active period
	// Registered: a student who signed up themselves and isn't (yet) in
	// HolliHop (spec 21). Everyone else is enrolled.
	Registered bool
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
	p := person(r.Kind, r.ID, r.FirstName, r.LastName, r.LoginPhone, r.DeactivatedAt, r.ActivatedAt)
	p.Registered = r.Source == SourceSelf
	return p, nil
}

// Get returns a stored person, active or not.
func (d *Directory) Get(ctx context.Context, kind string, id int32) (Person, error) {
	switch kind {
	case KindStudent:
		r, err := d.q.GetStudent(ctx, id)
		if err != nil {
			return Person{}, notFound(err)
		}
		p := person(kind, r.ClientID, r.FirstName, r.LastName, r.LoginPhone, r.DeactivatedAt, r.ActivatedAt)
		p.Registered = r.Source == SourceSelf
		return p, nil
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

// CreateSelfStudent stores a student who signed up themselves (spec 21) with
// their names and verified phone (E.164; "" for none yet), active from now.
// db is the sign-up's transaction. A phone another student already logs in
// with fails with a unique violation.
func CreateSelfStudent(ctx context.Context, db peopledb.DBTX, firstName, lastName, phone string) (int32, error) {
	id, err := peopledb.New(db).InsertSelfStudent(ctx, peopledb.InsertSelfStudentParams{
		FirstName: firstName, LastName: lastName, Phone: pgtype.Text{String: phone, Valid: phone != ""}})
	if err != nil {
		return 0, fmt.Errorf("create self-registered student: %w", err)
	}
	return id, nil
}

// SetSelfPhone gives a registered student without a phone their proved
// number (spec 21.4). ErrNotFound if they aren't one (or have a phone).
func SetSelfPhone(ctx context.Context, db peopledb.DBTX, id int32, phone string) error {
	n, err := peopledb.New(db).SetSelfPhone(ctx, peopledb.SetSelfPhoneParams{Phone: phone, ClientID: id})
	if err != nil {
		return fmt.Errorf("set phone: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// MergeSelfStudent folds a registered student into another person (spec
// 21.4), inside the caller's transaction: their attempts, reviews, parked
// questions, reports and (if the other has none) goals move to toStudent; when
// the other is a teacher (toStudent 0) those that can't move are deleted.
// Then the registered student is removed (the rest cascades).
func MergeSelfStudent(ctx context.Context, db peopledb.DBTX, from, toStudent int32) error {
	q := peopledb.New(db)
	if toStudent != 0 {
		for _, move := range []func(context.Context) error{
			func(ctx context.Context) error {
				return q.MoveStudentAttempts(ctx, peopledb.MoveStudentAttemptsParams{ToID: toStudent, FromID: from})
			},
			func(ctx context.Context) error {
				return q.MoveStudentReviews(ctx, peopledb.MoveStudentReviewsParams{ToID: toStudent, FromID: from})
			},
			func(ctx context.Context) error {
				return q.MoveStudentParked(ctx, peopledb.MoveStudentParkedParams{ToID: toStudent, FromID: from})
			},
			func(ctx context.Context) error {
				return q.MoveStudentReports(ctx, peopledb.MoveStudentReportsParams{ToID: toStudent, FromID: from})
			},
			func(ctx context.Context) error {
				return q.MoveStudentGoals(ctx, peopledb.MoveStudentGoalsParams{ToID: toStudent, FromID: from})
			},
		} {
			if err := move(ctx); err != nil {
				return fmt.Errorf("merge %d into %d: %w", from, toStudent, err)
			}
		}
	} else {
		if err := q.DeleteStudentAttempts(ctx, from); err != nil {
			return fmt.Errorf("merge %d: %w", from, err)
		}
		if err := q.DeleteStudentReviews(ctx, from); err != nil {
			return fmt.Errorf("merge %d: %w", from, err)
		}
	}
	n, err := q.DeleteSelfStudent(ctx, from)
	if err != nil {
		return fmt.Errorf("merge %d: remove: %w", from, err)
	}
	if n != 1 {
		return fmt.Errorf("merge %d: not a registered student", from)
	}
	return nil
}

// SetNames sets the names a person chose for themselves; the sync never
// changes them, and the person sees them instead of HolliHop's. nil clears
// one (back to HolliHop's). Names are stored as given: the caller checks them.
func (d *Directory) SetNames(ctx context.Context, kind string, id int32, firstName, lastName *string) error {
	first, last := text(firstName), text(lastName)
	var n int64
	var err error
	switch kind {
	case KindStudent:
		n, err = d.q.SetStudentNames(ctx, peopledb.SetStudentNamesParams{FirstName: first, LastName: last, ClientID: id})
	case KindTeacher:
		n, err = d.q.SetTeacherNames(ctx, peopledb.SetTeacherNamesParams{FirstName: first, LastName: last, ID: id})
	default:
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("set names: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func text(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *s, Valid: true}
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

// OpenIssue is an open data-quality problem, as the admin panel lists it: a
// person who can't sign in until an admin fixes their phone in HolliHop.
type OpenIssue struct {
	ID         int64     `json:"id"`
	PersonKind string    `json:"person_kind"` // student or teacher
	PersonID   int32     `json:"person_id"`   // HolliHop client id or teacher id
	FirstName  string    `json:"first_name"`
	LastName   string    `json:"last_name"`
	Phone      string    `json:"phone"` // as typed in HolliHop
	Kind       string    `json:"kind"`  // phone_missing, phone_invalid, phone_duplicate
	Detail     string    `json:"detail"`
	Since      time.Time `json:"since"`
}

// OpenIssues lists open data-quality issues in the given branches.
func (d *Directory) OpenIssues(ctx context.Context, offices scope.Offices) ([]OpenIssue, error) {
	rows, err := d.q.OpenIssues(ctx, peopledb.OpenIssuesParams{AllOffices: offices.All, OfficeIds: offices.List()})
	if err != nil {
		return nil, fmt.Errorf("open issues: %w", err)
	}
	out := make([]OpenIssue, 0, len(rows))
	for _, r := range rows {
		out = append(out, OpenIssue{
			ID: r.ID, PersonKind: r.PersonKind, PersonID: r.PersonID, FirstName: r.FirstName, LastName: r.LastName,
			Phone: r.PhoneRaw, Kind: r.Kind, Detail: r.Detail, Since: r.FirstSeenAt.Time,
		})
	}
	return out, nil
}
