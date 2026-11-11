package vocab

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/nazacd/makonbook-api/internal/auth"
	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/httpx"
)

// Error codes of the games, the leaderboard and students' decks.
const (
	CodeInvalidGames = "invalid_games"
	CodeInvalidDeck  = "invalid_deck"
	CodeTooManyDecks = "too_many_decks"
	CodeDeckTooSmall = "deck_too_small"
	CodeOwnDeck      = "own_deck"
	CodeEnrolledOnly = "enrolled_only"
)

func (h *Handler) registerSocial(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/vocab/games", h.require(h.student(h.saveGames)))
	mux.HandleFunc("GET /v1/me/vocab/games", h.require(h.student(h.myGames)))
	mux.HandleFunc("GET /v1/vocab/leaderboard", h.require(h.leaderboard))
	mux.HandleFunc("GET /v1/teacher/groups/{id}/vocab/games", h.require(h.groupGames))

	mux.HandleFunc("GET /v1/me/vocab/decks", h.require(h.student(h.myDecks)))
	mux.HandleFunc("GET /v1/vocab/decks", h.require(h.community))
	mux.HandleFunc("POST /v1/vocab/decks", h.require(h.student(h.createDeck)))
	mux.HandleFunc("GET /v1/vocab/decks/{id}", h.require(h.deckDetail))
	mux.HandleFunc("PUT /v1/vocab/decks/{id}", h.require(h.student(h.updateDeck)))
	mux.HandleFunc("DELETE /v1/vocab/decks/{id}", h.require(h.student(h.deleteDeck)))
	mux.HandleFunc("PUT /v1/vocab/decks/{id}/published", h.require(h.student(h.publish(true))))
	mux.HandleFunc("DELETE /v1/vocab/decks/{id}/published", h.require(h.student(h.publish(false))))
	mux.HandleFunc("PUT /v1/vocab/decks/{id}/added", h.require(h.student(h.add(true))))
	mux.HandleFunc("DELETE /v1/vocab/decks/{id}/added", h.require(h.student(h.add(false))))
	mux.HandleFunc("POST /v1/vocab/decks/{id}/report", h.require(h.student(h.report)))
	mux.HandleFunc("PUT /v1/vocab/decks/{id}/hidden", h.require(h.teacher(h.moderate(true))))
	mux.HandleFunc("DELETE /v1/vocab/decks/{id}/hidden", h.require(h.teacher(h.moderate(false))))
}

func (h *Handler) saveGames(w http.ResponseWriter, r *http.Request, student int32) {
	var in struct {
		Results []GameResult `json:"results"`
	}
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	added, err := h.svc.SaveGames(r.Context(), student, in.Results, h.location)
	var bad *GamesError
	switch {
	case errors.As(err, &bad):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeInvalidGames, bad.Reason)
	case err != nil:
		h.internal(w, r, "save games", err)
	default:
		httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"received": len(in.Results), "added": added})
	}
}

func (h *Handler) myGames(w http.ResponseWriter, r *http.Request, student int32) {
	stats, err := h.svc.GameStats(r.Context(), student)
	if err != nil {
		h.internal(w, r, "my games", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"games": stats})
}

// leaderboard: ?scope=all (the default) or groups (enrolled students only:
// free accounts have no group, and teachers see their groups' points in the
// group's vocabulary).
func (h *Handler) leaderboard(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = "all"
	}
	var viewer int32
	if p.Kind == people.KindStudent {
		viewer = p.ID
	}
	switch {
	case scope != "all" && scope != "groups":
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "scope is all or groups")
		return
	case scope == "groups" && p.Kind != people.KindStudent:
		httpx.WriteError(w, r, http.StatusForbidden, httpx.CodeForbidden, "the groups board is for students")
		return
	case scope == "groups" && p.Registered:
		httpx.WriteError(w, r, http.StatusForbidden, CodeEnrolledOnly, "the groups board is for SAT MAKON's students")
		return
	}
	b, err := h.svc.Leaderboard(r.Context(), viewer, scope, time.Now(), h.location)
	if err != nil {
		h.internal(w, r, "leaderboard", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, b)
}

func (h *Handler) groupGames(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	if p.Kind != people.KindTeacher {
		httpx.WriteError(w, r, http.StatusForbidden, httpx.CodeForbidden, "only teachers see a group's games")
		return
	}
	group, err := strconv.ParseInt(r.PathValue("id"), 10, 32)
	if err != nil {
		httpx.NotFound(w, r)
		return
	}
	y, m, d := time.Now().In(h.location).Date()
	games, err := h.svc.GroupGames(r.Context(), p.ID, int32(group), time.Date(y, m, d, 0, 0, 0, 0, time.UTC), 100)
	switch {
	case errors.Is(err, ErrNotTeacher):
		httpx.WriteError(w, r, http.StatusForbidden, httpx.CodeForbidden, err.Error())
	case err != nil:
		h.internal(w, r, "group games", err)
	default:
		httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"games": games})
	}
}

