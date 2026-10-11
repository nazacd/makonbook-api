package sync

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nazacd/makonbook-api/internal/sync/syncdb"
)

// Loop runs the sync until ctx is cancelled: an incremental run every
// interval, and a full run once a day after fullAt (local time of day in
// r.Location) — or immediately if no full run has succeeded since the last
// fullAt. Failures are logged and alerted, never fatal.
func (r *Runner) Loop(ctx context.Context, interval, fullAt time.Duration) error {
	for {
		mode := ModeIncremental
		last, err := syncdb.New(r.Pool).LastSuccessfulFullRun(ctx)
		if err != nil {
			r.Log.Error("read last full sync", slog.Any("err", err))
		} else if fullDue(r.now(), timeOf(last), fullAt, r.Location) {
			mode = ModeFull
		}

		rep, err := r.Run(ctx, mode, Options{})
		switch {
		case ctx.Err() != nil:
			return nil
		case errors.Is(err, ErrAlreadyRunning):
			r.Log.Info("sync skipped: another run is in progress")
		case err != nil:
			r.Log.Error("sync run failed", slog.Int64("run_id", rep.RunID), slog.String("mode", string(rep.Mode)), slog.Any("err", err))
		default:
			r.Log.Info("sync run succeeded",
				slog.Int64("run_id", rep.RunID),
				slog.String("mode", string(rep.Mode)),
				slog.Duration("duration", rep.Duration),
				slog.Int("active_students", rep.People.ActiveStudents),
				slog.Int("active_teachers", rep.People.ActiveTeachers),
				slog.Int("login_phones", rep.People.LoginPhones),
			)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

// fullDue reports whether the last successful full run happened before the
// most recent occurrence of fullAt.
func fullDue(now, lastFull time.Time, fullAt time.Duration, loc *time.Location) bool {
	local := now.In(loc)
	y, m, d := local.Date()
	slot := time.Date(y, m, d, 0, 0, 0, 0, loc).Add(fullAt)
	if local.Before(slot) {
		slot = slot.AddDate(0, 0, -1)
	}
	return lastFull.Before(slot)
}

func timeOf(ts pgtype.Timestamptz) time.Time {
	if !ts.Valid || ts.InfinityModifier != pgtype.Finite {
		return time.Time{}
	}
	return ts.Time
}
