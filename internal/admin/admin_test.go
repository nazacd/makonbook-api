package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nazacd/makonbook-api/internal/attendance"
	"github.com/nazacd/makonbook-api/internal/hollihop"
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

// fakeHolliHop: login "<name>" with password "pw-<name>".
type fakeHolliHop struct {
	members   map[string]hollihop.Member
	employees map[int32]hollihop.Employee
	down      bool
}

func (f *fakeHolliHop) CheckLogin(_ context.Context, login, password string) (hollihop.Member, error) {
	if f.down {
		return hollihop.Member{}, errors.New("status 502")
	}
	m, ok := f.members[login]
	if !ok || password != "pw-"+login {
		return hollihop.Member{}, hollihop.ErrBadCredentials
	}
	return m, nil
}

func (f *fakeHolliHop) FindEmployees(_ context.Context, term string) ([]hollihop.Employee, error) {
	var out []hollihop.Employee
	for _, e := range f.employees {
		if strings.Contains(e.FirstName+" "+e.LastName, term) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeHolliHop) GetEmployee(_ context.Context, id int32) (hollihop.Employee, bool, error) {
	e, ok := f.employees[id]
	return e, ok, nil
}

type env struct {
	t   *testing.T
	srv *httptest.Server
	svc *Service
	hh  *fakeHolliHop
}

func setup(t *testing.T) *env {
	pool := testdb.New(t)
	now := time.Now()
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	item := func(id int32, teacher int32) people.ScheduleItem {
		return people.ScheduleItem{ID: id, BeginTime: 14 * time.Hour, EndTime: 15 * time.Hour, Weekdays: 1, TeacherIDs: []int32{teacher}}
	}
	_, err = people.Apply(t.Context(), tx, people.Snapshot{
		Today:            time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC),
		StudentsComplete: true,
		Offices:          []people.Office{{ID: 3, Name: "NOVZA"}, {ID: 4, Name: "MINOR"}, {ID: 5, Name: "CHILONZOR"}},
		// The sync stores only teachers with classes.
		Groups: []people.Group{
			{ID: 100, Name: "SAT MATH", LearningType: "GROUP", OfficeID: 5, Schedule: []people.ScheduleItem{item(1, 10)}},
			{ID: 101, Name: "SAT ENGLISH", LearningType: "GROUP", OfficeID: 3, Schedule: []people.ScheduleItem{item(2, 11)}},
		},
		Teachers: []people.Teacher{
			{ID: 10, FirstName: "Ali", LastName: "Valiyev", PhoneRaw: "+998901000010"},
			{ID: 11, FirstName: "Bek", LastName: "Tursunov"}, // no phone: an issue in NOVZA
		},
		// Phone problems: one student in NOVZA, one in CHILONZOR.
		Students: []people.Student{
			{ClientID: 1, FirstName: "Aziz", LastName: "Karimov", StatusID: people.StatusStudying, OfficeIDs: []int32{3}},
			{ClientID: 2, FirstName: "Dilya", LastName: "Saidova", PhoneRaw: "123", StatusID: people.StatusStudying, OfficeIDs: []int32{5}},
		},
		Enrollments: []people.Enrollment{{GroupID: 101, StudentClientID: 1}, {GroupID: 100, StudentClientID: 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	monday := day("2026-10-12")
	err = lessons.ApplyDays(t.Context(), tx, monday, monday, []lessons.Day{
		{GroupID: 100, Date: monday, Minutes: 60, ScheduleItemIDs: []int32{1}},
		{GroupID: 101, Date: monday, Minutes: 60, ScheduleItemIDs: []int32{2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}

	hh := &fakeHolliHop{
		members: map[string]hollihop.Member{
			"boss":    {ID: 1, Type: TypeEmployee, FirstName: "Boss", LastName: "B"},
			"novza":   {ID: 2, Type: TypeEmployee, FirstName: "Nodira", LastName: "N"},
			"fired":   {ID: 3, Type: TypeEmployee, FirstName: "Former", LastName: "F"},
			"nobody":  {ID: 4, Type: TypeEmployee, FirstName: "Recep", LastName: "R"},
			"teacher": {ID: 10, Type: TypeTeacher, FirstName: "Ali", LastName: "Valiyev"},
			"student": {ID: 77, Type: "Student"},
		},
		employees: map[int32]hollihop.Employee{
			1: {ID: 1, FirstName: "Boss", LastName: "B"}, 2: {ID: 2, FirstName: "Nodira", LastName: "N"},
			3: {ID: 3, FirstName: "Former", LastName: "F"}, 4: {ID: 4, FirstName: "Recep", LastName: "R", Position: "Receptionist"},
			5: {ID: 5, FirstName: "Second", LastName: "Super"},
		},
	}
	svc := NewService(pool, hh)
	for _, g := range []struct {
		typ   string
		id    int32
		super bool
		offs  []int32
	}{
		{TypeEmployee, 1, true, nil},
		{TypeEmployee, 2, false, []int32{3, 4}},
		{TypeEmployee, 3, false, []int32{3}},
		{TypeTeacher, 10, false, []int32{5}},
	} {
		if _, err := svc.Grant(t.Context(), g.typ, g.id, "", g.super, g.offs); err != nil {
			t.Fatal(err)
		}
	}
	hh.employees[3] = hollihop.Employee{ID: 3, Fired: true} // fired after being given access

	lessonsSvc := lessons.NewService(pool, tashkent)
	panel := Panel{
		Lessons: lessonsSvc, Attendance: attendance.NewService(pool, lessonsSvc), People: people.NewDirectory(pool), Location: tashkent,
	}
	mux := http.NewServeMux()
	NewHandler(svc, panel, false, false).Register(mux)
	srv := httptest.NewServer(httpx.Chain(mux, httpx.WithRequestContext(slog.New(slog.NewTextHandler(io.Discard, nil)))))
	t.Cleanup(srv.Close)
	return &env{t: t, srv: srv, svc: svc, hh: hh}
}

func (e *env) do(method, path, cookie string, body any, csrf bool) (int, map[string]any, []*http.Cookie) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(e.t.Context(), method, e.srv.URL+path, rd)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: CookieName, Value: cookie})
	}
	if csrf {
		req.Header.Set(CSRFHeader, "1")
	}
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, resp.Cookies()
}

func (e *env) login(name, password string) (int, map[string]any, string) {
	e.t.Helper()
	status, body, cookies := e.do("POST", "/v1/admin/login", "", map[string]string{"login": name, "password": password}, true)
	cookie := ""
	for _, c := range cookies {
		if c.Name == CookieName {
			cookie = c.Value
			if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
				e.t.Errorf("cookie flags: %+v", c)
			}
		}
	}
	return status, body, cookie
}

func code(body map[string]any) string {
	m, _ := body["error"].(map[string]any)
	s, _ := m["code"].(string)
	return s
}

func officeNames(body map[string]any) []string {
	var out []string
	for _, o := range body["offices"].([]any) {
		out = append(out, o.(map[string]any)["name"].(string))
	}
	return out
}

func TestSignIn(t *testing.T) {
	e := setup(t)

	// A branch admin sees only their branches.
	status, body, cookie := e.login("novza", "pw-novza")
	if status != 200 || cookie == "" || body["super"] != false || body["name"] != "Nodira N" {
		t.Fatalf("novza: %d %v", status, body)
	}
	if got := officeNames(body); len(got) != 2 || got[0] != "MINOR" || got[1] != "NOVZA" {
		t.Errorf("novza's offices = %v", got)
	}
	if status, me, _ := e.do("GET", "/v1/admin/me", cookie, nil, false); status != 200 || me["name"] != "Nodira N" {
		t.Errorf("me: %d %v", status, me)
	}

	// A super admin sees all branches.
	_, body, _ = e.login("boss", "pw-boss")
	if got := officeNames(body); len(got) != 3 || body["super"] != true {
		t.Errorf("boss: %v", body)
	}
	// A teacher can be an admin too.
	if status, body, _ := e.login("teacher", "pw-teacher"); status != 200 || officeNames(body)[0] != "CHILONZOR" {
		t.Errorf("teacher admin: %d %v", status, body)
	}

	for _, tc := range []struct {
		login, password string
		status          int
		code            string
	}{
		{"novza", "wrong", 401, "invalid_credentials"},
		{"nobody", "pw-nobody", 403, "not_admin"}, // right HolliHop password, not on the list
		{"fired", "pw-fired", 403, "not_admin"},   // on the list, but fired in HolliHop
		{"student", "pw-student", 403, "not_admin"},
	} {
		if status, body, cookie := e.login(tc.login, tc.password); status != tc.status || code(body) != tc.code || cookie != "" {
			t.Errorf("%s: %d %v", tc.login, status, body)
		}
	}

	e.hh.down = true
	if status, body, _ := e.login("boss", "pw-boss"); status != 503 || code(body) != "unavailable" {
		t.Errorf("HolliHop down: %d %v", status, body)
	}
	e.hh.down = false

	// No CSRF header: refused before HolliHop is even asked.
	if status, _, _ := e.do("POST", "/v1/admin/login", "", map[string]string{"login": "boss", "password": "pw-boss"}, false); status != 403 {
		t.Errorf("login without CSRF header: %d", status)
	}
	if status, _, _ := e.do("POST", "/v1/admin/logout", cookie, nil, false); status != 403 {
		t.Errorf("logout without CSRF header: %d", status)
	}

	// Sign out ends the session.
	if status, _, _ := e.do("POST", "/v1/admin/logout", cookie, nil, true); status != 204 {
		t.Errorf("logout: %d", status)
	}
	if status, _, _ := e.do("GET", "/v1/admin/me", cookie, nil, false); status != 401 {
		t.Errorf("after logout: %d", status)
	}
}

func TestRevokeEndsSessions(t *testing.T) {
	e := setup(t)
	_, _, cookie := e.login("novza", "pw-novza")
	if err := e.svc.Revoke(t.Context(), TypeEmployee, 2); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := e.do("GET", "/v1/admin/me", cookie, nil, false); status != 401 {
		t.Errorf("after revoke: %d", status)
	}
	if _, err := e.svc.Grant(t.Context(), TypeEmployee, 9, "", false, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("branch admin without branches: %v", err)
	}
}

func TestSignInRateLimit(t *testing.T) {
	e := setup(t)
	for range loginLimit {
		if status, _, _ := e.login("novza", "wrong"); status != 401 {
			t.Fatalf("attempt: %d", status)
		}
	}
	// Even the right password is refused now: the limit is per login name.
	if status, body, _ := e.login("novza", "pw-novza"); status != 429 || code(body) != "rate_limited" {
		t.Errorf("after %d wrong passwords: %d %v", loginLimit, status, body)
	}
}

func TestPrincipalScope(t *testing.T) {
	branch := Principal{OfficeIDs: []int32{3, 4}}
	if !branch.CanSeeOffice(3) || branch.CanSeeOffice(5) || branch.Scope().All {
		t.Error("branch admin scope")
	}
	if super := (Principal{Super: true}); !super.CanSeeOffice(5) || !super.Scope().All {
		t.Error("super admin scope")
	}
}

func TestGrant(t *testing.T) {
	e := setup(t)
	if _, err := e.svc.Grant(t.Context(), TypeTeacher, 99, "", true, nil); !errors.Is(err, ErrUnknownTeacher) {
		t.Errorf("unknown teacher: %v", err)
	}
	if _, err := e.svc.Grant(t.Context(), TypeEmployee, 4, "", false, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("branch admin without branches: %v", err)
	}
}
