package sync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nazacd/makonbook-api/internal/hollihop"
	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/testdb"
	"github.com/nazacd/makonbook-api/internal/sync/syncdb"
)

var tashkent = mustLoad("Asia/Tashkent")

func mustLoad(name string) *time.Location {
	l, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return l
}

func TestFullDue(t *testing.T) {
	at := 3 * time.Hour
	now := time.Date(2026, 10, 10, 10, 0, 0, 0, tashkent) // 10:00 local
	cases := []struct {
		name string
		last time.Time
		want bool
	}{
		{"never ran", time.Time{}, true},
		{"ran today after 03:00", time.Date(2026, 10, 10, 3, 5, 0, 0, tashkent), false},
		{"ran yesterday", time.Date(2026, 10, 9, 3, 5, 0, 0, tashkent), true},
		{"ran today before 03:00", time.Date(2026, 10, 10, 2, 0, 0, 0, tashkent), true},
	}
	for _, c := range cases {
		if got := fullDue(now, c.last, at, tashkent); got != c.want {
			t.Errorf("%s: fullDue = %v, want %v", c.name, got, c.want)
		}
	}
	// Before 03:00 the relevant slot is yesterday's.
	early := time.Date(2026, 10, 10, 1, 0, 0, 0, tashkent)
	if fullDue(early, time.Date(2026, 10, 9, 3, 30, 0, 0, tashkent), at, tashkent) {
		t.Error("01:00: a full run done after yesterday's 03:00 should not be due")
	}
}

func TestGuard(t *testing.T) {
	g := DefaultGuard
	ids := func(n int) []int32 { return make([]int32, n) }
	cases := []struct {
		res  people.Result
		want bool
	}{
		{people.Result{ActiveStudentsBefore: 1000, DeactivatedStudents: ids(4)}, false},   // below MinCount
		{people.Result{ActiveStudentsBefore: 1000, DeactivatedStudents: ids(100)}, false}, // exactly 10 %
		{people.Result{ActiveStudentsBefore: 1000, DeactivatedStudents: ids(101)}, true},
		{people.Result{ActiveTeachersBefore: 48, DeactivatedTeachers: ids(5)}, true},
		{people.Result{ActiveTeachersBefore: 0}, false}, // first run
	}
	for i, c := range cases {
		err := g.Check(c.res)
		if (err != nil) != c.want || (err != nil && !errors.Is(err, ErrBlocked)) {
			t.Errorf("case %d: Check = %v, want blocked=%v", i, err, c.want)
		}
	}
}

func TestBuildSnapshotSkipsBadRowsAndMergesOffices(t *testing.T) {
	snap, skipped := buildSnapshot(fetched{
		units: []hollihop.EdUnit{{
			ID: 1, OfficeOrCompanyID: 3, OfficeOrCompanyName: "NOVZA", OfficeTimeZone: "+05:00",
			ScheduleItems: []hollihop.ScheduleItem{
				{ID: 10, BeginDate: "2026-09-01", BeginTime: "14:00", EndTime: "15:30", TeacherIDs: []int32{7}},
				{ID: 11, BeginDate: "01/09/2026", BeginTime: "14:00", EndTime: "15:30"},
			},
		}},
		students: []hollihop.Student{{ClientID: 5, Mobile: " +998901234567 ",
			OfficesAndCompanies: []hollihop.Office{{ID: 3, Name: "NOVZA"}, {ID: 9, Name: "BUXORO"}}}},
		enrollments: []hollihop.EdUnitStudent{
			{EdUnitID: 1, StudentClientID: 5, EndDate: "2026-12-31T00:00:00"},
			{EdUnitID: 1, StudentClientID: 6, BeginDate: "soon"},
		},
	}, time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC))

	if len(skipped) != 2 {
		t.Errorf("skipped = %v", skipped)
	}
	if len(snap.Groups[0].Schedule) != 1 || snap.Groups[0].Schedule[0].BeginTime != 14*time.Hour {
		t.Errorf("schedule = %+v", snap.Groups[0].Schedule)
	}
	if len(snap.Offices) != 2 {
		t.Errorf("offices = %+v", snap.Offices)
	}
	for _, o := range snap.Offices {
		if o.ID == 3 && o.UTCOffset != "+05:00" {
			t.Error("student office entry overwrote the group's UTC offset")
		}
	}
	if snap.Students[0].PhoneRaw != "+998901234567" {
		t.Errorf("phone not trimmed: %q", snap.Students[0].PhoneRaw)
	}
	if e := snap.Enrollments[0]; !e.BeginDate.IsZero() || e.EndDate.Format(time.DateOnly) != "2026-12-31" {
		t.Errorf("enrollment dates = %v – %v", e.BeginDate, e.EndDate)
	}
}

