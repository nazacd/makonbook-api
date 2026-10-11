package auth

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestHashAndVerify(t *testing.T) {
	h := NewHasher()
	enc, err := h.Hash("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Errorf("unexpected encoding %q", enc)
	}
	if !h.Verify(enc, "correct horse") {
		t.Error("right password rejected")
	}
	if h.Verify(enc, "correct horsE") {
		t.Error("wrong password accepted")
	}
	if again, _ := h.Hash("correct horse"); again == enc {
		t.Error("two hashes of one password are identical: salt missing")
	}
	for _, bad := range []string{"", "plain", "$argon2i$v=19$m=1,t=1,p=1$AA$AA", strings.Replace(enc, "v=19", "v=16", 1)} {
		if h.Verify(bad, "correct horse") {
			t.Errorf("malformed hash %q verified", bad)
		}
	}
}

func TestCheckNewPassword(t *testing.T) {
	const phone = "+998901234567"
	cases := []struct {
		pw, current string
		want        error
	}{
		{"good password", "4567", nil},
		{"short", "4567", errPasswordLength},
		{strings.Repeat("x", 129), "4567", errPasswordLength},
		{"same-as-before", "same-as-before", errPasswordReused},
		{"my 90 123 45 67!", "4567", errPasswordIsPhone},
		{"998901234567", "4567", errPasswordIsPhone},
		{"парольпароль", "4567", nil}, // non-latin is fine
	}
	for _, c := range cases {
		if got := checkNewPassword(c.pw, c.current, phone); !errors.Is(got, c.want) {
			t.Errorf("checkNewPassword(%q) = %v, want %v", c.pw, got, c.want)
		}
	}
	if err := checkNewPassword("901234567x", "1", ""); err != nil {
		t.Errorf("no phone: %v", err)
	}
}

func TestNormalizeUsername(t *testing.T) {
	for in, want := range map[string]string{"Aziz_99": "aziz_99", "  a.b.c ": "a.b.c"} {
		if got, err := normalizeUsername(in); err != nil || got != want {
			t.Errorf("normalizeUsername(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"ab", "9lives", "_x_", "trailing.", "space s", "аziz", strings.Repeat("a", 31)} {
		if _, err := normalizeUsername(bad); err == nil {
			t.Errorf("normalizeUsername(%q) accepted", bad)
		}
	}
}

func TestLooksLikePhone(t *testing.T) {
	for s, want := range map[string]bool{
		"+998 (90) 123-45-67": true, "901234567": true, "aziz": false, "a123": false, "": false,
	} {
		if got := looksLikePhone(s); got != want {
			t.Errorf("looksLikePhone(%q) = %v", s, got)
		}
	}
}

func TestDefaultPassword(t *testing.T) {
	if got := defaultPassword("+998901234567"); got != "4567" {
		t.Errorf("got %q", got)
	}
	if got := defaultPassword(""); got != "" {
		t.Errorf("no phone: got %q", got)
	}
}

func TestIPLimiter(t *testing.T) {
	l := newIPLimiter(3, time.Minute)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 3 {
		if !l.allow("1.1.1.1", now) {
			t.Fatalf("attempt %d refused", i+1)
		}
	}
	if l.allow("1.1.1.1", now) {
		t.Error("4th attempt allowed")
	}
	if !l.allow("2.2.2.2", now) {
		t.Error("other IP refused")
	}
	if !l.allow("1.1.1.1", now.Add(time.Minute)) {
		t.Error("new window refused")
	}
}

func slogDiscard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
