// Command makonbook is the MakonBook backend. One binary, several subcommands:
//
//	makonbook serve                      run the HTTP API
//	makonbook worker                     run the HolliHop sync on its schedule
//	makonbook sync full|incremental      run one HolliHop sync now
//	makonbook migrate up|down|status     manage database migrations
//	makonbook reset-password student|teacher <id>
//	makonbook admin …                    admin panel access (see adminUsage)
//	makonbook practice import|list       practice tests (see practiceUsage)
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
	_ "time/tzdata" // the school time zone must resolve even in a minimal container

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nazacd/makonbook-api/internal/app"
	"github.com/nazacd/makonbook-api/internal/attendance"
	"github.com/nazacd/makonbook-api/internal/auth"
	"github.com/nazacd/makonbook-api/internal/hollihop"
	"github.com/nazacd/makonbook-api/internal/lessons"
	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/alert"
	"github.com/nazacd/makonbook-api/internal/platform/config"
	"github.com/nazacd/makonbook-api/internal/platform/httpx"
	"github.com/nazacd/makonbook-api/internal/platform/logging"
	"github.com/nazacd/makonbook-api/internal/platform/migrate"
	"github.com/nazacd/makonbook-api/internal/platform/postgres"
	hhsync "github.com/nazacd/makonbook-api/internal/sync"
)

const usage = `usage: makonbook <command>

commands:
  serve                     run the HTTP API
  worker                    run the HolliHop sync every SYNC_INTERVAL, full nightly at SYNC_FULL_AT
  sync full|incremental [--allow-mass-change]
                            run one HolliHop sync now; --allow-mass-change applies a run
                            the mass-change guard blocked, once you've checked it is real
  migrate up|down|status    manage database migrations
  healthcheck               exit 0 if the local API answers /readyz (for Docker)
  reset-password student|teacher <id>
                            back to the default password (last 4 digits of the phone),
                            unlock the account and log out everywhere
` + adminUsage + practiceUsage

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
	// Container health probe: needs no configuration beyond HTTP_ADDR.
	if len(args) == 1 && args[0] == "healthcheck" {
		return healthcheck(ctx, getenv("HTTP_ADDR"))
	}
	if !validArgs(args) {
		return errUsage
	}

	cfg, err := config.Load(getenv)
	if err != nil {
		return fmt.Errorf("configuration:\n%w", err)
	}
	if args[0] == "worker" || args[0] == "sync" {
		if err := cfg.RequireSync(); err != nil {
			return fmt.Errorf("configuration:\n%w", err)
		}
	}
	log := logging.New(stderr, cfg.LogLevel, cfg.LogFormat)
	slog.SetDefault(log)

	cmd, sub, arg := args[0], "", ""
	if len(args) > 1 {
		sub = args[1]
	}
	if len(args) > 2 {
		arg = args[2]
	}
	switch cmd {
	case "serve":
		return serve(ctx, cfg, log)
	case "worker":
		return worker(ctx, cfg, log)
	case "sync":
		return syncOnce(ctx, cfg, log, hhsync.Mode(sub), len(args) == 3, stdout)
	case "admin":
		return runAdmin(ctx, cfg, args, stdout)
	case "practice":
		return runPractice(ctx, cfg, args, stdout)
	case "reset-password":
		id, _ := strconv.ParseInt(arg, 10, 32) // validated in validArgs
		return resetPassword(ctx, cfg, sub, int32(id), stdout)
	default:
		return runMigrate(ctx, cfg, sub, stdout)
	}
}

// validArgs checks the command line before configuration is loaded, so a
// typo gets the usage text rather than a "DATABASE_URL is required" error.
func validArgs(args []string) bool {
	switch {
	case len(args) == 1 && (args[0] == "serve" || args[0] == "worker"):
		return true
	case len(args) == 2 && args[0] == "migrate":
		return args[1] == "up" || args[1] == "down" || args[1] == "status"
	case len(args) == 3 && args[0] == "reset-password":
		_, err := strconv.ParseInt(args[2], 10, 32)
		return (args[1] == people.KindStudent || args[1] == people.KindTeacher) && err == nil
	case len(args) >= 2 && args[0] == "admin":
		return validAdminArgs(args)
	case len(args) >= 2 && args[0] == "practice":
		return validPracticeArgs(args)
	case (len(args) == 2 || len(args) == 3) && args[0] == "sync":
		modeOK := args[1] == string(hhsync.ModeFull) || args[1] == string(hhsync.ModeIncremental)
		return modeOK && (len(args) == 2 || args[2] == "--allow-mass-change")
	}
	return false
}

// openDB connects and refuses to continue against an outdated schema: deploys
// run `makonbook migrate up` first, so pending migrations mean a broken deploy.
func openDB(ctx context.Context, cfg config.Config) (*pgxpool.Pool, error) {
	pool, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	pending, err := migrate.HasPending(ctx, pool)
	if err == nil && pending {
		err = errors.New("database has pending migrations; run `makonbook migrate up` first")
	}
	if err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func serve(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	pool, err := openDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.HTTPAddr, err)
	}
	deps := app.Deps{Pool: pool, Log: log, Location: cfg.Location, TrustProxy: cfg.TrustProxy, SecureCookies: cfg.Env == config.EnvProduction,
		HolliHopWriteEnabled: cfg.HolliHopWriteEnabled,
		AdminHost:            cfg.AdminHost,
	}
	if cfg.HolliHopBaseURL != "" && cfg.HolliHopAuthKey != "" {
		deps.HolliHop = hollihop.New(cfg.HolliHopBaseURL, cfg.HolliHopAuthKey)
	} else {
		log.Warn("HOLLIHOP_BASE_URL/HOLLIHOP_AUTHKEY not set: admin sign-in is unavailable")
	}
	handler := app.Handler(deps)
	return httpx.Serve(ctx, ln, handler, log, cfg.ShutdownTimeout)
}

