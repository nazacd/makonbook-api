package sms

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func jwt(exp time.Time) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"HS256"}`)) + "." + enc([]byte(fmt.Sprintf(`{"exp":%d}`, exp.Unix()))) + ".sig"
}

func TestEskiz(t *testing.T) {
	var logins, sends atomic.Int32
	var reject atomic.Bool // the next send gets a 401
	var token atomic.Value
	token.Store(jwt(time.Now().Add(30 * 24 * time.Hour)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		switch r.URL.Path {
		case "/api/auth/login":
			logins.Add(1)
			if r.PostForm.Get("email") != "me@example.com" || r.PostForm.Get("password") != "secret" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"message":"Неверный Email или пароль"}`))
				return
			}
			_, _ = fmt.Fprintf(w, `{"message":"token_generated","data":{"token":%q},"token_type":"bearer"}`, token.Load())
		case "/api/message/sms/send":
			sends.Add(1)
			if reject.CompareAndSwap(true, false) || r.Header.Get("Authorization") != "Bearer "+token.Load().(string) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"message":"token expired"}`))
				return
			}
			if r.PostForm.Get("mobile_phone") != "998901234567" || r.PostForm.Get("from") != "4546" ||
				r.PostForm.Get("message") != "SATMAKON: your code is 123456" {
				t.Errorf("form = %v", r.PostForm)
			}
			_, _ = w.Write([]byte(`{"id":"8f2c","message":"Waiting for SMS provider","status":"waiting"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	e := &Eskiz{Email: "me@example.com", Password: "secret", From: "4546", BaseURL: srv.URL}
	ctx := context.Background()
	send := func() error { return e.Send(ctx, "+998901234567", "SATMAKON: your code is 123456") }
	for range 2 {
		if err := send(); err != nil {
			t.Fatal(err)
		}
	}
	if logins.Load() != 1 || sends.Load() != 2 {
		t.Errorf("logins %d, sends %d: the token wasn't kept", logins.Load(), sends.Load())
	}

	// Revoked: log in again once and resend.
	reject.Store(true)
	if err := send(); err != nil {
		t.Fatal(err)
	}
	if logins.Load() != 2 {
		t.Errorf("logins = %d after a 401", logins.Load())
	}

	// About to expire: renewed before sending.
	e.expires = time.Now().Add(time.Minute)
	if err := send(); err != nil || logins.Load() != 3 {
		t.Errorf("renewal: %v, logins %d", err, logins.Load())
	}

	// Wrong account: the error says why, and holds no password.
	bad := &Eskiz{Email: "me@example.com", Password: "wrong", From: "4546", BaseURL: srv.URL}
	err := bad.Send(ctx, "+998901234567", "x")
	if err == nil || !strings.Contains(err.Error(), "status 401") || strings.Contains(err.Error(), "wrong") {
		t.Errorf("bad login: %v", err)
	}
}

func TestTokenExpiry(t *testing.T) {
	exp := time.Now().Add(29 * 24 * time.Hour).Truncate(time.Second)
	if got := tokenExpiry(jwt(exp)); !got.Equal(exp) {
		t.Errorf("expiry = %v, want %v", got, exp)
	}
	if got := tokenExpiry("not-a-jwt"); time.Until(got) > 25*time.Hour || time.Until(got) < 23*time.Hour {
		t.Errorf("fallback = %v", got)
	}
}
