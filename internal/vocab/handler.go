package vocab

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/nazacd/makonbook-api/internal/auth"
	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/httpx"
)

// CodeInvalidReviews is the error code for a batch of reviews that can't be stored.
const CodeInvalidReviews = "invalid_reviews"

// Handler is the apps' vocabulary API.
type Handler struct {
	svc      *Service
	location *time.Location
	require  func(http.HandlerFunc) http.HandlerFunc
}

// NewHandler: require is auth's Require middleware.
func NewHandler(svc *Service, location *time.Location, require func(http.HandlerFunc) http.HandlerFunc) *Handler {
	return &Handler{svc: svc, location: location, require: require}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/vocab/deck", h.require(h.deck))
	mux.HandleFunc("GET /v1/vocab/lists/{slug}/images", h.require(h.images))
	mux.HandleFunc("POST /v1/vocab/reviews", h.require(h.student(h.saveReviews)))
	mux.HandleFunc("GET /v1/me/vocab/reviews", h.require(h.student(h.myReviews)))
	mux.HandleFunc("GET /v1/teacher/groups/{id}/vocab", h.require(h.groupProgress))
}

func (h *Handler) deck(w http.ResponseWriter, r *http.Request) {
	d, unchanged, err := h.svc.Deck(r.Context(), r.URL.Query().Get("have"))
	if err != nil {
		h.internal(w, r, "deck", err)
		return
	}
	if unchanged {
		httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"version": d.Version, "unchanged": true})
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"version": d.Version, "unchanged": false, "lists": d.Lists})
}

func (h *Handler) images(w http.ResponseWriter, r *http.Request) {
	links, expires, err := h.svc.Images(r.Context(), r.PathValue("slug"))
	switch {
	case errors.Is(err, ErrImagesOff):
		httpx.WriteError(w, r, http.StatusServiceUnavailable, httpx.CodeUnavailable, err.Error())
	case err != nil:
		h.internal(w, r, "images", err)
	default:
		httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"images": links, "expires_at": expires})
	}
}

func (h *Handler) saveReviews(w http.ResponseWriter, r *http.Request, student int32) {
	var in struct {
		Reviews []Review `json:"reviews"`
	}
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	added, err := h.svc.SaveReviews(r.Context(), student, in.Reviews)
	var bad *ReviewsError
	switch {
	case errors.As(err, &bad):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeInvalidReviews, bad.Reason)
	case err != nil:
		h.internal(w, r, "save reviews", err)
	default:
		httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"received": len(in.Reviews), "added": added})
	}
}

const pageSize = 1000

func (h *Handler) myReviews(w http.ResponseWriter, r *http.Request, student int32) {
	var after int64
	if s := r.URL.Query().Get("after"); s != "" {
		var err error
		if after, err = strconv.ParseInt(s, 10, 64); err != nil || after < 0 {
			httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "after must be a number from an earlier answer")
			return
		}
	}
	reviews, err := h.svc.Reviews(r.Context(), student, after, pageSize)
	if err != nil {
		h.internal(w, r, "my reviews", err)
		return
	}
	// next: where the following page starts; null when this was the last.
	var next *int64
	if len(reviews) == pageSize {
		next = &reviews[len(reviews)-1].Seq
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"reviews": reviews, "next": next})
}

func (h *Handler) groupProgress(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	if p.Kind != people.KindTeacher {
		httpx.WriteError(w, r, http.StatusForbidden, httpx.CodeForbidden, "only teachers see a group's vocabulary")
		return
	}
	group, err := strconv.ParseInt(r.PathValue("id"), 10, 32)
	if err != nil {
		httpx.NotFound(w, r)
		return
	}
	y, m, d := time.Now().In(h.location).Date()
	students, total, err := h.svc.GroupProgress(r.Context(), p.ID, int32(group), time.Date(y, m, d, 0, 0, 0, 0, time.UTC))
	switch {
	case errors.Is(err, ErrNotTeacher):
		httpx.WriteError(w, r, http.StatusForbidden, httpx.CodeForbidden, err.Error())
	case err != nil:
		h.internal(w, r, "group vocab", err)
	default:
		httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"words": total, "students": students})
	}
}

// student lets only students through.
func (h *Handler) student(next func(http.ResponseWriter, *http.Request, int32)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, _ := auth.PrincipalFrom(r.Context())
		if p.Kind != people.KindStudent {
			httpx.WriteError(w, r, http.StatusForbidden, httpx.CodeForbidden, "only students study words")
			return
		}
		next(w, r, p.ID)
	}
}

func (h *Handler) internal(w http.ResponseWriter, r *http.Request, what string, err error) {
	slog.ErrorContext(r.Context(), "vocab: "+what, "err", err)
	httpx.WriteError(w, r, http.StatusInternalServerError, httpx.CodeInternal, "something went wrong")
}
