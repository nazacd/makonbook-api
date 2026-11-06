// Package sms sends text messages: the codes people sign up and reset their
// password with (spec 21). Two senders: Log (development: the message goes
// to the log) and Eskiz (Eskiz.uz, the Uzbek SMS gateway).
package sms

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	gosync "sync"
	"time"
)

// Sender sends one message to an E.164 phone number.
type Sender interface {
	Send(ctx context.Context, phone, text string) error
}

// Log is the development sender: it writes the message, code included, to
// the log instead of sending it.
type Log struct{ Logger *slog.Logger }

func (l Log) Send(_ context.Context, phone, text string) error {
	l.Logger.Warn("SMS not sent (SMS_PROVIDER=log)", slog.String("phone", phone), slog.String("text", text))
	return nil
}

// EskizURL is Eskiz's API.
const EskizURL = "https://notify.eskiz.uz"

// Eskiz sends through Eskiz.uz: POST /api/auth/login with the account's email
// and password gives a bearer token (a JWT, valid for weeks), used for
// POST /api/message/sms/send (mobile_phone as digits without "+", message,
// from). The token is kept and fetched again before it expires, or when Eskiz
// answers 401. Eskiz moderates message texts: the text must match a template
// approved in the Eskiz account.
type Eskiz struct {
	Email, Password string
	From            string       // the sender name, e.g. "4546" (Eskiz's default)
	BaseURL         string       // EskizURL when empty; tests use their own
	Client          *http.Client // a client with a 15 s timeout when nil

	mu      gosync.Mutex
	token   string
	expires time.Time
}

// renewBefore: a token this close to its expiry is renewed first.
const renewBefore = time.Hour

func (e *Eskiz) Send(ctx context.Context, phone, text string) error {
	for attempt := 0; ; attempt++ {
		token, err := e.currentToken(ctx)
		if err != nil {
			return err
		}
		form := url.Values{
			"mobile_phone": {strings.TrimPrefix(phone, "+")},
			"message":      {text},
			"from":         {e.From},
		}
		status, body, err := e.post(ctx, "/api/message/sms/send", form, token)
		if err != nil {
			return err
		}
		switch {
		case status == http.StatusUnauthorized && attempt == 0:
			e.forget(token) // expired or revoked: log in again, once
			continue
		case status/100 != 2:
			return fmt.Errorf("eskiz send: status %d: %s", status, message(body))
		}
		var res struct {
			Status any `json:"status"`
		}
		if json.Unmarshal(body, &res) == nil && fmt.Sprint(res.Status) == "error" {
			return fmt.Errorf("eskiz send: %s", message(body))
		}
		return nil // {"id": …, "status": "waiting", "message": "Waiting for SMS provider"}
	}
}

// currentToken returns a token that isn't about to expire, logging in when
// needed.
func (e *Eskiz) currentToken(ctx context.Context) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.token != "" && time.Until(e.expires) > renewBefore {
		return e.token, nil
	}
	status, body, err := e.post(ctx, "/api/auth/login", url.Values{"email": {e.Email}, "password": {e.Password}}, "")
	if err != nil {
		return "", err
	}
	if status/100 != 2 {
		return "", fmt.Errorf("eskiz login: status %d: %s", status, message(body))
	}
	var res struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &res); err != nil || res.Data.Token == "" {
		return "", errors.New("eskiz login: no token in the answer")
	}
	e.token, e.expires = res.Data.Token, tokenExpiry(res.Data.Token)
	return e.token, nil
}

func (e *Eskiz) forget(token string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.token == token {
		e.token = ""
	}
}

func (e *Eskiz) post(ctx context.Context, path string, form url.Values, token string) (int, []byte, error) {
	base := e.BaseURL
	if base == "" {
		base = EskizURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+path, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := e.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err // the URL is harmless, but keep errors short
		}
		return 0, nil, fmt.Errorf("eskiz %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return 0, nil, fmt.Errorf("eskiz %s: %w", path, err)
	}
	return resp.StatusCode, body, nil
}

// tokenExpiry reads a JWT's exp claim; a token it can't read is assumed good
// for a day (a 401 renews it sooner).
func tokenExpiry(token string) time.Time {
	fallback := time.Now().Add(24 * time.Hour)
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return fallback
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fallback
	}
	var claims struct {
		Exp json.Number `json:"exp"`
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	if err := dec.Decode(&claims); err != nil {
		return fallback
	}
	exp, err := claims.Exp.Int64()
	if err != nil || exp <= 0 {
		return fallback
	}
	return time.Unix(exp, 0)
}

// message is Eskiz's error text ({"message": …}) or the start of the body.
func message(body []byte) string {
	var res struct {
		Message any `json:"message"`
	}
	if json.Unmarshal(body, &res) == nil && res.Message != nil {
		return fmt.Sprint(res.Message)
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
