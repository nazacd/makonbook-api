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
	"github.com/nazacd/makonbook-api/internal/parents"
	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/alert"
	"github.com/nazacd/makonbook-api/internal/platform/testdb"
	"github.com/nazacd/makonbook-api/internal/practice"
)

const password = "review-me-2026"

var tashkent = time.FixedZone("Asia/Tashkent", 5*60*60)

func service(pool *pgxpool.Pool) *demo.Service {
	codes := parents.NewService(pool, people.NewDirectory(pool), nil, alert.Log{}, "")
	return demo.NewService(pool, practice.NewService(pool, nil), codes, tashkent)
}

// publishDay1 stores a small published DAY1: 8 English and 6 Math questions,
// two of them grid-ins.
func publishDay1(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	key := func(s string) *string { return &s }
	section := func(subject string, n int) practice.Section {
		var qs []practice.PackageQuestion
		for i := 1; i <= n; i++ {
			q := practice.PackageQuestion{ID: fmt.Sprintf("%s-%d", subject, i), Version: 1, Position: int16(i), Type: "mcq",
				Stem: "Q", Choices: map[string]string{"A": "1", "B": "2", "C": "3", "D": "4"}, Key: key("C")}
			if subject == "math" && i > 4 {
				q.Type, q.Choices, q.Key, q.Accepted = "grid_in", nil, nil, []string{"1/2"}
			}
			qs = append(qs, q)
		}
		return practice.Section{Subject: subject, Modules: []practice.Module{{Module: 1, Minutes: 32, Questions: qs}},
			Scale: practice.Scale(subject, n)}
	}
	pkg, _ := json.Marshal(practice.Package{Format: 1, Code: "DAY1", Title: "Day 1", Version: 1,
		Sections: []practice.Section{section("english", 8), section("math", 6)}})
	if _, err := pool.Exec(t.Context(), `
		WITH t AS (INSERT INTO practice_tests (code, title) VALUES ('DAY1', 'Day 1') RETURNING id)
		INSERT INTO practice_test_versions (test_id, version, content_hash, package, bytes) SELECT id, 1, 'h', $1, 100 FROM t`, pkg); err != nil {
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
	if res.Username != demo.Username || len(res.ParentCode) != 5 || res.Practice == "" {
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

	// DAY1 not published: no result, and setting up again once it is adds it.
	if res.Practice == "" || len(mustAttempts(t, pool)) != 0 {
		t.Errorf("practice without DAY1: %q", res.Practice)
	}
	publishDay1(t, pool)
	res2, err := svc.Setup(ctx, password, now.Add(time.Hour))
	if err != nil || res2.ParentCode != res.ParentCode {
		t.Fatalf("setup again: %+v %v", res2, err)
	}
	attempts := mustAttempts(t, pool)
	if len(attempts) != 1 || attempts[0].Total == nil || len(attempts[0].Answers) != 14 {
		t.Fatalf("attempts: %+v", attempts)
	}
	e, m := attempts[0].Scores["english"], attempts[0].Scores["math"]
	if e.Raw == 0 || e.Raw == e.Questions || m.Raw == 0 || m.Raw == m.Questions {
		t.Errorf("scores should be good, not perfect: %+v %+v", e, m)
	}
	if _, err := svc.Setup(ctx, password, now.AddDate(0, 0, 1)); err != nil || len(mustAttempts(t, pool)) != 1 {
		t.Errorf("a third setup: %v, %d attempts", err, len(mustAttempts(t, pool)))
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
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var active, groups, items, enrolled, records, issues int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM students WHERE client_id < 0 AND deactivated_at IS NULL AND hh_status_id = 2),
		(SELECT count(*) FROM groups WHERE id < 0 AND removed_at IS NULL),
		(SELECT count(*) FROM schedule_items WHERE group_id < 0),
		(SELECT count(*) FROM enrollments WHERE student_client_id < 0),
		(SELECT count(*) FROM attendance_records WHERE student_client_id < 0),
		(SELECT count(*) FROM data_quality_issues WHERE person_id < 0)`).Scan(&active, &groups, &items, &enrolled, &records, &issues); err != nil {
		t.Fatal(err)
	}
	if active != 1 || groups != 2 || items != 2 || enrolled != 2 || records != 48 || issues != 0 {
		t.Errorf("after the sync: active %d, groups %d, schedule %d, enrolled %d, records %d, issues %d",
			active, groups, items, enrolled, records, issues)
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
