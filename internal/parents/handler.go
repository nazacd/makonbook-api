package parents

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	gosync "sync"
	"time"

	"github.com/nazacd/makonbook-api/internal/attendance"
	"github.com/nazacd/makonbook-api/internal/auth"
	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/httpx"
	"github.com/nazacd/makonbook-api/internal/platform/ratelimit"
)

// Error codes of parents' access (see api/openapi.yaml).
const (
	CodeInvalidCode   = "invalid_parent_code"
	CodeInvalidReport = "invalid_report"
)

// A code has 5 digits, so it can be guessed: tries are limited per network
// and per phone, and many wrong codes in an hour alert the admins.
const (
	tryWindow      = 15 * time.Minute
	triesPerIP     = 10
	triesPerDevice = 5
	alarmWrong     = 50 // wrong codes in an hour, from anywhere
)

type Handler struct {
	svc        *Service
	location   *time.Location
	requireMe  func(http.HandlerFunc) http.HandlerFunc // auth.Require: students' and teachers' tokens
	trustProxy bool
	byIP       *ratelimit.Limiter
	byDevice   *ratelimit.Limiter
	log        *slog.Logger

	mu        gosync.Mutex
	wrongHour time.Time      // the hour wrong codes are counted in
	wrong     map[string]int // wrong codes per network this hour
	alarmed   bool           // the admins were told this hour
}

// NewHandler: requireMe is auth's Require (for a student's own code).
func NewHandler(svc *Service, location *time.Location, requireMe func(http.HandlerFunc) http.HandlerFunc, trustProxy bool, log *slog.Logger) *Handler {
	return &Handler{
		svc: svc, location: location, requireMe: requireMe, trustProxy: trustProxy, log: log,
		byIP: ratelimit.New(triesPerIP, tryWindow), byDevice: ratelimit.New(triesPerDevice, tryWindow),
		wrong: map[string]int{},
	}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/parent/login", h.login)
	mux.HandleFunc("POST /v1/parent/logout", h.require(h.logout))
	mux.HandleFunc("GET /v1/parent/child", h.require(h.child))
	mux.HandleFunc("GET /v1/parent/attendance", h.require(h.attendanceTable))
	mux.HandleFunc("GET /v1/parent/reports", h.require(h.reports))
	mux.HandleFunc("POST /v1/parent/reports", h.require(h.sendReport))
	mux.HandleFunc("GET /v1/me/parent-code", h.requireMe(h.myCode))
}

// require lets a parent's session through (never a student's or teacher's).
func (h *Handler) require(next func(http.ResponseWriter, *http.Request, Session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		sess, err := h.svc.Authenticate(r.Context(), strings.TrimSpace(token))
		if errors.Is(err, ErrUnauthenticated) {
			httpx.WriteError(w, r, http.StatusUnauthorized, httpx.CodeUnauthorized, "enter the child's code again")
			return
		}
		if err != nil {
			h.internal(w, r, "authenticate", err)
			return
		}
		next(w, r, sess)
	}
}

func (h *Handler) today() time.Time {
	y, m, d := time.Now().In(h.location).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

type loginRequest struct {
	Code     string `json:"code"`
	DeviceID string `json:"device_id"`
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if req.DeviceID == "" || len(req.DeviceID) > 100 || len(req.Code) > 20 {
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "code and device_id are required")
		return
	}
	ip, now := httpx.ClientIP(r, h.trustProxy), time.Now()
	if !h.byIP.Allow(ip, now) || !h.byDevice.Allow(req.DeviceID, now) {
		w.Header().Set("Retry-After", strconv.Itoa(int(tryWindow.Seconds())))
		httpx.WriteError(w, r, http.StatusTooManyRequests, httpx.CodeRateLimited, "too many tries, wait 15 minutes")
		return
	}
	token, child, err := h.svc.Login(r.Context(), req.Code, req.DeviceID)
	switch {
	case errors.Is(err, ErrBadCode):
		h.wrongCode(r.Context(), ip, now)
		httpx.WriteError(w, r, http.StatusUnauthorized, CodeInvalidCode, "no student has this code")
	case err != nil:
		h.internal(w, r, "login", err)
	default:
		httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"token": token, "child": child})
	}
}

