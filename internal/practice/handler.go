package practice

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

// Error codes specific to practice tests in the apps (see api/openapi.yaml).
const (
	CodeInvalidAttempt = "invalid_attempt"
	CodeAttemptTaken   = "attempt_id_taken"
)

// Handler is the apps' practice API: the catalog, packages, attempts.
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
	mux.HandleFunc("GET /v1/practice/tests", h.require(h.catalog))
	mux.HandleFunc("GET /v1/practice/tests/{code}/package", h.require(h.download))
	mux.HandleFunc("POST /v1/practice/attempts", h.require(h.student(h.saveAttempt)))
	mux.HandleFunc("GET /v1/me/practice/attempts", h.require(h.student(h.myAttempts)))
	mux.HandleFunc("GET /v1/teacher/groups/{id}/practice", h.require(h.groupPractice))
}

func (h *Handler) catalog(w http.ResponseWriter, r *http.Request) {
	tests, err := h.svc.Catalog(r.Context())
	if err != nil {
		h.internal(w, r, "catalog", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"tests": tests})
}

func (h *Handler) download(w http.ResponseWriter, r *http.Request) {
	var version int64
	if v := r.URL.Query().Get("version"); v != "" {
		var err error
		if version, err = strconv.ParseInt(v, 10, 32); err != nil || version <= 0 {
			httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "version must be a positive number")
			return
		}
	}
	d, err := h.svc.Download(r.Context(), r.PathValue("code"), int32(version))
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.WriteError(w, r, http.StatusNotFound, httpx.CodeNotFound, "this test isn't published")
	case errors.Is(err, ErrUploadsOff):
		httpx.WriteError(w, r, http.StatusServiceUnavailable, httpx.CodeUnavailable, err.Error())
	case err != nil:
		h.internal(w, r, "download", err)
	default:
		httpx.WriteJSON(w, r, http.StatusOK, d)
	}
}

func (h *Handler) saveAttempt(w http.ResponseWriter, r *http.Request, student int32) {
	var in AttemptInput
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	a, err := h.svc.SaveAttempt(r.Context(), student, in)
	var bad *AttemptError
	switch {
	case errors.As(err, &bad):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeInvalidAttempt, bad.Reason)
	case errors.Is(err, ErrAttemptTaken):
		httpx.WriteError(w, r, http.StatusConflict, CodeAttemptTaken, err.Error())
	case err != nil:
		h.internal(w, r, "save attempt", err)
	default:
		httpx.WriteJSON(w, r, http.StatusOK, a)
	}
}

func (h *Handler) myAttempts(w http.ResponseWriter, r *http.Request, student int32) {
	attempts, err := h.svc.StudentAttempts(r.Context(), student)
	if err != nil {
		h.internal(w, r, "my attempts", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"attempts": attempts})
}

func (h *Handler) groupPractice(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	if p.Kind != people.KindTeacher {
		httpx.WriteError(w, r, http.StatusForbidden, httpx.CodeForbidden, "only teachers see a group's practice")
		return
	}
	group, err := strconv.ParseInt(r.PathValue("id"), 10, 32)
	if err != nil {
		httpx.NotFound(w, r)
		return
	}
	y, m, d := time.Now().In(h.location).Date()
	students, err := h.svc.GroupAttempts(r.Context(), p.ID, int32(group), time.Date(y, m, d, 0, 0, 0, 0, time.UTC))
	switch {
	case errors.Is(err, ErrNotTeacher):
		httpx.WriteError(w, r, http.StatusForbidden, httpx.CodeForbidden, err.Error())
	case err != nil:
		h.internal(w, r, "group practice", err)
	default:
		httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"students": students})
	}
}

// student lets only students through.
func (h *Handler) student(next func(http.ResponseWriter, *http.Request, int32)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, _ := auth.PrincipalFrom(r.Context())
		if p.Kind != people.KindStudent {
			httpx.WriteError(w, r, http.StatusForbidden, httpx.CodeForbidden, "only students take practice tests")
			return
		}
		next(w, r, p.ID)
	}
}

func (h *Handler) internal(w http.ResponseWriter, r *http.Request, what string, err error) {
	slog.ErrorContext(r.Context(), "practice: "+what, "err", err)
	httpx.WriteError(w, r, http.StatusInternalServerError, httpx.CodeInternal, "something went wrong")
}
