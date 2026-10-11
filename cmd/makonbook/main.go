// Command makonbook is the MakonBook backend. One binary, several subcommands:
//
//	makonbook serve            run the HTTP API
//	makonbook migrate up       apply pending database migrations
//	makonbook migrate down     roll back the last migration
//	makonbook migrate status   list migrations and their state
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/nazacd/makonbook-api/internal/app"
	"github.com/nazacd/makonbook-api/internal/platform/config"
	"github.com/nazacd/makonbook-api/internal/platform/httpx"
	"github.com/nazacd/makonbook-api/internal/platform/logging"
	"github.com/nazacd/makonbook-api/internal/platform/migrate"
	"github.com/nazacd/makonbook-api/internal/platform/postgres"
)

const usage = `usage: makonbook <command>

commands:
  serve                     run the HTTP API
  migrate up|down|status    manage database migrations
`

var errUsage = errors.New("invalid usage")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, errUsage) {
			fmt.Fprint(os.Stderr, usage)
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	// Validate the command line before touching configuration, so a typo gets
	// the usage text rather than a "DATABASE_URL is required" error.
	switch {
	case len(args) == 1 && args[0] == "serve":
	case len(args) == 2 && args[0] == "migrate" && (args[1] == "up" || args[1] == "down" || args[1] == "status"):
	default:
		return errUsage
	}

	cfg, err := config.Load(getenv)
	if err != nil {
		return fmt.Errorf("configuration:\n%w", err)
	}
	log := logging.New(stderr, cfg.LogLevel, cfg.LogFormat)
	slog.SetDefault(log)

	if args[0] == "serve" {
		return serve(ctx, cfg, log)
	}
	return runMigrate(ctx, cfg, args[1], stdout)
}

func serve(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	pool, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	// Refuse to serve against an outdated schema: deploys run
	// `makonbook migrate up` first, so pending migrations mean a broken deploy.
	pending, err := migrate.HasPending(ctx, pool)
	if err != nil {
		return err
	}
	if pending {
		return errors.New("database has pending migrations; run `makonbook migrate up` first")
	}

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.HTTPAddr, err)
	}
	return httpx.Serve(ctx, ln, app.Handler(pool, log), log, cfg.ShutdownTimeout)
}

func runMigrate(ctx context.Context, cfg config.Config, cmd string, stdout io.Writer) error {
	pool, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	switch cmd {
	case "up":
		return migrate.Up(ctx, pool, stdout)
	case "down":
		return migrate.Down(ctx, pool, stdout)
	default:
		return migrate.Status(ctx, pool, stdout)
	}
}
