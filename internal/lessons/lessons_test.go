package lessons_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nazacd/makonbook-api/internal/app"
	"github.com/nazacd/makonbook-api/internal/lessons"
	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/testdb"
)

var tashkent, _ = time.LoadLocation("Asia/Tashkent")

func day(s string) time.Time {
	t, _ := time.Parse(time.DateOnly, s)
	return t
}

// Teacher 10 teaches group 100 (in the office) and group 101 (online);
// teacher 11 teaches the one-to-one group 102. All three meet on Monday
// 2026-10-12.
func seed(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	now := time.Now().In(tashkent)
	item := func(id int32, from, to time.Duration, teacher int32) people.ScheduleItem {
		return people.ScheduleItem{ID: id, BeginDate: day("2026-01-01"), BeginTime: from, EndTime: to,
			Weekdays: 1 | 4 | 16, Classroom: "Room 1", TeacherIDs: []int32{teacher}}
	}
	snap := people.Snapshot{
		Today:            time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC),
		StudentsComplete: true,
		Offices:          []people.Office{{ID: 3, Name: "NOVZA"}, {ID: 4, Name: "ONLINE"}},
		Groups: []people.Group{
			{ID: 100, Name: "Pre-SAT Math 14:00", Discipline: "PRE SAT MATH", LearningType: "GROUP", OfficeID: 3,
				Schedule: []people.ScheduleItem{item(1, 14*time.Hour, 15*time.Hour+30*time.Minute, 10)}},
			{ID: 101, Name: "Online English", Discipline: "PRE SAT ENG", LearningType: "GROUP", OfficeID: 4,
				Schedule: []people.ScheduleItem{item(2, 16*time.Hour, 17*time.Hour, 10)}},
			{ID: 102, Name: "One-to-one", Discipline: "SAT MATH", LearningType: "IV OFFLINE", OfficeID: 3,
				Schedule: []people.ScheduleItem{item(3, 9*time.Hour, 10*time.Hour, 11)}},
		},
		Teachers: []people.Teacher{
			{ID: 10, FirstName: "Ali", LastName: "Valiyev", PhoneRaw: "+998901000010"},
			{ID: 11, FirstName: "Bek", LastName: "Tursunov", PhoneRaw: "+998901000011"},
		},
		Students: []people.Student{
			{ClientID: 1, FirstName: "A", LastName: "A", PhoneRaw: "+998902000001", StatusID: people.StatusStudying},
			{ClientID: 2, FirstName: "B", LastName: "B", PhoneRaw: "+998902000002", StatusID: people.StatusStudying},
			{ClientID: 3, FirstName: "C", LastName: "C", PhoneRaw: "+998902000003", StatusID: people.StatusStudying},
		},
		Enrollments: []people.Enrollment{
			{GroupID: 100, StudentClientID: 1, Weekdays: 1 | 4}, // Mon, Wed
			{GroupID: 100, StudentClientID: 2, Weekdays: 0},     // no weekday restriction
			{GroupID: 100, StudentClientID: 3, Weekdays: 16},    // Fridays only
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
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type client struct {
	t   *testing.T
	srv *httptest.Server
}

func (c client) do(method, path, token string, body any) (int, map[string]any) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(c.t.Context(), method, c.srv.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.srv.Client().Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// signIn logs in with the default password and replaces it, so the token
// passes auth's Require.
func (c client) signIn(phone, defaultPassword string) string {
	c.t.Helper()
	status, body := c.do("POST", "/v1/auth/login", "", map[string]string{"login": phone, "password": defaultPassword, "device_id": "d"})
	if status != http.StatusOK {
		c.t.Fatalf("login: %d %v", status, body)
	}
	token := body["token"].(string)
	status, body = c.do("POST", "/v1/auth/password", token, map[string]string{"current_password": defaultPassword, "new_password": "a-good-password"})
	if status != http.StatusNoContent {
		c.t.Fatalf("change password: %d %v", status, body)
	}
	return token
}

func errorCode(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

func TestTeacherLessons(t *testing.T) {
	pool := testdb.New(t)
	seed(t, pool)
	srv := httptest.NewServer(app.Handler(app.Deps{Pool: pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Location: tashkent}))
	t.Cleanup(srv.Close)
	c := client{t, srv}

	teacher := c.signIn("+998901000010", "0010")
	status, body := c.do("GET", "/v1/teacher/lessons?date=2026-10-12", teacher, nil)
	if status != http.StatusOK {
		t.Fatalf("lessons: %d %v", status, body)
	}
	if body["date"] != "2026-10-12" {
		t.Errorf("date = %v", body["date"])
	}
	list := body["lessons"].([]any)
	if len(list) != 2 {
		t.Fatalf("lessons = %v", list) // the one-to-one group is teacher 11's
	}
	first, second := list[0].(map[string]any), list[1].(map[string]any)
	if first["group_id"] != 100.0 || first["begin_time"] != "14:00" || first["end_time"] != "15:30" ||
		first["starts_at"] != "2026-10-12T14:00:00+05:00" || first["attendance_enabled"] != true || first["classroom"] != "Room 1" {
		t.Errorf("first lesson = %v", first)
	}
	// Roster on a Monday: student 1 (Mon/Wed) and 2 (any day), not 3 (Fridays).
	if first["students"] != 2.0 {
		t.Errorf("students = %v, want 2", first["students"])
	}
	if second["group_id"] != 101.0 || second["attendance_enabled"] != false {
		t.Errorf("online lesson = %v", second)
	}

	// No lessons on a day without lesson days, even though it's a schedule weekday.
	if _, body := c.do("GET", "/v1/teacher/lessons?date=2026-10-14", teacher, nil); len(body["lessons"].([]any)) != 0 {
		t.Errorf("Wednesday: %v", body)
	}
	if status, body := c.do("GET", "/v1/teacher/lessons?date=12.10.2026", teacher, nil); status != http.StatusBadRequest {
		t.Errorf("bad date: %d %v", status, body)
	}
	if status, _ := c.do("GET", "/v1/teacher/lessons", teacher, nil); status != http.StatusOK {
		t.Errorf("default date: %d", status)
	}

	student := c.signIn("+998902000001", "0001")
	if status, body := c.do("GET", "/v1/teacher/lessons", student, nil); status != http.StatusForbidden || errorCode(body) != "forbidden" {
		t.Errorf("student: %d %v", status, body)
	}
	if status, _ := c.do("GET", "/v1/teacher/lessons", "", nil); status != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", status)
	}
}

func TestAttendanceEnabled(t *testing.T) {
	for _, tc := range []struct {
		learningType, office string
		want                 bool
	}{
		{"GROUP", "NOVZA", true},
		{"GROUP", "online ", false},
		{"IV OFFLINE", "NOVZA", false},
		{"IV ONLINE", "NOVZA", false},
		{"ONLINE", "NOVZA", false},
	} {
		if got := lessons.AttendanceEnabled(tc.learningType, tc.office); got != tc.want {
			t.Errorf("AttendanceEnabled(%q, %q) = %v", tc.learningType, tc.office, got)
		}
	}
}
