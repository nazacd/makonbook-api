package practice

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nazacd/makonbook-api/internal/platform/testdb"
)

// published is a service with DAY1 published, and DAY1's package.
func published(t *testing.T) (*Service, Package) {
	t.Helper()
	pool := testdb.New(t)
	ctx := t.Context()
	if _, err := (Importer{Pool: pool}).Import(ctx, load(t, review(t, "DAY1", nil)), false); err != nil {
		t.Fatal(err)
	}
	svc := NewService(pool, signingStore{memStore{}})
	if _, err := svc.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	pkg, err := svc.LatestPackage(ctx, "DAY1")
	if err != nil {
		t.Fatal(err)
	}
	// Students 11 and 12 are in group 7 (teacher 3); 13 isn't.
	if _, err := pool.Exec(ctx, `
		INSERT INTO offices (id, name) VALUES (1, 'Main');
		INSERT INTO groups (id, name, discipline, level, learning_type, office_id) VALUES (7, 'SAT 1', 'SAT', '', 'GROUP', 1);
		INSERT INTO teachers (id, first_name, last_name, phone_raw, fired) VALUES (3, 'T', 'Teacher', '', false), (4, 'O', 'Other', '', false);
		INSERT INTO schedule_items (id, group_id, begin_time, end_time, weekdays, classroom, teacher_ids) VALUES (1, 7, '10:00', '12:00', 1, '', '{3}');
		INSERT INTO students (client_id, hh_student_id, first_name, last_name, phone_raw, office_ids)
		VALUES (11, 1, 'Ali', 'Valiyev', '', '{1}'), (12, 2, 'Bek', 'Aliyev', '', '{1}'), (13, 3, 'Cho', 'Other', '', '{1}');
		INSERT INTO enrollments (group_id, student_client_id, weekdays) VALUES (7, 11, 1), (7, 12, 1);`); err != nil {
		t.Fatal(err)
	}
	return svc, pkg
}

// ref names a question of pkg by its place.
func ref(pkg Package, subject string, module, position int16) QuestionRef {
	for _, sec := range pkg.Sections {
		for _, m := range sec.Modules {
			for _, q := range m.Questions {
				if sec.Subject == subject && m.Module == module && q.Position == position {
					return QuestionRef{Test: pkg.Code, Version: pkg.Version, Subject: subject, Module: module, Position: position, QuestionID: q.ID}
				}
			}
		}
	}
	return QuestionRef{}
}

