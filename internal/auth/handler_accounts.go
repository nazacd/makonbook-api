package auth

import (
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nazacd/makonbook-api/internal/platform/httpx"
)

// Error codes of accounts (spec 21; see api/openapi.yaml).
//
//nolint:gosec // G101: these are error codes, not credentials
const (
	CodeSMSUnavailable      = "sms_unavailable"
	CodeInvalidPhone        = "invalid_phone"
	CodeInvalidCode         = "invalid_code"
	CodeCodeExpired         = "code_expired"
	CodeTooManyTries        = "too_many_tries"
	CodeInvalidTicket       = "invalid_ticket"
	CodeInvalidProfile      = "invalid_profile"
	CodeNoAccount           = "no_account"
	CodeGoogleTaken         = "google_taken"
	CodeGoogleUnavailable   = "google_unavailable"
	CodeInvalidEmail        = "invalid_email"
	CodeEmailTaken          = "email_taken"
	CodeEmailUnavailable    = "email_unavailable"
	CodeTelegramUnavailable = "telegram_unavailable"
	CodeTelegramNoPhone     = "telegram_no_phone"
	CodePhoneAlreadySet     = "phone_already_set"
	CodePhoneRequired       = "phone_required"
	CodePhotoTooLarge       = "photo_too_large"
	CodeInvalidPhoto        = "invalid_photo"
)

func (h *Handler) registerAccounts(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/auth/email/start", h.emailStart)
	mux.HandleFunc("POST /v1/auth/email/verify", h.emailVerify)
	mux.HandleFunc("POST /v1/auth/signup", h.signup)
	mux.HandleFunc("POST /v1/auth/phone/start", h.phoneStart)
	mux.HandleFunc("POST /v1/auth/phone/verify", h.phoneVerify)
	mux.HandleFunc("POST /v1/auth/reset", h.reset)
	mux.HandleFunc("GET /v1/auth/google/start", h.googleStart)
	mux.HandleFunc("GET "+GoogleCallbackPath, h.googleCallback)
	mux.HandleFunc("POST /v1/auth/google/ticket", h.googleTicket)
	mux.HandleFunc("POST /v1/auth/telegram/start", h.telegramStart)
	mux.HandleFunc("GET "+TelegramCallbackPath, h.telegramCallback)
	mux.HandleFunc("POST /v1/auth/telegram/ticket", h.telegramTicket)
	mux.HandleFunc("POST /v1/me/phone/start", h.Require(h.myPhoneStart))
	mux.HandleFunc("POST /v1/me/phone/verify", h.Require(h.myPhoneVerify))
	mux.HandleFunc("POST /v1/me/phone/telegram", h.Require(h.myPhoneTelegram))
	mux.HandleFunc("PUT /v1/me/profile", h.Require(h.setProfile))
	mux.HandleFunc("GET "+photoPath+"{id}", h.servePhoto)
	// Everyone signed in, with or without a phone or a password of their own.
	mux.HandleFunc("PUT /v1/me/photo", h.RequireAllowingDefaultPassword(h.setPhoto))
	mux.HandleFunc("DELETE /v1/me/photo", h.RequireAllowingDefaultPassword(h.removePhoto))
}

// RequireContent is Require for the content (practice, vocabulary): a
// registered student without a phone gets 403 phone_required until they add
// one (spec 21.4). Enrolled people are never locked.
func (h *Handler) RequireContent(next http.HandlerFunc) http.HandlerFunc {
	return h.Require(func(w http.ResponseWriter, r *http.Request) {
		if p, _ := PrincipalFrom(r.Context()); p.NeedsPhone {
			httpx.WriteError(w, r, http.StatusForbidden, CodePhoneRequired, "add your phone number first")
			return
		}
		next(w, r)
	})
}

// --- Email ------------------------------------------------------------------------

type emailRequest struct {
	Email    string `json:"email"`
	Code     string `json:"code"`
	DeviceID string `json:"device_id"`
}

func (h *Handler) emailRequest(w http.ResponseWriter, r *http.Request) (emailRequest, bool) {
	var req emailRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return req, false
	}
	if len(req.Code) > 20 || len(req.DeviceID) > 100 {
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "code or device_id too long")
		return req, false
	}
	if len(req.Email) > 320 {
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeInvalidEmail, ErrInvalidEmail.Error())
		return req, false
	}
	return req, true
}

