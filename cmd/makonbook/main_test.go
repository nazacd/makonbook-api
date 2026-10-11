package main

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestRunRejectsBadUsageBeforeLoadingConfig(t *testing.T) {
	noEnv := func(string) string { return "" }
	for _, args := range [][]string{
		nil, {"bogus"}, {"migrate"}, {"migrate", "sideways"}, {"serve", "extra"},
		{"sync"}, {"sync", "partial"}, {"sync", "full", "--yolo"}, {"worker", "now"},
		{"reset-password"}, {"reset-password", "admin", "1"}, {"reset-password", "student", "x"},
		{"reset-password", "student", "99999999999"},
	} {
		if err := run(t.Context(), args, noEnv, io.Discard, io.Discard); !errors.Is(err, errUsage) {
			t.Errorf("run(%q) = %v, want errUsage", args, err)
		}
	}
}

func TestRunReportsConfigErrors(t *testing.T) {
	err := run(t.Context(), []string{"serve"}, func(string) string { return "" }, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("err = %v, want DATABASE_URL error", err)
	}
}

func TestSyncCommandsRequireHolliHopSettings(t *testing.T) {
	env := func(k string) string {
		if k == "DATABASE_URL" {
			return "postgres://localhost/x"
		}
		return ""
	}
	for _, args := range [][]string{{"worker"}, {"sync", "full", "--allow-mass-change"}} {
		err := run(t.Context(), args, env, io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "HOLLIHOP_AUTHKEY") {
			t.Errorf("run(%q) = %v, want HolliHop config error", args, err)
		}
	}
}
