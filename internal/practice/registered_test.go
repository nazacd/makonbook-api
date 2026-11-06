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

// Registered students (signed up themselves, spec 21) take DAY1 … DAY10;
// enrolled students and teachers see every test as before.
func TestRegisteredStudentsTakeTheTestsOpenToAll(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	tashkent, _ := time.LoadLocation("Asia/Tashkent")
	now := time.Now().In(tashkent)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := people.Apply(ctx, tx, people.Snapshot{
		Today:            time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC),
		StudentsComplete: true,
		Groups: []people.Group{{ID: 100, Name: "SAT Math", Discipline: "SAT MATH", LearningType: "GROUP",
			Schedule: []people.ScheduleItem{{ID: 1, BeginTime: 14 * time.Hour, EndTime: 15 * time.Hour, Weekdays: 1, TeacherIDs: []int32{10}}}}},
		Teachers:    []people.Teacher{{ID: 10, FirstName: "Ali", LastName: "Valiyev", PhoneRaw: "+998901000010"}},
		Students:    []people.Student{{ClientID: 1, FirstName: "S", LastName: "One", PhoneRaw: "+998902000001", StatusID: people.StatusStudying}},
		Enrollments: []people.Enrollment{{GroupID: 100, StudentClientID: 1, Weekdays: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"DAY1", "DAY12"} {
		if _, err := (Importer{Pool: pool}).Import(ctx, load(t, review(t, code, nil)), false); err != nil {
			t.Fatal(err)
		}
	}
	svc := NewService(pool, signingStore{memStore{}})
	if got, err := svc.Publish(ctx); err != nil || len(got) != 2 {
		t.Fatalf("publish: %+v %v", got, err)
	}

	// Someone who signed up on the web, with a password.
	self, err := people.CreateSelfStudent(ctx, pool, "Web", "Student", "+998935550010")
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := auth.NewHasher().Hash("a-good-password")
	if _, err := pool.Exec(ctx, `INSERT INTO credentials (person_kind, person_id, password_hash, password_changed_at)
		VALUES ('student', $1, $2, now())`, self, hash); err != nil {
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
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	signIn := func(phone, password string, change bool) string {
		t.Helper()
		_, body := do("POST", "/v1/auth/login", "", map[string]string{"login": phone, "password": password})
		token, _ := body["token"].(string)
		if change {
			if status, body := do("POST", "/v1/auth/password", token, map[string]string{"current_password": password, "new_password": "a-good-password"}); status != http.StatusNoContent {
				t.Fatalf("sign in %s: %d %v", phone, status, body)
			}
		}
		return token
	}
	enrolled, teacher := signIn("+998902000001", "0001", true), signIn("+998901000010", "0010", true)
	registered := signIn("+998935550010", "a-good-password", false)
	catalog := func(token string) map[string]map[string]any {
		t.Helper()
		status, body := do("GET", "/v1/practice/tests", token, nil)
		if status != http.StatusOK {
			t.Fatalf("catalog: %d %v", status, body)
		}
		out := map[string]map[string]any{}
		for _, x := range body["tests"].([]any) {
			out[x.(map[string]any)["code"].(string)] = x.(map[string]any)
		}
		return out
	}

	for who, token := range map[string]string{"enrolled": enrolled, "teacher": teacher} {
		for code, test := range catalog(token) {
			if _, marked := test["enrolled_only"]; marked || test["version"] != 1.0 {
				t.Errorf("%s, %s: %v", who, code, test)
			}
		}
	}
	cat := catalog(registered)
	if day1 := cat["DAY1"]; day1["version"] != 1.0 || day1["bytes"] == nil || day1["enrolled_only"] != nil {
		t.Errorf("registered, DAY1: %v", day1)
	}
	if day12 := cat["DAY12"]; day12["version"] != nil || day12["bytes"] != nil || day12["published_at"] != nil ||
		day12["enrolled_only"] != true || day12["questions"] != 98.0 {
		t.Errorf("registered, DAY12: %v", day12)
	}

	code := func(body map[string]any) any { return body["error"].(map[string]any)["code"] }
	if status, body := do("GET", "/v1/practice/tests/DAY12/package", registered, nil); status != http.StatusForbidden || code(body) != CodeEnrolledOnly {
		t.Errorf("registered, DAY12 package: %d %v", status, body)
	}
	if status, _ := do("GET", "/v1/practice/tests/DAY1/package", registered, nil); status != http.StatusOK {
		t.Errorf("registered, DAY1 package: %d", status)
	}
	if status, _ := do("GET", "/v1/practice/tests/DAY99/package", registered, nil); status != http.StatusNotFound {
		t.Errorf("registered, unknown test: %d", status)
	}
	if status, _ := do("GET", "/v1/practice/tests/DAY12/package", enrolled, nil); status != http.StatusOK {
		t.Errorf("enrolled, DAY12 package: %d", status)
	}
	if status, _ := do("GET", "/v1/practice/tests/DAY12/stats", registered, nil); status != http.StatusOK {
		t.Errorf("registered, DAY12 stats: %d", status)
	}

	attempt := func(id, test string) map[string]any {
		return map[string]any{"id": id, "test": test, "version": 1, "sections": []string{"math"},
			"started_at": "2026-10-20T09:00:00Z", "finished_at": "2026-10-20T10:10:00Z",
			"answers": []map[string]any{{"subject": "math", "module": 1, "position": 1, "answer": "B", "seconds": 40}}}
	}
	if status, body := do("POST", "/v1/practice/attempts", registered, attempt("0d2b8a6e-1c4d-4e5f-9a0b-1c2d3e4f5a6b", "DAY12")); status != http.StatusForbidden || code(body) != CodeEnrolledOnly {
		t.Errorf("registered, DAY12 attempt: %d %v", status, body)
	}
	if status, body := do("POST", "/v1/practice/attempts", registered, attempt("1d2b8a6e-1c4d-4e5f-9a0b-1c2d3e4f5a6b", "DAY1")); status != http.StatusOK {
		t.Errorf("registered, DAY1 attempt: %d %v", status, body)
	}
	if status, body := do("POST", "/v1/practice/attempts", enrolled, attempt("2d2b8a6e-1c4d-4e5f-9a0b-1c2d3e4f5a6b", "DAY12")); status != http.StatusOK {
		t.Errorf("enrolled, DAY12 attempt: %d %v", status, body)
	}
}

func TestOpenToAll(t *testing.T) {
	for code, want := range map[string]bool{"DAY1": true, "day10": true, "DAY9": true, "DAY11": false, "DAY0": false, "DAY100": false, "MOCK1": false} {
		if OpenToAll(code) != want {
			t.Errorf("OpenToAll(%q) = %v", code, !want)
		}
	}
}
