package alert

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTelegramSendsMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/botTOKEN/sendMessage" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil || r.Form.Get("chat_id") != "-100" || r.Form.Get("text") != "sync failed" {
			t.Errorf("form = %v (%v)", r.Form, err)
		}
	}))
	defer srv.Close()

	tg := NewTelegram("TOKEN", "-100")
	tg.apiBase = srv.URL
	if err := tg.Notify(t.Context(), "sync failed"); err != nil {
		t.Fatal(err)
	}
}

func TestTelegramErrorsHideToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close()

	tg := NewTelegram("SECRET-BOT-TOKEN", "-100")
	tg.apiBase = srv.URL
	err := tg.Notify(t.Context(), "x")
	if err == nil || strings.Contains(err.Error(), "SECRET-BOT-TOKEN") {
		t.Errorf("err = %v", err)
	}
}
