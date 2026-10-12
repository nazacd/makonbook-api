package lessons

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/nazacd/makonbook-api/internal/auth"
	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/httpx"
)

// Handler serves the teacher's lesson list.
type Handler struct {
	svc      *Service
	location *time.Location
	require  func(http.HandlerFunc) http.HandlerFunc
	now      func() time.Time
}

// NewHandler: require is auth's Require middleware.
func NewHandler(svc *Service, location *time.Location, require func(http.HandlerFunc) http.HandlerFunc) *Handler {
	return &Handler{svc: svc, location: location, require: require, now: time.Now}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/teacher/lessons", h.require(h.teacherLessons))
}

type lessonsResponse struct {
	Date    string   `json:"date"`
	Lessons []Lesson `json:"lessons"`
}

var errBadDate = errors.New("date must be YYYY-MM-DD")

func (h *Handler) teacherLessons(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	if p.Kind != people.KindTeacher {
		httpx.WriteError(w, r, http.StatusForbidden, httpx.CodeForbidden, "only teachers have lessons to run")
		return
	}
	day, err := h.day(r.URL.Query().Get("date"))
	if err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}
	lessons, err := h.svc.TeacherLessons(r.Context(), p.ID, day)
	if err != nil {
		httpx.Logger(r.Context()).Error("teacher lessons", slog.Any("err", err))
		httpx.WriteError(w, r, http.StatusInternalServerError, httpx.CodeInternal, "something went wrong")
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, lessonsResponse{Date: day.Format(time.DateOnly), Lessons: lessons})
}

// day parses ?date=, defaulting to today in the school's time zone.
func (h *Handler) day(s string) (time.Time, error) {
	if s == "" {
		y, m, d := h.now().In(h.location).Date()
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC), nil
	}
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return time.Time{}, errBadDate
	}
	return t, nil
}
