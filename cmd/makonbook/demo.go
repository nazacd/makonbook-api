package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nazacd/makonbook-api/internal/demo"
	"github.com/nazacd/makonbook-api/internal/parents"
	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/alert"
	"github.com/nazacd/makonbook-api/internal/platform/config"
	"github.com/nazacd/makonbook-api/internal/practice"
)

const demoUsage = `
  demo [remove]             the demo student account for app store reviewers (username
                            demo.student, password DEMO_PASSWORD): make it or bring it up to
                            date now (the worker also does it daily), or delete it
`

func validDemoArgs(args []string) bool {
	return len(args) == 1 || (len(args) == 2 && args[1] == "remove")
}

func demoService(cfg config.Config, pool *pgxpool.Pool, log *slog.Logger) *demo.Service {
	codes := parents.NewService(pool, people.NewDirectory(pool), nil, alert.Log{Logger: log}, "")
	return demo.NewService(pool, practice.NewService(pool, nil), codes, cfg.Location)
}

func runDemo(ctx context.Context, cfg config.Config, args []string, log *slog.Logger, stdout io.Writer) error {
	pool, err := openDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	svc := demoService(cfg, pool, log)
	if len(args) == 2 {
		if err := svc.Remove(ctx); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "The demo account is deleted. (While DEMO_PASSWORD is set, the worker makes it again: remove that from .env first.)")
		return nil
	}
	if cfg.DemoPassword == "" {
		return errors.New("DEMO_PASSWORD is not set: put the demo account's password in .env")
	}
	res, err := svc.Setup(ctx, cfg.DemoPassword, time.Now())
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Demo account ready.\n  username     %s\n  password     DEMO_PASSWORD from .env\n  parent code  %s\n  practice\n",
		res.Username, res.ParentCode)
	for _, line := range res.Practice {
		fmt.Fprintf(stdout, "    %s\n", line)
	}
	return nil
}

// demoLoop keeps the demo account current: at start, then each new day.
func demoLoop(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, log *slog.Logger) {
	svc := demoService(cfg, pool, log)
	last := ""
	for {
		if day := time.Now().In(cfg.Location).Format(time.DateOnly); day != last {
			if res, err := svc.Setup(ctx, cfg.DemoPassword, time.Now()); err != nil {
				if ctx.Err() == nil {
					log.Error("demo account", "err", err)
				}
			} else {
				last = day
				log.Info("demo account up to date", "username", res.Username, "practice", strings.Join(res.Practice, "; "))
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Hour):
		}
	}
}
