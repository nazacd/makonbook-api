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
	store Store
	db    Pinger
}

func NewHandler(store Store, db Pinger) *Handler {
	return &Handler{store: store, db: db}
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
}

func (h *Handler) appConfig(w http.ResponseWriter, r *http.Request) {
	raw, err := h.store.GetAppSetting(r.Context(), "min_app_version")
	if err != nil {
		httpx.Logger(r.Context()).Error("load min_app_version", slog.Any("err", err))
		httpx.WriteError(w, r, http.StatusInternalServerError, httpx.CodeInternal, "internal error")
		return
	}

	var cfg AppConfig
	if err := json.Unmarshal(raw, &cfg.MinAppVersion); err != nil {
		httpx.Logger(r.Context()).Error("decode min_app_version", slog.Any("err", err))
		httpx.WriteError(w, r, http.StatusInternalServerError, httpx.CodeInternal, "internal error")
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, cfg)
}
