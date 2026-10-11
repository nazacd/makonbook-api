package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/httpx"
	"github.com/nazacd/makonbook-api/internal/platform/testdb"
)

var tashkent, _ = time.LoadLocation("Asia/Tashkent")

func day(s string) time.Time {
	t, _ := time.Parse(time.DateOnly, s)
	return t
}

// snapshot: one group taught by teacher 10; student 1 can log in, students 2
// and 3 share a number (no login phone).
func snapshot(student1Status int32) people.Snapshot {
	today := time.Now().In(tashkent)
	return people.Snapshot{
		Today:            time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC),
		StudentsComplete: true,
		Offices:          []people.Office{{ID: 3, Name: "NOVZA"}},
		Groups: []people.Group{{ID: 100, Name: "PRE SAT MATH", Discipline: "PRE SAT MATH", LearningType: "GROUP", OfficeID: 3,
			Schedule: []people.ScheduleItem{{ID: 1, BeginDate: day("2026-01-01"), BeginTime: 14 * time.Hour, EndTime: 15*time.Hour + 30*time.Minute,
				Weekdays: 1 | 4 | 16, Classroom: "Room 1", TeacherIDs: []int32{10}}}}},
		Teachers: []people.Teacher{{ID: 10, FirstName: "Ali", LastName: "Valiyev", PhoneRaw: "+998901000010"}},
		Students: []people.Student{
			{ClientID: 1, FirstName: "Aziz", LastName: "Karimov", PhoneRaw: "+998902001234", StatusID: student1Status},
			{ClientID: 2, FirstName: "Sibling", LastName: "One", PhoneRaw: "+998902000002", StatusID: people.StatusStudying},
			{ClientID: 3, FirstName: "Sibling", LastName: "Two", PhoneRaw: "+998902000002", StatusID: people.StatusStudying},
		},
		Enrollments: []people.Enrollment{{GroupID: 100, StudentClientID: 1, Weekdays: 1 | 4 | 16}},
	}
}

