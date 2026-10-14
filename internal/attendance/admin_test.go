package attendance

import (
	"errors"
	"testing"
	"time"

	"github.com/nazacd/makonbook-api/internal/lessons"
	"github.com/nazacd/makonbook-api/internal/platform/scope"
)

var (
	novza  = scope.Offices{IDs: []int32{3}}
	minor  = scope.Offices{IDs: []int32{4}}
	every  = scope.Offices{All: true}
	office = Actor{ID: 7, MemberType: "Employee", Name: "Nodira N"}
)

func TestAdminCorrections(t *testing.T) {
	e := setup(t)
	ctx := t.Context()
	monday := day("2026-10-12")

	e.clock = at("14:05")
	s, err := e.svc.Open(ctx, 10, 100, monday)
	if err != nil {
		t.Fatal(err)
	}
	// The teacher marks student 1 present (no scan), then finishes:
	// 2, 4 and 5 become absent.
	if _, err := e.svc.SetMark(ctx, 10, s.ID, 1, StatePresent); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Finish(ctx, 10, s.ID); err != nil {
		t.Fatal(err)
	}

	// Branches: only NOVZA's admins (and super admins) see the lesson.
	if _, err := e.svc.AdminSession(ctx, minor, s.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("MINOR admin sees a NOVZA session: %v", err)
	}
	if _, err := e.svc.AdminSetMark(ctx, minor, office, s.ID, 2, StatePresent); !errors.Is(err, ErrNotFound) {
		t.Errorf("MINOR admin corrects a NOVZA session: %v", err)
	}
	got, err := e.svc.AdminSession(ctx, novza, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.QR != nil || got.Office != "NOVZA" || got.OpenedBy != "Ali Valiyev" || got.FinishedBy == nil || *got.FinishedBy != "teacher" {
		t.Errorf("session: %+v", got)
	}
	for _, m := range got.Marks {
		if m.HolliHop == nil || m.HolliHop.Status != "pending" {
			t.Errorf("mark %d: write-back %+v", m.ClientID, m.HolliHop)
		}
	}

	// Days later, past the teacher's 24 hours, an admin still corrects.
	e.clock = at("15:30").Add(72 * time.Hour)
	got, err = e.svc.AdminSetMark(ctx, novza, office, s.ID, 2, StateLate)
	if err != nil {
		t.Fatal(err)
	}
	if st := markOf(got.Session, 2); st.State != StateLate || *st.Source != ActorAdmin {
		t.Errorf("student 2 after the admin: %+v", st)
	}
	last := got.History[len(got.History)-1]
	if last.ActorKind != ActorAdmin || last.ActorName != "Nodira N" || last.From != StateAbsent || last.To != StateLate {
		t.Errorf("history: %+v", last)
	}
	// The change goes to HolliHop again; the absence not yet sent is replaced.
	var statuses []string
	rows, _ := e.pool.Query(ctx, "SELECT status FROM hollihop_outbox WHERE student_client_id = 2 ORDER BY id")
	for rows.Next() {
		var st string
		_ = rows.Scan(&st)
		statuses = append(statuses, st)
	}
	if len(statuses) != 2 || statuses[0] != "superseded" || statuses[1] != "pending" {
		t.Errorf("student 2's writes: %v", statuses)
	}

	// The teacher can't undo the office's correction, even within 24 hours.
	e.clock = at("16:00")
	if _, err := e.svc.SetMark(ctx, 10, s.ID, 2, StateAbsent); !errors.Is(err, ErrSetByAdmin) {
		t.Errorf("teacher overrides the office: %v", err)
	}
	if _, err := e.svc.SetMark(ctx, 10, s.ID, 4, StatePresent); err != nil {
		t.Errorf("teacher's own correction: %v", err)
	}

	// Report: teacher 10 marked 1 present (not scanned) and 4 present (was
	// absent); the admin's change is listed but not counted against them.
	teachers, changes, err := e.svc.CorrectionsReport(ctx, novza, monday, monday, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(teachers) != 1 || teachers[0].TeacherID != 10 || teachers[0].Corrections != 2 || teachers[0].MadePresent != 2 ||
		teachers[0].LessonsOpened != 1 || teachers[0].LessonsCorrected != 1 {
		t.Errorf("by teacher: %+v", teachers)
	}
	if len(changes) != 3 || changes[0].ActorKind != ActorAdmin || changes[1].ActorKind != ActorTeacher { // newest first
		t.Errorf("changes: %+v", changes)
	}
	if _, changes, _ := e.svc.CorrectionsReport(ctx, novza, monday, monday, 11); len(changes) != 0 {
		t.Errorf("teacher 11's changes: %+v", changes)
	}
	if teachers, changes, _ := e.svc.CorrectionsReport(ctx, minor, monday, monday, 0); len(teachers)+len(changes) != 0 {
		t.Errorf("MINOR sees NOVZA's corrections: %v %v", teachers, changes)
	}
}

func TestAdminWrites(t *testing.T) {
	e := setup(t)
	ctx := t.Context()
	e.clock = at("14:05")
	s, err := e.svc.Open(ctx, 10, 100, day("2026-10-12"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Finish(ctx, 10, s.ID); err != nil {
		t.Fatal(err)
	}
	var failed int64
	if err := e.pool.QueryRow(ctx, `UPDATE hollihop_outbox SET status = 'failed', last_error = 'status 502', done_at = now()
		WHERE student_client_id = 1 RETURNING id`).Scan(&failed); err != nil {
		t.Fatal(err)
	}

	counts, rows, err := e.svc.Writes(ctx, every, []string{"failed"})
	if err != nil {
		t.Fatal(err)
	}
	if counts["failed"] != 1 || counts["pending"] != 3 || counts["sent"] != 0 || len(rows) != 1 ||
		rows[0].ClientID != 1 || rows[0].Error == nil || *rows[0].Error != "status 502" || rows[0].GroupName != "Pre-SAT Math" {
		t.Errorf("writes: %v %+v", counts, rows)
	}
	if counts, rows, _ := e.svc.Writes(ctx, minor, []string{"failed"}); len(rows) != 0 || counts["pending"] != 0 {
		t.Errorf("MINOR sees NOVZA's writes: %v %v", counts, rows)
	}

	if err := e.svc.RetryWrite(ctx, minor, failed); !errors.Is(err, ErrNotFound) {
		t.Errorf("MINOR retries a NOVZA write: %v", err)
	}
	if err := e.svc.RetryWrite(ctx, novza, failed); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RetryWrite(ctx, novza, failed); !errors.Is(err, ErrNotFound) {
		t.Errorf("retrying a pending write: %v", err)
	}

	// A failed write that a later correction replaced no longer needs a person.
	if _, err := e.pool.Exec(ctx, `UPDATE hollihop_outbox SET status = 'failed' WHERE id = $1`, failed); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.AdminSetMark(ctx, novza, office, s.ID, 1, StatePresent); err != nil { // was absent
		t.Fatal(err)
	}
	if _, rows, _ := e.svc.Writes(ctx, every, []string{"failed"}); len(rows) != 0 {
		t.Errorf("replaced failed write still listed: %+v", rows)
	}
}

func TestOfficeLessons(t *testing.T) {
	e := setup(t)
	ctx := t.Context()
	monday := day("2026-10-12")
	lessonsSvc := lessons.NewService(e.pool, tashkent)

	e.clock = at("14:05")
	s, err := e.svc.Open(ctx, 10, 100, monday)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetMark(ctx, 10, s.ID, 1, StatePresent); err != nil {
		t.Fatal(err)
	}

	list, err := lessonsSvc.OfficeLessons(ctx, novza, monday)
	if err != nil {
		t.Fatal(err)
	}
	// Group 104 has lessons but no teacher: not listed.
	if len(list) != 4 || list[0].GroupID != 103 || list[1].GroupID != 100 {
		t.Fatalf("lessons: %+v", list)
	}
	l := list[1]
	if len(l.Teachers) != 1 || l.Teachers[0] != "Ali Valiyev" || l.Session == nil || l.Counts == nil ||
		l.Counts.Present != 1 || l.Counts.Pending != 3 || l.OfficeID == nil || *l.OfficeID != 3 {
		t.Errorf("group 100: %+v counts %+v", l, l.Counts)
	}
	if list[0].Counts != nil {
		t.Errorf("a lesson without attendance has counts: %+v", list[0].Counts)
	}
	if list, _ := lessonsSvc.OfficeLessons(ctx, minor, monday); len(list) != 0 {
		t.Errorf("MINOR sees NOVZA's lessons: %+v", list)
	}
}

func markOf(s Session, clientID int32) Mark {
	for _, m := range s.Marks {
		if m.ClientID == clientID {
			return m
		}
	}
	return Mark{}
}
