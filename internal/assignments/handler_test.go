package assignments

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
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

// The tests use a made-up course and made-up questions, never SAT MAKON's.

type reply struct {
	status int
	body   map[string]any
}

func (r reply) code() string {
	e, _ := r.body["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func (r reply) field(name string) string {
	e, _ := r.body["error"].(map[string]any)
	f, _ := e["fields"].(map[string]any)
	s, _ := f[name].(string)
	return s
}

func (r reply) list(key string) []any {
	l, _ := r.body[key].([]any)
	return l
}

func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

// course is a made-up course: one unit of two lessons, then a second unit
// of one, each lesson with one homework set.
func course() roadmap.Course {
	lesson := func(id string) roadmap.Lesson {
		return roadmap.Lesson{
			ID: id, Title: "Lesson " + id, Skills: []string{"Linear functions"}, Prerequisites: []string{}, Minutes: 30,
			Objectives: []string{"Do it"}, Notes: "Notes.", CommonMistakes: []string{},
			LevelCheck: roadmap.LevelCheck{Questions: 4, Mix: roadmap.Mix{Easy: 2, Medium: 1, Hard: 1}, Skills: []string{"Linear functions"}},
			Homework:   []roadmap.Homework{{Title: "Set " + id, Skills: []string{"Linear functions"}, Difficulty: "easy", Questions: 2}},
		}
	}
	stop := roadmap.Stop{University: "Example University", City: "Exampleton", Country: "Nowhere"}
	return roadmap.Course{
		Subject: "math", Level: "pre-sat",
		Countries: []roadmap.Country{{ID: "uzbekistan", Name: "Uzbekistan", Units: []string{"mp-1"}}, {ID: "atlantis", Name: "Atlantis", Units: []string{"mp-2"}}},
		Units: []roadmap.Unit{
			{ID: "mp-1", Title: "Unit one", Domain: "Algebra", Summary: "A unit.", Stop: stop, Lessons: []roadmap.Lesson{lesson("mp-1-1"), lesson("mp-1-2")}},
			{ID: "mp-2", Title: "Unit two", Domain: "Algebra", Summary: "A unit.", Stop: stop, Lessons: []roadmap.Lesson{lesson("mp-2-1")}},
		},
	}
}

// addQuestions puts n questions of a skill and difficulty in the bank,
// approved unless status says otherwise. Multiple choice, B is right.
func addQuestions(t *testing.T, pool *pgxpool.Pool, subject, skill, difficulty, stem string, n int) []string {
	t.Helper()
	var ids []string
	for range n {
		var id string
		err := pool.QueryRow(context.Background(), `
			INSERT INTO questions (subject, type, content_hash, stem, choices, correct, explanation, domain, skill, difficulty, status, approved_at)
			VALUES ($1, 'mcq', gen_random_uuid()::text, $4, '{"A": "1", "B": "2", "C": "3", "D": "4"}', 'B',
			        'Because.', 'Algebra', $2, $3, 'approved', now())
			RETURNING id::text`, subject, skill, difficulty, stem).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// publishTest stores a published mock test: each section one module of two
// questions (B right), scored 200, 500, 800 for 0, 1, 2 right.
func publishTest(t *testing.T, pool *pgxpool.Pool, code string) {
	t.Helper()
	q := func(n int, skill string) practice.PackageQuestion {
		key, diff := "B", "medium"
		return practice.PackageQuestion{
			ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", n), Version: 1, Position: int16(n%10 + 0), Type: "mcq", Stem: "Which?",
			Choices: map[string]string{"A": "1", "B": "2", "C": "3", "D": "4"}, Figures: []practice.Figure{}, Key: &key,
			Explanation: "Because.", Domain: "Domain", Skill: skill, Difficulty: &diff,
		}
	}
	e1, e2, m1, m2 := q(1, "Words in Context"), q(2, "Transitions"), q(1, "Percentages"), q(2, "Circles")
	m1.ID, m2.ID = "00000000-0000-4000-8000-000000000011", "00000000-0000-4000-8000-000000000012"
	pkg := practice.Package{
		Format: 1, Code: code, Title: "Mock " + code, Version: 1, PublishedAt: time.Now(),
		Sections: []practice.Section{
			{Subject: "english", Modules: []practice.Module{{Module: 1, Minutes: 32, Questions: []practice.PackageQuestion{e1, e2}}}, Scale: []int{200, 500, 800}},
			{Subject: "math", Modules: []practice.Module{{Module: 1, Minutes: 35, Questions: []practice.PackageQuestion{m1, m2}}}, Scale: []int{200, 500, 800}},
		},
		Images: map[string]practice.PackageImage{},
	}
	body, _ := json.Marshal(pkg)
	var id string
	if err := pool.QueryRow(context.Background(), `INSERT INTO practice_tests (code, title) VALUES ($1, $2) RETURNING id::text`, code, pkg.Title).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `INSERT INTO practice_test_versions (test_id, version, content_hash, package, bytes)
		VALUES ($1, 1, 'hash', $2, 100)`, id, body); err != nil {
		t.Fatal(err)
	}
}

func TestAssignments(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	tashkent, _ := time.LoadLocation("Asia/Tashkent")
	now := time.Now().In(tashkent)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	slot := func(id, teacher int32) []people.ScheduleItem {
		return []people.ScheduleItem{{ID: id, BeginTime: 14 * time.Hour, EndTime: 15 * time.Hour, Weekdays: 1, TeacherIDs: []int32{teacher}}}
	}
	_, err = people.Apply(ctx, tx, people.Snapshot{
		Today:            today,
		StudentsComplete: true,
		Offices:          []people.Office{{ID: 3, Name: "NOVZA"}},
		Groups: []people.Group{
			{ID: 100, Name: "Pre SAT Math 1", Discipline: "PRE SAT MATH", Level: "", LearningType: "GROUP", OfficeID: 3, Schedule: slot(1, 10)},
			// No level in the discipline: HolliHop's level decides (D38).
			{ID: 101, Name: "SAT Math", Discipline: "SAT MATH", Level: "PRE-SAT", LearningType: "GROUP", OfficeID: 3, Schedule: slot(2, 11)},
			{ID: 102, Name: "Olympiad", Discipline: "OLYMPIAD", Level: "ADVANCED", LearningType: "GROUP", OfficeID: 3, Schedule: slot(3, 11)},
		},
		Teachers: []people.Teacher{
			{ID: 10, FirstName: "Ali", LastName: "Valiyev", PhoneRaw: "+998901000010"},
			{ID: 11, FirstName: "Vali", LastName: "Aliyev", PhoneRaw: "+998901000011"},
		},
		Students: []people.Student{
			{ClientID: 1, FirstName: "Aziz", LastName: "Alpha", PhoneRaw: "+998902000001", StatusID: people.StatusStudying},
			{ClientID: 2, FirstName: "Bobur", LastName: "Beta", PhoneRaw: "+998902000002", StatusID: people.StatusStudying},
			{ClientID: 3, FirstName: "Dilnoza", LastName: "Gamma", PhoneRaw: "+998902000003", StatusID: people.StatusStudying},
		},
		Enrollments: []people.Enrollment{
			{GroupID: 100, StudentClientID: 1, Weekdays: 1}, {GroupID: 100, StudentClientID: 2, Weekdays: 1},
			{GroupID: 101, StudentClientID: 3, Weekdays: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// A registered student with a phone, and one without.
	hash, _ := auth.NewHasher().Hash("a-good-password")
	free, err := people.CreateSelfStudent(ctx, pool, "Web", "Student", "+998935550010")
	if err != nil {
		t.Fatal(err)
	}
	phoneless, err := people.CreateSelfStudent(ctx, pool, "No", "Phone", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO credentials (person_kind, person_id, password_hash, password_changed_at, email)
		VALUES ('student', $1, $3, now(), NULL), ('student', $2, $3, now(), 'nophone@example.com')`, free, phoneless, hash); err != nil {
		t.Fatal(err)
	}

	practiceSvc := practice.NewService(pool, nil)
	directory := people.NewDirectory(pool)
	roadmapSvc := roadmap.NewService(pool, practiceSvc, directory)
	if _, err := roadmapSvc.Import(ctx, []roadmap.Course{course()}); err != nil {
		t.Fatal(err)
	}
	easy := addQuestions(t, pool, "math", "Linear functions", "easy", "Linear easy", 6)
	addQuestions(t, pool, "math", "Linear functions", "medium", "Linear medium", 2)
	addQuestions(t, pool, "math", "Linear functions", "hard", "Linear hard", 2)
	percents := addQuestions(t, pool, "math", "Percentages", "easy", "What is 50% of 10_000?", 4)
	english := addQuestions(t, pool, "english", "Words in Context", "easy", "A word", 1)
	draft := addQuestions(t, pool, "math", "Percentages", "easy", "Not yet", 1)
	if _, err := pool.Exec(ctx, `UPDATE questions SET status = 'review', approved_at = NULL WHERE id = $1`, draft[0]); err != nil {
		t.Fatal(err)
	}
	publishTest(t, pool, "MOCK1")

	mux := http.NewServeMux()
	authSvc := auth.NewService(pool, directory, auth.NewHasher()).WithOnboarded(roadmapSvc.Onboarded)
	authHandler := auth.NewHandler(authSvc, tashkent, false)
	authHandler.Register(mux)
	roadmap.NewHandler(roadmapSvc, tashkent, authSvc.Profile, authHandler.Require, authHandler.RequireContent).Register(mux)
	NewHandler(NewService(pool, roadmapSvc, practiceSvc, tashkent), tashkent, authHandler.Require, authHandler.RequireContent).Register(mux)
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
	login := func(name, password string, change bool) string {
		t.Helper()
		r := do("POST", "/v1/auth/login", "", map[string]string{"login": name, "password": password, "device_id": "d"})
		token, _ := r.body["token"].(string)
		if token == "" {
			t.Fatalf("login %s: %v", name, r.body)
		}
		if change {
			if r := do("POST", "/v1/auth/password", token, map[string]string{"current_password": password, "new_password": "a-good-password"}); r.status != http.StatusNoContent {
				t.Fatalf("password %s: %v", name, r.body)
			}
		}
		return token
	}
	teacher, other := login("+998901000010", "0010", true), login("+998901000011", "0011", true)
	s1, s2, s3 := login("+998902000001", "0001", true), login("+998902000002", "0002", true), login("+998902000003", "0003", true)
	registered, noPhone := login("+998935550010", "a-good-password", false), login("nophone@example.com", "a-good-password", false)
	at := func(d time.Duration) string { return time.Now().Add(d).UTC().Format(time.RFC3339) }

	// Who may: teachers of the group only; students never.
	if r := do("GET", "/v1/teacher/assignments", s1, nil); r.status != http.StatusForbidden || r.code() != httpx.CodeForbidden {
		t.Errorf("a student's teacher list: %v", r)
	}
	if r := do("GET", "/v1/teacher/assignments?group_id=100", other, nil); r.status != http.StatusForbidden {
		t.Errorf("another teacher's group: %v", r)
	}
	if r := do("GET", "/v1/teacher/assignments", teacher, nil); r.status != http.StatusOK || len(r.list("assignments")) != 0 {
		t.Errorf("no assignments yet: %v", r)
	}

	// Drawing: a lesson's level check, easy to hard, whole.
	r := do("POST", "/v1/teacher/assignments/draw", teacher, map[string]any{"course": "math-pre-sat", "lesson_id": "mp-1-1"})
	items := r.list("items")
	if r.status != http.StatusOK || r.body["subject"] != "math" || len(items) != 4 || obj(r.body["mix"])["easy"] != 2.0 ||
		obj(items[0])["correct"] != "B" || obj(items[0])["explanation"] != "Because." || obj(items[0])["position"] != 1.0 {
		t.Fatalf("draw a level check: %v", r)
	}
	var levels []any
	var check []string
	for _, it := range items {
		levels = append(levels, obj(it)["difficulty"])
		check = append(check, obj(it)["id"].(string))
	}
	if !slices.Equal(levels, []any{"easy", "easy", "medium", "hard"}) {
		t.Errorf("draw order: %v", levels)
	}
	if r := do("POST", "/v1/teacher/assignments/draw", teacher, map[string]any{"course": "math-pre-sat", "lesson_id": "mp-1-1", "homework_index": 0}); r.status != http.StatusOK ||
		len(r.list("items")) != 2 || obj(r.body["mix"])["easy"] != 2.0 {
		t.Errorf("draw a homework set: %v", r)
	}
	if r := do("POST", "/v1/teacher/assignments/draw", teacher, map[string]any{"subject": "math", "skills": []string{"linear functions"}, "mix": map[string]int{"easy": 0, "medium": 1, "hard": 1}}); r.status != http.StatusOK ||
		len(r.list("items")) != 2 || obj(r.list("items")[0])["difficulty"] != "medium" {
		t.Errorf("draw custom: %v", r)
	}
	for _, body := range []map[string]any{{}, {"subject": "math"}, {"subject": "math", "skills": []string{"Percentages"}}, {"course": "math-pre-sat"},
		{"subject": "math", "skills": []string{"Percentages"}, "mix": map[string]int{"easy": 61}}} {
		if r := do("POST", "/v1/teacher/assignments/draw", teacher, body); r.status != http.StatusUnprocessableEntity || r.code() != CodeInvalidAssignment {
			t.Errorf("draw %v: %v", body, r)
		}
	}
	for _, body := range []map[string]any{{"course": "math-pre-sat", "lesson_id": "nope"}, {"course": "english-advanced", "lesson_id": "mp-1-1"},
		{"course": "math-pre-sat", "lesson_id": "mp-1-1", "homework_index": 3}} {
		if r := do("POST", "/v1/teacher/assignments/draw", teacher, body); r.status != http.StatusNotFound {
			t.Errorf("draw %v: %v", body, r)
		}
	}
	if r := do("POST", "/v1/teacher/assignments/draw", teacher, map[string]any{"subject": "math", "skills": []string{"Circles"}, "mix": map[string]int{"easy": 2}}); r.status != http.StatusConflict || r.code() != CodeNoQuestions {
		t.Errorf("draw with no questions: %v", r)
	}
	r = do("POST", "/v1/teacher/assignments/draw", teacher, map[string]any{"subject": "math", "question_ids": []string{percents[2], easy[0], percents[0]}})
	if ids := r.list("items"); r.status != http.StatusOK || r.body["mix"] != nil || len(ids) != 3 || obj(ids[0])["id"] != percents[2] ||
		obj(ids[1])["id"] != easy[0] || obj(ids[2])["position"] != 3.0 {
		t.Errorf("draw by ids: %v", r)
	}
	for _, ids := range [][]string{{english[0]}, {draft[0]}, {easy[0], easy[0]}, {"nope"}} {
		if r := do("POST", "/v1/teacher/assignments/draw", teacher, map[string]any{"subject": "math", "question_ids": ids}); r.status != http.StatusUnprocessableEntity || r.field("question_ids") == "" {
			t.Errorf("draw by ids %v: %v", ids, r)
		}
	}
	if r := do("POST", "/v1/teacher/assignments/draw", other, map[string]any{"group_id": 100, "subject": "math", "skills": []string{"Percentages"}, "mix": map[string]int{"easy": 1}}); r.status != http.StatusForbidden {
		t.Errorf("draw for another teacher's group: %v", r)
	}

	// Giving: what is checked.
	due := at(48 * time.Hour)
	levelCheck := map[string]any{"group_id": 100, "kind": "level_check", "due_at": due, "course": "math-pre-sat", "lesson_id": "mp-1-1", "question_ids": check}
	with := func(base map[string]any, kv ...any) map[string]any {
		b := map[string]any{}
		for k, v := range base {
			b[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			if kv[i+1] == nil {
				delete(b, kv[i].(string))
			} else {
				b[kv[i].(string)] = kv[i+1]
			}
		}
		return b
	}
	custom := map[string]any{"group_id": 100, "kind": "homework", "due_at": due, "subject": "math", "skills": []string{"Percentages"},
		"mix": map[string]int{"easy": 2}, "question_ids": []string{percents[0], percents[1]}}
	for field, body := range map[string]map[string]any{
		"kind":           with(levelCheck, "kind", "quiz"),
		"due_at":         with(levelCheck, "due_at", at(-time.Hour)),
		"title":          with(levelCheck, "title", string(bytes.Repeat([]byte("x"), 121))),
		"instructions":   with(levelCheck, "instructions", string(bytes.Repeat([]byte("x"), 2001))),
		"course":         with(levelCheck, "course", nil),
		"lesson_id":      with(levelCheck, "lesson_id", "nope"),
		"homework_index": with(levelCheck, "kind", "homework"),
		"subject":        with(custom, "subject", nil),
		"question_ids":   with(custom, "question_ids", []string{percents[0], draft[0]}),
		"mix":            with(custom, "mix", map[string]int{"easy": -1}),
		"test":           {"group_id": 100, "kind": "exam", "due_at": due, "test": "NOPE", "sections": []string{"math"}},
		"sections":       {"group_id": 100, "kind": "exam", "due_at": due, "test": "MOCK1", "sections": []string{"science"}},
	} {
		if r := do("POST", "/v1/teacher/assignments", teacher, body); r.status != http.StatusUnprocessableEntity || r.code() != CodeInvalidAssignment || r.field(field) == "" {
			t.Errorf("invalid %s: %v", field, r)
		}
	}
	for _, ids := range [][]string{{english[0]}, {percents[0], percents[0]}, {}} {
		if r := do("POST", "/v1/teacher/assignments", teacher, with(custom, "question_ids", ids)); r.status != http.StatusUnprocessableEntity || r.field("question_ids") == "" {
			t.Errorf("questions %v: %v", ids, r)
		}
	}
	if r := do("POST", "/v1/teacher/assignments", other, levelCheck); r.status != http.StatusForbidden {
		t.Errorf("another teacher gives: %v", r)
	}
	if r := do("POST", "/v1/teacher/assignments", s1, levelCheck); r.status != http.StatusForbidden {
		t.Errorf("a student gives: %v", r)
	}

	// A level check from the curriculum: the group's students are notified.
	r = do("POST", "/v1/teacher/assignments", teacher, levelCheck)
	if r.status != http.StatusCreated || r.body["title"] != "Lesson mp-1-1 · Level check" || r.body["status"] != StatusOpen ||
		obj(r.body["lesson"])["unit_title"] != "Unit one" || r.body["questions"] != 4.0 || len(r.list("items")) != 4 ||
		obj(r.list("items")[0])["id"] != check[0] || r.body["group_name"] != "Pre SAT Math 1" || r.body["teacher_id"] != 10.0 ||
		obj(r.body["summary"])["students"] != 2.0 || obj(r.body["summary"])["done"] != 0.0 || obj(r.body["mix"])["hard"] != 1.0 {
		t.Fatalf("give a level check: %v", r)
	}
	lc := r.body["id"].(string)
	r = do("GET", "/v1/me/notifications", s1, nil)
	if n := obj(r.list("items")[0]); r.status != http.StatusOK || len(r.list("items")) != 1 || r.body["unread"] != 1.0 ||
		n["title"] != "New level check: Lesson mp-1-1 · Level check" || n["link"] != "/level-checks" || n["kind"] != "assignment" || n["read_at"] != nil {
		t.Errorf("a student's notification: %v", r)
	}
	if r := do("GET", "/v1/me/notifications", s3, nil); len(r.list("items")) != 0 {
		t.Errorf("another group's student notified: %v", r)
	}

	// Students see a scheduled one too, locked: it can't be started before it
	// opens, and its notification says when it opens.
	r = do("POST", "/v1/teacher/assignments", teacher, with(custom, "opens_at", at(24*time.Hour), "due_at", at(72*time.Hour), "title", "Later"))
	if r.status != http.StatusCreated || r.body["status"] != StatusScheduled || r.body["title"] != "Later" {
		t.Fatalf("a scheduled homework: %v", r)
	}
	scheduled := r.body["id"].(string)
	r = do("GET", "/v1/me/assignments", s1, nil)
	if as := r.list("assignments"); r.status != http.StatusOK || len(as) != 2 || obj(as[0])["id"] != lc || obj(obj(as[0])["mine"])["status"] != StandingNew ||
		obj(as[0])["status"] != StatusOpen || obj(obj(as[0])["lesson"])["lesson_id"] != "mp-1-1" ||
		obj(as[1])["id"] != scheduled || obj(as[1])["status"] != StatusScheduled || obj(obj(as[1])["mine"])["status"] != StandingNew {
		t.Errorf("a student's list: %v", r)
	}
	if n := obj(do("GET", "/v1/me/notifications", s1, nil).list("items")[0]); n["title"] != "New homework: Later" ||
		!strings.Contains(n["body"].(string), " · Opens ") || !strings.Contains(n["body"].(string), " · Due ") {
		t.Errorf("a scheduled homework's notification: %v", n)
	}
	if r := do("GET", "/v1/me/assignments", s3, nil); len(r.list("assignments")) != 0 {
		t.Errorf("another group's assignments: %v", r)
	}
	if r := do("POST", "/v1/me/assignments/"+scheduled+"/start", s1, nil); r.status != http.StatusConflict || r.code() != CodeNotOpen {
		t.Errorf("start before it opens: %v", r)
	}
	if r := do("POST", "/v1/me/assignments/"+lc+"/start", s3, nil); r.status != http.StatusNotFound {
		t.Errorf("start another group's: %v", r)
	}
	if r := do("POST", "/v1/me/assignments/"+lc+"/start", teacher, nil); r.status != http.StatusForbidden {
		t.Errorf("a teacher starts: %v", r)
	}

	// Two students start it: the same questions; a second start continues.
	r = do("POST", "/v1/me/assignments/"+lc+"/start", s1, nil)
	if r.status != http.StatusOK || r.body["roadmap_id"] != "math-pre-sat" || r.body["lesson_id"] != "mp-1-1" || r.body["kind"] != "level_check" ||
		obj(r.body["assignment"])["id"] != lc || obj(r.body["assignment"])["kind"] != "level_check" || r.body["title"] != "Lesson mp-1-1 · Level check" ||
		len(obj(r.body["times"])) != 0 || r.body["result"] != nil {
		t.Fatalf("start: %v", r)
	}
	a1 := r.body["id"].(string)
	got := func(r reply) []string {
		var ids []string
		for _, q := range r.list("questions") {
			ids = append(ids, obj(q)["id"].(string))
		}
		return ids
	}
	if ids := got(r); !slices.Equal(ids, check) {
		t.Errorf("the assignment's questions: %v, want %v", ids, check)
	}
	r = do("POST", "/v1/me/assignments/"+lc+"/start", s2, nil)
	a2 := r.body["id"].(string)
	if ids := got(r); !slices.Equal(ids, check) || a2 == a1 {
		t.Errorf("the second student's: %v", r)
	}
	if r := do("POST", "/v1/me/assignments/"+lc+"/start", s1, nil); r.body["id"] != a1 {
		t.Errorf("a second start: %v", r.body["id"])
	}
	if r := do("GET", "/v1/me/assignments", s1, nil); obj(obj(r.list("assignments")[0])["mine"])["status"] != StandingInProgress ||
		obj(obj(r.list("assignments")[0])["mine"])["assessment_id"] != a1 {
		t.Errorf("in progress: %v", r)
	}

	// Answers and times as they go; then all right with more times.
	for _, body := range []map[string]any{
		{"answers": map[string]any{}, "times": map[string]any{easy[5] + "x": 3}},
		{"answers": map[string]any{}, "times": map[string]any{check[0]: 21601}},
		{"answers": map[string]any{}, "times": map[string]any{check[0]: 1.5}},
	} {
		if r := do("PUT", "/v1/me/assessments/"+a1+"/answers", s1, body); r.status != http.StatusUnprocessableEntity || r.code() != roadmap.CodeInvalidAnswers {
			t.Errorf("bad times %v: %v", body, r)
		}
	}
	if r := do("PUT", "/v1/me/assessments/"+a1+"/answers", s1, map[string]any{"answers": map[string]any{check[0]: "B"}, "times": map[string]any{check[0]: 30}}); r.status != http.StatusNoContent {
		t.Errorf("save: %v", r)
	}
	if r := do("GET", "/v1/me/assessments/"+a1, s1, nil); obj(r.body["times"])[check[0]] != 30.0 {
		t.Errorf("saved times: %v", r.body["times"])
	}
	all := map[string]any{}
	for _, id := range check {
		all[id] = "B"
	}
	r = do("POST", "/v1/me/assessments/"+a1+"/submit", s1, map[string]any{"answers": all, "times": map[string]any{check[1]: 45}})
	if res := obj(r.body["result"]); r.status != http.StatusOK || res["percent"] != 100.0 || res["passed"] != true ||
		obj(r.body["times"])[check[0]] != 30.0 || obj(r.body["times"])[check[1]] != 45.0 {
		t.Fatalf("submit: %v", r)
	}

	// It counts for the roadmap: the lesson's level check is passed.
	r = do("GET", "/v1/me/roadmaps/math-pre-sat/lessons/mp-1-1", s1, nil)
	if l := obj(r.body["level_check"]); r.status != http.StatusOK || l["passed"] != true || l["best"] != 100.0 || len(list(l["attempts"])) != 1 {
		t.Errorf("roadmap credit: %v", r)
	}

	// A retake counts in tries and best; the first stays the result.
	r = do("POST", "/v1/me/assignments/"+lc+"/start", s1, nil)
	retake := r.body["id"].(string)
	if retake == a1 || r.status != http.StatusOK {
		t.Fatalf("retake: %v", r)
	}
	do("POST", "/v1/me/assessments/"+retake+"/submit", s1, map[string]any{"answers": map[string]any{}})
	r = do("GET", "/v1/me/assignments", s1, nil)
	if m := obj(obj(r.list("assignments")[0])["mine"]); m["status"] != StandingDone || m["assessment_id"] != a1 || m["percent"] != 100.0 ||
		m["tries"] != 2.0 || m["best"] != 100.0 || m["passed"] != true || m["late"] != false || m["correct"] != 4.0 || m["total"] != 4.0 ||
		m["started_at"] == nil || m["finished_at"] == nil || m["attempt_id"] != nil || m["scores"] != nil {
		t.Errorf("standing after a retake: %v", m)
	}

	// The deadline moves into the past: the second student is late.
	update := map[string]any{"title": "Check 1", "instructions": "Do it.", "opens_at": at(-2 * time.Hour), "due_at": at(-time.Hour), "closed": false}
	r = do("PUT", "/v1/teacher/assignments/"+lc, teacher, update)
	if r.status != http.StatusOK || r.body["status"] != StatusOverdue || r.body["title"] != "Check 1" || r.body["instructions"] != "Do it." {
		t.Fatalf("update: %v", r)
	}
	if r := do("PUT", "/v1/teacher/assignments/"+lc, teacher, with(update, "due_at", at(-3*time.Hour))); r.status != http.StatusUnprocessableEntity || r.field("due_at") == "" {
		t.Errorf("update the deadline before the opening: %v", r)
	}
	if r := do("PUT", "/v1/teacher/assignments/"+lc, teacher, with(update, "title", " ")); r.status != http.StatusUnprocessableEntity || r.field("title") == "" {
		t.Errorf("update with no title: %v", r)
	}
	if r := do("PUT", "/v1/teacher/assignments/"+lc, other, update); r.status != http.StatusForbidden {
		t.Errorf("another teacher updates: %v", r)
	}
	r = do("POST", "/v1/me/assessments/"+a2+"/submit", s2, map[string]any{"answers": map[string]any{check[0]: "A", check[1]: "B"}})
	if r.status != http.StatusOK || obj(r.body["result"])["percent"] != 25.0 || obj(r.body["assignment"])["title"] != "Check 1" {
		t.Fatalf("a late submit: %v", r)
	}
	if r := do("GET", "/v1/me/assignments", s2, nil); obj(obj(r.list("assignments")[0])["mine"])["late"] != true ||
		obj(obj(r.list("assignments")[0])["mine"])["passed"] != false || obj(r.list("assignments")[0])["status"] != StatusOverdue {
		t.Errorf("late: %v", r)
	}

	// Results: each student, each question, the skills.
	r = do("GET", "/v1/teacher/assignments/"+lc+"/results", teacher, nil)
	students, qs := r.list("students"), r.list("questions")
	if r.status != http.StatusOK || len(students) != 2 || obj(students[0])["client_id"] != 1.0 || obj(students[0])["last_name"] != "Alpha" ||
		obj(students[1])["late"] != true || obj(students[0])["tries"] != 2.0 || len(qs) != 4 {
		t.Fatalf("results: %v", r)
	}
	// Late is against the deadline as it is now: both finished after it.
	sum := obj(obj(r.body["assignment"])["summary"])
	if sum["students"] != 2.0 || sum["done"] != 2.0 || sum["late"] != 2.0 || sum["in_progress"] != 0.0 || sum["average_percent"] != 62.5 || sum["average_score"] != nil {
		t.Errorf("summary: %v", sum)
	}
	q0, q2 := obj(qs[0]), obj(qs[2])
	if q0["question_id"] != check[0] || q0["answered"] != 2.0 || q0["correct"] != 1.0 || q0["avg_seconds"] != 30.0 || q0["subject"] != "math" ||
		q0["module"] != nil || q0["skill"] != "Linear functions" ||
		!equalCounts(obj(q0["answers"]), map[string]float64{"A": 1, "B": 1, "C": 0, "D": 0, "blank": 0}) {
		t.Errorf("question 1: %v", q0)
	}
	if q2["answered"] != 2.0 || q2["correct"] != 1.0 || q2["avg_seconds"] != nil || obj(q2["answers"])["blank"] != 1.0 {
		t.Errorf("question 3: %v", q2)
	}
	if sk := r.list("skills"); len(sk) != 1 || obj(sk[0])["wrong"] != 3.0 || obj(sk[0])["total"] != 8.0 {
		t.Errorf("skills: %v", sk)
	}
	r = do("GET", "/v1/teacher/assignments/"+lc+"/results/1", teacher, nil)
	if a := obj(r.body["assessment"]); r.status != http.StatusOK || a["id"] != a1 || obj(a["result"])["percent"] != 100.0 || r.body["attempt"] != nil ||
		obj(list(a["questions"])[0])["correct"] != "B" {
		t.Errorf("a student's work: %v", r)
	}
	if r := do("GET", "/v1/teacher/assignments/"+lc+"/results/3", teacher, nil); r.status != http.StatusNotFound {
		t.Errorf("a student not in the group: %v", r)
	}
	if r := do("GET", "/v1/teacher/assignments/"+lc+"/results", other, nil); r.status != http.StatusForbidden {
		t.Errorf("another teacher's results: %v", r)
	}
	if r := do("GET", "/v1/teacher/assignments/"+lc, other, nil); r.status != http.StatusForbidden {
		t.Errorf("another teacher reads: %v", r)
	}
	if r := do("GET", "/v1/teacher/assignments/00000000-0000-4000-8000-000000000000", teacher, nil); r.status != http.StatusNotFound {
		t.Errorf("no such assignment: %v", r)
	}

	// Curriculum homework: the roadmap's own start continues the assigned
	// one, and finishing it finishes the lesson.
	r = do("POST", "/v1/teacher/assignments", teacher, map[string]any{"group_id": 100, "kind": "homework", "due_at": due, "course": "math-pre-sat",
		"lesson_id": "mp-1-1", "homework_index": 0, "question_ids": []string{easy[4], easy[5]}})
	if r.status != http.StatusCreated || r.body["title"] != "Set mp-1-1" || r.body["homework_index"] != 0.0 {
		t.Fatalf("give curriculum homework: %v", r)
	}
	hw := r.body["id"].(string)
	r = do("POST", "/v1/me/assignments/"+hw+"/start", s1, nil)
	hwA := r.body["id"].(string)
	if r.body["homework_index"] != 0.0 || r.body["title"] != "Set mp-1-1" {
		t.Errorf("start curriculum homework: %v", r)
	}
	r = do("POST", "/v1/me/roadmaps/math-pre-sat/lessons/mp-1-1/assessments", s1, map[string]any{"kind": "homework", "index": 0})
	if r.body["id"] != hwA || obj(r.body["assignment"])["id"] != hw {
		t.Errorf("the roadmap's start continues the assigned one: %v", r)
	}
	if r := do("GET", "/v1/me/roadmaps/math-pre-sat/lessons/mp-1-1", s1, nil); obj(list(r.body["homework"])[0])["open"] == nil ||
		obj(obj(list(r.body["homework"])[0])["open"])["id"] != hwA {
		t.Errorf("the open homework: %v", r.body["homework"])
	}
	r = do("POST", "/v1/me/assessments/"+hwA+"/submit", s1, map[string]any{"answers": map[string]any{easy[4]: "B"}})
	if obj(r.body["result"])["lesson_done"] != true {
		t.Errorf("the lesson is done: %v", r.body["result"])
	}

	// Custom homework: no course or lesson, end to end; no roadmap credit.
	r = do("POST", "/v1/teacher/assignments", teacher, custom)
	if r.status != http.StatusCreated || r.body["title"] != "Homework" || r.body["lesson"] != nil || r.body["subject"] != "math" ||
		!slices.Equal(list(r.body["skills"]), []any{"Percentages"}) {
		t.Fatalf("give custom homework: %v", r)
	}
	ch := r.body["id"].(string)
	r = do("POST", "/v1/me/assignments/"+ch+"/start", s2, nil)
	if r.status != http.StatusOK || r.body["roadmap_id"] != nil || r.body["lesson_id"] != nil || r.body["homework_index"] != nil ||
		r.body["title"] != "Homework" || r.body["subject"] != "math" || len(r.list("questions")) != 2 {
		t.Fatalf("start custom homework: %v", r)
	}
	chA := r.body["id"].(string)
	if r := do("GET", "/v1/me/assessments/"+chA, s2, nil); r.status != http.StatusOK || r.body["id"] != chA {
		t.Errorf("read custom homework: %v", r)
	}
	if r := do("POST", "/v1/me/assessments/"+chA+"/submit", s2, map[string]any{"answers": map[string]any{percents[0]: "B"}}); r.status != http.StatusOK ||
		obj(r.body["result"])["percent"] != 50.0 || obj(r.body["result"])["passed"] != true || obj(r.body["result"])["lesson_done"] != false {
		t.Errorf("submit custom homework: %v", r)
	}
	if r := do("GET", "/v1/me/roadmaps", s2, nil); obj(r.list("roadmaps")[0])["lessons_done"] != 0.0 {
		t.Errorf("custom homework isn't roadmap progress: %v", r)
	}

	// Drawing for the group prefers what none of its students has had: two
	// of the four Percentages questions were given already.
	for range 3 {
		r = do("POST", "/v1/teacher/assignments/draw", teacher, map[string]any{"group_id": 100, "subject": "math", "skills": []string{"Percentages"}, "mix": map[string]int{"easy": 2}})
		if ids := got(reply{body: map[string]any{"questions": r.list("items")}}); len(ids) != 2 || slices.Contains(ids, percents[0]) || slices.Contains(ids, percents[1]) {
			t.Errorf("draw for the group: %v", ids)
		}
	}

	// Closing: no new starts, work in progress can still be finished; those
	// without work don't see it. Opening it again.
	r = do("POST", "/v1/teacher/assignments", teacher, with(custom, "question_ids", []string{percents[2]}, "title", "Closing"))
	closing := r.body["id"].(string)
	r = do("POST", "/v1/me/assignments/"+closing+"/start", s1, nil)
	closingA := r.body["id"].(string)
	r = do("PUT", "/v1/teacher/assignments/"+closing, teacher, map[string]any{"title": "Closing", "instructions": "", "opens_at": at(-time.Hour), "due_at": due, "closed": true})
	if r.status != http.StatusOK || r.body["status"] != StatusClosed || r.body["closed_at"] == nil {
		t.Fatalf("close: %v", r)
	}
	closedAt := r.body["closed_at"]
	if r := do("PUT", "/v1/teacher/assignments/"+closing, teacher, map[string]any{"title": "Closing", "instructions": "", "opens_at": at(-time.Hour), "due_at": due, "closed": true}); r.body["closed_at"] != closedAt {
		t.Errorf("closing again keeps when: %v", r.body["closed_at"])
	}
	if r := do("POST", "/v1/me/assignments/"+closing+"/start", s2, nil); r.status != http.StatusConflict || r.code() != CodeAssignmentClosed {
		t.Errorf("start a closed one: %v", r)
	}
	if r := do("POST", "/v1/me/assignments/"+closing+"/start", s1, nil); r.status != http.StatusOK || r.body["id"] != closingA {
		t.Errorf("continue a closed one: %v", r)
	}
	ids := func(token string) []string {
		var out []string
		for _, a := range do("GET", "/v1/me/assignments", token, nil).list("assignments") {
			out = append(out, obj(a)["id"].(string))
		}
		return out
	}
	if !slices.Contains(ids(s1), closing) || slices.Contains(ids(s2), closing) {
		t.Errorf("closed: s1 %v, s2 %v", ids(s1), ids(s2))
	}
	if r := do("PUT", "/v1/teacher/assignments/"+closing, teacher, map[string]any{"title": "Closing", "instructions": "", "opens_at": at(-time.Hour), "due_at": due, "closed": false}); r.body["closed_at"] != nil || r.body["status"] != StatusOpen {
		t.Errorf("reopen: %v", r)
	}
	if !slices.Contains(ids(s2), closing) {
		t.Errorf("reopened: %v", ids(s2))
	}

	// Deleting: not once a student started; else with its notifications.
	if r := do("DELETE", "/v1/teacher/assignments/"+lc, teacher, nil); r.status != http.StatusConflict || r.code() != CodeHasWork {
		t.Errorf("delete with work: %v", r)
	}
	if r := do("DELETE", "/v1/teacher/assignments/"+scheduled, other, nil); r.status != http.StatusForbidden {
		t.Errorf("another teacher deletes: %v", r)
	}
	before := len(do("GET", "/v1/me/notifications", s1, nil).list("items"))
	if r := do("DELETE", "/v1/teacher/assignments/"+scheduled, teacher, nil); r.status != http.StatusNoContent {
		t.Errorf("delete: %v", r)
	}
	if after := len(do("GET", "/v1/me/notifications", s1, nil).list("items")); after != before-1 {
		t.Errorf("notifications after a delete: %d, before %d", after, before)
	}
	if r := do("GET", "/v1/teacher/assignments/"+scheduled, teacher, nil); r.status != http.StatusNotFound {
		t.Errorf("deleted: %v", r)
	}

	// An exam: the math section of MOCK1. Attempts count from when it opens,
	// with the sections asked; the first is the result.
	r = do("POST", "/v1/teacher/assignments", teacher, map[string]any{"group_id": 100, "kind": "exam", "opens_at": at(-time.Hour), "due_at": at(time.Hour),
		"test": "MOCK1", "sections": []string{"math"}})
	if r.status != http.StatusCreated || r.body["title"] != "Mock MOCK1" || obj(r.body["test"])["math"] != 2.0 || r.body["questions"] != 2.0 ||
		len(r.list("items")) != 0 || r.body["subject"] != "math" {
		t.Fatalf("give an exam: %v", r)
	}
	exam := r.body["id"].(string)
	if r := do("POST", "/v1/me/assignments/"+exam+"/start", s1, nil); r.status != http.StatusConflict || r.code() != CodeNotAnAssessment {
		t.Errorf("start an exam: %v", r)
	}
	attempt := func(student int32, n int, sections []string, finished time.Duration, answers []practice.Answer) {
		t.Helper()
		end := time.Now().Add(finished)
		_, err := practiceSvc.SaveAttempt(ctx, student, practice.AttemptInput{
			ID: fmt.Sprintf("10000000-0000-4000-8000-%012d", n), Test: "MOCK1", Version: 1, Sections: sections,
			StartedAt: end.Add(-30 * time.Minute), FinishedAt: end, Answers: answers,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	b := "B"
	math := func(first, second *string, sec int32) []practice.Answer {
		return []practice.Answer{{Subject: "math", Module: 1, Position: 1, Answer: first, Seconds: sec}, {Subject: "math", Module: 1, Position: 2, Answer: second, Seconds: sec}}
	}
	attempt(1, 1, []string{"math"}, -2*time.Hour, math(&b, &b, 10))               // before it opened
	attempt(1, 2, []string{"english"}, -50*time.Minute, nil)                      // not the section asked
	attempt(1, 3, []string{"math"}, -40*time.Minute, math(&b, nil, 20))           // counts: 500
	attempt(1, 4, []string{"english", "math"}, -30*time.Minute, math(&b, &b, 40)) // a retake: 800
	attempt(2, 5, []string{"math"}, -20*time.Minute, math(nil, nil, 0))           // 200
	r = do("GET", "/v1/me/assignments", s1, nil)
	var mine map[string]any
	for _, a := range r.list("assignments") {
		if obj(a)["id"] == exam {
			mine = obj(obj(a)["mine"])
		}
	}
	if mine["status"] != StandingDone || mine["attempt_id"] != "10000000-0000-4000-8000-000000000003" || mine["total_score"] != 500.0 ||
		mine["tries"] != 2.0 || mine["best"] != 800.0 || mine["late"] != false || mine["percent"] != nil || mine["passed"] != nil ||
		obj(obj(mine["scores"])["math"])["raw"] != 1.0 || len(obj(mine["scores"])) != 1 {
		t.Errorf("exam standing: %v", mine)
	}
	r = do("GET", "/v1/teacher/assignments/"+exam+"/results", teacher, nil)
	if sum := obj(obj(r.body["assignment"])["summary"]); r.status != http.StatusOK || sum["done"] != 2.0 || sum["average_score"] != 350.0 || sum["average_percent"] != nil {
		t.Errorf("exam summary: %v", r)
	}
	if qs := r.list("questions"); len(qs) != 2 || obj(qs[0])["module"] != 1.0 || obj(qs[0])["subject"] != "math" || obj(qs[0])["answered"] != 2.0 ||
		obj(qs[0])["correct"] != 1.0 || obj(qs[0])["avg_seconds"] != 10.0 || obj(obj(qs[1])["answers"])["blank"] != 2.0 || obj(qs[1])["skill"] != "Circles" {
		t.Errorf("exam questions: %v", qs)
	}
	r = do("GET", "/v1/teacher/assignments/"+exam+"/results/1", teacher, nil)
	if a := obj(r.body["attempt"]); r.status != http.StatusOK || r.body["assessment"] != nil || a["id"] != "10000000-0000-4000-8000-000000000003" ||
		len(list(a["answers"])) != 2 || a["title"] != "Mock MOCK1" {
		t.Errorf("the exam attempt: %v", r)
	}
	if r := do("DELETE", "/v1/teacher/assignments/"+exam, teacher, nil); r.status != http.StatusConflict || r.code() != CodeHasWork {
		t.Errorf("delete an exam with attempts: %v", r)
	}

	// The teacher's list: newest first, with summaries.
	r = do("GET", "/v1/teacher/assignments?group_id=100", teacher, nil)
	if as := r.list("assignments"); r.status != http.StatusOK || len(as) != 5 || obj(as[0])["id"] != exam || obj(as[len(as)-1])["id"] != lc ||
		obj(obj(as[len(as)-1])["summary"])["done"] != 2.0 {
		t.Errorf("the teacher's list: %v", r)
	}
	if r := do("GET", "/v1/teacher/assignments", other, nil); len(r.list("assignments")) != 0 {
		t.Errorf("another teacher's list: %v", r)
	}

	// Notifications: read some, then all; others' ids are ignored.
	r = do("GET", "/v1/me/notifications", s1, nil)
	notes := r.list("items")
	if len(notes) != 5 || r.body["unread"] != 5.0 || obj(notes[0])["title"] != "New exam: Mock MOCK1" {
		t.Fatalf("notifications: %v", r)
	}
	first := obj(notes[0])["id"]
	if r := do("PUT", "/v1/me/notifications/read", s2, map[string]any{"ids": []any{first}}); r.status != http.StatusOK || r.body["unread"] != 5.0 {
		t.Errorf("read another's: %v", r)
	}
	if r := do("PUT", "/v1/me/notifications/read", s1, map[string]any{"ids": []any{first}}); r.body["unread"] != 4.0 {
		t.Errorf("read one: %v", r)
	}
	if r := do("GET", "/v1/me/notifications", s1, nil); obj(r.list("items")[0])["read_at"] == nil {
		t.Errorf("read: %v", r.list("items")[0])
	}
	if r := do("PUT", "/v1/me/notifications/read", s1, map[string]any{}); r.status != http.StatusBadRequest {
		t.Errorf("read nothing: %v", r)
	}
	if r := do("PUT", "/v1/me/notifications/read", s1, map[string]any{"all": true}); r.body["unread"] != 0.0 {
		t.Errorf("read all: %v", r)
	}
	if r := do("GET", "/v1/me/notifications", teacher, nil); r.status != http.StatusOK || len(r.list("items")) != 0 {
		t.Errorf("a teacher's notifications: %v", r)
	}

	// A group's roadmap: its course and each student's standing.
	r = do("GET", "/v1/teacher/groups/100/roadmap", teacher, nil)
	cs, st := r.list("courses"), r.list("students")
	if r.status != http.StatusOK || len(cs) != 1 || obj(cs[0])["id"] != "math-pre-sat" || len(list(obj(cs[0])["lessons"])) != 3 ||
		obj(list(obj(cs[0])["lessons"])[0])["unit_title"] != "Unit one" || len(st) != 2 {
		t.Fatalf("group roadmap: %v", r)
	}
	p1 := obj(obj(obj(st[0])["courses"])["math-pre-sat"])
	l1 := obj(list(p1["lessons"])[0])
	if p1["lessons_done"] != 1.0 || p1["lessons_total"] != 3.0 || p1["current_lesson_id"] != "mp-1-2" || l1["status"] != "done" || l1["passed"] != true ||
		l1["best"] != 100.0 || l1["homework_done"] != 1.0 || obj(list(p1["lessons"])[1])["status"] != "open" || obj(list(p1["lessons"])[2])["status"] != "locked" {
		t.Errorf("student 1's progress: %v", p1)
	}
	p2 := obj(obj(obj(st[1])["courses"])["math-pre-sat"])
	if obj(list(p2["lessons"])[0])["best"] != 25.0 || obj(list(p2["lessons"])[0])["passed"] != false || p2["current_lesson_id"] != "mp-1-1" {
		t.Errorf("student 2's progress: %v", p2)
	}
	r = do("GET", "/v1/teacher/groups/101/roadmap", other, nil)
	if cs := r.list("courses"); r.status != http.StatusOK || len(cs) != 1 || obj(cs[0])["id"] != "math-pre-sat" || len(r.list("students")) != 1 {
		t.Errorf("a group with HolliHop's level: %v", r)
	}
	if r := do("GET", "/v1/teacher/groups/102/roadmap", other, nil); r.status != http.StatusOK || len(r.list("courses")) != 0 {
		t.Errorf("a group with no course: %v", r)
	}
	if r := do("GET", "/v1/teacher/groups/100/roadmap", other, nil); r.status != http.StatusForbidden {
		t.Errorf("another teacher's group roadmap: %v", r)
	}
	if r := do("GET", "/v1/teacher/groups/100/roadmap", s1, nil); r.status != http.StatusForbidden {
		t.Errorf("a student's group roadmap: %v", r)
	}
	if r := do("GET", "/v1/me/roadmaps", s3, nil); len(r.list("roadmaps")) != 1 || obj(r.list("roadmaps")[0])["id"] != "math-pre-sat" {
		t.Errorf("a student's roadmap from HolliHop's level: %v", r)
	}

	// Who gets an empty list, and who is locked out.
	for _, token := range []string{registered, teacher} {
		if r := do("GET", "/v1/me/assignments", token, nil); r.status != http.StatusOK || len(r.list("assignments")) != 0 {
			t.Errorf("an empty list: %v", r)
		}
	}
	if r := do("GET", "/v1/me/assignments", noPhone, nil); r.status != http.StatusForbidden || r.code() != auth.CodePhoneRequired {
		t.Errorf("without a phone: %v", r)
	}
	if r := do("POST", "/v1/me/assignments/"+lc+"/start", registered, nil); r.status != http.StatusNotFound {
		t.Errorf("a registered student starts: %v", r)
	}

	// What teachers build sets from.
	r = do("GET", "/v1/teacher/questions?subject=math&skill=percentages&difficulty=easy&limit=3", teacher, nil)
	if r.status != http.StatusOK || r.body["total"] != 4.0 || len(r.list("questions")) != 3 || obj(r.list("questions")[0])["skill"] != "Percentages" ||
		obj(r.list("questions")[0])["preview"] == "" {
		t.Errorf("questions: %v", r)
	}
	if r := do("GET", "/v1/teacher/questions?subject=math&q=50%25+of+10_", teacher, nil); r.body["total"] != 4.0 {
		t.Errorf("questions by text: %v", r)
	}
	if r := do("GET", "/v1/teacher/questions?subject=math&q=5_%25", teacher, nil); r.body["total"] != 0.0 {
		t.Errorf("_ and %% match themselves: %v", r)
	}
	if r := do("GET", "/v1/teacher/questions?subject=math&offset=3&limit=100", teacher, nil); r.body["total"] != 14.0 || len(r.list("questions")) != 11 {
		t.Errorf("questions, offset: %v", r)
	}
	for _, q := range []string{"", "subject=chemistry", "subject=math&difficulty=x", "subject=math&limit=0", "subject=math&limit=101", "subject=math&offset=-1"} {
		if r := do("GET", "/v1/teacher/questions?"+q, teacher, nil); r.status != http.StatusBadRequest {
			t.Errorf("questions?%s: %v", q, r)
		}
	}
	if r := do("GET", "/v1/teacher/questions?subject=math", s1, nil); r.status != http.StatusForbidden {
		t.Errorf("a student's questions: %v", r)
	}
	r = do("GET", "/v1/teacher/taxonomy", teacher, nil)
	algebra := obj(list(obj(r.body["subjects"])["math"])[0])
	if r.status != http.StatusOK || algebra["domain"] != "Algebra" || len(list(obj(r.body["subjects"])["english"])) != 4 {
		t.Fatalf("taxonomy: %v", r)
	}
	for _, sk := range list(algebra["skills"]) {
		if obj(sk)["skill"] == "Linear functions" && (obj(sk)["easy"] != 6.0 || obj(sk)["medium"] != 2.0 || obj(sk)["total"] != 10.0) {
			t.Errorf("taxonomy counts: %v", sk)
		}
	}
	r = do("GET", "/v1/teacher/curriculum", teacher, nil)
	if cs := r.list("courses"); r.status != http.StatusOK || len(cs) != 1 || obj(cs[0])["title"] != "Math · Pre-SAT" ||
		obj(obj(list(obj(list(obj(cs[0])["units"])[0])["lessons"])[0])["level_check"])["questions"] != 4.0 ||
		obj(list(obj(list(obj(list(obj(cs[0])["units"])[0])["lessons"])[0])["homework"])[0])["difficulty"] != "easy" {
		t.Errorf("curriculum: %v", r)
	}
}

func equalCounts(got map[string]any, want map[string]float64) bool {
	if len(got) != len(want) {
		return false
	}
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}
