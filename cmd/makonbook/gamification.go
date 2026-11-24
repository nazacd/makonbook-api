package main

import (
	"context"
	"fmt"
	"io"

	"github.com/nazacd/makonbook-api/internal/gamification"
	"github.com/nazacd/makonbook-api/internal/platform/config"
	"github.com/nazacd/makonbook-api/internal/practice"
	"github.com/nazacd/makonbook-api/internal/roadmap"
)

const gamificationUsage = `
  gamification backfill     write the XP ledger (spec 27) from everything already recorded:
                            attempts, assessments, reviews, games, reading, attendance. Wipes the
                            ledger first, so running it again gives the same result; run it when
                            the API is quiet (XP awarded meanwhile would be redone from history)
`

func runGamification(ctx context.Context, cfg config.Config, args []string, stdout io.Writer) error {
	if len(args) != 2 || args[1] != "backfill" {
		return fmt.Errorf("usage:%s", gamificationUsage)
	}
	pool, err := openDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	svc := gamification.NewService(pool, cfg.Location, nil)
	roadmaps := roadmap.NewService(pool, practice.NewService(pool, nil), nil)
	n, err := svc.Backfill(ctx, func(done, total int) { fmt.Fprintf(stdout, "events written: %d of %d\n", done, total) }, roadmaps.Graduations)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%d events in the ledger\n", n)
	return nil
}
