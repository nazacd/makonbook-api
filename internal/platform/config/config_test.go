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
	}))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"APP_ENV", "DATABASE_URL", "LOG_LEVEL", "LOG_FORMAT", "SHUTDOWN_TIMEOUT",
		"TIMEZONE", "SYNC_INTERVAL", "SYNC_FULL_AT", "TELEGRAM_ALERT_CHAT_ID", "TRUST_PROXY"} {
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
