package gamification

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/nazacd/makonbook-api/internal/auth"
	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/httpx"
)

// Handler serves GET /v1/me/gamification (students with a phone).
type Handler struct {
	svc            *Service
	requireContent func(http.HandlerFunc) http.HandlerFunc
}

func NewHandler(svc *Service, requireContent func(http.HandlerFunc) http.HandlerFunc) *Handler {
	return &Handler{svc: svc, requireContent: requireContent}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/me/gamification", h.requireContent(h.state))
}

func (h *Handler) state(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	if p.Kind != people.KindStudent {
		httpx.WriteError(w, r, http.StatusForbidden, httpx.CodeForbidden, "not a student")
		return
	}
	st, err := h.svc.State(r.Context(), p.ID, time.Now())
	if err != nil {
		slog.ErrorContext(r.Context(), "gamification state", "error", err)
		httpx.WriteError(w, r, http.StatusInternalServerError, httpx.CodeInternal, "something went wrong")
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, st)
}