func (h *Handler) emailStart(w http.ResponseWriter, r *http.Request) {
	req, ok := h.emailRequest(w, r)
	if !ok {
		return
	}
	err := h.svc.StartEmail(r.Context(), req.Email, httpx.ClientIP(r, h.trustProxy))
	if h.limited(w, r, err) || h.accountsError(w, r, "send email code", err) {
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]int{"resend_in": int(ResendAfter.Seconds()), "expires_in": int(CodeTTL.Seconds())})
}

type emailNew struct {
	Status      string `json:"status"` // new
	EmailTicket string `json:"email_ticket"`
}

func (h *Handler) emailVerify(w http.ResponseWriter, r *http.Request) {
	req, ok := h.emailRequest(w, r)
	if !ok {
		return
	}
	res, err := h.svc.VerifyEmail(r.Context(), req.Email, req.Code, req.DeviceID)
	switch {
	case h.codeError(w, r, err):
	case h.accountsError(w, r, "verify email code", err):
	case res.SignedIn != nil:
		if resp, ok := h.loggedIn(w, r, *res.SignedIn); ok {
			httpx.WriteJSON(w, r, http.StatusOK, signedIn{Status: "signed_in", loginResponse: resp})
		}
	default:
		httpx.WriteJSON(w, r, http.StatusOK, emailNew{Status: "new", EmailTicket: res.Ticket})
	}
}

// signedIn is a login answer with a status, where an endpoint answers more
// than one shape.
type signedIn struct {
	Status string `json:"status"` // signed_in
	loginResponse
}

func (h *Handler) signup(w http.ResponseWriter, r *http.Request) {
	var req SignupInput
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if len(req.Password) > 256 || len(req.DeviceID) > 100 {
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "password or device_id too long")
		return
	}
	res, err := h.svc.Signup(r.Context(), req)
	h.answerLogin(w, r, "sign up", res, err)
}

// --- Phone: password reset ---------------------------------------------------------

type phoneRequest struct {
	Phone    string `json:"phone"`
	Code     string `json:"code"`
	Purpose  string `json:"purpose"`
	DeviceID string `json:"device_id"`
}

func (h *Handler) phoneRequest(w http.ResponseWriter, r *http.Request, purposeNeeded bool) (phoneRequest, bool) {
	var req phoneRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return req, false
	}
	if purposeNeeded && req.Purpose != PurposeReset {
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, `purpose must be "reset"`)
		return req, false
	}
	if len(req.Code) > 20 || len(req.DeviceID) > 100 {
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "code or device_id too long")
		return req, false
	}
	if len(req.Phone) > 50 {
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeInvalidPhone, ErrInvalidPhone.Error())
		return req, false
	}
	return req, true
}

func (h *Handler) phoneStart(w http.ResponseWriter, r *http.Request) {
	req, ok := h.phoneRequest(w, r, true)
	if !ok {
		return
	}
	err := h.svc.StartPhone(r.Context(), req.Phone, req.Purpose, httpx.ClientIP(r, h.trustProxy))
	if h.limited(w, r, err) || h.accountsError(w, r, "send code", err) {
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]int{"resend_in": int(ResendAfter.Seconds()), "expires_in": int(CodeTTL.Seconds())})
}

func (h *Handler) phoneVerify(w http.ResponseWriter, r *http.Request) {
	req, ok := h.phoneRequest(w, r, true)
	if !ok {
		return
	}
	ticket, err := h.svc.VerifyPhone(r.Context(), req.Phone, req.Code)
	switch {
	case h.codeError(w, r, err):
	case h.accountsError(w, r, "verify code", err):
	default:
		httpx.WriteJSON(w, r, http.StatusOK, map[string]string{"ticket": ticket})
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
	h.answerLogin(w, r, "reset password", res, err)
}

// --- Phone: a signed-in person adding theirs ---------------------------------------

func (h *Handler) myPhoneStart(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	req, ok := h.phoneRequest(w, r, false)
	if !ok {
		return
	}
	err := h.svc.StartMyPhone(r.Context(), p, req.Phone, httpx.ClientIP(r, h.trustProxy))
	if h.limited(w, r, err) || h.accountsError(w, r, "send code", err) {
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]int{"resend_in": int(ResendAfter.Seconds()), "expires_in": int(CodeTTL.Seconds())})
}

