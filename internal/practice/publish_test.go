package practice

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/nazacd/makonbook-api/internal/platform/testdb"
)

// signingStore is a bucket in memory that also makes download links.
type signingStore struct{ memStore }

func (signingStore) PresignGet(key string, _ time.Duration) (string, error) {
	return "https://r2.test/" + key + "?signed", nil
}

func TestScale(t *testing.T) {
	for subject, n := range map[string]int{"english": 54, "math": 44} {
		s := Scale(subject, n)
		if len(s) != n+1 || s[0] != 200 || s[n] != 800 {
			t.Errorf("%s: %v", subject, s)
		}
		for i := 1; i <= n; i++ {
			if s[i] < s[i-1] || s[i]%10 != 0 {
				t.Errorf("%s: not rising by tens at %d: %v", subject, i, s)
			}
		}
	}
	if s := Scale("math", 22); s[22] != 800 || s[11] != Scale("math", 44)[22] {
		t.Errorf("a shorter section stretches the scale: %v", s)
	}
}

func TestCorrect(t *testing.T) {
	key := "C"
	mcq := PackageQuestion{Type: "mcq", Key: &key}
	grid := PackageQuestion{Type: "grid_in", Accepted: []string{"1/2", "-3.5"}}
	for answer, want := range map[string]bool{"C": true, "c": true, " C ": true, "B": false, "": false} {
		if mcq.Correct(answer) != want {
			t.Errorf("mcq %q: want %v", answer, want)
		}
	}
	for answer, want := range map[string]bool{"1/2": true, ".5": true, "0.50": true, "2/4": true, "−3.5": true, "-7/2": true,
		"0.4": false, "": false, "abc": false} {
		if grid.Correct(answer) != want {
			t.Errorf("grid-in %q: want %v", answer, want)
		}
	}
}

