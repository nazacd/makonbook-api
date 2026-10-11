// Package sync mirrors HolliHop into MakonBook's database (spec 6): it fetches
// teachers, groups, students and enrollments, hands them to people.Apply in
// one transaction, and keeps a log of runs.
package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nazacd/makonbook-api/internal/hollihop"
	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/alert"
	"github.com/nazacd/makonbook-api/internal/sync/syncdb"
)

// Mode selects how students are pulled. Teachers, groups and enrollments are
// always pulled in full.
type Mode string

const (
	// ModeFull pulls every student; students no longer in HolliHop are
	// deactivated. Run nightly, because incremental pulls miss name changes.
	ModeFull Mode = "full"
	// ModeIncremental pulls only students whose status or contacts changed
	// since the last run.
	ModeIncremental Mode = "incremental"
)

const (
	cursorStudents = "hollihop_students_updated_since"
	// lockKey is the Postgres advisory lock that keeps runs from overlapping.
	lockKey int64 = 0x4d616b6f6e53796e // "MakonSyn"
)

var (
	// ErrAlreadyRunning means another process holds the sync lock.
	ErrAlreadyRunning = errors.New("another sync run is in progress")
	// ErrBlocked means the mass-change guard rejected the run; nothing was
	// written. An admin re-runs it with AllowMassChange once satisfied the
	// change is real.
	ErrBlocked = errors.New("sync blocked by mass-change guard")
)

// Source is the subset of the HolliHop client the sync uses.
type Source interface {
	GetTeachers(ctx context.Context) ([]hollihop.Teacher, error)
	GetEdUnits(ctx context.Context) ([]hollihop.EdUnit, error)
	GetStudents(ctx context.Context, updatedSince string) ([]hollihop.Student, string, error)
	GetEdUnitStudents(ctx context.Context) ([]hollihop.EdUnitStudent, error)
}

// Guard rejects a run that would deactivate too many people at once — the
// signature of a broken or partial HolliHop response, not of real life
// (spec 6.2).
type Guard struct {
	MaxRatio float64 // of the people active before the run
	MinCount int     // never block for fewer deactivations than this
}

// DefaultGuard blocks runs deactivating more than 10 % of active students or
// teachers, ignoring changes of fewer than 5 people.
var DefaultGuard = Guard{MaxRatio: 0.10, MinCount: 5}

// Check returns an ErrBlocked error describing what would change, or nil.
func (g Guard) Check(res people.Result) error {
	var problems []string
	if n, before := len(res.DeactivatedStudents), res.ActiveStudentsBefore; g.exceeded(before, n) {
		problems = append(problems, fmt.Sprintf("%d of %d active students would be deactivated", n, before))
	}
	if n, before := len(res.DeactivatedTeachers), res.ActiveTeachersBefore; g.exceeded(before, n) {
		problems = append(problems, fmt.Sprintf("%d of %d active teachers would be deactivated", n, before))
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrBlocked, strings.Join(problems, "; "))
	}
	return nil
}

func (g Guard) exceeded(before, deactivated int) bool {
	return deactivated >= g.MinCount && float64(deactivated) > float64(before)*g.MaxRatio
}

// Runner performs sync runs.
type Runner struct {
	Source   Source
	Pool     *pgxpool.Pool
	Notifier alert.Notifier
	Location *time.Location // school time zone: defines "today"
	Guard    Guard
	Log      *slog.Logger
	Now      func() time.Time // for tests; defaults to time.Now
}

// Options tweak a single run.
type Options struct {
	// AllowMassChange applies the run even if the guard would block it.
	AllowMassChange bool
}

// Report describes a finished run; it is also what sync_runs.stats stores.
type Report struct {
	RunID       int64         `json:"run_id"`
	Mode        Mode          `json:"mode"`
	Status      string        `json:"status"`
	Duration    time.Duration `json:"duration_ns"`
	Fetched     FetchCounts   `json:"fetched"`
	Skipped     []string      `json:"skipped,omitempty"`
	People      people.Result `json:"people"`
	MassChange  bool          `json:"mass_change_allowed,omitempty"`
	StudentsNow string        `json:"students_cursor,omitempty"`
}

type FetchCounts struct {
	Teachers    int `json:"teachers"`
	Groups      int `json:"groups"`
	Students    int `json:"students"`
	Enrollments int `json:"enrollments"`
}

// Run status values, as stored in sync_runs.status.
const (
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusBlocked   = "blocked"
)

