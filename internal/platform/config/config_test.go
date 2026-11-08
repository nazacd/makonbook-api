package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(map[string]string{"DATABASE_URL": "postgres://localhost/db"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Env != EnvDevelopment || cfg.HTTPAddr != ":8080" || cfg.LogLevel != slog.LevelInfo ||
		cfg.LogFormat != "text" || cfg.ShutdownTimeout != 15*time.Second ||
		cfg.Location.String() != "Asia/Tashkent" || cfg.SyncInterval != 15*time.Minute || cfg.SyncFullAt != 3*time.Hour {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadProductionDefaultsToJSONLogs(t *testing.T) {
	cfg, err := Load(env(map[string]string{"DATABASE_URL": "x", "APP_ENV": "production"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LogFormat != "json" {
		t.Errorf("LogFormat = %q, want json", cfg.LogFormat)
	}
}

func TestLoadReportsAllErrors(t *testing.T) {
	_, err := Load(env(map[string]string{
		"APP_ENV":            "staging",
		"LOG_LEVEL":          "loud",
		"LOG_FORMAT":         "xml",
		"SHUTDOWN_TIMEOUT":   "soon",
		"TIMEZONE":           "Mars/Olympus",
		"SYNC_INTERVAL":      "5s",
		"SYNC_FULL_AT":       "3am",
		"TELEGRAM_BOT_TOKEN": "t",
		"TRUST_PROXY":        "maybe",
		"DEMO_PASSWORD":      "short",
	}))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"APP_ENV", "DATABASE_URL", "LOG_LEVEL", "LOG_FORMAT", "SHUTDOWN_TIMEOUT",
		"TIMEZONE", "SYNC_INTERVAL", "SYNC_FULL_AT", "TELEGRAM_ALERT_CHAT_ID", "TRUST_PROXY", "DEMO_PASSWORD"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
}

func TestRequireSync(t *testing.T) {
	cfg, err := Load(env(map[string]string{"DATABASE_URL": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	err = cfg.RequireSync()
	if err == nil || !strings.Contains(err.Error(), "HOLLIHOP_BASE_URL") || !strings.Contains(err.Error(), "HOLLIHOP_AUTHKEY") {
		t.Errorf("RequireSync() = %v", err)
	}
}

func TestTrustProxy(t *testing.T) {
	for v, want := range map[string]bool{"": false, "false": false, "true": true, "TRUE": true, "1": true} {
		cfg, err := Load(env(map[string]string{"DATABASE_URL": "x", "TRUST_PROXY": v}))
		if err != nil || cfg.TrustProxy != want {
			t.Errorf("TRUST_PROXY=%q: %v, %v", v, cfg.TrustProxy, err)
		}
	}
}

func TestAccounts(t *testing.T) {
	cfg, err := Load(env(map[string]string{"DATABASE_URL": "x"}))
	if err != nil || cfg.SMSProvider != "" || cfg.GoogleClientID != "" || cfg.SMSText != DefaultSMSText || cfg.EskizFrom != "4546" {
		t.Errorf("defaults: %+v %v", cfg, err)
	}
	cfg, err = Load(env(map[string]string{"DATABASE_URL": "x", "APP_ENV": "production",
		"SMS_PROVIDER": "Eskiz", "ESKIZ_EMAIL": "a@b.uz", "ESKIZ_PASSWORD": "p", "ESKIZ_FROM": "SATMAKON",
		"SMS_TEXT": "Kod: {code}", "GOOGLE_CLIENT_ID": "id", "GOOGLE_CLIENT_SECRET": "s", "WEB_ORIGIN": "https://sat.makonbook.uz/"}))
	if err != nil || cfg.SMSProvider != "eskiz" || cfg.EskizFrom != "SATMAKON" || cfg.WebOrigin != "https://sat.makonbook.uz" {
		t.Errorf("set: %+v %v", cfg, err)
	}
	if cfg, err := Load(env(map[string]string{"DATABASE_URL": "x", "SMS_PROVIDER": "log", "WEB_ORIGIN": "http://localhost:5173"})); err != nil ||
		cfg.WebOrigin != "http://localhost:5173" {
		t.Errorf("development: %+v %v", cfg, err)
	}

	_, err = Load(env(map[string]string{"DATABASE_URL": "x", "APP_ENV": "production",
		"SMS_PROVIDER": "log", "SMS_TEXT": "no code", "GOOGLE_CLIENT_ID": "id"}))
	for _, want := range []string{"SMS_PROVIDER=log", "SMS_TEXT", "GOOGLE_CLIENT_SECRET", "WEB_ORIGIN"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
	for _, bad := range []map[string]string{
		{"SMS_PROVIDER": "twilio"},
		{"SMS_PROVIDER": "eskiz", "ESKIZ_EMAIL": "a@b.uz"},
		{"WEB_ORIGIN": "sat.makonbook.uz"},
		{"WEB_ORIGIN": "https://sat.makonbook.uz/app"},
		{"WEB_ORIGIN": "http://sat.makonbook.uz"},
		{"WEB_ORIGIN": "http://localhost:5173", "APP_ENV": "production"},
	} {
		bad["DATABASE_URL"] = "x"
		if _, err := Load(env(bad)); err == nil {
			t.Errorf("%v: no error", bad)
		}
	}
}

func TestMail(t *testing.T) {
	cfg, err := Load(env(map[string]string{"DATABASE_URL": "x"}))
	if err != nil || cfg.SMTPHost != "" || cfg.SMTPPort != 587 {
		t.Errorf("defaults: %+v %v", cfg, err)
	}
	cfg, err = Load(env(map[string]string{"DATABASE_URL": "x", "APP_ENV": "production", "SMTP_HOST": "smtp.gmail.com", "SMTP_PORT": "465",
		"SMTP_USERNAME": "no-reply@makonbook.uz", "SMTP_PASSWORD": "p", "MAIL_FROM": "SATMAKON <no-reply@makonbook.uz>"}))
	if err != nil || cfg.SMTPPort != 465 || cfg.MailFrom != "SATMAKON <no-reply@makonbook.uz>" {
		t.Errorf("set: %+v %v", cfg, err)
	}
	if cfg, err := Load(env(map[string]string{"DATABASE_URL": "x", "SMTP_HOST": "LOG"})); err != nil || cfg.SMTPHost != "log" {
		t.Errorf("log: %+v %v", cfg, err)
	}
	for _, bad := range []map[string]string{
		{"SMTP_HOST": "log", "APP_ENV": "production"},
		{"SMTP_HOST": "smtp.gmail.com"},
		{"SMTP_HOST": "smtp.gmail.com:587", "MAIL_FROM": "a@b.uz"},
		{"SMTP_PORT": "x"},
	} {
		bad["DATABASE_URL"] = "x"
		if _, err := Load(env(bad)); err == nil {
			t.Errorf("%v: no error", bad)
		}
	}
}

func TestTelegramLogin(t *testing.T) {
	cfg, err := Load(env(map[string]string{"DATABASE_URL": "x", "TELEGRAM_CLIENT_ID": "8123456789", "TELEGRAM_CLIENT_SECRET": "s",
		"WEB_ORIGIN": "https://sat.makonbook.uz"}))
	if err != nil || cfg.TelegramClientID != "8123456789" {
		t.Errorf("set: %+v %v", cfg, err)
	}
	for _, bad := range []map[string]string{
		{"TELEGRAM_CLIENT_ID": "1", "WEB_ORIGIN": "https://sat.makonbook.uz"},
		{"TELEGRAM_CLIENT_ID": "1", "TELEGRAM_CLIENT_SECRET": "s"},
	} {
		bad["DATABASE_URL"] = "x"
		if _, err := Load(env(bad)); err == nil {
			t.Errorf("%v: no error", bad)
		}
	}
}