func TestParked(t *testing.T) {
	svc, pkg := published(t)
	ctx := t.Context()
	const id1, id2 = "3f2b8a6e-1c4d-4e5f-9a0b-1c2d3e4f5a6b", "7d1e2f3a-4b5c-4d6e-8f70-8192a3b4c5d6"

	p, err := svc.SaveParked(ctx, 11, id1, ParkedInput{QuestionRef: ref(pkg, "math", 2, 5)})
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != id1 || p.Folder != DefaultFolder || p.Status != "waiting" || p.Reason != nil || p.Note != "" || p.Test != "DAY1" ||
		p.Module != 2 || p.Position != 5 || p.CreatedAt.IsZero() {
		t.Errorf("new: %+v", p)
	}
	folder, why := "  Algebra ", "lost_partway"
	changed, err := svc.SaveParked(ctx, 11, id1, ParkedInput{QuestionRef: ref(pkg, "math", 2, 5), Folder: &folder, Reason: &why,
		Note: "the second step", Fix: " divide both sides first ", Status: "sorted"})
	if err != nil {
		t.Fatal(err)
	}
	if changed.Folder != "Algebra" || *changed.Reason != why || changed.Fix != "divide both sides first" || changed.Status != "sorted" ||
		!changed.CreatedAt.Equal(p.CreatedAt) || changed.UpdatedAt.Before(p.UpdatedAt) {
		t.Errorf("changed: %+v", changed)
	}
	if _, err := svc.SaveParked(ctx, 12, id1, ParkedInput{QuestionRef: ref(pkg, "math", 2, 5)}); !errors.Is(err, ErrNotFound) {
		t.Errorf("another student's id: %v", err)
	}

	long := strings.Repeat("é", MaxFolder+1)
	bad, nothing := "bored", "   "
	var invalid *ParkedError
	for name, c := range map[string]struct {
		id string
		in ParkedInput
	}{
		"id":            {"x", ParkedInput{QuestionRef: ref(pkg, "math", 1, 1)}},
		"folder":        {id2, ParkedInput{QuestionRef: ref(pkg, "math", 1, 1), Folder: &long}},
		"reason":        {id2, ParkedInput{QuestionRef: ref(pkg, "math", 1, 1), Reason: &bad}},
		"note":          {id2, ParkedInput{QuestionRef: ref(pkg, "math", 1, 1), Note: strings.Repeat("a", MaxNote+1)}},
		"fix":           {id2, ParkedInput{QuestionRef: ref(pkg, "math", 1, 1), Fix: strings.Repeat("a", MaxFix+1)}},
		"status":        {id2, ParkedInput{QuestionRef: ref(pkg, "math", 1, 1), Status: "done"}},
		"no test":       {id2, ParkedInput{}},
		"subject":       {id2, ParkedInput{QuestionRef: QuestionRef{Test: "DAY1", Version: 1, Subject: "art", Module: 1, Position: 1}}},
		"module":        {id2, ParkedInput{QuestionRef: QuestionRef{Test: "DAY1", Version: 1, Subject: "math", Module: 3, Position: 1}}},
		"question_id":   {id2, ParkedInput{QuestionRef: QuestionRef{Test: "DAY1", Version: 1, Subject: "math", Module: 1, Position: 1, QuestionID: "q"}}},
		"another place": {id2, ParkedInput{QuestionRef: QuestionRef{Test: "DAY1", Version: 1, Subject: "math", Module: 1, Position: 1, QuestionID: ref(pkg, "math", 1, 2).QuestionID}}},
		"no such place": {id2, ParkedInput{QuestionRef: QuestionRef{Test: "DAY1", Version: 1, Subject: "math", Module: 1, Position: 23, QuestionID: ref(pkg, "math", 1, 2).QuestionID}}},
		"no version":    {id2, ParkedInput{QuestionRef: QuestionRef{Test: "DAY1", Version: 2, Subject: "math", Module: 1, Position: 1, QuestionID: ref(pkg, "math", 1, 1).QuestionID}}},
	} {
		if _, err := svc.SaveParked(ctx, 11, c.id, c.in); !errors.As(err, &invalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A blank folder is the default one.
	if p, err := svc.SaveParked(ctx, 12, id2, ParkedInput{QuestionRef: ref(pkg, "english", 1, 3), Folder: &nothing}); err != nil || p.Folder != DefaultFolder {
		t.Errorf("blank folder: %+v %v", p, err)
	}

	if mine, err := svc.MyParked(ctx, 11); err != nil || len(mine) != 1 || mine[0].ID != id1 || mine[0].Fix != changed.Fix {
		t.Errorf("mine: %+v %v", mine, err)
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	if _, err := svc.GroupParked(ctx, 4, 7, today); !errors.Is(err, ErrNotTeacher) {
		t.Errorf("another teacher: %v", err)
	}
	group, err := svc.GroupParked(ctx, 3, 7, today)
	if err != nil || len(group) != 2 || group[0].ID != id2 || group[0].Student.LastName != "Aliyev" || group[1].Student.ID != 11 {
		t.Errorf("group, newest first: %+v %v", group, err)
	}
	if _, err := svc.SaveParked(ctx, 13, "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d", ParkedInput{QuestionRef: ref(pkg, "math", 1, 1)}); err != nil {
		t.Fatal(err)
	}
	if group, _ := svc.GroupParked(ctx, 3, 7, today); len(group) != 2 {
		t.Errorf("a student of another group: %+v", group)
	}

	if err := svc.DeleteParked(ctx, 12, id1); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting another student's: %v", err)
	}
	if err := svc.DeleteParked(ctx, 11, id1); err != nil {
		t.Errorf("delete: %v", err)
	}
	if err := svc.DeleteParked(ctx, 11, id1); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted twice: %v", err)
	}
}