// Run performs one sync. An incremental run without a stored cursor (first
// run ever) becomes a full run.
func (r *Runner) Run(ctx context.Context, mode Mode, opts Options) (Report, error) {
	conn, err := r.Pool.Acquire(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	locked, err := syncdb.New(conn).TryLock(ctx, lockKey)
	if err != nil {
		return Report{}, fmt.Errorf("take sync lock: %w", err)
	}
	if !locked {
		return Report{}, ErrAlreadyRunning
	}
	// Bookkeeping must survive the caller cancelling (e.g. shutdown) so the
	// run is recorded as failed instead of staying "running" forever.
	bg := context.WithoutCancel(ctx)
	defer func() { _, _ = syncdb.New(conn).Unlock(bg, lockKey) }()

	q := syncdb.New(r.Pool)
	since := ""
	if mode == ModeIncremental {
		since, err = q.GetCursor(ctx, cursorStudents)
		if errors.Is(err, pgx.ErrNoRows) {
			mode = ModeFull
		} else if err != nil {
			return Report{}, fmt.Errorf("read cursor: %w", err)
		}
	}

	runID, err := q.CreateRun(ctx, string(mode))
	if err != nil {
		return Report{}, fmt.Errorf("record run: %w", err)
	}
	start := r.now()
	rep := Report{RunID: runID, Mode: mode, MassChange: opts.AllowMassChange}

	runErr := r.run(ctx, &rep, since, opts)

	rep.Duration = r.now().Sub(start)
	rep.Status = StatusSucceeded
	errText := ""
	switch {
	case errors.Is(runErr, ErrBlocked):
		rep.Status, errText = StatusBlocked, runErr.Error()
	case runErr != nil:
		rep.Status, errText = StatusFailed, runErr.Error()
	}

	stats, _ := json.Marshal(rep)
	if err := q.FinishRun(bg, syncdb.FinishRunParams{ID: runID, Status: rep.Status, Stats: stats, Error: errText}); err != nil {
		r.Log.Error("record sync run result", slog.Int64("run_id", runID), slog.Any("err", err))
	}
	r.alertOnChange(bg, q, rep, errText)
	return rep, runErr
}

func (r *Runner) run(ctx context.Context, rep *Report, since string, opts Options) error {
	var f fetched
	var err error
	if f.teachers, err = r.Source.GetTeachers(ctx); err != nil {
		return err
	}
	if f.units, err = r.Source.GetEdUnits(ctx); err != nil {
		return err
	}
	var now string
	if f.students, now, err = r.Source.GetStudents(ctx, since); err != nil {
		return err
	}
	if f.enrollments, err = r.Source.GetEdUnitStudents(ctx); err != nil {
		return err
	}
	rep.Fetched = FetchCounts{len(f.teachers), len(f.units), len(f.students), len(f.enrollments)}
	rep.StudentsNow = now

	// An empty answer for data that is never empty is a HolliHop problem,
	// not a school with no teachers.
	if len(f.teachers) == 0 || len(f.units) == 0 || len(f.enrollments) == 0 ||
		(rep.Mode == ModeFull && len(f.students) == 0) {
		return fmt.Errorf("HolliHop returned an empty list (%+v); refusing to apply", rep.Fetched)
	}

	snap, skipped := buildSnapshot(f, dateIn(r.now(), r.Location))
	snap.StudentsComplete = rep.Mode == ModeFull
	rep.Skipped = skipped
	for _, s := range skipped {
		r.Log.Warn("sync skipped a HolliHop row", slog.String("row", s))
	}

	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if rep.People, err = people.Apply(ctx, tx, snap); err != nil {
		return err
	}
	if err := r.Guard.Check(rep.People); err != nil && !opts.AllowMassChange {
		return err // deferred rollback discards everything
	}
	// The cursor moves only together with the data it describes.
	if err := syncdb.New(tx).SetCursor(ctx, syncdb.SetCursorParams{Name: cursorStudents, Value: now}); err != nil {
		return fmt.Errorf("save cursor: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// alertOnChange notifies admins when the sync starts failing or is blocked,
// and when it recovers — not on every run of a long outage.
func (r *Runner) alertOnChange(ctx context.Context, q *syncdb.Queries, rep Report, errText string) {
	prev, err := q.PreviousRunStatus(ctx, rep.RunID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		r.Log.Warn("read previous sync status", slog.Any("err", err))
		return
	}

	var msg string
	switch {
	case rep.Status == StatusBlocked && prev != StatusBlocked:
		msg = fmt.Sprintf("MakonBook sync is BLOCKED (run %d): %s.\nNothing was changed. If this is a real change in HolliHop, re-run with: makonbook sync full --allow-mass-change", rep.RunID, errText)
	case rep.Status == StatusFailed && prev != StatusFailed:
		msg = fmt.Sprintf("MakonBook sync FAILED (run %d, %s): %s", rep.RunID, rep.Mode, errText)
	case rep.Status == StatusSucceeded && (prev == StatusFailed || prev == StatusBlocked):
		msg = fmt.Sprintf("MakonBook sync recovered (run %d, %s).", rep.RunID, rep.Mode)
	default:
		return
	}
	if err := r.Notifier.Notify(ctx, msg); err != nil {
		r.Log.Warn("send sync alert", slog.Any("err", err))
	}
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}
