package demo_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nazacd/makonbook-api/internal/attendance"
	"github.com/nazacd/makonbook-api/internal/auth"
	"github.com/nazacd/makonbook-api/internal/demo"
	"github.com/nazacd/makonbook-api/internal/lessons"
	"github.com/nazacd/makonbook-api/internal/parents"
	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/alert"
	"github.com/nazacd/makonbook-api/internal/platform/scope"
	"github.com/nazacd/makonbook-api/internal/platform/testdb"
	"github.com/nazacd/makonbook-api/internal/practice"
)

const password = "review-me-2026"

var tashkent = time.FixedZone("Asia/Tashkent", 5*60*60)

func service(pool *pgxpool.Pool) *demo.Service {
	codes := parents.NewService(pool, people.NewDirectory(pool), nil, alert.Log{}, "")
	return demo.NewService(pool, practice.NewService(pool, nil), codes, tashkent)
}

// publish stores a published test the size of the real ones: 54 English and
// 44 Math questions, the last few of Math grid-ins.
func publish(t *testing.T, pool *pgxpool.Pool, code string) {
	t.Helper()
	key := func(s string) *string { return &s }
	section := func(subject string, n int) practice.Section {
		var qs []practice.PackageQuestion
		for i := 1; i <= n; i++ {
			q := practice.PackageQuestion{ID: fmt.Sprintf("%s-%d", subject, i), Version: 1, Position: int16(i), Type: "mcq",
				Stem: "Q", Choices: map[string]string{"A": "1", "B": "2", "C": "3", "D": "4"}, Key: key("C")}
			if subject == "math" && i > 38 {
				q.Type, q.Choices, q.Key, q.Accepted = "grid_in", nil, nil, []string{"1/2"}
			}
			qs = append(qs, q)
		}
		return practice.Section{Subject: subject, Modules: []practice.Module{{Module: 1, Minutes: 32, Questions: qs}},
			Scale: practice.Scale(subject, n)}
	}
	pkg, _ := json.Marshal(practice.Package{Format: 1, Code: code, Title: code, Version: 1,
		Sections: []practice.Section{section("english", 54), section("math", 44)}})
	if _, err := pool.Exec(t.Context(), `
		WITH t AS (INSERT INTO practice_tests (code, title) VALUES ($2, $2) RETURNING id)
		INSERT INTO practice_test_versions (test_id, version, content_hash, package, bytes) SELECT id, 1, 'h', $1, 100 FROM t`, pkg, code); err != nil {
		t.Fatal(err)
	}
}