// wrongCode counts a wrong code; past alarmWrong in an hour the admins are
// told once, with the networks most of them came from.
func (h *Handler) wrongCode(ctx context.Context, ip string, now time.Time) {
	h.mu.Lock()
	hour := now.Truncate(time.Hour)
	if !hour.Equal(h.wrongHour) {
		h.wrongHour, h.wrong, h.alarmed = hour, map[string]int{}, false
	}
	h.wrong[ip]++
	total := 0
	for _, n := range h.wrong {
		total += n
	}
	alarm := total >= alarmWrong && !h.alarmed
	var top []string
	if alarm {
		h.alarmed = true
		for k, n := range h.wrong {
			if n >= 5 {
				top = append(top, fmt.Sprintf("%s (%d)", k, n))
			}
		}
	}
	h.mu.Unlock()
	if alarm {
		h.log.Warn("parents: many wrong codes", "count", total)
		msg := fmt.Sprintf("⚠️ Parent codes: %d wrong codes this hour — someone may be guessing.", total)
		if len(top) > 0 {
			msg += " Networks: " + strings.Join(top, ", ")
		}
		_ = h.svc.notifier.Notify(ctx, msg)
	}
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request, sess Session) {
	if err := h.svc.Logout(r.Context(), sess); err != nil {
		h.internal(w, r, "logout", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) child(w http.ResponseWriter, r *http.Request, sess Session) {
	v, err := h.svc.Child(r.Context(), sess, h.today())
	if err != nil {
		h.internal(w, r, "child", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, v)
}

// attendanceTable is the child's attendance, in the shape of the student's
// own (GET /v1/me/attendance).
func (h *Handler) attendanceTable(w http.ResponseWriter, r *http.Request, sess Session) {
	today := h.today()
	groups, err := h.svc.Attendance(r.Context(), sess, today)
	if err != nil {
		h.internal(w, r, "attendance", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{
		"from":   today.AddDate(0, 0, -attendance.RecordsBackFull).Format(time.DateOnly),
		"to":     today.Format(time.DateOnly),
		"groups": groups,
	})
}

func (h *Handler) reports(w http.ResponseWriter, r *http.Request, sess Session) {
	list, err := h.svc.Reports(r.Context(), sess)
	if err != nil {
		h.internal(w, r, "reports", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"reports": list})
}

type reportRequest struct {
	GroupID *int32 `json:"group_id"`
	Message string `json:"message"`
	Lang    string `json:"lang"`
}

func (h *Handler) sendReport(w http.ResponseWriter, r *http.Request, sess Session) {
	var req reportRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	rep, err := h.svc.SendReport(r.Context(), sess, req.GroupID, req.Message, req.Lang, h.today())
	var bad *InvalidError
	switch {
	case errors.As(err, &bad):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeInvalidReport, bad.Reason)
	case err != nil:
		h.internal(w, r, "report", err)
	default:
		httpx.WriteJSON(w, r, http.StatusCreated, rep)
	}
}

// myCode is a student's parent code, to give to their parents.
func (h *Handler) myCode(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	if p.Kind != people.KindStudent {
		httpx.WriteError(w, r, http.StatusForbidden, httpx.CodeForbidden, "only students have a parent code")
		return
	}
	code, err := h.svc.Code(r.Context(), p.ID)
	if err != nil {
		h.internal(w, r, "code", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]string{"code": code})
}

func (h *Handler) internal(w http.ResponseWriter, r *http.Request, what string, err error) {
	h.log.ErrorContext(r.Context(), "parents: "+what, "err", err)
	httpx.WriteError(w, r, http.StatusInternalServerError, httpx.CodeInternal, "something went wrong")
}
