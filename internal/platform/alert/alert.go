// Package alert tells admins about problems that need a human, such as a
// failing or blocked HolliHop sync.
package alert

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Notifier delivers a short plain-text message to admins.
type Notifier interface {
	Notify(ctx context.Context, msg string) error
}

// Log is the fallback when no channel is configured: it writes the alert to
// the log at error level, so it at least shows up in log monitoring.
type Log struct{ Logger *slog.Logger }

func (l Log) Notify(_ context.Context, msg string) error {
	l.Logger.Error("ALERT", slog.String("message", msg))
	return nil
}

// Telegram sends alerts to a chat through a bot (outbound only — this is not
// the Telegram login feature, which is a later phase).
type Telegram struct {
	token   string
	chatID  string
	apiBase string
	http    *http.Client
}

func NewTelegram(token, chatID string) *Telegram {
	return &Telegram{
		token:   token,
		chatID:  chatID,
		apiBase: "https://api.telegram.org",
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

func (t *Telegram) Notify(ctx context.Context, msg string) error {
	form := url.Values{"chat_id": {t.chatID}, "text": {msg}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		t.apiBase+"/bot"+t.token+"/sendMessage", strings.NewReader(form.Encode()))
	if err != nil {
		return errors.New("telegram: build request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := t.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL contains the bot token
		}
		return fmt.Errorf("telegram: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("telegram: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