func TestSetupAndRemove(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	svc := service(pool)
	now := time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC) // a Thursday
	today := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)

	if _, err := svc.Setup(ctx, "short", now); err == nil {
		t.Error("a short password was taken")
	}
	res, err := svc.Setup(ctx, password, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Username != demo.Username || len(res.ParentCode) != 5 || len(res.Practice) != 12 {
		t.Errorf("result: %+v", res)
	}

	// Signs in with the username, straight into the app (no forced change).
	authSvc := auth.NewService(pool, people.NewDirectory(pool), auth.NewHasher())
	login, err := authSvc.Login(ctx, "Demo.Student", password, "phone-1")
	if err != nil || login.Principal.MustChangePassword || login.Principal.ID != demo.StudentID {
		t.Fatalf("login: %+v %v", login.Principal, err)
	}
	profile, err := authSvc.Profile(ctx, login.Principal, today)
	if err != nil || len(profile.Groups) != 2 {
		t.Fatalf("profile: %+v %v", profile, err)
	}
	for _, g := range profile.Groups {
		if len(g.Schedule) != 1 || len(g.Schedule[0].Teachers) != 1 || g.Office == "" {
			t.Errorf("group %+v", g)
		}
	}

	// Eight weeks of attendance before today, three lessons a week each.
	table, err := attendance.NewService(pool, nil).StudentTable(ctx, demo.StudentID, today)
	if err != nil || len(table) != 2 {
		t.Fatalf("attendance: %+v %v", table, err)
	}
	for _, g := range table {
		if g.Summary.Lessons != 24 || g.Summary.Absent == 0 || g.Days[0].Date >= today.Format(time.DateOnly) {
			t.Errorf("%s: %+v, latest %s", g.GroupName, g.Summary, g.Days[0].Date)
		}
	}

	// Tests not published: no results; setting up again once they are adds them.
	if len(mustAttempts(t, pool)) != 0 {
		t.Errorf("results without the tests: %q", res.Practice)
	}
	for _, code := range []string{"DAY1", "DAY2", "DAY3", "DAY4", "DAY5", "DAY6", "DAY7", "DAY8", "DAY9"} {
		publish(t, pool, code)
	}
	res2, err := svc.Setup(ctx, password, now.Add(time.Hour))
	if err != nil || res2.ParentCode != res.ParentCode {
		t.Fatalf("setup again: %+v %v", res2, err)
	}
	// Oldest first: up and down, but rising.
	want := []struct {
		test  string
		total int
	}{{"DAY1", 1240}, {"DAY2", 1290}, {"DAY2", 1340}, {"DAY3", 1310}, {"DAY4", 1360}, {"DAY5", 1330},
		{"DAY6", 1390}, {"DAY7", 1370}, {"DAY8", 1380}, {"DAY9", 1410}, {"DAY9", 1520}}
	attempts := mustAttempts(t, pool) // latest first
	if len(attempts) != len(want) {
		t.Fatalf("attempts: %+v", attempts)
	}
	for i, w := range want {
		a := attempts[len(attempts)-1-i]
		if a.Test != w.test || a.Total == nil || *a.Total != w.total || len(a.Answers) != 98 {
			t.Errorf("result %d: %s %v, want %s %d", i, a.Test, a.Total, w.test, w.total)
		}
		if i > 0 && !a.FinishedAt.After(attempts[len(attempts)-i].FinishedAt) {
			t.Errorf("result %d isn't after the one before", i)
		}
	}
	// A day later the results are a day later too, still one of each.
	if _, err := svc.Setup(ctx, password, now.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
	later := mustAttempts(t, pool)
	if len(later) != len(want) || !later[len(later)-1].FinishedAt.Equal(attempts[len(attempts)-1].FinishedAt.AddDate(0, 0, 1)) {
		t.Errorf("the next day: %d attempts, Day 1 finished %v (was %v)", len(later), later[len(later)-1].FinishedAt, attempts[len(attempts)-1].FinishedAt)
	}

	// A reviewer changes the password; the next setup puts it back.
	if err := authSvc.ChangePassword(ctx, login.Principal, password, "something-else-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Setup(ctx, password, now.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := authSvc.Login(ctx, demo.Username, password, "phone-1"); err != nil {
		t.Errorf("login after the reset: %v", err)
	}

	if err := svc.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := authSvc.Login(ctx, demo.Username, password, "phone-1"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("login after remove: %v", err)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM students WHERE client_id < 0) + (SELECT count(*) FROM groups WHERE id < 0)
		+ (SELECT count(*) FROM teachers WHERE id < 0) + (SELECT count(*) FROM offices WHERE id < 0)
		+ (SELECT count(*) FROM practice_attempts WHERE student_client_id < 0)`).Scan(&left); err != nil || left != 0 {
		t.Errorf("left after remove: %d %v", left, err)
	}
}

func mustAttempts(t *testing.T, pool *pgxpool.Pool) []practice.OwnAttempt {
	t.Helper()
	a, err := practice.NewService(pool, nil).StudentAttempts(t.Context(), demo.StudentID)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// The HolliHop sync leaves the demo account alone.
func TestSyncKeepsIt(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	today := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	if _, err := service(pool).Setup(ctx, password, today.Add(9*time.Hour)); err != nil {
		t.Fatal(err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	snap := people.Snapshot{
		Today: today, StudentsComplete: true,
		Offices: []people.Office{{ID: 3, Name: "NOVZA", UTCOffset: "+05:00"}},
		Groups: []people.Group{{ID: 100, Name: "SAT MATH", Discipline: "SAT MATH", LearningType: "GROUP", OfficeID: 3,
			Schedule: []people.ScheduleItem{{ID: 1000, BeginTime: 14 * time.Hour, EndTime: 15 * time.Hour, Weekdays: 21, TeacherIDs: []int32{10}}}}},
		Teachers: []people.Teacher{{ID: 10, FirstName: "Ali", LastName: "Valiyev", PhoneRaw: "+998901000010"}},
		Students: []people.Student{{ClientID: 1, HHStudentID: 501, FirstName: "Aziz", LastName: "Karimov",
			PhoneRaw: "+998902000001", StatusID: people.StatusStudying, OfficeIDs: []int32{3}}},
		Enrollments: []people.Enrollment{{GroupID: 100, StudentClientID: 1}},
	}
	if _, err := people.Apply(ctx, tx, snap); err != nil {
		t.Fatal(err)
	}
	if err := attendance.ApplyRecords(ctx, tx, today.AddDate(0, 0, -attendance.RecordsBackFull), today, nil); err != nil {
		t.Fatal(err)
	}
	if err := lessons.ApplyDays(ctx, tx, today.AddDate(0, 0, -30), today.AddDate(0, 0, 30), nil); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var active, groups, items, enrolled, records, issues, days int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM students WHERE client_id < 0 AND deactivated_at IS NULL AND hh_status_id = 2),
		(SELECT count(*) FROM groups WHERE id < 0 AND removed_at IS NULL),
		(SELECT count(*) FROM schedule_items WHERE group_id < 0),
		(SELECT count(*) FROM enrollments WHERE student_client_id < 0),
		(SELECT count(*) FROM attendance_records WHERE student_client_id < 0),
		(SELECT count(*) FROM data_quality_issues WHERE person_id < 0),
		(SELECT count(*) FROM lesson_days WHERE group_id < 0)`).Scan(&active, &groups, &items, &enrolled, &records, &issues, &days); err != nil {
		t.Fatal(err)
	}
	// The demo student and 10 classmates; Math, English, the open class.
	if active != 11 || groups != 3 || items != 3 || enrolled != 22 || records != 48 || issues != 0 || days == 0 {
		t.Errorf("after the sync: active %d, groups %d, schedule %d, enrolled %d, records %d, issues %d, lesson days %d",
			active, groups, items, enrolled, records, issues, days)
	}
	var teachers int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM teachers WHERE id < 0 AND deactivated_at IS NULL`).Scan(&teachers); err != nil || teachers != 2 {
		t.Errorf("demo teachers after the sync: %d %v", teachers, err)
	}
}

func TestUsernameTaken(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `INSERT INTO credentials (person_kind, person_id, username) VALUES ('student', 7, $1)`, demo.Username); err != nil {
		t.Fatal(err)
	}
	if _, err := service(pool).Setup(ctx, password, time.Now()); !errors.Is(err, demo.ErrUsernameTaken) {
		t.Errorf("setup with the username taken: %v", err)
	}
}

// The demo teacher: their classes, attendance at any hour (never sent to
// HolliHop), their students' practice; admins don't see any of it.
func TestTeacher(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	publish(t, pool, "DAY1")
	publish(t, pool, "DAY2")
	now := time.Now()
	y, m, d := now.In(tashkent).Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	res, err := service(pool).Setup(ctx, password, now)
	if err != nil || res.TeacherUsername != demo.TeacherUsername {
		t.Fatalf("setup: %+v %v", res, err)
	}

	authSvc := auth.NewService(pool, people.NewDirectory(pool), auth.NewHasher())
	login, err := authSvc.Login(ctx, demo.TeacherUsername, password, "phone-2")
	if err != nil || login.Principal.MustChangePassword || login.Principal.Kind != people.KindTeacher {
		t.Fatalf("teacher login: %+v %v", login.Principal, err)
	}
	profile, err := authSvc.Profile(ctx, login.Principal, today)
	if err != nil || len(profile.Groups) != 2 {
		t.Fatalf("teacher's groups: %+v %v", profile.Groups, err)
	}

	lessonsSvc := lessons.NewService(pool, tashkent)
	todays, err := lessonsSvc.TeacherLessons(ctx, demo.TeacherID, today)
	if err != nil {
		t.Fatal(err)
	}
	var open *lessons.Lesson
	for i, l := range todays {
		if l.GroupID == -3 {
			open = &todays[i]
		}
	}
	if open == nil || open.Students != 8 || !open.Attendance {
		t.Fatalf("today's lessons: %+v", todays)
	}

	att := attendance.NewService(pool, lessonsSvc)
	sess, err := att.Open(ctx, demo.TeacherID, -3, today)
	if err != nil || len(sess.Marks) != 8 {
		t.Fatalf("open attendance: %+v %v", sess, err)
	}
	if _, err := att.SetMark(ctx, demo.TeacherID, sess.ID, -2, attendance.StatePresent); err != nil {
		t.Fatal(err)
	}
	if _, err := att.Finish(ctx, demo.TeacherID, sess.ID); err != nil {
		t.Fatal(err)
	}
	var queued int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM hollihop_outbox`).Scan(&queued); err != nil || queued != 0 {
		t.Errorf("queued for HolliHop: %d %v", queued, err)
	}

	group, err := practice.NewService(pool, nil).GroupAttempts(ctx, demo.TeacherID, -1, today)
	if err != nil || len(group) != 7 {
		t.Fatalf("Math group's practice: %+v %v", group, err)
	}
	for _, st := range group {
		if len(st.Attempts) == 0 {
			t.Errorf("%s %s has no results", st.FirstName, st.LastName)
		}
	}

	everywhere := scope.Offices{All: true}
	admin, err := lessonsSvc.OfficeLessons(ctx, everywhere, today)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range admin {
		if l.GroupID < 0 {
			t.Errorf("admins see a demo lesson: %+v", l)
		}
	}
	teachers, corrections, err := att.CorrectionsReport(ctx, everywhere, today.AddDate(0, 0, -1), today, 0)
	if err != nil || len(teachers) != 0 || len(corrections) != 0 {
		t.Errorf("admins see the demo's corrections: %+v %+v %v", teachers, corrections, err)
	}

	// The next day: yesterday's attendance is gone, the lesson can be taken again.
	if _, err := service(pool).Setup(ctx, password, now.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
	var sessions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM attendance_sessions WHERE group_id < 0`).Scan(&sessions); err != nil || sessions != 0 {
		t.Errorf("sessions left the next day: %d %v", sessions, err)
	}
	if err := service(pool).Remove(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := authSvc.Login(ctx, demo.TeacherUsername, password, "phone-2"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("teacher login after remove: %v", err)
	}
}
