package gamification

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/nazacd/makonbook-api/internal/auth"
	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/httpx"
)

// Handler serves GET /v1/me/gamification and Mako's room under /v1/me/room
// (students with a phone).
type Handler struct {
	svc            *Service
	requireContent func(http.HandlerFunc) http.HandlerFunc
}

func NewHandler(svc *Service, requireContent func(http.HandlerFunc) http.HandlerFunc) *Handler {
	return &Handler{svc: svc, requireContent: requireContent}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/me/gamification", h.requireContent(h.state))
	mux.HandleFunc("GET /v1/me/room", h.requireContent(h.room))
	mux.HandleFunc("PUT /v1/me/room/slots/{slot}", h.requireContent(h.equipRoom))
	mux.HandleFunc("POST /v1/me/room/seen", h.requireContent(h.seenRoom))
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

// student answers 403 for anyone who isn't one.
func student(w http.ResponseWriter, r *http.Request) (int32, bool) {
	p, _ := auth.PrincipalFrom(r.Context())
	if p.Kind != people.KindStudent {
		httpx.WriteError(w, r, http.StatusForbidden, httpx.CodeForbidden, "not a student")
		return 0, false
	}
	return p.ID, true
}

func (h *Handler) writeRoom(w http.ResponseWriter, r *http.Request, room Room, err error) {
	switch {
	case errors.Is(err, ErrRoomItem):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, "invalid_room_item", "no such item for this slot")
	case errors.Is(err, ErrRoomLocked):
		httpx.WriteError(w, r, http.StatusForbidden, "room_item_locked", "this item isn't unlocked yet")
	case err != nil:
		slog.ErrorContext(r.Context(), "room", "error", err)
		httpx.WriteError(w, r, http.StatusInternalServerError, httpx.CodeInternal, "something went wrong")
	default:
		httpx.WriteJSON(w, r, http.StatusOK, room)
	}
}

func (h *Handler) room(w http.ResponseWriter, r *http.Request) {
	id, ok := student(w, r)
	if !ok {
		return
	}
	room, err := h.svc.Room(r.Context(), id, time.Now())
	h.writeRoom(w, r, room, err)
}

func (h *Handler) equipRoom(w http.ResponseWriter, r *http.Request) {
	id, ok := student(w, r)
	if !ok {
		return
	}
	var in struct {
		Item string `json:"item"`
	}
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	room, err := h.svc.EquipRoom(r.Context(), id, r.PathValue("slot"), in.Item, time.Now())
	h.writeRoom(w, r, room, err)
}

func (h *Handler) seenRoom(w http.ResponseWriter, r *http.Request) {
	id, ok := student(w, r)
	if !ok {
		return
	}
	room, err := h.svc.SeenRoom(r.Context(), id, time.Now())
	h.writeRoom(w, r, room, err)
}