func (h *Handler) myPhoneVerify(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	req, ok := h.phoneRequest(w, r, false)
	if !ok {
		return
	}
	res, err := h.svc.VerifyMyPhone(r.Context(), p, req.Phone, req.Code, req.DeviceID)
	if h.codeError(w, r, err) {
		return
	}
	h.answerLogin(w, r, "verify phone", res, err)
}

type ticketRequest struct {
	Ticket   string `json:"ticket"`
	DeviceID string `json:"device_id"`
}

func (h *Handler) ticketRequest(w http.ResponseWriter, r *http.Request) (ticketRequest, bool) {
	var req ticketRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return req, false
	}
	if len(req.DeviceID) > 100 {
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "device_id too long")
		return req, false
	}
	return req, true
}

func (h *Handler) myPhoneTelegram(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	req, ok := h.ticketRequest(w, r)
	if !ok {
		return
	}
	res, err := h.svc.MyPhoneTelegram(r.Context(), p, req.Ticket, req.DeviceID)
	h.answerLogin(w, r, "telegram phone", res, err)
}

// --- Google -----------------------------------------------------------------------

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

func (h *Handler) googleTicket(w http.ResponseWriter, r *http.Request) {
	req, ok := h.ticketRequest(w, r)
	if !ok {
		return
	}
	res, err := h.svc.GoogleTicket(r.Context(), req.Ticket, req.DeviceID)
	h.answerLogin(w, r, "google ticket", res, err)
}

// --- Telegram ---------------------------------------------------------------------

type telegramStartRequest struct {
	Purpose string `json:"purpose"`
	Return  string `json:"return"`
}

func (h *Handler) telegramStart(w http.ResponseWriter, r *http.Request) {
	var req telegramStartRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	var pr *Principal
	switch req.Purpose {
	case TelegramSignIn:
	case TelegramVerify:
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
		if _, err := h.svc.phoneless(r.Context(), p); h.accountsError(w, r, "telegram start", err) {
			return
		}
		pr = &p
	default:
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, `purpose must be "signin" or "verify"`)
		return
	}
	u, err := h.svc.TelegramStart(r.Context(), req.Purpose, pr, req.Return)
	if h.accountsError(w, r, "telegram start", err) {
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]string{"url": u})
}

func (h *Handler) telegramCallback(w http.ResponseWriter, r *http.Request) {
	if !h.svc.TelegramEnabled() {
		h.accountsError(w, r, "telegram callback", ErrTelegramOff)
		return
	}
	location, reason := h.svc.TelegramCallback(r.Context(), r.URL.Query())
	if reason != nil {
		httpx.Logger(r.Context()).Warn("telegram sign-in failed", slog.Any("err", reason))
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, location, http.StatusFound) //nolint:gosec // G710: always WEB_ORIGIN's page; the return path is checked by safeReturn
}

func (h *Handler) telegramTicket(w http.ResponseWriter, r *http.Request) {
	req, ok := h.ticketRequest(w, r)
	if !ok {
		return
	}
	res, err := h.svc.TelegramTicket(r.Context(), req.Ticket, req.DeviceID)
	h.answerLogin(w, r, "telegram ticket", res, err)
}

// --- Profile ----------------------------------------------------------------------

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

// --- Own photo --------------------------------------------------------------------

// setPhoto takes the photo as the raw body (image/jpeg, image/png or
// image/webp, at most 512 KB); its bytes decide what it is.
func (h *Handler) setPhoto(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPhotoBytes+1))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) || len(data) > maxPhotoBytes {
		httpx.WriteError(w, r, http.StatusRequestEntityTooLarge, CodePhotoTooLarge, ErrPhotoTooLarge.Error())
		return
	}
	if err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "couldn't read the photo")
		return
	}
	err = h.svc.SetMyPhoto(r.Context(), p, data)
	if h.limited(w, r, err) || h.accountsError(w, r, "set photo", err) {
		return
	}
	h.me(w, r)
}