func applySnapshot(t *testing.T, pool *pgxpool.Pool, s people.Snapshot) {
	t.Helper()
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := people.Apply(t.Context(), tx, s); err != nil {
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

type response struct {
	status int
	header http.Header
	body   map[string]any
}

func (r response) code() string {
	e, _ := r.body["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

func (c client) do(method, path, token string, body any) response {
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
	out := response{status: resp.StatusCode, header: resp.Header}
	_ = json.NewDecoder(resp.Body).Decode(&out.body)
	return out
}

func (c client) login(login, password string) response {
	c.t.Helper()
	return c.do("POST", "/v1/auth/login", "", map[string]string{"login": login, "password": password, "device_id": "test-device"})
}

func (c client) mustLogin(login, password string) (string, map[string]any) {
	c.t.Helper()
	r := c.login(login, password)
	if r.status != http.StatusOK {
		c.t.Fatalf("login %s: %d %v", login, r.status, r.body)
	}
	return r.body["token"].(string), r.body["user"].(map[string]any)
}

func setup(t *testing.T) (client, *pgxpool.Pool, *Service) {
	pool := testdb.New(t)
	applySnapshot(t, pool, snapshot(people.StatusStudying))
	svc := NewService(pool, people.NewDirectory(pool), NewHasher())
	mux := http.NewServeMux()
	NewHandler(svc, tashkent, false).Register(mux)
	srv := httptest.NewServer(httpx.Chain(mux, httpx.WithRequestContext(slogDiscard()), httpx.Recover))
	t.Cleanup(srv.Close)
	return client{t, srv}, pool, svc
}

func TestFirstLoginAndPasswordChange(t *testing.T) {
	c, _, _ := setup(t)

	// Default password: last 4 digits of the phone, in any phone format.
	token, user := c.mustLogin("90 200 12 34", "1234")
	if user["must_change_password"] != true || user["first_name"] != "Aziz" {
		t.Errorf("user = %v", user)
	}
	groups := user["groups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("groups = %v", groups)
	}
	slot := groups[0].(map[string]any)["schedule"].([]any)[0].(map[string]any)
	if slot["begin_time"] != "14:00" || slot["teachers"].([]any)[0] != "Ali Valiyev" ||
		len(slot["weekdays"].([]any)) != 3 || slot["weekdays"].([]any)[0] != "mon" {
		t.Errorf("slot = %v", slot)
	}

	// Until the password is changed, only a few endpoints work.
	if r := c.do("PUT", "/v1/auth/username", token, map[string]string{"username": "aziz"}); r.code() != CodePasswordChangeRequired {
		t.Errorf("username before change: %d %v", r.status, r.body)
	}
	if r := c.do("GET", "/v1/me", token, nil); r.status != http.StatusOK {
		t.Errorf("me: %d", r.status)
	}

	for _, tc := range []struct {
		current, next, code string
	}{
		{"0000", "a fine password", CodeWrongPassword},
		{"1234", "short", CodeWeakPassword},
		{"1234", "pass902001234", CodeWeakPassword}, // contains the phone number
	} {
		r := c.do("POST", "/v1/auth/password", token, map[string]string{"current_password": tc.current, "new_password": tc.next})
		if r.code() != tc.code {
			t.Errorf("change %q→%q: %d %v, want %s", tc.current, tc.next, r.status, r.body, tc.code)
		}
	}
	if r := c.do("POST", "/v1/auth/password", token, map[string]string{"current_password": "1234", "new_password": "a fine password"}); r.status != http.StatusNoContent {
		t.Fatalf("change password: %d %v", r.status, r.body)
	}

	// Same session, now unrestricted.
	if r := c.do("GET", "/v1/me", token, nil); r.body["must_change_password"] != false {
		t.Errorf("me after change: %v", r.body)
	}
	if r := c.do("PUT", "/v1/auth/username", token, map[string]string{"username": "Aziz.K"}); r.status != http.StatusOK || r.body["username"] != "aziz.k" {
		t.Errorf("set username: %d %v", r.status, r.body)
	}

	// The default password is gone; username and phone both log in.
	if r := c.login("+998902001234", "1234"); r.code() != CodeInvalidCredentials {
		t.Errorf("default password still works: %d", r.status)
	}
	if _, u := c.mustLogin("aziz.k", "a fine password"); u["must_change_password"] != false || u["username"] != "aziz.k" {
		t.Errorf("username login user = %v", u)
	}
	c.mustLogin("+998902001234", "a fine password")
}

func TestPasswordChangeLogsOutOtherDevices(t *testing.T) {
	c, _, _ := setup(t)
	phone, _ := c.mustLogin("+998902001234", "1234")
	tablet, _ := c.mustLogin("+998902001234", "1234")

	if r := c.do("POST", "/v1/auth/password", phone, map[string]string{"current_password": "1234", "new_password": "a fine password"}); r.status != http.StatusNoContent {
		t.Fatalf("change: %d %v", r.status, r.body)
	}
	if r := c.do("GET", "/v1/me", tablet, nil); r.status != http.StatusUnauthorized {
		t.Errorf("other device still logged in: %d", r.status)
	}
	if r := c.do("GET", "/v1/me", phone, nil); r.status != http.StatusOK {
		t.Errorf("changing device logged out: %d", r.status)
	}

	if r := c.do("POST", "/v1/auth/logout", phone, nil); r.status != http.StatusNoContent {
		t.Fatalf("logout: %d", r.status)
	}
	if r := c.do("GET", "/v1/me", phone, nil); r.status != http.StatusUnauthorized {
		t.Errorf("token works after logout: %d", r.status)
	}
}

func TestLockoutEscalates(t *testing.T) {
	c, pool, _ := setup(t)

	for i := 1; i <= 5; i++ {
		if r := c.login("+998902001234", "0000"); r.code() != CodeInvalidCredentials {
			t.Fatalf("attempt %d: %d %v", i, r.status, r.body)
		}
	}
	r := c.login("+998902001234", "1234") // right password, but locked
	if r.code() != CodeAccountLocked {
		t.Fatalf("after 5 failures: %d %v", r.status, r.body)
	}
	if s, _ := strconv.Atoi(r.header.Get("Retry-After")); s < 14*60 || s > 15*60 {
		t.Errorf("Retry-After = %q, want ~15 min", r.header.Get("Retry-After"))
	}

	// Pretend the lock ran out; the next 5 failures lock for 30 minutes.
	if _, err := pool.Exec(t.Context(), "UPDATE credentials SET locked_until = now() - interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		c.login("+998902001234", "0000")
	}
	var mins float64
	_ = pool.QueryRow(t.Context(), "SELECT extract(epoch FROM locked_until - now()) / 60 FROM credentials").Scan(&mins)
	if mins < 29 || mins > 30 {
		t.Errorf("second lockout = %.1f min, want 30", mins)
	}

	// A successful login clears the counter.
	_, _ = pool.Exec(t.Context(), "UPDATE credentials SET locked_until = NULL")
	c.mustLogin("+998902001234", "1234")
	var failures int
	_ = pool.QueryRow(t.Context(), "SELECT failed_attempts FROM credentials").Scan(&failures)
	if failures != 0 {
		t.Errorf("failed_attempts = %d after success", failures)
	}
}

func TestDeactivationAndReturn(t *testing.T) {
	c, pool, _ := setup(t)
	token, _ := c.mustLogin("+998902001234", "1234")
	if r := c.do("POST", "/v1/auth/password", token, map[string]string{"current_password": "1234", "new_password": "a fine password"}); r.status != http.StatusNoContent {
		t.Fatal(r.status)
	}

	// The student pauses their studies: sessions die at once, login fails.
	applySnapshot(t, pool, snapshot(6))
	if r := c.do("GET", "/v1/me", token, nil); r.status != http.StatusUnauthorized {
		t.Errorf("session of deactivated student: %d", r.status)
	}
	if r := c.login("+998902001234", "a fine password"); r.code() != CodeInvalidCredentials {
		t.Errorf("deactivated login: %d %v", r.status, r.body)
	}

	// They come back: the old password and old session stay dead, the
	// default password works and must be replaced again.
	applySnapshot(t, pool, snapshot(people.StatusStudying))
	if r := c.do("GET", "/v1/me", token, nil); r.status != http.StatusUnauthorized {
		t.Errorf("old session revived: %d", r.status)
	}
	if r := c.login("+998902001234", "a fine password"); r.code() != CodeInvalidCredentials {
		t.Errorf("old password works after return: %d", r.status)
	}
	if _, u := c.mustLogin("+998902001234", "1234"); u["must_change_password"] != true {
		t.Errorf("returning student user = %v", u)
	}
}

func TestNoLoginWithoutAValidUniquePhone(t *testing.T) {
	c, _, _ := setup(t)
	if r := c.login("+998902000002", "0002"); r.code() != CodeInvalidCredentials {
		t.Errorf("shared number logged in: %d %v", r.status, r.body)
	}
	if r := c.login("nobody", "whatever1"); r.code() != CodeInvalidCredentials {
		t.Errorf("unknown username: %d", r.status)
	}
	if r := c.do("GET", "/v1/me", "not-a-token", nil); r.code() != httpx.CodeUnauthorized {
		t.Errorf("bad token: %d", r.status)
	}
	if r := c.do("POST", "/v1/auth/login", "", "not json"); r.code() != httpx.CodeBadRequest {
		t.Errorf("bad body: %d", r.status)
	}
}

func TestTeacherLoginSeesTheirGroups(t *testing.T) {
	c, _, _ := setup(t)
	_, u := c.mustLogin("+998901000010", "0010")
	if u["kind"] != people.KindTeacher || len(u["groups"].([]any)) != 1 {
		t.Errorf("teacher user = %v", u)
	}
}

func TestResetToDefault(t *testing.T) {
	c, _, svc := setup(t)
	token, _ := c.mustLogin("+998902001234", "1234")
	c.do("POST", "/v1/auth/password", token, map[string]string{"current_password": "1234", "new_password": "forgotten pass"})
	for range 5 {
		c.login("+998902001234", "wrong")
	}

	if _, err := svc.ResetToDefault(t.Context(), people.KindStudent, 1); err != nil {
		t.Fatal(err)
	}
	if r := c.do("GET", "/v1/me", token, nil); r.status != http.StatusUnauthorized {
		t.Errorf("session survived reset: %d", r.status)
	}
	if _, u := c.mustLogin("+998902001234", "1234"); u["must_change_password"] != true {
		t.Errorf("after reset: %v", u)
	}
}
