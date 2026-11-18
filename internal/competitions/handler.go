// Package competitions is the HTTP side of competitions (spec 26): students
// list, join and see the board; editors make them in the admin panel. The
// rules live in the roadmap package, beside the assessments they reuse.
package competitions

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/nazacd/makonbook-api/internal/admin"
	"github.com/nazacd/makonbook-api/internal/auth"
	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/httpx"
	"github.com/nazacd/makonbook-api/internal/roadmap"
)

// Error codes (see api/openapi.yaml).
const (
	CodeNotLive            = "not_live"
	CodeAttempted          = "already_attempted"
	CodeTimeUp             = "time_up"
	CodeInvalidCompetition = "invalid_competition"
	CodeCompetitionLocked  = "competition_locked"
)

// Handler serves competitions.
type Handler struct {
	svc                             *roadmap.Service
	require, requireContent, editor func(http.HandlerFunc) http.HandlerFunc
}

// NewHandler: require and requireContent are auth's; editor is the admin
// panel's RequireEditor.
func NewHandler(svc *roadmap.Service, require, requireContent, editor func(http.HandlerFunc) http.HandlerFunc) *Handler {
	return &Handler{svc: svc, require: require, requireContent: requireContent, editor: editor}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/competitions", h.require(h.list))
	mux.HandleFunc("GET /v1/competitions/{id}", h.require(h.get))
	mux.HandleFunc("GET /v1/competitions/{id}/board", h.require(h.board))
	mux.HandleFunc("POST /v1/competitions/{id}/start", h.requireContent(h.start))

	mux.HandleFunc("GET /v1/admin/competitions", h.editor(h.adminList))
	mux.HandleFunc("POST /v1/admin/competitions", h.editor(h.adminSave))
	mux.HandleFunc("POST /v1/admin/competitions/draw", h.editor(h.adminDraw))
	mux.HandleFunc("GET /v1/admin/competitions/reservable", h.editor(h.adminReservable))
	mux.HandleFunc("GET /v1/admin/competitions/{id}", h.editor(h.adminGet))
	mux.HandleFunc("PUT /v1/admin/competitions/{id}", h.editor(h.adminSave))
	mux.HandleFunc("DELETE /v1/admin/competitions/{id}", h.editor(h.adminDelete))
	mux.HandleFunc("PUT /v1/admin/competitions/{id}/published", h.editor(h.adminPublish(true)))
	mux.HandleFunc("DELETE /v1/admin/competitions/{id}/published", h.editor(h.adminPublish(false)))
	mux.HandleFunc("GET /v1/admin/competitions/{id}/board", h.editor(h.adminBoard))
}

// studentID is the signed-in student's id, 0 for anyone else.
func studentID(r *http.Request) int32 {
	if p, _ := auth.PrincipalFrom(r.Context()); p.Kind == people.KindStudent {
		return p.ID
	}
	return 0
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.Competitions(r.Context(), studentID(r), false, time.Now())
	if err != nil {
		h.fail(w, r, "list", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"competitions": list, "server_time": time.Now().UTC()})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	c, err := h.svc.CompetitionByID(r.Context(), r.PathValue("id"), studentID(r), false, time.Now())
	if err != nil {
		h.fail(w, r, "get", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"competition": c, "server_time": time.Now().UTC()})
}