func TestPublishAndAttempts(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	var shot bytes.Buffer
	if err := png.Encode(&shot, image.NewGray(image.Rect(0, 0, 40, 30))); err != nil {
		t.Fatal(err)
	}
	bank := memStore{"media/sat/g.png": shot.Bytes()}
	uploads := signingStore{memStore{}}
	day := review(t, "DAY1", func(subject string, module, n int, rec map[string]any) {
		switch {
		case subject == "math" && module == 1 && n == 1:
			rec["stem"] = "The graph:\n\n![a line](r2:sat/g.png)\n\nWhat is the slope?"
			rec["figures"] = []map[string]string{{"key": "sat/g.png", "kind": "graph", "alt": "a line"}}
		case subject == "math" && module == 2 && n == 3:
			rec["type"], rec["choices"], rec["key"], rec["accepted"] = "grid_in", nil, nil, []string{"1/2"}
		case subject == "english" && module == 1 && n == 2:
			rec["risk"] = "yellow"
			rec["tags"] = map[string]any{"domain": "Craft and Structure", "skill": "Words in Context", "difficulty": "easy"}
		}
	})
	if _, err := (Importer{Pool: pool}).Import(ctx, load(t, day), false); err != nil {
		t.Fatal(err)
	}
	svc := NewService(pool, uploads).WithBank(bank)

	// One question isn't approved: the test stays locked.
	if got, err := svc.Publish(ctx); err != nil || len(got) != 0 {
		t.Fatalf("locked test published: %+v %v", got, err)
	}
	cat, err := svc.Catalog(ctx)
	if err != nil || len(cat) != 1 || cat[0].Version != nil || cat[0].Approved != cat[0].Questions-1 || cat[0].English != 54 || cat[0].Math != 44 {
		t.Fatalf("catalog while locked: %+v %v", cat, err)
	}
	if _, err := svc.Download(ctx, "DAY1", 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("download of a locked test: %v", err)
	}

	yellow, err := svc.QuestionID(ctx, "DAY1", "english-m1-02")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetApproval(ctx, yellow, "editor", 1, true); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Publish(ctx)
	if err != nil || len(got) != 1 || got[0].Code != "DAY1" || got[0].Version != 1 {
		t.Fatalf("publish: %+v %v", got, err)
	}
	if got, err := svc.Publish(ctx); err != nil || len(got) != 0 {
		t.Errorf("published again without a change: %+v %v", got, err)
	}

	d, err := svc.Download(ctx, "DAY1", 0)
	if err != nil {
		t.Fatal(err)
	}
	var pkg Package
	if err := json.Unmarshal(d.Package, &pkg); err != nil {
		t.Fatal(err)
	}
	img := pkg.Images["sat/g.png"]
	if pkg.Version != 1 || pkg.Format != PackageFormat || len(pkg.Sections) != 2 || pkg.Sections[0].Subject != "english" ||
		len(pkg.Sections[1].Modules) != 2 || pkg.Sections[1].Modules[0].Minutes != 35 || len(pkg.Sections[1].Scale) != 45 ||
		img.Width != 40 || img.Height != 30 || !strings.HasSuffix(img.File, ".png") {
		t.Fatalf("package: %+v", pkg)
	}
	if _, ok := uploads.memStore[ImagePrefix+img.File]; !ok || d.Images[img.File] != "https://r2.test/p/img/"+img.File+"?signed" {
		t.Errorf("image not copied or linked: %v %v", d.Images, len(uploads.memStore))
	}

	// An edit sends the question back to review: students keep version 1
	// until it is approved again, then get version 2.
	q, err := svc.Question(ctx, yellow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update(ctx, yellow, "editor", Edit{BaseVersion: q.Version, Stem: "A better question?", Choices: q.Choices,
		Correct: *q.Correct, Explanation: q.Explanation, Domain: q.Domain, Skill: q.Skill, Figures: q.Figures}); err != nil {
		t.Fatal(err)
	}
	if got, err := svc.Publish(ctx); err != nil || len(got) != 0 {
		t.Errorf("published with a question in review: %+v %v", got, err)
	}
	if cat, _ := svc.Catalog(ctx); cat[0].Version == nil || *cat[0].Version != 1 {
		t.Errorf("the published version stays: %+v", cat[0])
	}
	if _, err := svc.SetApproval(ctx, yellow, "editor", q.Version+1, true); err != nil {
		t.Fatal(err)
	}
	if got, err := svc.Publish(ctx); err != nil || len(got) != 1 || got[0].Version != 2 {
		t.Fatalf("republish: %+v %v", got, err)
	}

	// Attempts, graded from the version the student took (1).
	if _, err := pool.Exec(ctx, `
		INSERT INTO offices (id, name) VALUES (1, 'Main');
		INSERT INTO groups (id, name, discipline, level, learning_type, office_id) VALUES (7, 'SAT 1', 'SAT', '', 'GROUP', 1);
		INSERT INTO teachers (id, first_name, last_name, phone_raw, fired) VALUES (3, 'T', 'Teacher', '', false), (4, 'O', 'Other', '', false);
		INSERT INTO schedule_items (id, group_id, begin_time, end_time, weekdays, classroom, teacher_ids) VALUES (1, 7, '10:00', '12:00', 1, '', '{3}');
		INSERT INTO students (client_id, hh_student_id, first_name, last_name, phone_raw, office_ids)
		VALUES (11, 1, 'Ali', 'Valiyev', '', '{1}'), (12, 2, 'Bek', 'Aliyev', '', '{1}');
		INSERT INTO enrollments (group_id, student_client_id, weekdays) VALUES (7, 11, 1), (7, 12, 1);`); err != nil {
		t.Fatal(err)
	}
	b, half := "B", ".5"
	start := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	in := AttemptInput{ID: "3f2b8a6e-1c4d-4e5f-9a0b-1c2d3e4f5a6b", Test: "DAY1", Version: 1, Sections: []string{"math"},
		StartedAt: start, FinishedAt: start.Add(70 * time.Minute),
		Answers: []Answer{
			{Subject: "math", Module: 1, Position: 1, Answer: &b, Seconds: 60},
			{Subject: "math", Module: 1, Position: 2, Answer: nil, Seconds: 5},
			{Subject: "math", Module: 2, Position: 3, Answer: &half, Seconds: 90},
		}}
	a, err := svc.SaveAttempt(ctx, 11, in)
	if err != nil {
		t.Fatal(err)
	}
	if m := a.Scores["math"]; a.Version != 1 || m.Raw != 2 || m.Questions != 44 || m.Score != Scale("math", 44)[2] || a.Total != nil {
		t.Errorf("graded: %+v", a)
	}
	if again, err := svc.SaveAttempt(ctx, 11, in); err != nil || again.ID != a.ID || again.Scores["math"] != a.Scores["math"] {
		t.Errorf("sent twice: %+v %v", again, err)
	}
	if _, err := svc.SaveAttempt(ctx, 12, in); !errors.Is(err, ErrAttemptTaken) {
		t.Errorf("someone else's id: %v", err)
	}
	var bad *AttemptError
	for name, mod := range map[string]func(*AttemptInput){
		"a section not taken": func(in *AttemptInput) { in.Sections = []string{"english"} },
		"no such version":     func(in *AttemptInput) { in.Version = 9 },
		"twice":               func(in *AttemptInput) { in.Answers = append(in.Answers, in.Answers[0]) },
		"no such question":    func(in *AttemptInput) { in.Answers[0].Position = 99 },
		"backwards":           func(in *AttemptInput) { in.FinishedAt = in.StartedAt.Add(-time.Minute) },
	} {
		c := in
		c.ID = "7d1e2f3a-4b5c-4d6e-8f70-8192a3b4c5d6"
		c.Answers = append([]Answer(nil), in.Answers...)
		mod(&c)
		if _, err := svc.SaveAttempt(ctx, 12, c); !errors.As(err, &bad) {
			t.Errorf("%s: %v", name, err)
		}
	}
	full := in
	full.ID, full.Sections = "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d", []string{"english", "math"}
	if a, err := svc.SaveAttempt(ctx, 12, full); err != nil || a.Total == nil || *a.Total != 200+a.Scores["math"].Score {
		t.Errorf("both sections: %+v %v", a, err)
	}

	mine, err := svc.StudentAttempts(ctx, 11)
	if err != nil || len(mine) != 1 || mine[0].Test != "DAY1" || mine[0].Title == "" {
		t.Errorf("student's attempts: %+v %v", mine, err)
	}
	today := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	if _, err := svc.GroupAttempts(ctx, 4, 7, today); !errors.Is(err, ErrNotTeacher) {
		t.Errorf("another teacher: %v", err)
	}
	group, err := svc.GroupAttempts(ctx, 3, 7, today)
	if err != nil || len(group) != 2 || group[0].LastName != "Aliyev" || len(group[0].Attempts) != 1 || len(group[1].Attempts) != 1 {
		t.Errorf("group: %+v %v", group, err)
	}
}