func newRunner(cfg config.Config, pool *pgxpool.Pool, log *slog.Logger) *hhsync.Runner {
	var notifier alert.Notifier = alert.Log{Logger: log}
	if cfg.TelegramBotToken != "" {
		notifier = alert.NewTelegram(cfg.TelegramBotToken, cfg.TelegramAlertChatID)
	}
	return &hhsync.Runner{
		Source:   hollihop.New(cfg.HolliHopBaseURL, cfg.HolliHopAuthKey),
		Pool:     pool,
		Notifier: notifier,
		Location: cfg.Location,
		Guard:    hhsync.DefaultGuard,
		Log:      log,
	}
}

func worker(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	pool, err := openDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	log.Info("sync worker started",
		slog.Duration("interval", cfg.SyncInterval),
		slog.Duration("full_at", cfg.SyncFullAt),
		slog.String("time_zone", cfg.Location.String()))
	// Attendance sessions nobody finished close on their own (spec 8.1), and
	// finished attendance is written back to HolliHop (8.6; dry run unless
	// HOLLIHOP_WRITE_ENABLED).
	go attendance.NewService(pool, lessons.NewService(pool, cfg.Location)).FinishLoop(ctx, time.Minute, log)
	writer := &attendance.Writer{
		Pool:          pool,
		HolliHop:      hollihop.New(cfg.HolliHopBaseURL, cfg.HolliHopAuthKey),
		Enabled:       cfg.HolliHopWriteEnabled,
		AbsentPayable: cfg.HolliHopAbsentPayable,
		Log:           log,
	}
	go writer.Loop(ctx, 30*time.Second)
	return newRunner(cfg, pool, log).Loop(ctx, cfg.SyncInterval, cfg.SyncFullAt)
}

func syncOnce(ctx context.Context, cfg config.Config, log *slog.Logger, mode hhsync.Mode, allowMassChange bool, stdout io.Writer) error {
	pool, err := openDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	rep, err := newRunner(cfg, pool, log).Run(ctx, mode, hhsync.Options{AllowMassChange: allowMassChange})
	if rep.RunID != 0 {
		out, _ := json.MarshalIndent(summary(rep), "", "  ")
		fmt.Fprintln(stdout, string(out))
	}
	return err
}

// summary is the human-facing part of a report: counts, not id lists.
func summary(r hhsync.Report) map[string]any {
	p := r.People
	return map[string]any{
		"run_id":   r.RunID,
		"mode":     r.Mode,
		"status":   r.Status,
		"duration": r.Duration.Round(1e6).String(),
		"fetched":  r.Fetched,
		"skipped":  len(r.Skipped),
		"students": map[string]int{
			"active_before": p.ActiveStudentsBefore, "active": p.ActiveStudents,
			"deactivated": len(p.DeactivatedStudents), "reactivated": len(p.ReactivatedStudents),
			"missing_from_hollihop": p.StudentsMissing,
		},
		"teachers": map[string]int{
			"active_before": p.ActiveTeachersBefore, "active": p.ActiveTeachers,
			"deactivated": len(p.DeactivatedTeachers), "reactivated": len(p.ReactivatedTeachers),
		},
		"groups":          map[string]int{"total": p.Groups, "removed": p.GroupsRemoved, "schedule_items": p.ScheduleItems},
		"enrollments":     p.Enrollments,
		"login_phones":    p.LoginPhones,
		"open_issues":     p.Issues,
		"issues_resolved": p.IssuesResolved,
	}
}

func resetPassword(ctx context.Context, cfg config.Config, kind string, id int32, stdout io.Writer) error {
	pool, err := openDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	svc := auth.NewService(pool, people.NewDirectory(pool), auth.NewHasher())
	p, err := svc.ResetToDefault(ctx, kind, id)
	if errors.Is(err, people.ErrNotFound) {
		return fmt.Errorf("no %s with id %d", kind, id)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Password of %s %d (%s %s) reset to the default.\n", kind, id, p.LastName, p.FirstName)
	if p.LoginPhone == "" {
		fmt.Fprintln(stdout, "Note: they have no valid login phone, so the default password can't be used until their number is fixed in HolliHop.")
	}
	if !p.Active {
		fmt.Fprintln(stdout, "Note: they are not active, so they can't log in at all.")
	}
	return nil
}

// healthcheck asks the API on this machine whether it is ready. The
// production image has no shell or curl, so Docker's HEALTHCHECK runs this.
func healthcheck(ctx context.Context, addr string) error {
	if addr == "" {
		addr = ":8080"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("HTTP_ADDR %q: %w", addr, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(host, port)+"/readyz", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("readyz: status %d", resp.StatusCode)
	}
	return nil
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