// --- integration ------------------------------------------------------------

type fakeSource struct {
	teachers    []hollihop.Teacher
	units       []hollihop.EdUnit
	students    []hollihop.Student
	enrollments []hollihop.EdUnitStudent
	lessonDays  []hollihop.EdUnitDays
	studentDays []hollihop.EdUnitStudentDays
	now         string
	err         error

	gotSince []string
	gotDays  [][2]string
	gotRecs  [][2]string
}

func (f *fakeSource) GetTeachers(context.Context) ([]hollihop.Teacher, error) {
	return f.teachers, f.err
}
func (f *fakeSource) GetEdUnits(context.Context) ([]hollihop.EdUnit, error) { return f.units, nil }
func (f *fakeSource) GetEdUnitStudents(context.Context) ([]hollihop.EdUnitStudent, error) {
	return f.enrollments, nil
}

func (f *fakeSource) GetLessonDays(_ context.Context, from, to string) ([]hollihop.EdUnitDays, error) {
	f.gotDays = append(f.gotDays, [2]string{from, to})
	return f.lessonDays, nil
}

func (f *fakeSource) GetStudentDays(_ context.Context, from, to string) ([]hollihop.EdUnitStudentDays, error) {
	f.gotRecs = append(f.gotRecs, [2]string{from, to})
	return f.studentDays, nil
}

func (f *fakeSource) GetStudents(_ context.Context, since string) ([]hollihop.Student, string, error) {
	f.gotSince = append(f.gotSince, since)
	return f.students, f.now, nil
}

type fakeNotifier struct{ msgs []string }

func (n *fakeNotifier) Notify(_ context.Context, msg string) error {
	n.msgs = append(n.msgs, msg)
	return nil
}

func newFixture(nStudents int) *fakeSource {
	f := &fakeSource{
		now:      "2026-10-10T10:00:00",
		teachers: []hollihop.Teacher{{ID: 10, FirstName: "Ali", LastName: "Valiyev", Mobile: "+998901000010"}},
		units: []hollihop.EdUnit{{ID: 100, Name: "G", LearningType: "GROUP", OfficeOrCompanyID: 3, OfficeOrCompanyName: "NOVZA",
			ScheduleItems: []hollihop.ScheduleItem{{ID: 1, BeginDate: "2026-09-01", BeginTime: "14:00", EndTime: "15:30", Weekdays: 21, TeacherIDs: []int32{10}}}}},
		lessonDays: []hollihop.EdUnitDays{{ID: 100, Days: []hollihop.LessonDay{
			{Date: "2026-10-09", Minutes: 90, ScheduleItemIDs: []int32{1}},
			{Date: "2026-10-12", Minutes: 90, ScheduleItemIDs: []int32{1}},
		}}},
	}
	for i := int32(1); i <= int32(nStudents); i++ {
		f.students = append(f.students, hollihop.Student{ID: 500 + i, ClientID: i, FirstName: "S", LastName: "S",
			Mobile: fmt.Sprintf("+99890200%04d", i), StatusID: people.StatusStudying})
		f.enrollments = append(f.enrollments, hollihop.EdUnitStudent{EdUnitID: 100, StudentClientID: i, Weekdays: 21, Status: "Normal"})
		f.studentDays = append(f.studentDays, hollihop.EdUnitStudentDays{EdUnitID: 100, StudentClientID: i, Days: []hollihop.StudentDay{
			{Date: "2026-10-07T00:00:00", Pass: false}, {Date: "2026-10-09", Pass: i == 1},
		}})
	}
	return f
}

func newRunner(pool *pgxpool.Pool, src Source, n *fakeNotifier) *Runner {
	return &Runner{
		Source:   src,
		Pool:     pool,
		Notifier: n,
		Location: tashkent,
		Guard:    DefaultGuard,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:      func() time.Time { return time.Date(2026, 10, 10, 10, 0, 0, 0, tashkent) },
	}
}

