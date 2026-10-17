package admin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nazacd/makonbook-api/internal/platform/r2"
	"github.com/nazacd/makonbook-api/internal/practice"
)

// loadTest imports a reviewed test whose questions are plain mcqs, except
// math module 1 #1, which has a figure.
func (e *env) loadTest(code string) {
	e.t.Helper()
	var lines []string
	for _, subject := range []string{"english", "math"} {
		size := map[string]int{"english": 27, "math": 22}[subject]
		for module := 1; module <= 2; module++ {
			for n := 1; n <= size; n++ {
				rec := map[string]any{
					"qid":     fmt.Sprintf("%s-m%d-%02d", subject, module, n),
					"source":  map[string]any{"subject": subject, "csv_id": n, "test_id": code, "module": fmt.Sprintf("module_%d", module), "number": n},
					"type":    "mcq",
					"stem":    fmt.Sprintf("%s %s %d %d?", code, subject, module, n),
					"choices": map[string]string{"A": "a", "B": "b", "C": "c", "D": "d"},
					"figures": []any{}, "key": "A", "verdict": "key_correct", "risk": "green",
					"explanation": map[string]any{"text": "Because."},
					"tags":        map[string]any{"domain": "Algebra", "skill": "Linear functions", "difficulty": "easy"},
				}
				if subject == "math" && module == 1 && n == 1 {
					rec["risk"] = "red"
					rec["stem"] = "The graph:\n\n![a line](r2:sat/g.png)\n\nWhat is the slope?"
					rec["figures"] = []map[string]string{{"key": "sat/g.png", "kind": "graph", "alt": "a line"}}
				}
				b, _ := json.Marshal(rec)
				lines = append(lines, string(b))
			}
		}
	}
	path := filepath.Join(e.t.TempDir(), "questions.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		e.t.Fatal(err)
	}
	test, err := practice.Read(path)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := (practice.Importer{Pool: e.pool}).Import(e.t.Context(), test, false); err != nil {
		e.t.Fatal(err)
	}
}

