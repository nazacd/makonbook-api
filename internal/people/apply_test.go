package people_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/testdb"
)

func day(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

func apply(t *testing.T, pool *pgxpool.Pool, s people.Snapshot) people.Result {
	t.Helper()
	ctx := t.Context()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	res, err := people.Apply(ctx, tx, s)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return res
}

func baseSnapshot() people.Snapshot {
	return people.Snapshot{
		Today:            day("2026-10-10"),
		StudentsComplete: true,
		Offices:          []people.Office{{ID: 3, Name: "NOVZA", UTCOffset: "+05:00"}},
		Groups: []people.Group{{
			ID: 100, Name: "PRE SAT MATH MWF", Discipline: "PRE SAT MATH", Level: "Средний", LearningType: "GROUP", OfficeID: 3,
			Schedule: []people.ScheduleItem{{
				ID: 1000, BeginDate: day("2026-09-01"), BeginTime: 14 * time.Hour, EndTime: 15*time.Hour + 30*time.Minute,
				Weekdays: 21, Classroom: "Room 1", TeacherIDs: []int32{10},
			}},
		}},
		Teachers: []people.Teacher{
			{ID: 10, FirstName: "Ali", LastName: "Valiyev", PhoneRaw: "+998901000010"},
			{ID: 11, FirstName: "Not", LastName: "Scheduled", PhoneRaw: "+998901000011"}, // no current group
		},
		Students: []people.Student{
			{ClientID: 1, HHStudentID: 501, FirstName: "Aziz", LastName: "Karimov", PhoneRaw: "+998902000001", StatusID: people.StatusStudying, OfficeIDs: []int32{3}},
			{ClientID: 2, HHStudentID: 502, FirstName: "Sibling", LastName: "One", PhoneRaw: "+998902000002", StatusID: people.StatusStudying},
			{ClientID: 3, HHStudentID: 503, FirstName: "Sibling", LastName: "Two", PhoneRaw: "902000002", StatusID: people.StatusStudying},
			{ClientID: 4, HHStudentID: 504, FirstName: "No", LastName: "Phone", StatusID: people.StatusStudying},
			{ClientID: 5, HHStudentID: 505, FirstName: "Graduated", LastName: "Long Ago", PhoneRaw: "+998902000005", StatusID: 3},
		},
		Enrollments: []people.Enrollment{
			{GroupID: 100, StudentClientID: 1, BeginDate: day("2026-09-01"), Weekdays: 21},
			{GroupID: 100, StudentClientID: 2, Weekdays: 21},                                             // open on both ends
			{GroupID: 100, StudentClientID: 3, BeginDate: day("2026-03-01"), EndDate: day("2026-06-01")}, // already over
			{GroupID: 100, StudentClientID: 5, Weekdays: 21},                                             // student never stored
			{GroupID: 999, StudentClientID: 1, Weekdays: 21},                                             // unknown group
		},
	}
}

func scalar[T any](t *testing.T, pool *pgxpool.Pool, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := pool.QueryRow(t.Context(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func TestApplyLifecycle(t *testing.T) {
	pool := testdb.New(t)

	// --- First sync -------------------------------------------------------
	res := apply(t, pool, baseSnapshot())

	if res.ActiveStudents != 4 || res.ActiveTeachers != 1 || res.Enrollments != 2 || res.ScheduleItems != 1 {
		t.Errorf("first run: %+v", res)
	}
	if n := scalar[int](t, pool, "SELECT count(*) FROM students WHERE client_id = 5"); n != 0 {
		t.Error("a never-studying student was stored")
	}
	if n := scalar[int](t, pool, "SELECT count(*) FROM teachers WHERE id = 11"); n != 0 {
		t.Error("an unscheduled teacher was stored")
	}
	if p := scalar[*string](t, pool, "SELECT login_phone FROM students WHERE client_id = 1"); p == nil || *p != "+998902000001" {
		t.Errorf("student 1 login_phone = %v", p)
	}
	if n := scalar[int](t, pool, "SELECT count(*) FROM students WHERE client_id IN (2, 3, 4) AND login_phone IS NOT NULL"); n != 0 {
		t.Error("students with duplicate or missing phones got a login phone")
	}
	if res.Issues[people.IssuePhoneDuplicate] != 2 || res.Issues[people.IssuePhoneMissing] != 1 {
		t.Errorf("issues = %v", res.Issues)
	}

	// --- Second sync: admins fixed a sibling's number, a student left,
	// the teacher was fired, HolliHop renamed the group ------------------
	s := baseSnapshot()
	s.StudentsComplete = false // incremental: only changed students
	s.Students = []people.Student{
		{ClientID: 3, HHStudentID: 503, FirstName: "Sibling", LastName: "Two", PhoneRaw: "+998902000003", StatusID: people.StatusStudying},
		{ClientID: 1, HHStudentID: 501, FirstName: "Aziz", LastName: "Karimov", PhoneRaw: "+998902000001", StatusID: 6}, // paused
	}
	s.Teachers[0].Fired = true
	s.Groups[0].Name = "PRE SAT MATH MWF 14:00"
	res = apply(t, pool, s)

	if len(res.DeactivatedStudents) != 1 || res.DeactivatedStudents[0] != 1 {
		t.Errorf("deactivated students = %v", res.DeactivatedStudents)
	}
	if len(res.DeactivatedTeachers) != 1 || res.ActiveTeachers != 0 {
		t.Errorf("teachers: %+v", res)
	}
	if p := scalar[*string](t, pool, "SELECT login_phone FROM students WHERE client_id = 1"); p != nil {
		t.Error("a deactivated student kept a login phone")
	}
	if n := scalar[int](t, pool, "SELECT count(*) FROM students WHERE client_id IN (2, 3) AND login_phone IS NOT NULL"); n != 2 {
		t.Error("siblings did not get their login phones after the fix")
	}
	if res.IssuesResolved != 2 || res.Issues[people.IssuePhoneDuplicate] != 0 {
		t.Errorf("issues resolved = %d, open = %v", res.IssuesResolved, res.Issues)
	}
	if n := scalar[int](t, pool, "SELECT count(*) FROM data_quality_issues WHERE resolved_at IS NOT NULL"); n != 2 {
		t.Errorf("resolved issue rows = %d, want 2 kept as history", n)
	}
	if name := scalar[string](t, pool, "SELECT name FROM groups WHERE id = 100"); name != "PRE SAT MATH MWF 14:00" {
		t.Errorf("group name = %q", name)
	}

	// --- Third sync (full): student 2 has vanished from HolliHop, student 1
	// is back, the group is gone --------------------------------------------
	s = baseSnapshot()
	s.Students = []people.Student{
		{ClientID: 1, HHStudentID: 501, FirstName: "Aziz", LastName: "Karimov", PhoneRaw: "+998902000001", StatusID: people.StatusStudying},
		{ClientID: 3, HHStudentID: 503, FirstName: "Sibling", LastName: "Two", PhoneRaw: "+998902000003", StatusID: people.StatusStudying},
		{ClientID: 4, HHStudentID: 504, FirstName: "No", LastName: "Phone", StatusID: people.StatusStudying},
	}
	s.Groups = nil
	s.Enrollments = nil
	res = apply(t, pool, s)

	if res.StudentsMissing != 1 || len(res.DeactivatedStudents) != 1 || res.DeactivatedStudents[0] != 2 {
		t.Errorf("missing=%d deactivated=%v", res.StudentsMissing, res.DeactivatedStudents)
	}
	if len(res.ReactivatedStudents) != 1 || res.ReactivatedStudents[0] != 1 {
		t.Errorf("reactivated = %v", res.ReactivatedStudents)
	}
	if res.GroupsRemoved != 1 || res.ScheduleItems != 0 || res.Enrollments != 0 {
		t.Errorf("groups: %+v", res)
	}
	// Nothing is ever hard-deleted.
	if n := scalar[int](t, pool, "SELECT count(*) FROM students"); n != 4 {
		t.Errorf("students rows = %d, want 4", n)
	}
	if n := scalar[int](t, pool, "SELECT count(*) FROM groups WHERE removed_at IS NOT NULL"); n != 1 {
		t.Errorf("removed groups = %d", n)
	}
}

// A run that changes nothing must not touch rows, so updated_at stays
// meaningful ("last changed in HolliHop").
func TestApplyIsIdempotent(t *testing.T) {
	pool := testdb.New(t)
	apply(t, pool, baseSnapshot())
	before := scalar[time.Time](t, pool, "SELECT max(updated_at) FROM students")

	time.Sleep(10 * time.Millisecond)
	res := apply(t, pool, baseSnapshot())

	if after := scalar[time.Time](t, pool, "SELECT max(updated_at) FROM students"); !after.Equal(before) {
		t.Errorf("unchanged students were rewritten: %v -> %v", before, after)
	}
	if len(res.DeactivatedStudents)+len(res.ReactivatedStudents)+res.IssuesResolved != 0 {
		t.Errorf("second identical run changed state: %+v", res)
	}
}
