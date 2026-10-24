package parents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	gosync "sync"
	"testing"
	"time"

	"github.com/nazacd/makonbook-api/internal/attendance"
	"github.com/nazacd/makonbook-api/internal/auth"
	"github.com/nazacd/makonbook-api/internal/lessons"
	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/httpx"
	"github.com/nazacd/makonbook-api/internal/platform/scope"
	"github.com/nazacd/makonbook-api/internal/platform/testdb"
)

type notes struct {
	mu   gosync.Mutex
	msgs []string
}

func (n *notes) Notify(_ context.Context, msg string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.msgs = append(n.msgs, msg)
	return nil
}

func TestParents(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	tashkent, _ := time.LoadLocation("Asia/Tashkent")
	now := time.Now().In(tashkent)
	snapshot := func(students ...people.Student) people.Snapshot {
		return people.Snapshot{
			Today:            time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC),
			StudentsComplete: true,
			Offices:          []people.Office{{ID: 3, Name: "NOVZA"}, {ID: 4, Name: "CHILANZAR"}},
			Groups: []people.Group{
				{ID: 100, Name: "SAT Math", Discipline: "SAT MATH", LearningType: "GROUP", OfficeID: 3,
					Schedule: []people.ScheduleItem{{ID: 1, BeginTime: 14 * time.Hour, EndTime: 15 * time.Hour, Weekdays: 1 | 4, Classroom: "4", TeacherIDs: []int32{10}}}},
				{ID: 200, Name: "SAT English", Discipline: "SAT ENG", LearningType: "GROUP", OfficeID: 4,
					Schedule: []people.ScheduleItem{{ID: 2, BeginTime: 16 * time.Hour, EndTime: 17 * time.Hour, Weekdays: 2, TeacherIDs: []int32{10}}}},
			},
			Teachers:    []people.Teacher{{ID: 10, FirstName: "Ali", LastName: "Valiyev", PhoneRaw: "+998901000010"}},
			Students:    students,
			Enrollments: []people.Enrollment{{GroupID: 100, StudentClientID: 1, Weekdays: 1}},
		}
	}
	apply := func(s people.Snapshot) {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if _, err := people.Apply(ctx, tx, s); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	ali := people.Student{ClientID: 1, FirstName: "Aziz", LastName: "Karimov", PhoneRaw: "+998902000001", StatusID: people.StatusStudying}
	bek := people.Student{ClientID: 2, FirstName: "Bek", LastName: "Aliyev", PhoneRaw: "+998902000002", StatusID: people.StatusStudying}
	apply(snapshot(ali, bek))

	dir := people.NewDirectory(pool)
	note := &notes{}
	svc := NewService(pool, dir, attendance.NewService(pool, lessons.NewService(pool, tashkent)), note, "https://admin.test")

	// Every student gets a code: 5 digits, all different, never changing.
	if n, err := svc.EnsureCodes(ctx); err != nil || n != 2 {
		t.Fatalf("codes: %d %v", n, err)
	}
	if n, _ := svc.EnsureCodes(ctx); n != 0 {
		t.Errorf("codes given twice: %d", n)
	}
	code1, err := svc.Code(ctx, 1)
	code2, _ := svc.Code(ctx, 2)
	if err != nil || !regexp.MustCompile(`^\d{5}$`).MatchString(code1) || code1 == code2 {
		t.Fatalf("codes: %q %q %v", code1, code2, err)
	}
	if again, _ := svc.Code(ctx, 1); again != code1 {
		t.Error("a code changed")
	}

	mux := http.NewServeMux()
	authHandler := auth.NewHandler(auth.NewService(pool, dir, auth.NewHasher()), tashkent, true)
	authHandler.Register(mux)
	h := NewHandler(svc, tashkent, authHandler.Require, true, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.Register(mux)
	srv := httptest.NewServer(httpx.Chain(mux, httpx.WithRequestContext(slog.New(slog.NewTextHandler(io.Discard, nil)))))
	defer srv.Close()
	ip := 0
	do := func(method, path, token string, body any) (int, map[string]any) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequestWithContext(ctx, method, srv.URL+path, rd)
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("10.0.0.%d", ip))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	errCode := func(body map[string]any) string {
		e, _ := body["error"].(map[string]any)
		c, _ := e["code"].(string)
		return c
	}

	// The student sees their code (with their own sign-in).
	_, body := do("POST", "/v1/auth/login", "", map[string]string{"login": "+998902000001", "password": "0001", "device_id": "s"})
	studentToken, _ := body["token"].(string)
	do("POST", "/v1/auth/password", studentToken, map[string]string{"current_password": "0001", "new_password": "a-good-password"})
	if status, body := do("GET", "/v1/me/parent-code", studentToken, nil); status != 200 || body["code"] != code1 {
		t.Fatalf("student's code: %d %v", status, body)
	}

	// A parent: a wrong code, then the child's.
	wrong := "00000"
	if wrong == code1 || wrong == code2 {
		wrong = "99999"
	}
	if status, body := do("POST", "/v1/parent/login", "", map[string]string{"code": wrong, "device_id": "p1"}); status != 401 || errCode(body) != CodeInvalidCode {
		t.Errorf("wrong code: %d %v", status, body)
	}
	status, body := do("POST", "/v1/parent/login", "", map[string]string{"code": code1, "device_id": "p1"})
	parent, _ := body["token"].(string)
	if child, _ := body["child"].(map[string]any); status != 200 || parent == "" || child["first_name"] != "Aziz" {
		t.Fatalf("login: %d %v", status, body)
	}

	// Kept apart: a parent's token opens nothing of the student's, and the
	// student's token nothing of the parent's.
	if status, _ := do("GET", "/v1/me", parent, nil); status != 401 {
		t.Errorf("parent token on /v1/me: %d", status)
	}
	if status, _ := do("GET", "/v1/parent/child", studentToken, nil); status != 401 {
		t.Errorf("student token on /v1/parent/child: %d", status)
	}

	status, body = do("GET", "/v1/parent/child", parent, nil)
	groups, _ := body["groups"].([]any)
	if status != 200 || len(groups) != 1 || groups[0].(map[string]any)["name"] != "SAT Math" {
		t.Fatalf("child: %d %v", status, body)
	}
	if status, body := do("GET", "/v1/parent/attendance", parent, nil); status != 200 || body["groups"] == nil {
		t.Errorf("attendance: %d %v", status, body)
	}

	// Reports: about the child's class, in Uzbek or Russian.
	for name, req := range map[string]map[string]any{
		"empty":      {"message": "  ", "lang": "uz"},
		"lang":       {"message": "Salom", "lang": "en"},
		"not theirs": {"message": "Salom", "lang": "uz", "group_id": 200},
	} {
		if status, body := do("POST", "/v1/parent/reports", parent, req); status != 422 || errCode(body) != CodeInvalidReport {
			t.Errorf("%s: %d %v", name, status, body)
		}
	}
	status, body = do("POST", "/v1/parent/reports", parent, map[string]any{"message": "Учитель не отметил сына", "lang": "ru", "group_id": 100})
	if status != 201 || body["group_name"] != "SAT Math" {
		t.Fatalf("report: %d %v", status, body)
	}
	reportID := int64(body["id"].(float64))
	if len(note.msgs) != 1 || !regexp.MustCompile(`(?s)Parent report #\d+.*Karimov Aziz.*SAT Math.*NOVZA.*Russian.*Учитель не отметил сына.*admin\.test/parents`).MatchString(note.msgs[0]) {
		t.Errorf("admins told: %q", note.msgs)
	}
	if status, body := do("GET", "/v1/parent/reports", parent, nil); status != 200 || len(body["reports"].([]any)) != 1 {
		t.Errorf("reports: %d %v", status, body)
	}
	// Another phone with the same code doesn't see this phone's reports.
	ip = 1
	_, body = do("POST", "/v1/parent/login", "", map[string]string{"code": code1, "device_id": "p2"})
	other, _ := body["token"].(string)
	if _, body := do("GET", "/v1/parent/reports", other, nil); len(body["reports"].([]any)) != 0 {
		t.Errorf("another phone's reports: %v", body)
	}

	// Admins: only of the child's branches; handled with an answer the parent sees.
	chilanzar := scope.Offices{IDs: []int32{4}}
	if list, _ := svc.AdminReports(ctx, chilanzar, false); len(list) != 0 {
		t.Errorf("another branch sees it: %+v", list)
	}
	if err := svc.MarkHandled(ctx, chilanzar, reportID, "Admin", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("another branch handles it: %v", err)
	}
	novza := scope.Offices{IDs: []int32{3}}
	list, err := svc.AdminReports(ctx, novza, false)
	if err != nil || len(list) != 1 || list[0].Lang != "ru" || list[0].LastName != "Karimov" {
		t.Fatalf("admin reports: %+v %v", list, err)
	}
	if err := svc.MarkHandled(ctx, novza, reportID, "Nodira", "Исправили, спасибо."); err != nil {
		t.Fatal(err)
	}
	if err := svc.MarkHandled(ctx, novza, reportID, "Nodira", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("handled twice: %v", err)
	}
	ip = 0
	_, body = do("GET", "/v1/parent/reports", parent, nil)
	if r := body["reports"].([]any)[0].(map[string]any); r["handled_at"] == nil || r["reply"] != "Исправили, спасибо." {
		t.Errorf("parent sees the answer: %v", r)
	}
	if found, _ := svc.SearchCodes(ctx, novza, "karim", time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)); len(found) != 1 || found[0].Code != code1 || found[0].Groups != "SAT Math" {
		t.Errorf("code search: %+v", found)
	}
	if found, _ := svc.SearchCodes(ctx, chilanzar, "karim", now); len(found) != 0 {
		t.Errorf("code search, another branch: %+v", found)
	}

	// Signing out ends the session.
	if status, _ := do("POST", "/v1/parent/logout", other, nil); status != 204 {
		t.Errorf("logout: %d", status)
	}
	if status, _ := do("GET", "/v1/parent/child", other, nil); status != 401 {
		t.Errorf("after logout: %d", status)
	}

	// The child leaves: the code no longer opens anything.
	bek.StatusID = people.StatusStudying
	snap := snapshot(bek)
	snap.Enrollments = nil
	apply(snap)
	if status, _ := do("GET", "/v1/parent/child", parent, nil); status != 401 {
		t.Errorf("a child who left: %d", status)
	}
	if status, body := do("POST", "/v1/parent/login", "", map[string]string{"code": code1, "device_id": "p1"}); status != 401 || errCode(body) != CodeInvalidCode {
		t.Errorf("code of a child who left: %d %v", status, body)
	}
}

// Tries are limited per phone and per network; many wrong codes in an hour
// tell the admins once.
func TestGuessing(t *testing.T) {
	note := &notes{}
	h := NewHandler(&Service{notifier: note}, time.UTC, nil, true, slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Date(2026, 10, 24, 10, 5, 0, 0, time.UTC)
	allowed := 0
	for range 20 {
		if h.byDevice.Allow("phone", now) {
			allowed++
		}
	}
	if allowed != triesPerDevice {
		t.Errorf("per phone: %d allowed", allowed)
	}
	for i := range alarmWrong + 30 {
		h.wrongCode(t.Context(), fmt.Sprintf("10.0.0.%d", i%3), now)
	}
	if len(note.msgs) != 1 || !regexp.MustCompile(`50 wrong codes this hour.*10\.0\.0\.`).MatchString(note.msgs[0]) {
		t.Errorf("alarm: %q", note.msgs)
	}
	h.wrongCode(t.Context(), "10.0.0.9", now.Add(time.Hour))
	if len(note.msgs) != 1 {
		t.Errorf("a new hour starts counting again: %q", note.msgs)
	}
}
