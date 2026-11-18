package competitions

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

	"github.com/nazacd/makonbook-api/internal/auth"
	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/httpx"
	"github.com/nazacd/makonbook-api/internal/platform/testdb"
	"github.com/nazacd/makonbook-api/internal/practice"
	"github.com/nazacd/makonbook-api/internal/roadmap"
)

// Made-up questions and students only.

type reply struct {
	status int
	body   map[string]any
}

func (r reply) code() string {
	e, _ := r.body["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func addQuestions(t *testing.T, pool *pgxpool.Pool, subject string, n int) []string {
	t.Helper()
	var ids []string
	for range n {
		var id string
		if err := pool.QueryRow(context.Background(), `
			INSERT INTO questions (subject, type, content_hash, stem, choices, correct, explanation, domain, skill, difficulty, status, approved_at)
			VALUES ($1, 'mcq', gen_random_uuid()::text, 'Which one?', '{"A": "1", "B": "2", "C": "3", "D": "4"}', 'B',
			        'Because.', 'Algebra', 'Linear functions', 'easy', 'approved', now())
			RETURNING id::text`, subject).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// The routes over HTTP: who may take part (free accounts with a phone),
// the codes a client branches on, and the editors' side.
func TestHandler(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	tashkent, _ := time.LoadLocation("Asia/Tashkent")
	directory := people.NewDirectory(pool)
	practiceSvc := practice.NewService(pool, nil)
	roadmapSvc := roadmap.NewService(pool, practiceSvc, directory)

	hash, _ := auth.NewHasher().Hash("a-good-password")
	free, err := people.CreateSelfStudent(ctx, pool, "Web", "Student", "+998935550030")
	if err != nil {
		t.Fatal(err)
	}
	phoneless, err := people.CreateSelfStudent(ctx, pool, "No", "Phone", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO credentials (person_kind, person_id, password_hash, password_changed_at, email)
		VALUES ('student', $1, $3, now(), NULL), ('student', $2, $3, now(), 'nophone30@example.com')`, free, phoneless, hash); err != nil {
		t.Fatal(err)
	}
	math, english := addQuestions(t, pool, "math", 4), addQuestions(t, pool, "english", 2)

	mux := http.NewServeMux()
	authSvc := auth.NewService(pool, directory, auth.NewHasher())
	authHandler := auth.NewHandler(authSvc, tashkent, false)
	authHandler.Register(mux)
	roadmap.NewHandler(roadmapSvc, tashkent, authSvc.Profile, authHandler.Require, authHandler.RequireContent).Register(mux)
	editor := func(next http.HandlerFunc) http.HandlerFunc { return next } // the admin panel's sign-in is tested in admin
	NewHandler(roadmapSvc, authHandler.Require, authHandler.RequireContent, editor).Register(mux)
	srv := httptest.NewServer(httpx.Chain(mux, httpx.WithRequestContext(slog.New(slog.NewTextHandler(io.Discard, nil)))))
	defer srv.Close()
	do := func(method, path, token string, body any) reply {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequestWithContext(ctx, method, srv.URL+path, rd)
		req.Header.Set("Content-Type", "application/json")
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
		return reply{resp.StatusCode, out}
	}
	login := func(name string) string {
		t.Helper()
		r := do("POST", "/v1/auth/login", "", map[string]string{"login": name, "password": "a-good-password", "device_id": "d"})
		token, _ := r.body["token"].(string)
		if token == "" {
			t.Fatalf("login %s: %v", name, r.body)
		}
		return token
	}
	student, noPhone := login("+998935550030"), login("nophone30@example.com")
	at := func(d time.Duration) string { return time.Now().Add(d).UTC().Format(time.RFC3339) }
	create := func(title, subject string, ids []string, from, until time.Duration) string {
		t.Helper()
		r := do("POST", "/v1/admin/competitions", "", map[string]any{"title": title, "subject": subject, "source": "bank", "question_ids": ids, "starts_at": at(from), "ends_at": at(until), "minutes": 10})
		id, _ := obj(r.body["competition"])["id"].(string)
		if r.status != http.StatusCreated || id == "" {
			t.Fatalf("make %s: %v", title, r)
		}
		if r := do("PUT", "/v1/admin/competitions/"+id+"/published", "", nil); r.status != http.StatusNoContent {
			t.Fatalf("publish %s: %v", title, r)
		}
		return id
	}

	// Editors: a draw, a bad input.
	if r := do("POST", "/v1/admin/competitions/draw", "", map[string]any{"subject": "math", "easy": 2}); r.status != http.StatusOK || len(r.body["question_ids"].([]any)) != 2 {
		t.Errorf("draw: %v", r)
	}
	if r := do("POST", "/v1/admin/competitions", "", map[string]any{"title": "", "subject": "math", "source": "bank", "starts_at": at(0), "ends_at": at(time.Hour), "minutes": 10}); r.status != http.StatusUnprocessableEntity || r.code() != CodeInvalidCompetition {
		t.Errorf("no title: %v", r)
	}

	live := create("Live", "math", math[:2], -time.Minute, time.Hour)
	soon := create("Soon", "math", math[2:3], time.Hour, 2*time.Hour)
	mixed := create("Mixed", "mixed", []string{math[3], english[0]}, -time.Minute, time.Hour)

	// A free account with a phone takes part; without a phone it can't.
	if r := do("GET", "/v1/competitions", student, nil); r.status != http.StatusOK || len(r.body["competitions"].([]any)) != 3 || r.body["server_time"] == nil {
		t.Errorf("list: %v", r)
	}
	if r := do("POST", "/v1/competitions/"+live+"/start", noPhone, nil); r.status != http.StatusForbidden || r.code() != auth.CodePhoneRequired {
		t.Errorf("start without a phone: %v", r)
	}
	r := do("POST", "/v1/competitions/"+live+"/start", student, nil)
	attempt, _ := r.body["id"].(string)
	if r.status != http.StatusOK || r.body["kind"] != "competition" || obj(r.body["competition"])["deadline"] == nil || obj(r.body["questions"].([]any)[0])["correct"] != nil {
		t.Fatalf("start: %v", r)
	}
	if r := do("POST", "/v1/competitions/"+soon+"/start", student, nil); r.status != http.StatusConflict || r.code() != CodeNotLive {
		t.Errorf("start before it opens: %v", r)
	}

	// Answers, submit; the second start is refused; the result keeps its keys.
	if r := do("PUT", "/v1/me/assessments/"+attempt+"/answers", student, map[string]any{"answers": map[string]string{math[0]: "B"}}); r.status != http.StatusNoContent {
		t.Fatalf("save: %v", r)
	}
	r = do("POST", "/v1/me/assessments/"+attempt+"/submit", student, map[string]any{"answers": map[string]string{math[1]: "B"}})
	if res := obj(r.body["result"]); r.status != http.StatusOK || res["correct"] != 2.0 || len(res["items"].([]any)) != 0 {
		t.Fatalf("submit: %v", r)
	}
	if r := do("POST", "/v1/competitions/"+live+"/start", student, nil); r.status != http.StatusConflict || r.code() != CodeAttempted {
		t.Errorf("second start: %v", r)
	}
	if r := do("GET", "/v1/competitions/"+live+"/board", student, nil); r.status != http.StatusOK || obj(r.body["me"])["rank"] != 1.0 {
		t.Errorf("board: %v", r)
	}

	// A mixed one: the attempt says so; a late answer is refused.
	r = do("POST", "/v1/competitions/"+mixed+"/start", student, nil)
	if r.status != http.StatusOK || r.body["subject"] != "mixed" {
		t.Fatalf("mixed start: %v", r)
	}
	late, _ := r.body["id"].(string)
	if _, err := pool.Exec(ctx, `UPDATE roadmap_assessments SET created_at = now() - interval '11 minutes' WHERE id = $1::uuid`, late); err != nil {
		t.Fatal(err)
	}
	if r := do("PUT", "/v1/me/assessments/"+late+"/answers", student, map[string]any{"answers": map[string]string{math[3]: "B"}}); r.status != http.StatusConflict || r.code() != CodeTimeUp {
		t.Errorf("late answer: %v", r)
	}
	if r := do("GET", "/v1/competitions/"+mixed, student, nil); r.status != http.StatusOK || obj(obj(r.body["competition"])["mine"])["finished"] != true {
		t.Errorf("a late attempt reads finished: %v", r)
	}
}
