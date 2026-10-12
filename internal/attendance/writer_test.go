package attendance

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/nazacd/makonbook-api/internal/hollihop"
)

type fakeHolliHop struct {
	got    [][]hollihop.StudentPass
	errs   []hollihop.PassError
	failed error
}

func (f *fakeHolliHop) SetStudentPasses(_ context.Context, rows []hollihop.StudentPass) ([]hollihop.PassError, error) {
	f.got = append(f.got, rows)
	return f.errs, f.failed
}

// outbox returns student → "status pass" for the session's rows that are not superseded.
func (e *env) outbox() map[int32]string {
	e.t.Helper()
	rows, err := e.pool.Query(e.t.Context(),
		`SELECT student_client_id, status, pass FROM hollihop_outbox WHERE status <> 'superseded' ORDER BY id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := map[int32]string{}
	for rows.Next() {
		var id int32
		var status string
		var pass bool
		if err := rows.Scan(&id, &status, &pass); err != nil {
			e.t.Fatal(err)
		}
		out[id] = status + map[bool]string{true: " absent", false: " present"}[pass]
	}
	return out
}

// finishedLesson: group 100 on Monday — 1 present, 2 late, 4 and 5 absent.
func finishedLesson(t *testing.T, e *env) Session {
	t.Helper()
	ctx := t.Context()
	e.clock = at("13:55")
	s, err := e.svc.Open(ctx, 10, 100, day("2026-10-12"))
	if err != nil {
		t.Fatal(err)
	}
	secret := mustSecret(t, s)
	if _, err := e.svc.Scan(ctx, 1, s.ID, Window(e.clock), Sign(secret, s.ID, Window(e.clock)), "p1"); err != nil {
		t.Fatal(err)
	}
	e.clock = at("14:20")
	if _, err := e.svc.Scan(ctx, 2, s.ID, Window(e.clock), Sign(secret, s.ID, Window(e.clock)), "p2"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Finish(ctx, 10, s.ID); err != nil {
		t.Fatal(err)
	}
	return s
}

func mustSecret(t *testing.T, s Session) []byte {
	t.Helper()
	b, err := decodeSecret(s.QR.Secret)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFinishQueuesWriteBack(t *testing.T) {
	e := setup(t)
	ctx := t.Context()
	s := finishedLesson(t, e)

	want := map[int32]string{1: "pending present", 2: "pending present", 4: "pending absent", 5: "pending absent"}
	if got := e.outbox(); !equal(got, want) {
		t.Errorf("after finish: %v", got)
	}

	// Corrections: late → present changes nothing in HolliHop; absent →
	// present replaces the queued row.
	e.clock = at("16:00")
	if _, err := e.svc.SetMark(ctx, 10, s.ID, 2, StatePresent); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetMark(ctx, 10, s.ID, 5, StatePresent); err != nil {
		t.Fatal(err)
	}
	want[5] = "pending present"
	if got := e.outbox(); !equal(got, want) {
		t.Errorf("after corrections: %v", got)
	}
	var superseded int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM hollihop_outbox WHERE status = 'superseded'`).Scan(&superseded)
	if superseded != 1 {
		t.Errorf("superseded rows = %d", superseded)
	}
}

func TestWriterDryRun(t *testing.T) {
	e := setup(t)
	finishedLesson(t, e)
	hh := &fakeHolliHop{}
	w := &Writer{Pool: e.pool, HolliHop: hh, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return e.clock }}

	res, err := w.RunOnce(t.Context())
	if err != nil || res.DryRun != 4 || len(hh.got) != 0 {
		t.Fatalf("dry run: %+v %v, calls %d", res, err, len(hh.got))
	}
	if got := e.outbox(); got[1] != "dry_run present" || got[4] != "dry_run absent" {
		t.Errorf("outbox = %v", got)
	}
	if res, _ := w.RunOnce(t.Context()); res.Claimed != 0 {
		t.Errorf("dry-run rows were claimed again: %+v", res)
	}
}

func TestWriterSends(t *testing.T) {
	e := setup(t)
	ctx := t.Context()
	finishedLesson(t, e)
	hh := &fakeHolliHop{errs: []hollihop.PassError{
		{Message: hollihop.PassUnchanged, Date: "2026-10-12T00:00:00", EdUnitID: 100, StudentClientID: 2},
		{Message: hollihop.PassSetManually, Date: "2026-10-12", EdUnitID: 100, StudentClientID: 4},
		{Message: "Something odd", Date: "2026-10-12", EdUnitID: 100, StudentClientID: 5},
	}}
	w := &Writer{Pool: e.pool, HolliHop: hh, Enabled: true, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return e.clock }}

	res, err := w.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 1 || res.Unchanged != 1 || res.SetManually != 1 || res.Retried != 1 {
		t.Errorf("result = %+v", res)
	}
	// The request: present/late → Pass=false, absent → Pass=true, billing
	// untouched, admins' manual entries never overwritten.
	for _, p := range hh.got[0] {
		if p.Date != "2026-10-12" || p.EdUnitID != 100 || p.Payable != nil || p.OverwriteAcceptedManually {
			t.Errorf("row = %+v", p)
		}
		if wantPass := p.StudentClientID == 4 || p.StudentClientID == 5; p.Pass != wantPass {
			t.Errorf("student %d: Pass = %v", p.StudentClientID, p.Pass)
		}
	}
	want := map[int32]string{1: "sent present", 2: "unchanged present", 4: "set_manually absent", 5: "pending absent"}
	if got := e.outbox(); !equal(got, want) {
		t.Errorf("outbox = %v", got)
	}

	// The odd row is retried after its backoff, with Payable=true when
	// absences are configured as billed; then HolliHop is down for good.
	hh.errs, hh.failed = nil, errors.New("status 502")
	w.AbsentPayable = true
	if res, _ := w.RunOnce(ctx); res.Claimed != 0 {
		t.Errorf("retried before its backoff: %+v", res)
	}
	for i := range 12 {
		e.clock = e.clock.Add(2 * time.Hour)
		res, err := w.RunOnce(ctx)
		if i == 0 {
			if err == nil || res.Retried != 1 {
				t.Errorf("failed call: %+v %v", res, err)
			}
			last := hh.got[len(hh.got)-1][0]
			if last.StudentClientID != 5 || last.Payable == nil || !*last.Payable {
				t.Errorf("retried row = %+v", last)
			}
		}
	}
	if got := e.outbox(); got[5] != "failed absent" {
		t.Errorf("after giving up: %v", got)
	}
}

func equal(a, b map[int32]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
