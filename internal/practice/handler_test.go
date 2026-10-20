package practice

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

	"github.com/nazacd/makonbook-api/internal/auth"
	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/httpx"
	"github.com/nazacd/makonbook-api/internal/platform/testdb"
)

func TestHandler(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	tashkent, _ := time.LoadLocation("Asia/Tashkent")
	now := time.Now().In(tashkent)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	_, err = people.Apply(ctx, tx, people.Snapshot{
		Today:            time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC),
		StudentsComplete: true,
		Offices:          []people.Office{{ID: 3, Name: "NOVZA"}},
		Groups: []people.Group{{ID: 100, Name: "SAT Math", Discipline: "SAT MATH", LearningType: "GROUP", OfficeID: 3,
			Schedule: []people.ScheduleItem{{ID: 1, BeginTime: 14 * time.Hour, EndTime: 15 * time.Hour, Weekdays: 1, TeacherIDs: []int32{10}}}}},
		Teachers: []people.Teacher{{ID: 10, FirstName: "Ali", LastName: "Valiyev", PhoneRaw: "+998901000010"}},
		Students: []people.Student{
			{ClientID: 1, FirstName: "S", LastName: "One", PhoneRaw: "+998902000001", StatusID: people.StatusStudying},
		},
		Enrollments: []people.Enrollment{{GroupID: 100, StudentClientID: 1, Weekdays: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := (Importer{Pool: pool}).Import(ctx, load(t, review(t, "DAY1", nil)), false); err != nil {
		t.Fatal(err)
	}
	svc := NewService(pool, signingStore{memStore{}})
	if _, err := svc.Publish(ctx); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	authHandler := auth.NewHandler(auth.NewService(pool, people.NewDirectory(pool), auth.NewHasher()), tashkent, false)
	authHandler.Register(mux)
	NewHandler(svc, tashkent, authHandler.Require).Register(mux)
	srv := httptest.NewServer(httpx.Chain(mux, httpx.WithRequestContext(slog.New(slog.NewTextHandler(io.Discard, nil)))))
	defer srv.Close()
	do := func(method, path, token string, body any) (int, map[string]any) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequestWithContext(ctx, method, srv.URL+path, rd)
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
	signIn := func(phone, password string) string {
		t.Helper()
		_, body := do("POST", "/v1/auth/login", "", map[string]string{"login": phone, "password": password, "device_id": "d"})
		token, _ := body["token"].(string)
		if status, body := do("POST", "/v1/auth/password", token, map[string]string{"current_password": password, "new_password": "a-good-password"}); status != http.StatusNoContent {
			t.Fatalf("sign in %s: %d %v", phone, status, body)
		}
		return token
	}
	student, teacher := signIn("+998902000001", "0001"), signIn("+998901000010", "0010")

	if status, _ := do("GET", "/v1/practice/tests", "", nil); status != http.StatusUnauthorized {
		t.Errorf("catalog signed out: %d", status)
	}
	status, body := do("GET", "/v1/practice/tests", student, nil)
	tests, _ := body["tests"].([]any)
	if status != http.StatusOK || len(tests) != 1 || tests[0].(map[string]any)["version"] != 1.0 {
		t.Fatalf("catalog: %d %v", status, body)
	}
	if status, body := do("GET", "/v1/practice/tests/DAY1/package", teacher, nil); status != http.StatusOK || body["package"].(map[string]any)["code"] != "DAY1" {
		t.Errorf("package: %d %v", status, body)
	}
	for path, want := range map[string]int{"/v1/practice/tests/DAY2/package": 404, "/v1/practice/tests/DAY1/package?version=2": 404,
		"/v1/practice/tests/DAY1/package?version=x": 400} {
		if status, _ := do("GET", path, student, nil); status != want {
			t.Errorf("%s: %d, want %d", path, status, want)
		}
	}

	attempt := map[string]any{"id": "3f2b8a6e-1c4d-4e5f-9a0b-1c2d3e4f5a6b", "test": "DAY1", "version": 1, "sections": []string{"math"},
		"started_at": "2026-10-20T09:00:00Z", "finished_at": "2026-10-20T10:10:00Z",
		"answers": []map[string]any{{"subject": "math", "module": 1, "position": 1, "answer": "B", "seconds": 40}}}
	if status, body := do("POST", "/v1/practice/attempts", teacher, attempt); status != http.StatusForbidden {
		t.Errorf("a teacher's attempt: %d %v", status, body)
	}
	for range 2 {
		status, body := do("POST", "/v1/practice/attempts", student, attempt)
		if scores, _ := body["scores"].(map[string]any); status != http.StatusOK || scores["math"].(map[string]any)["raw"] != 1.0 {
			t.Errorf("attempt: %d %v", status, body)
		}
	}
	attempt["id"], attempt["version"] = "7d1e2f3a-4b5c-4d6e-8f70-8192a3b4c5d6", 5
	if status, body := do("POST", "/v1/practice/attempts", student, attempt); status != http.StatusUnprocessableEntity || body["error"].(map[string]any)["code"] != CodeInvalidAttempt {
		t.Errorf("a version that doesn't exist: %d %v", status, body)
	}
	if status, body := do("GET", "/v1/me/practice/attempts", student, nil); status != http.StatusOK || len(body["attempts"].([]any)) != 1 {
		t.Errorf("my attempts: %d %v", status, body)
	}
	status, body = do("GET", "/v1/teacher/groups/100/practice", teacher, nil)
	if students, _ := body["students"].([]any); status != http.StatusOK || len(students) != 1 ||
		len(students[0].(map[string]any)["attempts"].([]any)) != 1 {
		t.Errorf("group practice: %d %v", status, body)
	}
	for path, token := range map[string]string{"/v1/teacher/groups/100/practice": student, "/v1/teacher/groups/999/practice": teacher} {
		if status, _ := do("GET", path, token, nil); status != http.StatusForbidden {
			t.Errorf("%s: %d", path, status)
		}
	}
}
