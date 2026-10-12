package attendance

import (
	"context"
	"net/http"
	"testing"
)

func TestStudentTable(t *testing.T) {
	e := setup(t)
	ctx := t.Context()

	// HolliHop's marks for group 100: student 2 absent on Fri 9 Oct, present
	// otherwise; on Mon 12 Oct an admin marked student 4 present.
	monday := day("2026-10-12")
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	err = ApplyRecords(ctx, tx, day("2026-10-01"), monday, []Record{
		{GroupID: 100, ClientID: 2, Date: day("2026-10-07"), Pass: false},
		{GroupID: 100, ClientID: 2, Date: day("2026-10-09"), Pass: true},
		{GroupID: 100, ClientID: 2, Date: monday, Pass: false},
		{GroupID: 100, ClientID: 4, Date: monday, Pass: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	finishedLesson(t, e) // app on Monday: 1 present, 2 late, 4 and 5 absent

	// On the day itself only the app knows: student 2 sees "late" at once.
	got, err := e.svc.StudentTable(ctx, 2, monday)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].GroupName != "Pre-SAT Math" || len(got[0].Days) != 3 ||
		got[0].Days[0] != (RecordDay{"2026-10-12", "late"}) || got[0].Days[1] != (RecordDay{"2026-10-09", "absent"}) {
		t.Errorf("student 2 on Monday: %+v", got)
	}

	// The next day: HolliHop's mark for Monday counts, and the app adds "late".
	tuesday := day("2026-10-13")
	got, _ = e.svc.StudentTable(ctx, 2, tuesday)
	if s := got[0].Summary; s != (Summary{Lessons: 3, Present: 1, Late: 1, Absent: 1}) {
		t.Errorf("student 2 summary: %+v", s)
	}
	// An admin's correction wins over the app: 4 is present, not absent.
	got, _ = e.svc.StudentTable(ctx, 4, tuesday)
	if len(got) != 1 || got[0].Days[0].State != "present" {
		t.Errorf("student 4: %+v", got)
	}
	// Nothing recorded: an empty table, not an error.
	if got, err := e.svc.StudentTable(ctx, 3, tuesday); err != nil || len(got) != 0 {
		t.Errorf("student 3: %+v %v", got, err)
	}

	// Over HTTP: students only.
	student := e.signIn("+998902000002", "0002")
	if status, body := e.do("GET", "/v1/me/attendance", student, nil); status != http.StatusOK || body["groups"] == nil {
		t.Errorf("student: %d %v", status, body)
	}
	teacher := e.signIn("+998901000010", "0010")
	if status, _ := e.do("GET", "/v1/me/attendance", teacher, nil); status != http.StatusForbidden {
		t.Errorf("teacher: %d", status)
	}
}