func lastRun(t *testing.T, pool *pgxpool.Pool) (status, errText string) {
	t.Helper()
	err := pool.QueryRow(t.Context(), "SELECT status, error FROM sync_runs ORDER BY id DESC LIMIT 1").Scan(&status, &errText)
	if err != nil {
		t.Fatal(err)
	}
	return status, errText
}

func activeStudents(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM students WHERE deactivated_at IS NULL").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRunLifecycle(t *testing.T) {
	pool := testdb.New(t)
	src := newFixture(20)
	notes := &fakeNotifier{}
	r := newRunner(pool, src, notes)
	ctx := t.Context()

	// 1. First run asked for incremental, but there is no cursor yet.
	rep, err := r.Run(ctx, ModeIncremental, Options{})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if rep.Mode != ModeFull || src.gotSince[0] != "" || rep.People.ActiveStudents != 20 || rep.People.LoginPhones != 21 {
		t.Errorf("first run: mode=%s since=%q %+v", rep.Mode, src.gotSince[0], rep.People)
	}

	// 2. Incremental run passes the stored cursor.
	src.now = "2026-10-10T10:15:00"
	if _, err := r.Run(ctx, ModeIncremental, Options{}); err != nil {
		t.Fatal(err)
	}
	if src.gotSince[1] != "2026-10-10T10:00:00" {
		t.Errorf("incremental since = %q", src.gotSince[1])
	}

	// 3. HolliHop suddenly reports most students as gone: blocked, nothing
	// written, cursor unchanged, admins alerted once.
	for i := range src.students[:15] {
		src.students[i].StatusID = 3
	}
	src.now = "2026-10-10T10:30:00"
	for range 2 {
		if _, err := r.Run(ctx, ModeIncremental, Options{}); !errors.Is(err, ErrBlocked) {
			t.Fatalf("mass change: err = %v, want ErrBlocked", err)
		}
	}
	if status, msg := lastRun(t, pool); status != StatusBlocked || !strings.Contains(msg, "15 of 20 active students") {
		t.Errorf("last run = %s %q", status, msg)
	}
	if n := activeStudents(t, pool); n != 20 {
		t.Errorf("blocked run changed data: %d active", n)
	}
	var cursor string
	_ = pool.QueryRow(ctx, "SELECT value FROM sync_cursors").Scan(&cursor)
	if cursor != "2026-10-10T10:15:00" {
		t.Errorf("blocked run moved the cursor to %q", cursor)
	}
	if len(notes.msgs) != 1 || !strings.Contains(notes.msgs[0], "BLOCKED") {
		t.Errorf("alerts = %q", notes.msgs)
	}

	// 4. An admin confirms the change.
	if _, err := r.Run(ctx, ModeIncremental, Options{AllowMassChange: true}); err != nil {
		t.Fatal(err)
	}
	if n := activeStudents(t, pool); n != 5 {
		t.Errorf("after allowed mass change: %d active, want 5", n)
	}
	if len(notes.msgs) != 2 || !strings.Contains(notes.msgs[1], "recovered") {
		t.Errorf("alerts = %q", notes.msgs)
	}

	// 5. HolliHop is down: failed run, one alert, then recovery.
	src.err = errors.New("hollihop GetTeachers: status 502")
	for range 3 {
		if _, err := r.Run(ctx, ModeIncremental, Options{}); err == nil {
			t.Fatal("expected failure")
		}
	}
	if status, _ := lastRun(t, pool); status != StatusFailed {
		t.Errorf("status = %s", status)
	}
	src.err = nil
	if _, err := r.Run(ctx, ModeIncremental, Options{}); err != nil {
		t.Fatal(err)
	}
	if len(notes.msgs) != 4 || !strings.Contains(notes.msgs[2], "FAILED") || !strings.Contains(notes.msgs[3], "recovered") {
		t.Errorf("alerts = %q", notes.msgs)
	}
}

func TestRunRefusesEmptyResponses(t *testing.T) {
	pool := testdb.New(t)
	src := newFixture(3)
	src.teachers = nil
	_, err := newRunner(pool, src, &fakeNotifier{}).Run(t.Context(), ModeFull, Options{})
	if err == nil || !strings.Contains(err.Error(), "empty list") {
		t.Errorf("err = %v", err)
	}
}

