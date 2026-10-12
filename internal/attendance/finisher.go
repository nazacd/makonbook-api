package attendance

import (
	"context"
	"log/slog"
	"time"
)

// FinishLoop finishes due sessions every interval until ctx is cancelled
// (the worker runs it next to the sync).
func (s *Service) FinishLoop(ctx context.Context, interval time.Duration, log *slog.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		n, err := s.FinishDue(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			log.Error("finish due attendance sessions", slog.Any("err", err))
		case n > 0:
			log.Info("finished attendance sessions", slog.Int("count", n))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
