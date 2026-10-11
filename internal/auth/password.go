package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters: OWASP's minimum recommendation (19 MiB, 2 passes,
// 1 lane). Memory is per hash, so concurrency is capped below.
const (
	argonMemory  = 19 * 1024 // KiB
	argonTime    = 2
	argonThreads = 1
	argonKeyLen  = 32
	argonSaltLen = 16
)

// Hasher hashes and verifies passwords with argon2id. It limits concurrent
// hashes so a burst of logins (a whole class opening the app at once) can't
// exhaust memory.
type Hasher struct {
	sem chan struct{}
}

func NewHasher() *Hasher {
	return &Hasher{sem: make(chan struct{}, max(2, runtime.NumCPU()))}
}

// Hash returns the password in PHC format:
// $argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>
func (h *Hasher) Hash(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := h.derive(password, salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// Verify reports whether password matches encoded. It reads the parameters
// from encoded, so hashes made with older parameters keep working.
func (h *Hasher) Verify(encoded, password string) bool {
	var version int
	var memory, passes uint32
	var threads uint8
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &passes, &threads); err != nil {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(want) == 0 || len(want) > 128 {
		return false
	}
	got := h.derive(password, salt, passes, memory, threads, uint32(len(want))) //nolint:gosec // len ≤ 128, checked above
	return subtle.ConstantTimeCompare(got, want) == 1
}

// Burn spends the time of one hash. Login paths that have nothing to verify
// call it so response time doesn't reveal whether an account exists or still
// uses the default password.
func (h *Hasher) Burn() {
	h.derive("burn", make([]byte, argonSaltLen), argonTime, argonMemory, argonThreads, argonKeyLen)
}

func (h *Hasher) derive(password string, salt []byte, passes, memory uint32, threads uint8, keyLen uint32) []byte {
	h.sem <- struct{}{}
	defer func() { <-h.sem }()
	return argon2.IDKey([]byte(password), salt, passes, memory, threads, keyLen)
}

// Password and username rules (spec 7).
var (
	errPasswordLength  = errors.New("password must be 8 to 128 characters long")
	errPasswordIsPhone = errors.New("password must not contain your phone number")
	errPasswordReused  = errors.New("new password must differ from the current one")
)

// checkNewPassword validates a password the user is choosing. phone is the
// person's E.164 login phone ("" if none).
func checkNewPassword(password, current, phone string) error {
	if n := utf8.RuneCountInString(password); n < 8 || n > 128 {
		return errPasswordLength
	}
	if password == current {
		return errPasswordReused
	}
	// Classmates know each other's numbers; a password containing the
	// 9-digit national number is barely better than the default.
	if national := lastDigits(phone, 9); len(national) == 9 && strings.Contains(digitsOnly(password), national) {
		return errPasswordIsPhone
	}
	return nil
}

// defaultPassword is the first password everyone gets: the last 4 digits of
// their login phone (spec D2). It is derived, never stored, so it follows the
// phone if admins correct the number in HolliHop.
func defaultPassword(phone string) string {
	return lastDigits(phone, 4)
}

func lastDigits(s string, n int) string {
	d := digitsOnly(s)
	if len(d) < n {
		return ""
	}
	return d[len(d)-n:]
}

func digitsOnly(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}

var errUsernameFormat = errors.New("username must be 3 to 30 characters: latin letters, digits, '_' or '.', starting with a letter")

// normalizeUsername lower-cases and validates a username. Usernames start
// with a letter so they can never be mistaken for a phone number at login.
func normalizeUsername(s string) (string, error) {
	u := strings.ToLower(strings.TrimSpace(s))
	if len(u) < 3 || len(u) > 30 || u[0] < 'a' || u[0] > 'z' || strings.HasSuffix(u, ".") {
		return "", errUsernameFormat
	}
	for _, r := range u {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' && r != '.' {
			return "", errUsernameFormat
		}
	}
	return u, nil
}

// looksLikePhone decides whether a login identifier is a phone number (only
// digits and phone punctuation) or a username.
func looksLikePhone(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) && !strings.ContainsRune("+ -()", r) {
			return false
		}
	}
	return true
}
