package httpx

import (
	"net/http/httptest"
	"testing"
)

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.5:4321"
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.9")

	if got := ClientIP(r, false); got != "10.0.0.5" {
		t.Errorf("untrusted: %q, want the socket address", got)
	}
	if got := ClientIP(r, true); got != "203.0.113.9" {
		t.Errorf("trusted: %q, want the proxy-appended address", got)
	}
	r.Header.Set("X-Forwarded-For", "garbage")
	if got := ClientIP(r, true); got != "10.0.0.5" {
		t.Errorf("bad header: %q", got)
	}
}
