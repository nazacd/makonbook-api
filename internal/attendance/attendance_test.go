package attendance

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nazacd/makonbook-api/internal/auth"
	"github.com/nazacd/makonbook-api/internal/lessons"
	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/httpx"
	"github.com/nazacd/makonbook-api/internal/platform/testdb"
)

var tashkent, _ = time.LoadLocation("Asia/Tashkent")

func day(s string) time.Time {
	t, _ := time.Parse(time.DateOnly, s)
	return t
}

// at is a time on Monday 2026-10-12 at the school.
func at(hhmm string) time.Time {
	t, _ := time.ParseInLocation("2006-01-02 15:04", "2026-10-12 "+hhmm, tashkent)
	return t
}

// Monday 2026-10-12: teacher 10 has group 100 at 14:00–15:30 and group 101
// at 16:00–17:00, and a one-to-one (102, no app attendance) at 18:00.
// Teacher 11 teaches nothing that day. Students 1, 2, 4, 5 attend group 100
// on Mondays; student 3 only on Fridays.
func seed(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	now := time.Now().In(tashkent)
	item := func(id int32, from, to time.Duration, teacher int32) people.ScheduleItem {
		return people.ScheduleItem{ID: id, BeginDate: day("2026-01-01"), BeginTime: from, EndTime: to,
			Weekdays: 1 | 4 | 16, Classroom: "Room 1", TeacherIDs: []int32{teacher}}
	}
	student := func(id int32) people.Student {
		return people.Student{ClientID: id, FirstName: "S", LastName: string('A' + id),
			PhoneRaw: fmt.Sprintf("+99890200000%d", id), StatusID: people.StatusStudying}
	}
	snap := people.Snapshot{
		Today:            time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC),
		StudentsComplete: true,
		Offices:          []people.Office{{ID: 3, Name: "NOVZA"}},
		Groups: []people.Group{
			{ID: 100, Name: "Pre-SAT Math", Discipline: "PRE SAT MATH", LearningType: "GROUP", OfficeID: 3,
				Schedule: []people.ScheduleItem{item(1, 14*time.Hour, 15*time.Hour+30*time.Minute, 10)}},
			{ID: 101, Name: "Pre-SAT English", Discipline: "PRE SAT ENG", LearningType: "GROUP", OfficeID: 3,
				Schedule: []people.ScheduleItem{item(2, 16*time.Hour, 17*time.Hour, 10)}},
			{ID: 102, Name: "One-to-one", Discipline: "SAT MATH", LearningType: "IV OFFLINE", OfficeID: 3,
				Schedule: []people.ScheduleItem{item(3, 18*time.Hour, 19*time.Hour, 10)}},
			{ID: 103, Name: "Other teacher's", Discipline: "SAT MATH", LearningType: "GROUP", OfficeID: 3,
				Schedule: []people.ScheduleItem{item(4, 9*time.Hour, 10*time.Hour, 11)}},
			// A finished group whose schedule was never closed: no teacher.
			{ID: 104, Name: "No teacher", Discipline: "SAT MATH", LearningType: "GROUP", OfficeID: 3,
				Schedule: []people.ScheduleItem{{ID: 5, BeginDate: day("2026-01-01"), BeginTime: 11 * time.Hour, EndTime: 12 * time.Hour, Weekdays: 1}}},
		},
		Teachers: []people.Teacher{
			{ID: 10, FirstName: "Ali", LastName: "Valiyev", PhoneRaw: "+998901000010"},
			{ID: 11, FirstName: "Bek", LastName: "Tursunov", PhoneRaw: "+998901000011"},
		},
		Students: []people.Student{student(1), student(2), student(3), student(4), student(5)},
		Enrollments: []people.Enrollment{
			{GroupID: 100, StudentClientID: 1, Weekdays: 1},
			{GroupID: 100, StudentClientID: 2, Weekdays: 1},
			{GroupID: 100, StudentClientID: 3, Weekdays: 16},
			{GroupID: 100, StudentClientID: 4, Weekdays: 0},
			{GroupID: 100, StudentClientID: 5, Weekdays: 1 | 4},
			{GroupID: 101, StudentClientID: 1, Weekdays: 1},
			{GroupID: 103, StudentClientID: 2, Weekdays: 1},
		},
	}
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := people.Apply(t.Context(), tx, snap); err != nil {
		t.Fatal(err)
	}
	monday := day("2026-10-12")
	err = lessons.ApplyDays(t.Context(), tx, monday.AddDate(0, 0, -7), monday.AddDate(0, 0, 14), []lessons.Day{
		{GroupID: 100, Date: monday, Minutes: 90, ScheduleItemIDs: []int32{1}},
		{GroupID: 101, Date: monday, Minutes: 60, ScheduleItemIDs: []int32{2}},
		{GroupID: 102, Date: monday, Minutes: 60, ScheduleItemIDs: []int32{3}},
		{GroupID: 103, Date: monday, Minutes: 60, ScheduleItemIDs: []int32{4}},
		{GroupID: 104, Date: monday, Minutes: 60, ScheduleItemIDs: []int32{5}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type env struct {
	t     *testing.T
	srv   *httptest.Server
	pool  *pgxpool.Pool
	svc   *Service
	clock time.Time
}

func setup(t *testing.T) *env {
	pool := testdb.New(t)
	seed(t, pool)
	e := &env{t: t, pool: pool}
	lessonsSvc := lessons.NewService(pool, tashkent)
	e.svc = NewService(pool, lessonsSvc)
	e.svc.now = func() time.Time { return e.clock }

	mux := http.NewServeMux()
	authHandler := auth.NewHandler(auth.NewService(pool, people.NewDirectory(pool), auth.NewHasher()), tashkent, false)
	authHandler.Register(mux)
	NewHandler(e.svc, tashkent, authHandler.Require).Register(mux)
	lessons.NewHandler(lessonsSvc, tashkent, authHandler.Require).Register(mux)
	e.srv = httptest.NewServer(httpx.Chain(mux, httpx.WithRequestContext(slog.New(slog.NewTextHandler(io.Discard, nil)))))
	t.Cleanup(e.srv.Close)
	return e
}

func (e *env) do(method, path, token string, body any) (int, map[string]any) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(e.t.Context(), method, e.srv.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (e *env) signIn(phone, defaultPassword string) string {
	e.t.Helper()
	status, body := e.do("POST", "/v1/auth/login", "", map[string]string{"login": phone, "password": defaultPassword, "device_id": "d"})
	if status != http.StatusOK {
		e.t.Fatalf("login %s: %d %v", phone, status, body)
	}
	token := body["token"].(string)
	if status, body := e.do("POST", "/v1/auth/password", token, map[string]string{"current_password": defaultPassword, "new_password": "a-good-password"}); status != http.StatusNoContent {
		e.t.Fatalf("change password: %d %v", status, body)
	}
	return token
}

// scan signs a code the way the teacher's app does.
func (e *env) scan(token string, session map[string]any, window int64, device string) (int, map[string]any) {
	e.t.Helper()
	secret, _ := base64.RawURLEncoding.DecodeString(session["qr"].(map[string]any)["secret"].(string))
	id := session["id"].(string)
	return e.do("POST", "/v1/attendance/scan", token, map[string]any{
		"session_id": id, "window": window, "sig": Sign(secret, id, window), "device_id": device,
	})
}

func code(body map[string]any) string {
	m, _ := body["error"].(map[string]any)
	s, _ := m["code"].(string)
	return s
}

func states(session map[string]any) map[float64]string {
	out := map[float64]string{}
	for _, m := range session["marks"].([]any) {
		mark := m.(map[string]any)
		out[mark["client_id"].(float64)] = mark["state"].(string)
	}
	return out
}

func TestAttendanceFlow(t *testing.T) {
	e := setup(t)
	teacher := e.signIn("+998901000010", "0010")
	s1, s2, s3, s4 := e.signIn("+998902000001", "0001"), e.signIn("+998902000002", "0002"),
		e.signIn("+998902000003", "0003"), e.signIn("+998902000004", "0004")
	open := map[string]any{"group_id": 100, "date": "2026-10-12"}

	e.clock = at("13:40")
	if status, body := e.do("POST", "/v1/attendance/sessions", teacher, open); status != 422 || code(body) != "too_early" {
		t.Fatalf("too early: %d %v", status, body)
	}
	if status, body := e.do("POST", "/v1/attendance/sessions", s1, open); status != 403 || code(body) != "forbidden" {
		t.Errorf("student opening: %d %v", status, body)
	}

	e.clock = at("13:50")
	status, session := e.do("POST", "/v1/attendance/sessions", teacher, open)
	if status != 200 || session["status"] != "open" || session["qr"] == nil {
		t.Fatalf("open: %d %v", status, session)
	}
	// Roster: Monday students 1, 2, 4, 5 — not 3 (Fridays only).
	if got := states(session); len(got) != 4 || got[1] != "pending" || got[3] != "" {
		t.Errorf("roster = %v", got)
	}
	if _, again := e.do("POST", "/v1/attendance/sessions", teacher, open); again["id"] != session["id"] {
		t.Errorf("reopening made a new session")
	}

	// Scans: on time, repeated, from a phone that already checked someone in.
	e.clock = at("13:55")
	w := Window(e.clock)
	if status, body := e.scan(s1, session, w, "phone-1"); status != 200 || body["state"] != "present" || body["already"] != false {
		t.Errorf("scan: %d %v", status, body)
	}
	if status, body := e.scan(s1, session, w, "phone-1"); status != 200 || body["already"] != true {
		t.Errorf("rescan: %d %v", status, body)
	}
	if status, body := e.scan(s2, session, w, "phone-1"); status != 409 || code(body) != "device_already_used" {
		t.Errorf("shared phone: %d %v", status, body)
	}
	if status, body := e.scan(s2, session, w-3, "phone-2"); status != 422 || code(body) != "code_expired" {
		t.Errorf("old code: %d %v", status, body)
	}
	if status, body := e.do("POST", "/v1/attendance/scan", s2, map[string]any{
		"session_id": session["id"], "window": w, "sig": "AAAAAAAAAAAAAAAA", "device_id": "phone-2"}); status != 422 || code(body) != "invalid_code" {
		t.Errorf("forged code: %d %v", status, body)
	}
	if status, body := e.scan(s3, session, w, "phone-3"); status != 403 || code(body) != "not_in_roster" {
		t.Errorf("not on the list: %d %v", status, body)
	}
	if status, body := e.scan(teacher, session, w, "phone-t"); status != 403 || code(body) != "forbidden" {
		t.Errorf("teacher scanning: %d %v", status, body)
	}

	// More than 10 minutes after the start: late.
	e.clock = at("14:12")
	if status, body := e.scan(s2, session, Window(e.clock), "phone-2"); status != 200 || body["state"] != "late" {
		t.Errorf("late scan: %d %v", status, body)
	}

	// The teacher marks 4 absent; 4's scan is refused; then corrected to present.
	id := session["id"].(string)
	mark := func(client, state string) (int, map[string]any) {
		return e.do("PUT", "/v1/attendance/sessions/"+id+"/marks/"+client, teacher, map[string]string{"state": state})
	}
	if status, body := mark("4", "absent"); status != 200 || states(body)[4] != "absent" {
		t.Errorf("mark absent: %d %v", status, body)
	}
	if status, body := e.scan(s4, session, Window(e.clock), "phone-4"); status != 409 || code(body) != "marked_absent" {
		t.Errorf("scan after absent: %d %v", status, body)
	}
	if status, _ := mark("4", "present"); status != 200 {
		t.Errorf("correct to present: %d", status)
	}
	if status, body := mark("3", "present"); status != 403 || code(body) != "not_in_roster" {
		t.Errorf("mark someone not on the list: %d %v", status, body)
	}
	if status, body := mark("4", "pending"); status != 400 {
		t.Errorf("mark pending: %d %v", status, body)
	}

	// Another teacher can't see it; the lesson list shows it open.
	other := e.signIn("+998901000011", "0011")
	if status, _ := e.do("GET", "/v1/attendance/sessions/"+id, other, nil); status != 404 {
		t.Errorf("other teacher: %d", status)
	}
	_, list := e.do("GET", "/v1/teacher/lessons?date=2026-10-12", teacher, nil)
	if ref := list["lessons"].([]any)[0].(map[string]any)["session"].(map[string]any); ref["id"] != id || ref["status"] != "open" {
		t.Errorf("lesson's session = %v", ref)
	}

	// Finish: student 5 never scanned → absent. Scans are closed.
	e.clock = at("15:20")
	status, done := e.do("POST", "/v1/attendance/sessions/"+id+"/finish", teacher, nil)
	if status != 200 || done["status"] != "finished" || done["qr"] != nil {
		t.Fatalf("finish: %d %v", status, done)
	}
	if got := states(done); got[1] != "present" || got[2] != "late" || got[4] != "present" || got[5] != "absent" {
		t.Errorf("final marks = %v", got)
	}
	counts := done["counts"].(map[string]any)
	if counts["present"] != 2.0 || counts["late"] != 1.0 || counts["absent"] != 1.0 || counts["pending"] != 0.0 {
		t.Errorf("counts = %v", counts)
	}
	if status, body := e.scan(s1, session, Window(e.clock), "phone-1"); status != 409 || code(body) != "session_closed" {
		t.Errorf("scan after finish: %d %v", status, body)
	}

	// Corrections are allowed for 24 hours after the lesson, then refused.
	e.clock = at("15:30").Add(23 * time.Hour)
	if status, _ := mark("5", "present"); status != 200 {
		t.Errorf("next-day correction: %d", status)
	}
	e.clock = at("15:30").Add(25 * time.Hour)
	if status, body := mark("5", "absent"); status != 403 || code(body) != "edit_window_closed" {
		t.Errorf("late correction: %d %v", status, body)
	}

	// The audit log has every change, by whom.
	var students, teachers, system int
	err := e.pool.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE actor_kind = 'student'),
		count(*) FILTER (WHERE actor_kind = 'teacher'), count(*) FILTER (WHERE actor_kind = 'system')
		FROM attendance_audit`).Scan(&students, &teachers, &system)
	if err != nil {
		t.Fatal(err)
	}
	if students != 2 || teachers != 3 || system != 1 {
		t.Errorf("audit: %d by students, %d by teachers, %d by system", students, teachers, system)
	}
}

func TestOpenRules(t *testing.T) {
	e := setup(t)
	ctx := t.Context()
	monday := day("2026-10-12")

	e.clock = at("18:00")
	if _, err := e.svc.Open(ctx, 10, 102, monday); !errors.Is(err, ErrDisabled) {
		t.Errorf("one-to-one: %v", err)
	}
	if _, err := e.svc.Open(ctx, 10, 103, monday); !errors.Is(err, ErrNoLesson) {
		t.Errorf("another teacher's group: %v", err)
	}
	if _, err := e.svc.Open(ctx, 10, 100, day("2026-10-13")); !errors.Is(err, ErrNoLesson) {
		t.Errorf("no lesson that day: %v", err)
	}
	if _, err := e.svc.Open(ctx, 10, 100, monday); !errors.Is(err, ErrLessonOver) {
		t.Errorf("after the lesson: %v", err)
	}
}

func TestUnfinishedSessionsFinishOnTheirOwn(t *testing.T) {
	e := setup(t)
	ctx := t.Context()

	e.clock = at("16:05")
	s, err := e.svc.Open(ctx, 10, 101, day("2026-10-12"))
	if err != nil {
		t.Fatal(err)
	}
	e.clock = at("17:10") // lesson ended at 17:00; not yet due
	if n, err := e.svc.FinishDue(ctx); err != nil || n != 0 {
		t.Errorf("before due: %d %v", n, err)
	}
	e.clock = at("17:16")
	if n, err := e.svc.FinishDue(ctx); err != nil || n != 1 {
		t.Errorf("due: %d %v", n, err)
	}
	got, err := e.svc.Get(ctx, 10, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "finished" || got.Counts.Absent != 1 || *got.Marks[0].Source != "auto" {
		t.Errorf("after auto-finish: %+v", got)
	}
	if n, _ := e.svc.FinishDue(ctx); n != 0 {
		t.Errorf("finished twice")
	}
}

func TestSign(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	sig := Sign(secret, "0b7c6c1e-3c58-4bd6-9f27-2a3f0d9b1e44", 177_000_000)
	if len(sig) != 16 || sig != Sign(secret, "0b7c6c1e-3c58-4bd6-9f27-2a3f0d9b1e44", 177_000_000) {
		t.Errorf("sig = %q", sig)
	}
	if sig == Sign(secret, "0b7c6c1e-3c58-4bd6-9f27-2a3f0d9b1e44", 177_000_001) {
		t.Error("windows share a code")
	}
	now := time.Unix(177_000_000*WindowSeconds+5, 0)
	for w, want := range map[int64]bool{177_000_000 - 2: false, 177_000_000 - 1: true, 177_000_000: true, 177_000_000 + 1: true, 177_000_000 + 2: false} {
		if freshWindow(w, now) != want {
			t.Errorf("freshWindow(%d) != %v", w, want)
		}
	}
}
