package ratelimit

import (
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	l := New(3, time.Minute)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 3 {
		if !l.Allow("1.1.1.1", now) {
			t.Fatalf("attempt %d refused", i+1)
		}
	}
	if l.Allow("1.1.1.1", now) {
		t.Error("4th attempt allowed")
	}
	if !l.Allow("2.2.2.2", now) {
		t.Error("other key refused")
	}
	if !l.Allow("1.1.1.1", now.Add(time.Minute)) {
		t.Error("new window refused")
	}
}
