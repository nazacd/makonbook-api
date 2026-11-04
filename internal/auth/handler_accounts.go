package auth

import (
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/nazacd/makonbook-api/internal/platform/httpx"
)

// Error codes of accounts (spec 21; see api/openapi.yaml).
//
//nolint:gosec // G101: these are error codes, not credentials
const (
	CodeSMSUnavailable    = "sms_unavailable"
	CodeInvalidPhone      = "invalid_phone"
	CodeInvalidCode       = "invalid_code"
	CodeCodeExpired       = "code_expired"
	CodeTooManyTries      = "too_many_tries"
	CodeInvalidTicket     = "invalid_ticket"
	CodeInvalidProfile    = "invalid_profile"
	CodeNoAccount         = "no_account"
	CodeGoogleTaken       = "google_taken"
	CodeGoogleUnavailable = "google_unavailable"
)

func (h *Handler) registerAccounts(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/auth/phone/start", h.phoneStart)
	mux.HandleFunc("POST /v1/auth/phone/verify", h.phoneVerify)
	mux.HandleFunc("POST /v1/auth/signup", h.signup)
	mux.HandleFunc("POST /v1/auth/reset", h.reset)
	mux.HandleFunc("GET /v1/auth/google/start", h.googleStart)
	mux.HandleFunc("GET "+GoogleCallbackPath, h.googleCallback)
	mux.HandleFunc("POST /v1/auth/google/ticket", h.googleTicket)
	mux.HandleFunc("PUT /v1/me/profile", h.Require(h.setProfile))
}

type phoneRequest struct {
	Phone   string `json:"phone"`
	Code    string `json:"code"`
	Purpose string `json:"purpose"`
}

func (h *Handler) phoneRequest(w http.ResponseWriter, r *http.Request) (phoneRequest, bool) {
	var req phoneRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return req, false
	}
	if req.Purpose != PurposeSignup && req.Purpose != PurposeReset {
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, `purpose must be "signup" or "reset"`)
		return req, false
	}
	if len(req.Phone) > 50 || len(req.Code) > 20 {
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeInvalidPhone, ErrInvalidPhone.Error())
		return req, false
	}
	return req, true
}

func (h *Handler) phoneStart(w http.ResponseWriter, r *http.Request) {
	req, ok := h.phoneRequest(w, r)
	if !ok {
		return
	}
	err := h.svc.StartPhone(r.Context(), req.Phone, req.Purpose, httpx.ClientIP(r, h.trustProxy))
	var limited *RateLimitedError
	switch {
	case errors.As(err, &limited):
		retryAfter(w, limited.RetryAfter)
		httpx.WriteError(w, r, http.StatusTooManyRequests, httpx.CodeRateLimited, limited.Error())
	case h.accountsError(w, r, "send code", err):
	default:
		httpx.WriteJSON(w, r, http.StatusOK, map[string]int{
			"resend_in": int(ResendAfter.Seconds()), "expires_in": int(CodeTTL.Seconds()),
		})
	}
}

func (h *Handler) phoneVerify(w http.ResponseWriter, r *http.Request) {
	req, ok := h.phoneRequest(w, r)
	if !ok {
		return
	}
	ticket, err := h.svc.VerifyPhone(r.Context(), req.Phone, req.Code, req.Purpose)
	var wrong *WrongCodeError
	switch {
	case errors.As(err, &wrong) && wrong.TriesLeft == 0:
		retryAfter(w, wrong.RetryAfter)
		httpx.WriteError(w, r, http.StatusTooManyRequests, CodeTooManyTries, wrong.Error())
	case errors.As(err, &wrong):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeInvalidCode, wrong.Error())
	case errors.Is(err, ErrCodeExpired):
		httpx.WriteError(w, r, http.StatusGone, CodeCodeExpired, err.Error())
	case h.accountsError(w, r, "verify code", err):
	default:
		httpx.WriteJSON(w, r, http.StatusOK, map[string]string{"ticket": ticket})
	}
}

// signupResponse is loginResponse with whether the phone had an account.
type signupResponse struct {
	loginResponse
	Existing bool `json:"existing"`
}

func (h *Handler) signup(w http.ResponseWriter, r *http.Request) {
	var req SignupInput
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if req.Password != nil && len(*req.Password) > 256 || len(req.DeviceID) > 100 {
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "password or device_id too long")
		return
	}
	res, existing, err := h.svc.Signup(r.Context(), req)
	if h.accountsError(w, r, "sign up", err) {
		return
	}
	if resp, ok := h.loggedIn(w, r, res); ok {
		httpx.WriteJSON(w, r, http.StatusOK, signupResponse{loginResponse: resp, Existing: existing})
	}
}

type resetRequest struct {
	Ticket   string `json:"ticket"`
	Password string `json:"password"`
	DeviceID string `json:"device_id"`
}

func (h *Handler) reset(w http.ResponseWriter, r *http.Request) {
	var req resetRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if len(req.Password) > 256 || len(req.DeviceID) > 100 {
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "password or device_id too long")
		return
	}
	res, err := h.svc.Reset(r.Context(), req.Ticket, req.Password, req.DeviceID)
	if h.accountsError(w, r, "reset password", err) {
		return
	}
	if resp, ok := h.loggedIn(w, r, res); ok {
		httpx.WriteJSON(w, r, http.StatusOK, resp)
	}
}

