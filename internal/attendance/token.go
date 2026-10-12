package attendance

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"time"
)

// QR codes (spec 8.2). The teacher's device gets the session secret once and
// computes a new code every WindowSeconds on its own, so codes keep changing
// even if the teacher's connection drops:
//
//	window = floor(unix_seconds / 10)
//	sig    = first 16 chars of base64url(HMAC-SHA256(secret, "<session_id>:<window>"))
//
// session_id is the lowercase UUID string; base64url has no padding. The
// mobile app implements the same function (keep the two in step).
const (
	WindowSeconds = 10
	sigLength     = 16
)

// Window is the QR window containing t.
func Window(t time.Time) int64 { return t.Unix() / WindowSeconds }

// Sign returns the code for a session and window.
func Sign(secret []byte, sessionID string, window int64) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(sessionID + ":" + strconv.FormatInt(window, 10)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))[:sigLength]
}

// validSig compares in constant time.
func validSig(secret []byte, sessionID string, window int64, sig string) bool {
	return hmac.Equal([]byte(Sign(secret, sessionID, window)), []byte(sig))
}

// freshWindow accepts the current window and its neighbours: the previous one
// covers a code scanned just as it changed, the next one a teacher's clock a
// few seconds fast. A code is therefore good for 20–30 seconds — too short to
// send to a friend at home and have them scan it.
func freshWindow(window int64, now time.Time) bool {
	cur := Window(now)
	return window >= cur-1 && window <= cur+1
}

func encodeSecret(secret []byte) string { return base64.RawURLEncoding.EncodeToString(secret) }

func decodeSecret(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
