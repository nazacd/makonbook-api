package main

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestRunRejectsBadUsageBeforeLoadingConfig(t *testing.T) {
	noEnv := func(string) string { return "" }
	for _, args := range [][]string{nil, {"bogus"}, {"migrate"}, {"migrate", "sideways"}, {"serve", "extra"}} {
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