func TestRunsDoNotOverlap(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()

	holder, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	if ok, err := syncdb.New(holder).TryLock(ctx, lockKey); err != nil || !ok {
		t.Fatalf("take lock: %v %v", ok, err)
	}

	_, err = newRunner(pool, newFixture(3), &fakeNotifier{}).Run(ctx, ModeFull, Options{})
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("err = %v, want ErrAlreadyRunning", err)
	}
}

func lessonDates(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var s string
	err := pool.QueryRow(t.Context(), "SELECT coalesce(string_agg(to_char(date, 'MM-DD'), ',' ORDER BY date), '') FROM lesson_days").Scan(&s)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRunMirrorsLessonDays(t *testing.T) {
	pool := testdb.New(t)
	src := newFixture(3)
	r := newRunner(pool, src, &fakeNotifier{})
	ctx := t.Context()

	// A day outside the window (or of a group the sync doesn't know) is ignored.
	src.lessonDays = append(src.lessonDays, hollihop.EdUnitDays{ID: 999, Days: []hollihop.LessonDay{{Date: "2026-10-12", Minutes: 90}}})
	src.lessonDays[0].Days = append(src.lessonDays[0].Days, hollihop.LessonDay{Date: "2026-12-01", Minutes: 90})
	rep, err := r.Run(ctx, ModeFull, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := src.gotDays[0]; got != [2]string{"2026-10-03", "2026-10-24"} {
		t.Errorf("asked for days %v", got)
	}
	if d := lessonDates(t, pool); d != "10-09,10-12" {
		t.Errorf("lesson days = %q", d)
	}
	if rep.Fetched.LessonDays != 4 {
		t.Errorf("fetched %d lesson days", rep.Fetched.LessonDays)
	}

	// The office cancels Monday and adds Tuesday.
	src.lessonDays = []hollihop.EdUnitDays{{ID: 100, Days: []hollihop.LessonDay{
		{Date: "2026-10-09", Minutes: 90, ScheduleItemIDs: []int32{1}},
		{Date: "2026-10-13", Minutes: 60, ScheduleItemIDs: []int32{1}},
	}}}
	if _, err := r.Run(ctx, ModeIncremental, Options{}); err != nil {
		t.Fatal(err)
	}
	if d := lessonDates(t, pool); d != "10-09,10-13" {
		t.Errorf("after cancellation: lesson days = %q", d)
	}

	// An empty answer keeps what we have instead of wiping it.
	src.lessonDays = nil
	if _, err := r.Run(ctx, ModeIncremental, Options{}); err != nil {
		t.Fatal(err)
	}
	if d := lessonDates(t, pool); d != "10-09,10-13" {
		t.Errorf("after empty answer: lesson days = %q", d)
	}
}

func TestRunMirrorsAttendanceRecords(t *testing.T) {
	pool := testdb.New(t)
	src := newFixture(3)
	r := newRunner(pool, src, &fakeNotifier{})
	ctx := t.Context()

	// Records of someone MakonBook doesn't store are skipped.
	src.studentDays = append(src.studentDays, hollihop.EdUnitStudentDays{EdUnitID: 100, StudentClientID: 999,
		Days: []hollihop.StudentDay{{Date: "2026-10-09", Pass: true}}})
	rep, err := r.Run(ctx, ModeFull, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := src.gotRecs[0]; got != [2]string{"2026-06-12", "2026-10-10"} {
		t.Errorf("full run asked for records %v", got)
	}
	var stored, absent int
	_ = pool.QueryRow(ctx, "SELECT count(*), count(*) FILTER (WHERE pass) FROM attendance_records").Scan(&stored, &absent)
	if stored != 6 || absent != 1 || rep.Fetched.AttendanceRecords != 7 {
		t.Errorf("stored %d (%d absent), fetched %d", stored, absent, rep.Fetched.AttendanceRecords)
	}

	// An admin excuses student 1 on the 9th; the incremental run looks a week back.
	src.studentDays[0].Days[1].Pass = false
	if _, err := r.Run(ctx, ModeIncremental, Options{}); err != nil {
		t.Fatal(err)
	}
	if got := src.gotRecs[1]; got != [2]string{"2026-10-03", "2026-10-10"} {
		t.Errorf("incremental run asked for records %v", got)
	}
	_ = pool.QueryRow(ctx, "SELECT count(*) FILTER (WHERE pass) FROM attendance_records").Scan(&absent)
	if absent != 0 {
		t.Errorf("correction not applied: %d absent", absent)
	}
}