func TestPracticeEditor(t *testing.T) {
	e := setup(t)
	e.loadTest("DAY5")
	_, _, boss := e.login("boss", "pw-boss")
	_, _, novza := e.login("novza", "pw-novza")

	// A branch admin isn't an editor.
	if status, _, _ := e.do("GET", "/v1/admin/practice/tests", novza, nil, false); status != 403 {
		t.Errorf("branch admin: %d", status)
	}
	// A super admin makes the teacher an editor only: they see the practice
	// pages and no branch.
	if status, _, _ := e.do("PUT", "/v1/admin/access/teacher/10", boss, map[string]any{"office_ids": []int{}, "editor": true}, true); status != 200 {
		t.Fatalf("make editor: %d", status)
	}
	_, profile, teacher := e.login("teacher", "pw-teacher")
	if profile["editor"] != true || len(profile["offices"].([]any)) != 0 {
		t.Errorf("editor's profile: %v", profile)
	}

	status, body, _ := e.do("GET", "/v1/admin/practice/tests", teacher, nil, false)
	tests, _ := body["tests"].([]any)
	if status != 200 || len(tests) != 1 || tests[0].(map[string]any)["code"] != "DAY5" || tests[0].(map[string]any)["open"] != false {
		t.Fatalf("tests: %d %v", status, body)
	}
	if status, body, _ := e.do("GET", "/v1/admin/practice/tests/DAY5", teacher, nil, false); status != 200 || len(body["slots"].([]any)) != 98 || body["questions"] != 98.0 {
		t.Errorf("test: %d", status)
	}
	if status, _, _ := e.do("GET", "/v1/admin/practice/tests/NOPE", teacher, nil, false); status != 404 {
		t.Errorf("unknown test: %d", status)
	}
	if status, _, _ := e.do("GET", "/v1/admin/practice/questions?risk=purple", teacher, nil, false); status != 400 {
		t.Errorf("bad filter: %d", status)
	}

	// The red question comes first.
	_, body, _ = e.do("GET", "/v1/admin/practice/questions?limit=5", teacher, nil, false)
	first := body["questions"].([]any)[0].(map[string]any)
	if body["total"] != 98.0 || first["ai_risk"] != "red" {
		t.Fatalf("queue: %v", body)
	}
	id := first["id"].(string)
	_, q, _ := e.do("GET", "/v1/admin/practice/questions/"+id, teacher, nil, false)
	if q["version"] != 1.0 || len(q["reviews"].([]any)) != 1 {
		t.Fatalf("question: %v", q)
	}

	edit := map[string]any{
		"version": 1, "passage": nil, "stem": q["stem"], "choices": q["choices"], "figures": q["figures"],
		"correct": "B", "explanation": "Rise over run.", "domain": "Algebra", "skill": "Linear functions", "difficulty": "medium",
	}
	if status, _, _ := e.do("PUT", "/v1/admin/practice/questions/"+id, teacher, edit, false); status != 403 {
		t.Errorf("save without CSRF header: %d", status)
	}
	status, saved, _ := e.do("PUT", "/v1/admin/practice/questions/"+id, teacher, edit, true)
	versions, _ := saved["versions"].([]any)
	if status != 200 || saved["version"] != 2.0 || saved["correct"] != "B" || len(versions) != 2 ||
		versions[0].(map[string]any)["created_by"] != "Ali Valiyev" {
		t.Fatalf("save: %d %v", status, saved)
	}
	if status, body, _ := e.do("PUT", "/v1/admin/practice/questions/"+id, boss, edit, true); status != 409 || code(body) != CodeConflict {
		t.Errorf("stale save: %d %v", status, body)
	}
	edit["version"], edit["stem"] = 2, `Unclosed \(x`
	status, body, _ = e.do("PUT", "/v1/admin/practice/questions/"+id, boss, edit, true)
	fields, _ := body["error"].(map[string]any)["fields"].(map[string]any)
	if status != 422 || code(body) != CodeInvalidQuestion || fields["stem"] == nil {
		t.Errorf("invalid save: %d %v", status, body)
	}

	// Taxonomy for the form.
	_, body, _ = e.do("GET", "/v1/admin/practice/taxonomy", teacher, nil, false)
	if _, ok := body["subjects"].(map[string]any)["math"].(map[string]any)["Algebra"]; !ok {
		t.Errorf("taxonomy: %v", body)
	}
}

func TestPracticeFigures(t *testing.T) {
	e := setup(t)
	e.figs["media/sat/g.png"] = r2.Object{Body: []byte("PNG"), ContentType: "image/png"}
	e.figs["sat/bare.png"] = r2.Object{Body: []byte("BARE"), ContentType: "image/png"}
	e.figs["media/sat/x.svg"] = r2.Object{Body: []byte("<svg onload=alert(1)>"), ContentType: "image/svg+xml"}
	_, _, boss := e.login("boss", "pw-boss")
	_, _, novza := e.login("novza", "pw-novza")

	get := func(cookie, key string) (int, string, string) {
		req, _ := http.NewRequestWithContext(t.Context(), "GET", e.srv.URL+"/v1/admin/practice/figures?key="+key, nil)
		req.AddCookie(&http.Cookie{Name: CookieName, Value: cookie})
		resp, err := e.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("Content-Type"), string(b)
	}
	if status, ct, body := get(boss, "sat/g.png"); status != 200 || ct != "image/png" || body != "PNG" {
		t.Errorf("figure: %d %s %s", status, ct, body)
	}
	if status, _, body := get(boss, "sat/bare.png"); status != 200 || body != "BARE" {
		t.Errorf("bare key: %d %s", status, body)
	}
	if _, ct, _ := get(boss, "sat/x.svg"); ct != "application/octet-stream" {
		t.Errorf("svg served as %s", ct)
	}
	for _, key := range []string{"../secret", "sat/a<b>.png", ""} {
		if status, _, _ := get(boss, key); status != 400 {
			t.Errorf("key %q: %d", key, status)
		}
	}
	if status, _, _ := get(boss, "sat/missing.png"); status != 404 {
		t.Errorf("missing: %d", status)
	}
	if status, _, _ := get(novza, "sat/g.png"); status != 403 {
		t.Errorf("branch admin: %d", status)
	}
}
