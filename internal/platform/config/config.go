// Package config loads process configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
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

	// AdminHost is the host name the admin panel is served on (e.g.
	// admin.makonbook.uz); empty serves no panel. On that host only the
	// panel and /v1/admin/* are reachable.
	AdminHost string

	// Optional Telegram alerts for sync failures. Both or neither.
	// R2 (read only): SAT MAKON's bucket holding question figures and
	// screenshots, shown to practice editors. Optional; without it the
	// editor shows no images.
	R2Endpoint, R2Bucket, R2Region, R2AccessKeyID, R2SecretAccessKey string
	// R2 uploads: MakonBook's own bucket, where editors' images go (read and
	// write). Optional; without it editors can't upload images. The endpoint
	// defaults to R2_ENDPOINT_URL (the same Cloudflare account).
	R2UploadsEndpoint, R2UploadsBucket, R2UploadsAccessKeyID, R2UploadsSecretAccessKey string

	// RendererURL is the DevTools address of the headless Chrome that draws
	// question pictures (the renderer container), e.g. ws://renderer:9222.
	// Optional; without it there are no pictures.
	RendererURL string

	TelegramBotToken    string
	TelegramAlertChatID string

	// The teachers' group review bot (makonbook review-bot): its own bot
	// (from @BotFather) and the group it posts to. Without the group's ID
	// the bot only logs the groups it is in, so it can be found.
	TelegramReviewToken  string
	TelegramReviewChatID int64

	// DemoPassword is the password of the demo accounts store reviewers sign
	// in with (internal/demo; demo.student and demo.teacher). Secret.
	// Optional: without it there are no demo accounts.
	DemoPassword string
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

	cfg.AdminHost = strings.ToLower(strings.TrimSpace(getenv("ADMIN_HOST")))
	if strings.ContainsAny(cfg.AdminHost, "/: ") {
		errs = append(errs, fmt.Errorf("ADMIN_HOST must be a bare host name like admin.makonbook.uz, got %q", cfg.AdminHost))
	}

	cfg.R2Endpoint = strings.TrimSpace(getenv("R2_ENDPOINT_URL"))
	cfg.R2Bucket = strings.TrimSpace(getenv("R2_BUCKET_NAME"))
	cfg.R2Region = withDefault(getenv("R2_REGION"), "auto")
	cfg.R2AccessKeyID = getenv("R2_ACCESS_KEY_ID")
	cfg.R2SecretAccessKey = getenv("R2_SECRET_ACCESS_KEY")
	r2Set := 0
	for _, v := range []string{cfg.R2Endpoint, cfg.R2Bucket, cfg.R2AccessKeyID, cfg.R2SecretAccessKey} {
		if v != "" {
			r2Set++
		}
	}
	if r2Set != 0 && r2Set != 4 {
		errs = append(errs, errors.New("R2_ENDPOINT_URL, R2_BUCKET_NAME, R2_ACCESS_KEY_ID and R2_SECRET_ACCESS_KEY must be set together"))
	}

	cfg.R2UploadsBucket = strings.TrimSpace(getenv("R2_UPLOADS_BUCKET"))
	cfg.R2UploadsAccessKeyID = getenv("R2_UPLOADS_ACCESS_KEY_ID")
	cfg.R2UploadsSecretAccessKey = getenv("R2_UPLOADS_SECRET_ACCESS_KEY")
	cfg.R2UploadsEndpoint = withDefault(strings.TrimSpace(getenv("R2_UPLOADS_ENDPOINT_URL")), cfg.R2Endpoint)
	if up := cfg.R2UploadsBucket + cfg.R2UploadsAccessKeyID + cfg.R2UploadsSecretAccessKey; up != "" &&
		(cfg.R2UploadsBucket == "" || cfg.R2UploadsAccessKeyID == "" || cfg.R2UploadsSecretAccessKey == "" || cfg.R2UploadsEndpoint == "") {
		errs = append(errs, errors.New("R2_UPLOADS_BUCKET, R2_UPLOADS_ACCESS_KEY_ID and R2_UPLOADS_SECRET_ACCESS_KEY must be set together, with R2_UPLOADS_ENDPOINT_URL or R2_ENDPOINT_URL"))
	}

	cfg.RendererURL = strings.TrimSpace(getenv("RENDERER_URL"))
	if cfg.RendererURL != "" && !strings.HasPrefix(cfg.RendererURL, "ws://") && !strings.HasPrefix(cfg.RendererURL, "http://") {
		errs = append(errs, errors.New("RENDERER_URL: want ws://host:port"))
	}

	cfg.TelegramReviewToken = strings.TrimSpace(getenv("TELEGRAM_REVIEW_TOKEN"))
	if v := strings.TrimSpace(getenv("TELEGRAM_REVIEW_CHAT_ID")); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id == 0 {
			errs = append(errs, errors.New("TELEGRAM_REVIEW_CHAT_ID: want the group's numeric ID, e.g. -1001234567890"))
		}
		cfg.TelegramReviewChatID = id
	}

	cfg.TelegramBotToken = getenv("TELEGRAM_BOT_TOKEN")
	cfg.TelegramAlertChatID = getenv("TELEGRAM_ALERT_CHAT_ID")
	if (cfg.TelegramBotToken == "") != (cfg.TelegramAlertChatID == "") {
		errs = append(errs, errors.New("TELEGRAM_BOT_TOKEN and TELEGRAM_ALERT_CHAT_ID must be set together"))
	}

	cfg.DemoPassword = getenv("DEMO_PASSWORD")
	if n := utf8.RuneCountInString(cfg.DemoPassword); n > 0 && (n < 8 || n > 128) {
		errs = append(errs, errors.New("DEMO_PASSWORD must be 8 to 128 characters"))
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
