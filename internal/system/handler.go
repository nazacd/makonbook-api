// Package system serves endpoints about the service itself: health checks and
// the public app configuration (minimum supported app version).
package system

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/nazacd/makonbook-api/internal/platform/httpx"
)

// Store reads app settings. Satisfied by *systemdb.Queries.
type Store interface {
	GetAppSetting(ctx context.Context, key string) ([]byte, error)
}

// Pinger checks database connectivity. Satisfied by *pgxpool.Pool.
type Pinger interface {
	Ping(ctx context.Context) error
}

type Handler struct {
	store  Store
	db     Pinger
	signIn SignIn
}

// SignIn is which ways in the app config offers (spec 21).
type SignIn struct {
	Signup bool // sign-up (an email code or Google, then an SMS code)
	Reset  bool // password reset by SMS code
	Email  bool // sign-up can start with an email code
	Google bool // Google sign-in
	// Telegram: the sign-in bot, for the Login Widget; nil when off.
	Telegram *TelegramBot
}

// TelegramBot is the public face of the sign-in bot: its id (the token's
// number) and username.
type TelegramBot struct {
	BotID       int64  `json:"bot_id"`
	BotUsername string `json:"bot_username"`
}

func NewHandler(store Store, db Pinger) *Handler {
	return &Handler{store: store, db: db}
}

// WithSignIn says which ways in the app config offers.
func (h *Handler) WithSignIn(s SignIn) *Handler {
	h.signIn = s
	return h
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", h.health)
	mux.HandleFunc("GET /readyz", h.ready)
	mux.HandleFunc("GET /v1/app/config", h.appConfig)
}

// health reports that the process is up. It never touches dependencies, so a
// database outage doesn't get the container restarted in a loop.
func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, r, http.StatusOK, map[string]string{"status": "ok"})
}

// ready reports whether the service can serve traffic (database reachable).
func (h *Handler) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := h.db.Ping(ctx); err != nil {
		httpx.Logger(r.Context()).Warn("readiness check failed", slog.Any("err", err))
		httpx.WriteError(w, r, http.StatusServiceUnavailable, httpx.CodeUnavailable, "database unavailable")
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]string{"status": "ok"})
}

// MinAppVersion is the oldest app version allowed per platform. Older apps
// show an "update the app" screen (spec D15).
type MinAppVersion struct {
	Android string `json:"android"`
	IOS     string `json:"ios"`
}

type AppConfig struct {
	MinAppVersion MinAppVersion `json:"min_app_version"`
	// Signup: people can sign up (an email code or Google, then an SMS
	// code). Reset: they can reset their password with an SMS code. Email:
	// sign-up can start with an email. Google: Google sign-in.
	Signup bool `json:"signup"`
	Reset  bool `json:"reset"`
	Email  bool `json:"email"`
	Google bool `json:"google"`
	// Telegram: the sign-in bot, or null when Telegram is off.
	Telegram *TelegramBot `json:"telegram"`
}

func (h *Handler) appConfig(w http.ResponseWriter, r *http.Request) {
	raw, err := h.store.GetAppSetting(r.Context(), "min_app_version")
	if err != nil {
		httpx.Logger(r.Context()).Error("load min_app_version", slog.Any("err", err))
		httpx.WriteError(w, r, http.StatusInternalServerError, httpx.CodeInternal, "internal error")
		return
	}

	cfg := AppConfig{Signup: h.signIn.Signup, Reset: h.signIn.Reset, Email: h.signIn.Email, Google: h.signIn.Google, Telegram: h.signIn.Telegram}
	if err := json.Unmarshal(raw, &cfg.MinAppVersion); err != nil {
		httpx.Logger(r.Context()).Error("decode min_app_version", slog.Any("err", err))
		httpx.WriteError(w, r, http.StatusInternalServerError, httpx.CodeInternal, "internal error")
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, cfg)
}
