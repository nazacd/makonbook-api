package attendance

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

// Error codes specific to attendance (see api/openapi.yaml).
const (
	CodeAttendanceDisabled = "attendance_disabled"
	CodeTooEarly           = "too_early"
	CodeLessonOver         = "lesson_over"
	CodeSessionClosed      = "session_closed"
	CodeInvalidCode        = "invalid_code"
	CodeCodeExpired        = "code_expired"
	CodeNotInRoster        = "not_in_roster"
	CodeDeviceUsed         = "device_already_used"
	CodeMarkedAbsent       = "marked_absent"
	CodeEditWindowClosed   = "edit_window_closed"
)

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
	mux.HandleFunc("POST /v1/attendance/sessions", h.require(h.teacher(h.open)))
	mux.HandleFunc("GET /v1/attendance/sessions/{id}", h.require(h.teacher(h.get)))
	mux.HandleFunc("POST /v1/attendance/sessions/{id}/finish", h.require(h.teacher(h.finish)))
	mux.HandleFunc("PUT /v1/attendance/sessions/{id}/marks/{client_id}", h.require(h.teacher(h.setMark)))
	mux.HandleFunc("POST /v1/attendance/scan", h.require(h.scan))
}

// teacher lets only teachers through.
func (h *Handler) teacher(next func(http.ResponseWriter, *http.Request, int32)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, _ := auth.PrincipalFrom(r.Context())
		if p.Kind != people.KindTeacher {
			httpx.WriteError(w, r, http.StatusForbidden, httpx.CodeForbidden, "only teachers run attendance")
			return
		}
		next(w, r, p.ID)
	}
}

type openRequest struct {
	GroupID int32  `json:"group_id"`
	Date    string `json:"date"` // optional; today at the school
}

func (h *Handler) open(w http.ResponseWriter, r *http.Request, teacherID int32) {
	var req openRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	day, ok := h.day(req.Date)
	if req.GroupID == 0 || !ok {
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "group_id is required; date, if given, is YYYY-MM-DD")
		return
	}
	s, err := h.svc.Open(r.Context(), teacherID, req.GroupID, day)
	h.respond(w, r, s, err)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request, teacherID int32) {
	s, err := h.svc.Get(r.Context(), teacherID, r.PathValue("id"))
	h.respond(w, r, s, err)
}

func (h *Handler) finish(w http.ResponseWriter, r *http.Request, teacherID int32) {
	s, err := h.svc.Finish(r.Context(), teacherID, r.PathValue("id"))
	h.respond(w, r, s, err)
}

type markRequest struct {
	State string `json:"state"`
}

func (h *Handler) setMark(w http.ResponseWriter, r *http.Request, teacherID int32) {
	clientID, err := strconv.ParseInt(r.PathValue("client_id"), 10, 32)
	if err != nil {
		httpx.WriteError(w, r, http.StatusNotFound, httpx.CodeNotFound, "no such student")
		return
	}
	var req markRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	s, err := h.svc.SetMark(r.Context(), teacherID, r.PathValue("id"), int32(clientID), req.State)
	h.respond(w, r, s, err)
}

type scanRequest struct {
	SessionID string `json:"session_id"`
	Window    int64  `json:"window"`
	Sig       string `json:"sig"`
	DeviceID  string `json:"device_id"`
}

func (h *Handler) scan(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	if p.Kind != people.KindStudent {
		httpx.WriteError(w, r, http.StatusForbidden, httpx.CodeForbidden, "only students check in")
		return
	}
	var req scanRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if req.DeviceID == "" || len(req.DeviceID) > 100 || len(req.Sig) > 64 {
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "session_id, window, sig and device_id are required")
		return
	}
	res, err := h.svc.Scan(r.Context(), p.ID, req.SessionID, req.Window, req.Sig, req.DeviceID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, res)
}

func (h *Handler) respond(w http.ResponseWriter, r *http.Request, s Session, err error) {
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, s)
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var early *TooEarlyError
	switch {
	case errors.As(err, &early):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeTooEarly,
			"attendance opens at "+early.OpensAt.In(h.location).Format("15:04"))
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrNoLesson):
		httpx.WriteError(w, r, http.StatusNotFound, httpx.CodeNotFound, err.Error())
	case errors.Is(err, ErrDisabled):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeAttendanceDisabled, err.Error())
	case errors.Is(err, ErrLessonOver):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeLessonOver, err.Error())
	case errors.Is(err, ErrSessionClosed):
		httpx.WriteError(w, r, http.StatusConflict, CodeSessionClosed, err.Error())
	case errors.Is(err, ErrInvalidCode):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeInvalidCode, err.Error())
	case errors.Is(err, ErrCodeExpired):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeCodeExpired, "this code has expired; scan the one on the screen now")
	case errors.Is(err, ErrNotInRoster):
		httpx.WriteError(w, r, http.StatusForbidden, CodeNotInRoster, err.Error())
	case errors.Is(err, ErrDeviceUsed):
		httpx.WriteError(w, r, http.StatusConflict, CodeDeviceUsed, err.Error())
	case errors.Is(err, ErrMarkedAbsent):
		httpx.WriteError(w, r, http.StatusConflict, CodeMarkedAbsent, err.Error())
	case errors.Is(err, ErrEditWindowClosed):
		httpx.WriteError(w, r, http.StatusForbidden, CodeEditWindowClosed, err.Error())
	case errors.Is(err, ErrBadState):
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
	default:
		httpx.Logger(r.Context()).Error("attendance", slog.Any("err", err))
		httpx.WriteError(w, r, http.StatusInternalServerError, httpx.CodeInternal, "something went wrong")
	}
}

// day parses a date, defaulting to today at the school.
func (h *Handler) day(s string) (time.Time, bool) {
	if s == "" {
		y, m, d := time.Now().In(h.location).Date()
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC), true
	}
	t, err := time.Parse(time.DateOnly, s)
	return t, err == nil
}