func (h *Handler) myDecks(w http.ResponseWriter, r *http.Request, student int32) {
	decks, err := h.svc.MyDecks(r.Context(), student)
	if err != nil {
		h.internal(w, r, "my decks", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"decks": decks})
}

// community: ?q= searches; ?moderation=1 (teachers) lists the hidden and reported decks.
func (h *Handler) community(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	teacher := p.Kind == people.KindTeacher
	var viewer int32
	if !teacher {
		viewer = p.ID
	}
	decks, err := h.svc.Community(r.Context(), viewer, teacher, r.URL.Query().Get("moderation") == "1", r.URL.Query().Get("q"))
	if err != nil {
		h.internal(w, r, "community", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"decks": decks})
}

func (h *Handler) createDeck(w http.ResponseWriter, r *http.Request, student int32) {
	var in DeckInput
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	id, err := h.svc.CreateDeck(r.Context(), student, in)
	if h.deckError(w, r, err) {
		return
	}
	httpx.WriteJSON(w, r, http.StatusCreated, map[string]any{"id": id})
}

func (h *Handler) deckDetail(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	teacher := p.Kind == people.KindTeacher
	var viewer int32
	if !teacher {
		viewer = p.ID
	}
	d, reasons, err := h.svc.DeckDetail(r.Context(), viewer, teacher, r.PathValue("id"))
	if h.deckError(w, r, err) {
		return
	}
	out := map[string]any{"deck": d}
	if teacher {
		if reasons == nil {
			reasons = []string{}
		}
		out["report_reasons"] = reasons
	}
	httpx.WriteJSON(w, r, http.StatusOK, out)
}

func (h *Handler) updateDeck(w http.ResponseWriter, r *http.Request, student int32) {
	var in DeckInput
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	if h.deckError(w, r, h.svc.UpdateDeck(r.Context(), student, r.PathValue("id"), in)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) deleteDeck(w http.ResponseWriter, r *http.Request, student int32) {
	if h.deckError(w, r, h.svc.DeleteDeck(r.Context(), student, r.PathValue("id"))) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) publish(on bool) func(http.ResponseWriter, *http.Request, int32) {
	return func(w http.ResponseWriter, r *http.Request, student int32) {
		if h.deckError(w, r, h.svc.Publish(r.Context(), student, r.PathValue("id"), on)) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *Handler) add(on bool) func(http.ResponseWriter, *http.Request, int32) {
	return func(w http.ResponseWriter, r *http.Request, student int32) {
		if h.deckError(w, r, h.svc.AddDeck(r.Context(), student, r.PathValue("id"), on)) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *Handler) report(w http.ResponseWriter, r *http.Request, student int32) {
	var in struct {
		Reason string `json:"reason"`
	}
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	if h.deckError(w, r, h.svc.ReportDeck(r.Context(), student, r.PathValue("id"), in.Reason)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) moderate(hide bool) func(http.ResponseWriter, *http.Request, int32) {
	return func(w http.ResponseWriter, r *http.Request, teacher int32) {
		var in struct {
			Reason string `json:"reason"`
		}
		if hide && !httpx.DecodeJSON(w, r, &in) {
			return
		}
		if h.deckError(w, r, h.svc.Moderate(r.Context(), teacher, r.PathValue("id"), hide, in.Reason)) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// deckError answers a deck error and says whether there was one.
func (h *Handler) deckError(w http.ResponseWriter, r *http.Request, err error) bool {
	var bad *DeckError
	switch {
	case err == nil:
		return false
	case errors.As(err, &bad):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeInvalidDeck, bad.Reason)
	case errors.Is(err, ErrDeckNotFound):
		httpx.NotFound(w, r)
	case errors.Is(err, ErrNotOwner):
		httpx.WriteError(w, r, http.StatusForbidden, httpx.CodeForbidden, err.Error())
	case errors.Is(err, ErrTooManyDecks):
		httpx.WriteError(w, r, http.StatusConflict, CodeTooManyDecks, err.Error())
	case errors.Is(err, ErrDeckTooSmall):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeDeckTooSmall, err.Error())
	case errors.Is(err, ErrOwnDeck):
		httpx.WriteError(w, r, http.StatusConflict, CodeOwnDeck, err.Error())
	default:
		h.internal(w, r, "deck", err)
	}
	return true
}

// teacher lets only teachers through.
func (h *Handler) teacher(next func(http.ResponseWriter, *http.Request, int32)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, _ := auth.PrincipalFrom(r.Context())
		if p.Kind != people.KindTeacher {
			httpx.WriteError(w, r, http.StatusForbidden, httpx.CodeForbidden, "only teachers can do this")
			return
		}
		next(w, r, p.ID)
	}
}
