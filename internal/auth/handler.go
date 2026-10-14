package auth

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nazacd/makonbook-api/internal/platform/httpx"
	"github.com/nazacd/makonbook-api/internal/platform/ratelimit"
)

// Error codes specific to auth (see api/openapi.yaml).
//
//nolint:gosec // G101: these are error codes, not credentials
const (
	CodeInvalidCredentials     = "invalid_credentials"
	CodeAccountLocked          = "account_locked"
	CodePasswordChangeRequired = "password_change_required"
	CodeWrongPassword          = "wrong_password"
	CodeWeakPassword           = "weak_password"
	CodeInvalidUsername        = "invalid_username"
	CodeUsernameTaken          = "username_taken"
)

// Per-IP login attempts. Generous: a classroom shares one IP.
const (
	ipLimit     = 100
	loginWindow = 10 * time.Minute
)

type Handler struct {
	svc        *Service
	limiter    *ratelimit.Limiter
	trustProxy bool
	location   *time.Location // school time zone, for "today"
}

// NewHandler serves the auth endpoints. trustProxy says the API runs behind a
// reverse proxy whose X-Forwarded-For can be trusted for client IPs.
func NewHandler(svc *Service, location *time.Location, trustProxy bool) *Handler {
	return &Handler{
		svc:        svc,
		limiter:    ratelimit.New(ipLimit, loginWindow),
		trustProxy: trustProxy,
		location:   location,
	}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/auth/login", h.login)
	// Reachable while the default password is still in use, so the app can
	// show who is logged in and let them pick a password.
	mux.HandleFunc("POST /v1/auth/password", h.RequireAllowingDefaultPassword(h.changePassword))
	mux.HandleFunc("POST /v1/auth/logout", h.RequireAllowingDefaultPassword(h.logout))
	mux.HandleFunc("GET /v1/me", h.RequireAllowingDefaultPassword(h.me))
	mux.HandleFunc("PUT /v1/auth/username", h.Require(h.setUsername))
}

type ctxKey struct{}

// PrincipalFrom returns the logged-in person for a request that passed
// Require; ok is false otherwise.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// Require wraps an endpoint for logged-in users who have chosen their own
// password. Other features use it for their routes.
func (h *Handler) Require(next http.HandlerFunc) http.HandlerFunc {
	return h.authenticate(next, false)
}

// RequireAllowingDefaultPassword is Require for the few endpoints a person
// needs before replacing the default password.
func (h *Handler) RequireAllowingDefaultPassword(next http.HandlerFunc) http.HandlerFunc {
	return h.authenticate(next, true)
}

func (h *Handler) authenticate(next http.HandlerFunc, allowDefault bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		p, err := h.svc.Authenticate(r.Context(), strings.TrimSpace(token))
		if errors.Is(err, ErrUnauthenticated) {
			httpx.WriteError(w, r, http.StatusUnauthorized, httpx.CodeUnauthorized, "log in again")
			return
		}
		if err != nil {
			h.internal(w, r, "authenticate", err)
			return
		}
		if p.MustChangePassword && !allowDefault {
			httpx.WriteError(w, r, http.StatusForbidden, CodePasswordChangeRequired, "choose your own password first")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)))
	}
}

type loginRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
	DeviceID string `json:"device_id"`
}

type loginResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	User      Profile   `json:"user"`
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if !h.limiter.Allow(httpx.ClientIP(r, h.trustProxy), time.Now()) {
		w.Header().Set("Retry-After", strconv.Itoa(int(loginWindow.Seconds())))
		httpx.WriteError(w, r, http.StatusTooManyRequests, httpx.CodeRateLimited, "too many login attempts from this network, try again later")
		return
	}
	var req loginRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if req.Login == "" || req.Password == "" || len(req.Login) > 100 || len(req.Password) > 256 || len(req.DeviceID) > 100 {
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "login and password are required")
		return
	}

	res, err := h.svc.Login(r.Context(), req.Login, req.Password, req.DeviceID)
	var locked *LockedError
	switch {
	case errors.Is(err, ErrInvalidCredentials):
		httpx.WriteError(w, r, http.StatusUnauthorized, CodeInvalidCredentials,
			"wrong phone number, username or password; if this keeps happening, contact the administrator")
		return
	case errors.As(err, &locked):
		h.writeLocked(w, r, locked)
		return
	case err != nil:
		h.internal(w, r, "login", err)
		return
	}

	prof, err := h.svc.Profile(r.Context(), res.Principal, h.today())
	if err != nil {
		h.internal(w, r, "load profile", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, loginResponse{Token: res.Token, ExpiresAt: res.ExpiresAt, User: prof})
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (h *Handler) changePassword(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	var req changePasswordRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}

	err := h.svc.ChangePassword(r.Context(), p, req.CurrentPassword, req.NewPassword)
	var locked *LockedError
	var invalid *ValidationError
	switch {
	case errors.Is(err, ErrWrongPassword):
		httpx.WriteError(w, r, http.StatusForbidden, CodeWrongPassword, "current password is wrong")
	case errors.As(err, &locked):
		h.writeLocked(w, r, locked)
	case errors.As(err, &invalid):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeWeakPassword, invalid.Error())
	case err != nil:
		h.internal(w, r, "change password", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

type usernameRequest struct {
	Username string `json:"username"`
}

func (h *Handler) setUsername(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	var req usernameRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}

	u, err := h.svc.SetUsername(r.Context(), p, req.Username)
	var invalid *ValidationError
	switch {
	case errors.As(err, &invalid):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeInvalidUsername, invalid.Error())
	case errors.Is(err, ErrUsernameTaken):
		httpx.WriteError(w, r, http.StatusConflict, CodeUsernameTaken, "this username is taken")
	case err != nil:
		h.internal(w, r, "set username", err)
	default:
		httpx.WriteJSON(w, r, http.StatusOK, usernameRequest{Username: u})
	}
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	if err := h.svc.Logout(r.Context(), p); err != nil {
		h.internal(w, r, "logout", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	prof, err := h.svc.Profile(r.Context(), p, h.today())
	if err != nil {
		h.internal(w, r, "load profile", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, prof)
}

func (h *Handler) writeLocked(w http.ResponseWriter, r *http.Request, e *LockedError) {
	secs := int(math.Ceil(time.Until(e.Until).Seconds()))
	w.Header().Set("Retry-After", strconv.Itoa(max(secs, 1)))
	httpx.WriteError(w, r, http.StatusTooManyRequests, CodeAccountLocked,
		"too many wrong passwords; try again after "+e.Until.In(h.location).Format("15:04"))
}

func (h *Handler) internal(w http.ResponseWriter, r *http.Request, what string, err error) {
	httpx.Logger(r.Context()).Error(what, slog.Any("err", err))
	httpx.WriteError(w, r, http.StatusInternalServerError, httpx.CodeInternal, "internal error")
}

// today is the current date in the school's time zone, as midnight UTC.
func (h *Handler) today() time.Time {
	y, m, d := time.Now().In(h.location).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