func (h *Handler) removePhoto(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	if err := h.svc.RemoveMyPhoto(r.Context(), p); h.accountsError(w, r, "remove photo", err) {
		return
	}
	h.me(w, r)
}

// --- Shared -----------------------------------------------------------------------

// answerLogin answers a new session (LoginResponse), or the error.
func (h *Handler) answerLogin(w http.ResponseWriter, r *http.Request, what string, res LoginResult, err error) {
	if h.accountsError(w, r, what, err) {
		return
	}
	if resp, ok := h.loggedIn(w, r, res); ok {
		httpx.WriteJSON(w, r, http.StatusOK, resp)
	}
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

// limited answers 429 for too many codes; false (and nothing written)
// otherwise.
func (h *Handler) limited(w http.ResponseWriter, r *http.Request, err error) bool {
	var limited *RateLimitedError
	if !errors.As(err, &limited) {
		return false
	}
	retryAfter(w, limited.RetryAfter)
	httpx.WriteError(w, r, http.StatusTooManyRequests, httpx.CodeRateLimited, limited.Error())
	return true
}

// codeError answers a wrong, spent or expired code; false (and nothing
// written) for anything else.
func (h *Handler) codeError(w http.ResponseWriter, r *http.Request, err error) bool {
	var wrong *WrongCodeError
	switch {
	case errors.As(err, &wrong) && wrong.TriesLeft == 0:
		retryAfter(w, wrong.RetryAfter)
		httpx.WriteError(w, r, http.StatusTooManyRequests, CodeTooManyTries, wrong.Error())
	case errors.As(err, &wrong):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeInvalidCode, wrong.Error())
	case errors.Is(err, ErrCodeExpired):
		httpx.WriteError(w, r, http.StatusGone, CodeCodeExpired, err.Error())
	default:
		return false
	}
	return true
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
	case errors.Is(err, ErrTelegramOff):
		httpx.WriteError(w, r, http.StatusServiceUnavailable, CodeTelegramUnavailable, err.Error())
	case errors.Is(err, ErrMailOff):
		httpx.WriteError(w, r, http.StatusServiceUnavailable, CodeEmailUnavailable, err.Error())
	case errors.Is(err, ErrMailFailed):
		httpx.Logger(r.Context()).Error(what, slog.Any("err", err))
		httpx.WriteError(w, r, http.StatusServiceUnavailable, CodeEmailUnavailable, ErrMailFailed.Error())
	case errors.Is(err, ErrInvalidPhone):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeInvalidPhone, err.Error())
	case errors.Is(err, ErrInvalidEmail):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeInvalidEmail, err.Error())
	case errors.Is(err, ErrInvalidTicket):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeInvalidTicket, err.Error())
	case errors.Is(err, ErrNoTelegramPhone):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeTelegramNoPhone, err.Error())
	case errors.As(err, &invalid):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeWeakPassword, invalid.Error())
	case errors.As(err, &profile):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeInvalidProfile, profile.Error())
	case errors.Is(err, ErrNoAccount):
		httpx.WriteError(w, r, http.StatusNotFound, CodeNoAccount, err.Error())
	case errors.Is(err, ErrGoogleTaken):
		httpx.WriteError(w, r, http.StatusConflict, CodeGoogleTaken, err.Error())
	case errors.Is(err, ErrEmailTaken):
		httpx.WriteError(w, r, http.StatusConflict, CodeEmailTaken, err.Error())
	case errors.Is(err, ErrPhotoTooLarge):
		httpx.WriteError(w, r, http.StatusRequestEntityTooLarge, CodePhotoTooLarge, err.Error())
	case errors.Is(err, ErrInvalidPhoto):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeInvalidPhoto, err.Error())
	case errors.Is(err, ErrPhoneSet):
		httpx.WriteError(w, r, http.StatusConflict, CodePhoneAlreadySet, err.Error())
	default:
		h.internal(w, r, what, err)
	}
	return true
}

func retryAfter(w http.ResponseWriter, d time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(max(int(math.Ceil(d.Seconds())), 1)))
}
