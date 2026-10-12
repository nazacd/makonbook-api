package attendance

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nazacd/makonbook-api/internal/attendance/attendancedb"
	"github.com/nazacd/makonbook-api/internal/hollihop"
)

// Outbox row statuses (see migration 00008).
const (
	OutboxSent        = "sent"
	OutboxUnchanged   = "unchanged"
	OutboxSetManually = "set_manually"
	OutboxDryRun      = "dry_run"
	OutboxSuperseded  = "superseded"
	OutboxFailed      = "failed"
)

const (
	writerBatch       = 200
	writerLease       = 5 * time.Minute // a claimed batch is retried after this if the worker dies
	writerMaxAttempts = 10
	writerMaxBackoff  = time.Hour
)

// PassSetter is the part of the HolliHop client the writer uses.
type PassSetter interface {
	SetStudentPasses(ctx context.Context, rows []hollihop.StudentPass) ([]hollihop.PassError, error)
}

// Writer sends queued attendance to HolliHop (spec 8.6):
//
//   - present / late → Pass=false; absent → Pass=true;
//   - billing is never set, except Payable=true for absences when
//     AbsentPayable is on (D17) — never false;
//   - OverwriteAcceptedManually=false: an admin's manual entry always wins
//     (the row ends as set_manually and is not retried).
//
// With Enabled false (the default) nothing is sent: rows are marked dry_run,
// and logged, so the queue can be checked before go-live. Dry-run rows are
// not sent later when writing is switched on.
type Writer struct {
	Pool          *pgxpool.Pool
	HolliHop      PassSetter
	Enabled       bool
	AbsentPayable bool
	Log           *slog.Logger
	Now           func() time.Time // for tests; defaults to time.Now
}

// WriteResult counts what one batch did.
type WriteResult struct {
	Claimed, Sent, Unchanged, SetManually, DryRun, Retried, Failed int
}

// RunOnce sends one batch of due rows.
func (w *Writer) RunOnce(ctx context.Context) (WriteResult, error) {
	var res WriteResult
	now := w.now()
	q := attendancedb.New(w.Pool)
	rows, err := q.ClaimDue(ctx, attendancedb.ClaimDueParams{
		LeaseUntil: ts(now.Add(writerLease)), DueBefore: ts(now), BatchSize: writerBatch,
	})
	if err != nil {
		return res, fmt.Errorf("claim outbox rows: %w", err)
	}
	res.Claimed = len(rows)
	if len(rows) == 0 {
		return res, nil
	}

	passes := make([]hollihop.StudentPass, len(rows))
	for i, r := range rows {
		passes[i] = hollihop.StudentPass{
			Date:            r.Date.Time.Format(time.DateOnly),
			EdUnitID:        r.GroupID,
			StudentClientID: r.StudentClientID,
			Pass:            r.Pass,
		}
		if r.Pass && w.AbsentPayable {
			payable := true
			passes[i].Payable = &payable
		}
	}

	if !w.Enabled {
		for _, r := range rows {
			if err := w.complete(ctx, q, r.ID, OutboxDryRun, ""); err != nil {
				return res, err
			}
		}
		res.DryRun = len(rows)
		w.Log.Info("HolliHop write-back is off; recorded what would be sent",
			slog.Int("rows", len(rows)), slog.Any("would_send", passes))
		return res, nil
	}

	rowErrs, err := w.HolliHop.SetStudentPasses(ctx, passes)
	if err != nil {
		// The whole call failed: every row is retried.
		for _, r := range rows {
			if err := w.retry(ctx, q, r, err.Error(), &res); err != nil {
				return res, err
			}
		}
		return res, fmt.Errorf("send attendance to HolliHop: %w", err)
	}

	type key struct {
		date           string
		group, student int32
	}
	complaints := map[key]string{}
	for _, e := range rowErrs {
		date := e.Date
		if len(date) > 10 {
			date = date[:10]
		}
		complaints[key{date, e.EdUnitID, e.StudentClientID}] = e.Message
	}
	for i, r := range rows {
		msg, complained := complaints[key{passes[i].Date, r.GroupID, r.StudentClientID}]
		var err error
		switch {
		case !complained:
			res.Sent++
			err = w.complete(ctx, q, r.ID, OutboxSent, "")
		case msg == hollihop.PassUnchanged:
			res.Unchanged++
			err = w.complete(ctx, q, r.ID, OutboxUnchanged, "")
		case msg == hollihop.PassSetManually:
			res.SetManually++
			err = w.complete(ctx, q, r.ID, OutboxSetManually, msg)
		default:
			err = w.retry(ctx, q, r, msg, &res)
		}
		if err != nil {
			return res, err
		}
	}
	return res, nil
}

// Loop runs batches every interval until ctx is cancelled, and immediately
// again while there is more to send.
func (w *Writer) Loop(ctx context.Context, interval time.Duration) {
	if w.Enabled {
		w.Log.Info("HolliHop attendance write-back is ON")
	} else {
		w.Log.Info("HolliHop attendance write-back is off (dry run)")
	}
	for {
		res, err := w.RunOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			w.Log.Error("HolliHop write-back", slog.Any("err", err))
		}
		if res.Sent+res.Unchanged+res.SetManually+res.Retried+res.Failed > 0 {
			w.Log.Info("HolliHop write-back batch",
				slog.Int("sent", res.Sent), slog.Int("unchanged", res.Unchanged),
				slog.Int("set_manually", res.SetManually), slog.Int("retried", res.Retried), slog.Int("failed", res.Failed))
		}
		wait := interval
		if err == nil && res.Claimed == writerBatch {
			wait = 0 // more waiting
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (w *Writer) complete(ctx context.Context, q *attendancedb.Queries, id int64, status, msg string) error {
	err := q.CompleteRow(ctx, attendancedb.CompleteRowParams{
		Status: status, At: ts(w.now()), LastError: pgtype.Text{String: msg, Valid: msg != ""}, ID: id,
	})
	if err != nil {
		return fmt.Errorf("record outbox result: %w", err)
	}
	return nil
}

// retry schedules another attempt with exponential backoff (1, 2, 4 … 60
// minutes), or gives up after writerMaxAttempts.
func (w *Writer) retry(ctx context.Context, q *attendancedb.Queries, r attendancedb.ClaimDueRow, msg string, res *WriteResult) error {
	if r.Attempts >= writerMaxAttempts {
		res.Failed++
		return w.complete(ctx, q, r.ID, OutboxFailed, msg)
	}
	res.Retried++
	backoff := min(time.Minute<<(r.Attempts-1), writerMaxBackoff)
	err := q.RetryRow(ctx, attendancedb.RetryRowParams{
		RetryAt: ts(w.now().Add(backoff)), LastError: pgtype.Text{String: msg, Valid: true}, ID: r.ID,
	})
	if err != nil {
		return fmt.Errorf("reschedule outbox row: %w", err)
	}
	return nil
}

func (w *Writer) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}