func (h *Handler) board(w http.ResponseWriter, r *http.Request) {
	b, err := h.svc.CompetitionBoard(r.Context(), r.PathValue("id"), studentID(r), false)
	if err != nil {
		h.fail(w, r, "board", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, b)
}

func (h *Handler) start(w http.ResponseWriter, r *http.Request) {
	id := studentID(r)
	if id == 0 {
		httpx.WriteError(w, r, http.StatusForbidden, httpx.CodeForbidden, "only students take part")
		return
	}
	a, err := h.svc.StartCompetition(r.Context(), id, r.PathValue("id"), time.Now())
	if err != nil {
		h.fail(w, r, "start", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, a)
}

func (h *Handler) adminList(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.Competitions(r.Context(), 0, true, time.Now())
	if err != nil {
		h.fail(w, r, "admin list", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"competitions": list})
}

// adminGet is a competition with its questions' previews.
func (h *Handler) adminGet(w http.ResponseWriter, r *http.Request) {
	c, err := h.svc.CompetitionByID(r.Context(), r.PathValue("id"), 0, true, time.Now())
	if err != nil {
		h.fail(w, r, "admin get", err)
		return
	}
	cards, err := h.svc.QuestionCards(r.Context(), c.QuestionIDs)
	if err != nil {
		h.fail(w, r, "admin get", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"competition": c, "questions": cards})
}

func (h *Handler) adminSave(w http.ResponseWriter, r *http.Request) {
	var in roadmap.CompetitionInput
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	p, _ := admin.PrincipalFrom(r.Context())
	c, err := h.svc.SaveCompetition(r.Context(), r.PathValue("id"), in, fmt.Sprintf("%s:%d", p.MemberType, p.MemberID), time.Now())
	if err != nil {
		h.fail(w, r, "save", err)
		return
	}
	status := http.StatusOK
	if r.PathValue("id") == "" {
		status = http.StatusCreated
	}
	httpx.WriteJSON(w, r, status, map[string]any{"competition": c})
}

func (h *Handler) adminDelete(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeleteCompetition(r.Context(), r.PathValue("id")); err != nil {
		h.fail(w, r, "delete", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) adminPublish(on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := h.svc.PublishCompetition(r.Context(), r.PathValue("id"), on, time.Now()); err != nil {
			h.fail(w, r, "publish", err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *Handler) adminDraw(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Subject string   `json:"subject"`
		Skills  []string `json:"skills"`
		Easy    int      `json:"easy"`
		Medium  int      `json:"medium"`
		Hard    int      `json:"hard"`
	}
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	ids, err := h.svc.DrawCompetition(r.Context(), in.Subject, in.Skills, map[string]int{"easy": in.Easy, "medium": in.Medium, "hard": in.Hard})
	if err != nil {
		h.fail(w, r, "draw", err)
		return
	}
	cards, err := h.svc.QuestionCards(r.Context(), ids)
	if err != nil {
		h.fail(w, r, "draw", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"question_ids": ids, "questions": cards})
}

func (h *Handler) adminReservable(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query()
	list, err := h.svc.ReservableQuestions(r.Context(), v.Get("competition"), v.Get("subject"), v.Get("q"))
	if err != nil {
		h.fail(w, r, "reservable", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"questions": list})
}

func (h *Handler) adminBoard(w http.ResponseWriter, r *http.Request) {
	b, err := h.svc.CompetitionBoard(r.Context(), r.PathValue("id"), 0, true)
	if err != nil {
		h.fail(w, r, "admin board", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, b)
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	var bad *roadmap.CompetitionError
	switch {
	case errors.Is(err, roadmap.ErrNotFound):
		httpx.NotFound(w, r)
	case errors.Is(err, roadmap.ErrNotLive):
		httpx.WriteError(w, r, http.StatusConflict, CodeNotLive, err.Error())
	case errors.Is(err, roadmap.ErrAttempted):
		httpx.WriteError(w, r, http.StatusConflict, CodeAttempted, err.Error())
	case errors.Is(err, roadmap.ErrCompetitionLocked):
		httpx.WriteError(w, r, http.StatusConflict, CodeCompetitionLocked, err.Error())
	case errors.Is(err, roadmap.ErrNoQuestions):
		httpx.WriteError(w, r, http.StatusConflict, roadmap.CodeNoQuestions, err.Error())
	case errors.As(err, &bad):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeInvalidCompetition, bad.Reason)
	default:
		slog.ErrorContext(r.Context(), "competitions: "+what, "err", err)
		httpx.WriteError(w, r, http.StatusInternalServerError, httpx.CodeInternal, "something went wrong")
	}
}