func (h *Handler) googleStart(w http.ResponseWriter, r *http.Request) {
	redirect, cookie, err := h.svc.GoogleStart(r.URL.Query().Get("return"))
	if h.accountsError(w, r, "google start", err) {
		return
	}
	http.SetCookie(w, googleCookieFor(cookie, int(googleStateTTL.Seconds())))
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, redirect, http.StatusFound) //nolint:gosec // G710: Google's address from the settings; the request only adds query values
}

func (h *Handler) googleCallback(w http.ResponseWriter, r *http.Request) {
	if !h.svc.GoogleEnabled() {
		h.accountsError(w, r, "google callback", ErrGoogleOff)
		return
	}
	var cookie string
	if c, err := r.Cookie(googleCookie); err == nil {
		cookie = c.Value
	}
	location, reason := h.svc.GoogleCallback(r.Context(), cookie, r.URL.Query())
	if reason != nil {
		httpx.Logger(r.Context()).Warn("google sign-in failed", slog.Any("err", reason))
	}
	http.SetCookie(w, googleCookieFor("", -1))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, location, http.StatusFound) //nolint:gosec // G710: always WEB_ORIGIN's page; the request's path goes in the fragment, checked by safeReturn
}

// googleCookieFor keeps the sign-in's state between start and callback: only
// for those two addresses, sent on Google's top-level redirect back
// (SameSite=Lax), never readable by scripts, HTTPS only (browsers count
// http://localhost as secure, for development).
func googleCookieFor(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     googleCookie,
		Value:    value,
		Path:     "/v1/auth/google/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
}

type googleTicketRequest struct {
	Ticket   string `json:"ticket"`
	DeviceID string `json:"device_id"`
}

type googleSignedIn struct {
	Status string `json:"status"` // signed_in
	loginResponse
}

type googleNeedsPhone struct {
	Status       string `json:"status"` // needs_phone
	GoogleTicket string `json:"google_ticket"`
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name"`
	Email        string `json:"email"`
}

func (h *Handler) googleTicket(w http.ResponseWriter, r *http.Request) {
	var req googleTicketRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if len(req.DeviceID) > 100 {
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "device_id too long")
		return
	}
	res, err := h.svc.GoogleTicket(r.Context(), req.Ticket, req.DeviceID)
	if h.accountsError(w, r, "google ticket", err) {
		return
	}
	if res.SignedIn == nil {
		a := res.Account
		httpx.WriteJSON(w, r, http.StatusOK, googleNeedsPhone{Status: "needs_phone", GoogleTicket: res.SignupTicket,
			FirstName: a.FirstName, LastName: a.LastName, Email: a.Email})
		return
	}
	if resp, ok := h.loggedIn(w, r, *res.SignedIn); ok {
		httpx.WriteJSON(w, r, http.StatusOK, googleSignedIn{Status: "signed_in", loginResponse: resp})
	}
}

type profileRequest struct {
	FirstName *string `json:"first_name"`
	LastName  *string `json:"last_name"`
}

func (h *Handler) setProfile(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	var req profileRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if err := h.svc.SetNames(r.Context(), p, req.FirstName, req.LastName); h.accountsError(w, r, "set names", err) {
		return
	}
	h.me(w, r)
}

// loggedIn is the login answer for a new session.
func (h *Handler) loggedIn(w http.ResponseWriter, r *http.Request, res LoginResult) (loginResponse, bool) {
	prof, err := h.svc.Profile(r.Context(), res.Principal, h.today())
	if err != nil {
		h.internal(w, r, "load profile", err)
		return loginResponse{}, false
	}
	return loginResponse{Token: res.Token, ExpiresAt: res.ExpiresAt, User: prof}, true
}

// accountsError answers the errors the accounts endpoints share; false (and
// nothing written) for nil.
func (h *Handler) accountsError(w http.ResponseWriter, r *http.Request, what string, err error) bool {
	var invalid *ValidationError
	var profile *ProfileError
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrSMSOff):
		httpx.WriteError(w, r, http.StatusServiceUnavailable, CodeSMSUnavailable, err.Error())
	case errors.Is(err, ErrSMSFailed):
		httpx.Logger(r.Context()).Error(what, slog.Any("err", err))
		httpx.WriteError(w, r, http.StatusServiceUnavailable, CodeSMSUnavailable, ErrSMSFailed.Error())
	case errors.Is(err, ErrGoogleOff):
		httpx.WriteError(w, r, http.StatusServiceUnavailable, CodeGoogleUnavailable, err.Error())
	case errors.Is(err, ErrInvalidPhone):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeInvalidPhone, err.Error())
	case errors.Is(err, ErrInvalidTicket):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeInvalidTicket, err.Error())
	case errors.As(err, &invalid):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeWeakPassword, invalid.Error())
	case errors.As(err, &profile):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeInvalidProfile, profile.Error())
	case errors.Is(err, ErrNoAccount):
		httpx.WriteError(w, r, http.StatusNotFound, CodeNoAccount, err.Error())
	case errors.Is(err, ErrGoogleTaken):
		httpx.WriteError(w, r, http.StatusConflict, CodeGoogleTaken, err.Error())
	default:
		h.internal(w, r, what, err)
	}
	return true
}

func retryAfter(w http.ResponseWriter, d time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(max(int(math.Ceil(d.Seconds())), 1)))
}
