package roadmap

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/nazacd/makonbook-api/internal/auth"
	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/httpx"
	"github.com/nazacd/makonbook-api/internal/platform/testdb"
	"github.com/nazacd/makonbook-api/internal/practice"
)

type reply struct {
	status int
	body   map[string]any
}

func (r reply) code() string {
	e, _ := r.body["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func (r reply) list(key string) []any {
	l, _ := r.body[key].([]any)
	return l
}

func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func TestRoadmaps(t *testing.T) {
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
	slot := func(id int32) []people.ScheduleItem {
		return []people.ScheduleItem{{ID: id, BeginTime: 14 * time.Hour, EndTime: 15 * time.Hour, Weekdays: 1, TeacherIDs: []int32{10}}}
	}
	_, err = people.Apply(ctx, tx, people.Snapshot{
		Today:            today,
		StudentsComplete: true,
		Offices:          []people.Office{{ID: 3, Name: "NOVZA"}},
		Groups: []people.Group{
			{ID: 100, Name: "Pre SAT Math 1", Discipline: "PRE SAT MATH", LearningType: "GROUP", OfficeID: 3, Schedule: slot(1)},
			{ID: 101, Name: "Pre SAT Math 2", Discipline: "PRE-SAT MATH", LearningType: "GROUP", OfficeID: 3, Schedule: slot(2)},
			{ID: 102, Name: "SAT English", Discipline: "SAT ENG", LearningType: "GROUP", OfficeID: 3, Schedule: slot(3)},
			{ID: 103, Name: "Foundation English", Discipline: "FOUNDATION ENGLISH", LearningType: "GROUP", OfficeID: 3, Schedule: slot(4)},
		},
		Teachers: []people.Teacher{{ID: 10, FirstName: "Ali", LastName: "Valiyev", PhoneRaw: "+998901000010"}},
		Students: []people.Student{
			{ClientID: 1, FirstName: "S", LastName: "One", PhoneRaw: "+998902000001", StatusID: people.StatusStudying},
		},
		Enrollments: []people.Enrollment{
			{GroupID: 100, StudentClientID: 1, Weekdays: 1}, {GroupID: 101, StudentClientID: 1, Weekdays: 1},
			{GroupID: 102, StudentClientID: 1, Weekdays: 1}, {GroupID: 103, StudentClientID: 1, Weekdays: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// Two registered students: one with a phone, one without (email only).
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
	svc := NewService(pool, practiceSvc, people.NewDirectory(pool))
	if _, err := svc.Import(ctx, []Course{
		fixture("math", "pre-sat", "mp-"), fixture("english", "foundation", "ef-"), fixture("math", "foundation", "mf-"),
	}); err != nil {
		t.Fatal(err)
	}
	easy := addQuestions(t, pool, "math", "Linear functions", "easy", 8)
	addQuestions(t, pool, "math", "Linear functions", "medium", 2)
	addQuestions(t, pool, "math", "Linear functions", "hard", 2)

	mux := http.NewServeMux()
	authSvc := auth.NewService(pool, people.NewDirectory(pool), auth.NewHasher()).WithOnboarded(svc.Onboarded)
	authHandler := auth.NewHandler(authSvc, tashkent, false)
	authHandler.Register(mux)
	NewHandler(svc, tashkent, authSvc.Profile, authHandler.Require, authHandler.RequireContent).Register(mux)
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
	student, teacher := login("+998902000001", "0001", true), login("+998901000010", "0010", true)
	registered, noPhone := login("+998935550010", "a-good-password", false), login("nophone@example.com", "a-good-password", false)

	// Teachers have none and don't onboard.
	if r := do("GET", "/v1/me/roadmaps", teacher, nil); r.status != http.StatusOK || len(r.list("roadmaps")) != 0 {
		t.Errorf("a teacher's roadmaps: %v", r)
	}
	if r := do("GET", "/v1/me/roadmaps/math-pre-sat", teacher, nil); r.status != http.StatusNotFound {
		t.Errorf("a teacher's roadmap: %v", r)
	}
	onboarding := map[string]any{
		"graduation": "2028", "sat_dates": []string{today.AddDate(0, 3, 0).Format(dateLayout), today.AddDate(0, -1, 0).Format(dateLayout)},
		"current": map[string]int{"total": 800}, "target": map[string]int{"total": 1300, "math": 650, "rw": 650},
		"focus": "math", "schedule": map[string]any{"days": []string{"tue", "mon"}, "minutes": 45},
	}
	if r := do("POST", "/v1/me/onboarding", teacher, onboarding); r.status != http.StatusConflict || r.code() != CodeNotNeeded {
		t.Errorf("a teacher onboarding: %v", r)
	}

	// An enrolled student: a roadmap per course their groups teach, once each.
	r := do("GET", "/v1/me/roadmaps", student, nil)
	maps := r.list("roadmaps")
	if r.status != http.StatusOK || len(maps) != 2 {
		t.Fatalf("an enrolled student's roadmaps: %v", r)
	}
	mp := obj(maps[0])
	if mp["id"] != "math-pre-sat" || mp["source"] != SourceGroup || mp["group_name"] != "Pre SAT Math 1" || mp["title"] != "Math · Pre-SAT" ||
		mp["free_units"] != nil || mp["lessons_total"] != 4.0 || obj(mp["current"])["lesson_id"] != "mp-1-1" ||
		obj(mp["current"])["university"] != "Example University" || obj(maps[1])["id"] != "english-foundation" {
		t.Errorf("enrolled roadmaps: %v", maps)
	}
	if r := do("GET", "/v1/me", student, nil); r.body["onboarded"] != true {
		t.Errorf("an enrolled student is onboarded: %v", r.body)
	}
	if r := do("POST", "/v1/me/onboarding", student, onboarding); r.status != http.StatusConflict || r.code() != CodeNotNeeded {
		t.Errorf("an enrolled student onboarding: %v", r)
	}
	if r := do("GET", "/v1/me/onboarding", student, nil); r.status != http.StatusNotFound {
		t.Errorf("an enrolled student's onboarding: %v", r)
	}
	r = do("GET", "/v1/me/roadmaps/math-pre-sat", student, nil)
	units := r.list("units")
	if r.status != http.StatusOK || len(units) != 3 || len(r.list("countries")) != 2 ||
		obj(r.list("countries")[0])["code"] != "uz" || obj(r.list("countries")[1])["code"] != nil ||
		obj(units[1])["status"] != StatusLocked || obj(units[1])["lock"] != LockPrevious {
		t.Errorf("an enrolled roadmap: %v", r)
	}
	if r := do("GET", "/v1/me/roadmaps/math-foundation", student, nil); r.status != http.StatusNotFound {
		t.Errorf("a roadmap that isn't theirs: %v", r)
	}
	if r := do("GET", "/v1/me/roadmaps/math-pre-sat/lessons/mp-1-2", student, nil); r.status != http.StatusForbidden || r.code() != CodeLessonLocked || r.body["lesson"] != nil {
		t.Errorf("a locked lesson: %v", r)
	}

	// A registered student: onboarding first.
	if r := do("GET", "/v1/me", registered, nil); r.body["onboarded"] != false {
		t.Errorf("before onboarding: %v", r.body)
	}
	if r := do("GET", "/v1/me/roadmaps", registered, nil); r.status != http.StatusOK || len(r.list("roadmaps")) != 0 {
		t.Errorf("before onboarding: %v", r)
	}
	if r := do("GET", "/v1/me/onboarding", registered, nil); r.status != http.StatusNotFound {
		t.Errorf("onboarding before sending it: %v", r)
	}
	for _, change := range []func(b map[string]any){
		func(b map[string]any) { b["focus"] = "chemistry" },
		func(b map[string]any) { b["graduation"] = 2028 },
		func(b map[string]any) { b["target"] = map[string]int{"total": 1300, "math": 600, "rw": 650} },
		func(b map[string]any) { b["schedule"] = map[string]any{"days": []string{}, "minutes": 45} },
	} {
		b := map[string]any{}
		for k, v := range onboarding {
			b[k] = v
		}
		change(b)
		if r := do("POST", "/v1/me/onboarding", registered, b); r.status != http.StatusUnprocessableEntity || r.code() != CodeInvalidOnboarding {
			t.Errorf("invalid onboarding %v: %v", b, r)
		}
	}
	r = do("POST", "/v1/me/onboarding", registered, onboarding)
	if p := obj(r.body["profile"]); r.status != http.StatusOK || p["onboarded"] != true || len(r.list("roadmaps")) != 1 ||
		obj(r.list("roadmaps")[0])["id"] != "math-foundation" || obj(r.list("roadmaps")[0])["source"] != SourceOnboarding ||
		obj(r.list("roadmaps")[0])["free_units"] != 1.0 {
		t.Fatalf("onboarding: %v", r)
	}
	r = do("GET", "/v1/me/onboarding", registered, nil)
	if r.status != http.StatusOK || r.body["focus"] != "math" || !slices.Equal(r.list("sat_dates"), []any{today.AddDate(0, -1, 0).Format(dateLayout), today.AddDate(0, 3, 0).Format(dateLayout)}) ||
		r.body["updated_at"] == nil {
		t.Errorf("stored onboarding: %v", r)
	}
	goals, err := practiceSvc.Goals(ctx, free)
	if err != nil || goals.TargetMath == nil || *goals.TargetMath != 650 || goals.ExamDate == nil || *goals.ExamDate != today.AddDate(0, 3, 0).Format(dateLayout) {
		t.Errorf("goals: %+v %v", goals, err)
	}

	// The free limit: the first country only.
	r = do("GET", "/v1/me/roadmaps/math-foundation", registered, nil)
	if units := r.list("units"); r.status != http.StatusOK || len(units) != 3 || obj(units[0])["status"] != StatusOpen ||
		obj(units[1])["lock"] != LockEnrolledOnly || obj(obj(units[2])["lessons"].([]any)[0])["lock"] != LockEnrolledOnly {
		t.Errorf("free roadmap: %v", r)
	}
	if r := do("GET", "/v1/me/roadmaps/math-foundation/lessons/mf-2-1", registered, nil); r.status != http.StatusForbidden || r.code() != CodeEnrolledOnly {
		t.Errorf("past the free limit: %v", r)
	}
	if r := do("POST", "/v1/me/roadmaps/math-foundation/lessons/mf-2-1/assessments", registered, map[string]any{"kind": "level_check"}); r.status != http.StatusForbidden || r.code() != CodeEnrolledOnly {
		t.Errorf("an assessment past the free limit: %v", r)
	}
	if r := do("POST", "/v1/me/roadmaps/math-foundation/lessons/mf-1-2/assessments", registered, map[string]any{"kind": "level_check"}); r.status != http.StatusForbidden || r.code() != CodeLessonLocked {
		t.Errorf("an assessment of a locked lesson: %v", r)
	}
	if r := do("GET", "/v1/me/roadmaps/math-foundation/lessons/mf-9-9", registered, nil); r.status != http.StatusNotFound {
		t.Errorf("no such lesson: %v", r)
	}
	r = do("GET", "/v1/me/roadmaps/math-foundation/lessons/mf-1-1", registered, nil)
	if l := obj(r.body["lesson"]); r.status != http.StatusOK || l["unit_id"] != "mf-1" || l["notes"] == "" || len(l["examples"].([]any)) != 1 ||
		obj(r.body["level_check"])["pass_percent"] != 70.0 || obj(r.body["level_check"])["open"] != nil || len(r.list("homework")) != 1 {
		t.Errorf("a lesson: %v", r)
	}

	// The level check: 2 easy, 1 medium, 1 hard, without their keys.
	for _, body := range []map[string]any{{"kind": "quiz"}, {"kind": "homework"}, {"kind": "homework", "index": -1}} {
		if r := do("POST", "/v1/me/roadmaps/math-foundation/lessons/mf-1-1/assessments", registered, body); r.status != http.StatusUnprocessableEntity {
			t.Errorf("start %v: %v", body, r)
		}
	}
	if r := do("POST", "/v1/me/roadmaps/math-foundation/lessons/mf-1-1/assessments", registered, map[string]any{"kind": "homework", "index": 5}); r.status != http.StatusNotFound {
		t.Errorf("a homework set that doesn't exist: %v", r)
	}
	r = do("POST", "/v1/me/roadmaps/math-foundation/lessons/mf-1-1/assessments", registered, map[string]any{"kind": "level_check"})
	qs := r.list("questions")
	if r.status != http.StatusOK || r.body["kind"] != "level_check" || r.body["homework_index"] != nil || r.body["subject"] != "math" ||
		r.body["result"] != nil || r.body["finished_at"] != nil || len(qs) != 4 || obj(r.body["images"]) == nil {
		t.Fatalf("level check: %v", r)
	}
	var levels []any
	for _, q := range qs {
		levels = append(levels, obj(q)["difficulty"])
		if _, ok := obj(q)["correct"]; ok {
			t.Errorf("a key while open: %v", q)
		}
		if _, ok := obj(q)["explanation"]; ok {
			t.Errorf("an explanation while open: %v", q)
		}
	}
	if !slices.Equal(levels, []any{"easy", "easy", "medium", "hard"}) || obj(qs[0])["position"] != 1.0 {
		t.Errorf("mix: %v", levels)
	}
	check := r.body["id"].(string)
	if r := do("POST", "/v1/me/roadmaps/math-foundation/lessons/mf-1-1/assessments", registered, map[string]any{"kind": "level_check"}); r.body["id"] != check {
		t.Errorf("a second start makes another: %v", r.body["id"])
	}
	if r := do("GET", "/v1/me/roadmaps/math-foundation/lessons/mf-1-1", registered, nil); obj(obj(r.body["level_check"])["open"])["id"] != check {
		t.Errorf("the open level check: %v", r.body["level_check"])
	}

	// Answers are saved as they come, and only for its questions.
	ids := make([]string, len(qs))
	for i, q := range qs {
		ids[i] = obj(q)["id"].(string)
	}
	if r := do("PUT", "/v1/me/assessments/"+check+"/answers", registered, map[string]any{"answers": map[string]any{easy[0] + "x": "A"}}); r.status != http.StatusUnprocessableEntity || r.code() != CodeInvalidAnswers {
		t.Errorf("an answer to another question: %v", r)
	}
	if r := do("PUT", "/v1/me/assessments/"+check+"/answers", registered, map[string]any{"answers": map[string]any{ids[0]: "B", ids[1]: "C"}}); r.status != http.StatusNoContent {
		t.Errorf("save: %v", r)
	}
	if r := do("PUT", "/v1/me/assessments/"+check+"/answers", registered, map[string]any{"answers": map[string]any{ids[1]: nil}}); r.status != http.StatusNoContent {
		t.Errorf("clear: %v", r)
	}
	if r := do("GET", "/v1/me/assessments/"+check, registered, nil); obj(r.body["answers"])[ids[0]] != "B" || obj(r.body["answers"])[ids[1]] != nil {
		t.Errorf("saved answers: %v", r.body["answers"])
	}
	if r := do("GET", "/v1/me/assessments/"+check, student, nil); r.status != http.StatusNotFound {
		t.Errorf("someone else's assessment: %v", r)
	}
	if r := do("GET", "/v1/me/assessments/not-a-uuid", registered, nil); r.status != http.StatusNotFound {
		t.Errorf("not an id: %v", r)
	}

	// 2 of 4 right: 50 %, not passed; the keys and explanations come back.
	r = do("POST", "/v1/me/assessments/"+check+"/submit", registered, map[string]any{"answers": map[string]any{ids[2]: "b", ids[3]: "A"}})
	res := obj(r.body["result"])
	if r.status != http.StatusOK || res["percent"] != 50.0 || res["correct"] != 2.0 || res["total"] != 4.0 || res["passed"] != false ||
		res["lesson_done"] != false || r.body["finished_at"] == nil || len(res["items"].([]any)) != 4 {
		t.Fatalf("submit: %v", r)
	}
	item := obj(res["items"].([]any)[3])
	if item["given"] != "A" || item["correct"] != false || item["correct_answer"] != "B" || item["explanation"] != "Because." {
		t.Errorf("an item: %v", item)
	}
	if q := obj(r.list("questions")[0]); q["correct"] != "B" || q["explanation"] != "Because." {
		t.Errorf("a question after: %v", q)
	}
	if r := do("POST", "/v1/me/assessments/"+check+"/submit", registered, map[string]any{"answers": map[string]any{}}); r.status != http.StatusConflict || r.code() != CodeFinished {
		t.Errorf("submit twice: %v", r)
	}
	if r := do("PUT", "/v1/me/assessments/"+check+"/answers", registered, map[string]any{"answers": map[string]any{}}); r.status != http.StatusConflict || r.code() != CodeFinished {
		t.Errorf("save after submit: %v", r)
	}

	// A retake gets new questions where it can (8 easy: the two had come last).
	r = do("POST", "/v1/me/roadmaps/math-foundation/lessons/mf-1-1/assessments", registered, map[string]any{"kind": "level_check"})
	retake := r.body["id"].(string)
	answers := map[string]any{}
	for _, q := range r.list("questions") {
		id := obj(q)["id"].(string)
		answers[id] = "B"
		if obj(q)["difficulty"] == "easy" && slices.Contains(ids, id) {
			t.Errorf("a repeated easy question: %s", id)
		}
	}
	if retake == check {
		t.Fatal("the retake is the finished one")
	}
	r = do("POST", "/v1/me/assessments/"+retake+"/submit", registered, map[string]any{"answers": answers})
	if res := obj(r.body["result"]); res["percent"] != 100.0 || res["passed"] != true || res["lesson_done"] != false {
		t.Errorf("retake: %v", r.body["result"])
	}
	r = do("GET", "/v1/me/roadmaps/math-foundation/lessons/mf-1-1", registered, nil)
	if lc := obj(r.body["level_check"]); lc["best"] != 100.0 || lc["passed"] != true || len(lc["attempts"].([]any)) != 2 ||
		obj(lc["attempts"].([]any)[0])["id"] != retake {
		t.Errorf("level check after: %v", lc)
	}

	// Homework at any score finishes the lesson; the next one opens.
	r = do("POST", "/v1/me/roadmaps/math-foundation/lessons/mf-1-1/assessments", registered, map[string]any{"kind": "homework", "index": 0})
	if r.status != http.StatusOK || r.body["homework_index"] != 0.0 || r.body["title"] != "Set 1" || len(r.list("questions")) != 2 {
		t.Fatalf("homework: %v", r)
	}
	r = do("POST", "/v1/me/assessments/"+r.body["id"].(string)+"/submit", registered, map[string]any{"answers": map[string]any{}})
	if res := obj(r.body["result"]); res["percent"] != 0.0 || res["passed"] != true || res["lesson_done"] != true {
		t.Errorf("homework submitted blank: %v", r.body["result"])
	}
	r = do("GET", "/v1/me/roadmaps/math-foundation", registered, nil)
	lessons := obj(r.list("units")[0])["lessons"].([]any)
	if obj(lessons[0])["status"] != StatusDone || obj(lessons[1])["status"] != StatusOpen || r.body["lessons_done"] != 1.0 ||
		obj(r.body["current"])["lesson_id"] != "mf-1-2" || obj(obj(lessons[0])["homework"])["done"] != 1.0 {
		t.Errorf("after the first lesson: %v", r.body)
	}
	if r := do("GET", "/v1/me/roadmaps/math-foundation/lessons/mf-1-1", registered, nil); obj(r.list("homework")[0])["done"] != true ||
		obj(r.list("homework")[0])["attempt"] == nil {
		t.Errorf("done homework: %v", r.list("homework"))
	}

	// Onboarding again replaces the roadmaps it gave; progress stays.
	onboarding["focus"], onboarding["current"] = "both", map[string]int{"math": 400, "rw": 610}
	r = do("POST", "/v1/me/onboarding", registered, onboarding)
	if r.status != http.StatusOK || len(r.list("roadmaps")) != 1 || obj(r.list("roadmaps")[0])["id"] != "math-foundation" ||
		obj(r.list("roadmaps")[0])["lessons_done"] != 1.0 {
		t.Errorf("onboarding again (english-advanced isn't imported): %v", r)
	}

	// No questions for the skills: 409.
	if r := do("POST", "/v1/me/roadmaps/english-foundation/lessons/ef-1-1/assessments", student, map[string]any{"kind": "level_check"}); r.status != http.StatusConflict || r.code() != CodeNoQuestions {
		t.Errorf("no questions: %v", r)
	}

	// Without a phone: onboarding yes, roadmaps not yet.
	if r := do("POST", "/v1/me/onboarding", noPhone, onboarding); r.status != http.StatusOK {
		t.Errorf("onboarding without a phone: %v", r)
	}
	if r := do("GET", "/v1/me/roadmaps", noPhone, nil); r.status != http.StatusForbidden || r.code() != auth.CodePhoneRequired {
		t.Errorf("roadmaps without a phone: %v", r)
	}
	if r := do("GET", "/v1/me/roadmaps", "", nil); r.status != http.StatusUnauthorized {
		t.Errorf("signed out: %v", r)
	}
}
