// Package config loads process configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

const (
	EnvDevelopment = "development"
	EnvProduction  = "production"
)

// Config holds settings shared by every subcommand.
type Config struct {
	Env             string
	HTTPAddr        string
	DatabaseURL     string
	LogLevel        slog.Level
	LogFormat       string // "text" or "json"
	ShutdownTimeout time.Duration
	// TrustProxy: the API runs behind a reverse proxy (the edge nginx) and may take the
	// client IP from X-Forwarded-For. Never enable without a proxy in front.
	TrustProxy bool

	// Location is the school's time zone; it decides what "today" means for
	// schedules and enrollments.
	Location *time.Location

	// HolliHop sync (required only by the worker and sync commands; see
	// RequireSync).
	HolliHopBaseURL string
	HolliHopAuthKey string
	SyncInterval    time.Duration // between incremental runs
	SyncFullAt      time.Duration // local time of day for the nightly full run

	// Attendance write-back to HolliHop (spec 8.6). Off by default: the
	// writer only records what it would send ("dry run") until an admin has
	// watched one real lesson go through.
	HolliHopWriteEnabled bool
	// HolliHopAbsentPayable sends Payable=true with absences instead of
	// leaving billing untouched — only if the test lesson shows HolliHop
	// would otherwise leave absences unbilled (spec D17). Never false.
	HolliHopAbsentPayable bool

	// Optional Telegram alerts for sync failures. Both or neither.
	TelegramBotToken    string
	TelegramAlertChatID string
}

// Load reads configuration through getenv (os.Getenv in production, a map
// lookup in tests) and reports every invalid or missing variable at once.
func Load(getenv func(string) string) (Config, error) {
	var errs []error

	cfg := Config{
		Env:         withDefault(getenv("APP_ENV"), EnvDevelopment),
		HTTPAddr:    withDefault(getenv("HTTP_ADDR"), ":8080"),
		DatabaseURL: getenv("DATABASE_URL"),
	}

	if cfg.Env != EnvDevelopment && cfg.Env != EnvProduction {
		errs = append(errs, fmt.Errorf("APP_ENV must be %q or %q, got %q", EnvDevelopment, EnvProduction, cfg.Env))
	}
	if cfg.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	}

	if err := cfg.LogLevel.UnmarshalText([]byte(withDefault(getenv("LOG_LEVEL"), "info"))); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
	}

	defaultFormat := "text"
	if cfg.Env == EnvProduction {
		defaultFormat = "json"
	}
	cfg.LogFormat = strings.ToLower(withDefault(getenv("LOG_FORMAT"), defaultFormat))
	if cfg.LogFormat != "text" && cfg.LogFormat != "json" {
		errs = append(errs, fmt.Errorf("LOG_FORMAT must be \"text\" or \"json\", got %q", cfg.LogFormat))
	}

	timeout, err := time.ParseDuration(withDefault(getenv("SHUTDOWN_TIMEOUT"), "15s"))
	if err != nil || timeout <= 0 {
		errs = append(errs, fmt.Errorf("SHUTDOWN_TIMEOUT must be a positive duration such as 15s, got %q", getenv("SHUTDOWN_TIMEOUT")))
	}
	cfg.ShutdownTimeout = timeout

	if cfg.TrustProxy, err = boolVar(getenv, "TRUST_PROXY"); err != nil {
		errs = append(errs, err)
	}

	tz := withDefault(getenv("TIMEZONE"), "Asia/Tashkent")
	if cfg.Location, err = time.LoadLocation(tz); err != nil {
		errs = append(errs, fmt.Errorf("TIMEZONE: unknown time zone %q", tz))
	}

	cfg.HolliHopBaseURL = getenv("HOLLIHOP_BASE_URL")
	cfg.HolliHopAuthKey = getenv("HOLLIHOP_AUTHKEY")

	interval, err := time.ParseDuration(withDefault(getenv("SYNC_INTERVAL"), "15m"))
	if err != nil || interval < time.Minute {
		errs = append(errs, fmt.Errorf("SYNC_INTERVAL must be a duration of at least 1m, got %q", getenv("SYNC_INTERVAL")))
	}
	cfg.SyncInterval = interval

	fullAt, err := time.Parse("15:04", withDefault(getenv("SYNC_FULL_AT"), "03:00"))
	if err != nil {
		errs = append(errs, fmt.Errorf("SYNC_FULL_AT must be a time like 03:00, got %q", getenv("SYNC_FULL_AT")))
	}
	cfg.SyncFullAt = time.Duration(fullAt.Hour())*time.Hour + time.Duration(fullAt.Minute())*time.Minute

	if cfg.HolliHopWriteEnabled, err = boolVar(getenv, "HOLLIHOP_WRITE_ENABLED"); err != nil {
		errs = append(errs, err)
	}
	if cfg.HolliHopAbsentPayable, err = boolVar(getenv, "HOLLIHOP_ABSENT_PAYABLE"); err != nil {
		errs = append(errs, err)
	}

	cfg.TelegramBotToken = getenv("TELEGRAM_BOT_TOKEN")
	cfg.TelegramAlertChatID = getenv("TELEGRAM_ALERT_CHAT_ID")
	if (cfg.TelegramBotToken == "") != (cfg.TelegramAlertChatID == "") {
		errs = append(errs, errors.New("TELEGRAM_BOT_TOKEN and TELEGRAM_ALERT_CHAT_ID must be set together"))
	}

	return cfg, errors.Join(errs...)
}

// RequireSync reports the settings the HolliHop sync needs but serve doesn't.
func (c Config) RequireSync() error {
	var errs []error
	if c.HolliHopBaseURL == "" {
		errs = append(errs, errors.New("HOLLIHOP_BASE_URL is required"))
	}
	if c.HolliHopAuthKey == "" {
		errs = append(errs, errors.New("HOLLIHOP_AUTHKEY is required"))
	}
	return errors.Join(errs...)
}

func withDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// boolVar reads an optional true/false variable (default false).
func boolVar(getenv func(string) string, name string) (bool, error) {
	switch v := strings.ToLower(strings.TrimSpace(getenv(name))); v {
	case "", "false", "0":
		return false, nil
	case "true", "1":
		return true, nil
	default:
		return false, fmt.Errorf("%s must be true or false, got %q", name, v)
	}
}
