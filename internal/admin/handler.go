package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nazacd/makonbook-api/internal/platform/httpx"
	"github.com/nazacd/makonbook-api/internal/platform/ratelimit"
)

// CookieName holds the admin session. HttpOnly (the page's scripts can't read
// it), SameSite=Strict, Secure outside development.
const CookieName = "mb_admin"

// CSRFHeader must accompany every admin request that changes something. A
// page on another origin can't add a custom header without a CORS preflight,
// which this API never grants — so the cookie alone is never enough.
const CSRFHeader = "X-MakonBook-Admin"

// Error codes specific to the admin panel (see api/openapi.yaml).
//
//nolint:gosec // G101: these are error codes, not credentials
const (
	CodeInvalidCredentials = "invalid_credentials"
	CodeNotAdmin           = "not_admin"
)

// Sign-in limits: per client IP, and per login name — the panel must not
// become a way to guess HolliHop passwords.
const (
	ipLimit    = 20
	loginLimit = 5
	window     = 15 * time.Minute
)

type Handler struct {
	svc        *Service
	panel      Panel
	trustProxy bool
	secure     bool
	byIP       *ratelimit.Limiter
	byLogin    *ratelimit.Limiter
}

// NewHandler: secureCookie should be true everywhere but local development
// over plain HTTP.
func NewHandler(svc *Service, panel Panel, trustProxy, secureCookie bool) *Handler {
	return &Handler{
		svc:        svc,
		panel:      panel,
		trustProxy: trustProxy,
		secure:     secureCookie,
		byIP:       ratelimit.New(ipLimit, window),
		byLogin:    ratelimit.New(loginLimit, window),
	}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/admin/login", h.login)
	mux.HandleFunc("POST /v1/admin/logout", h.Require(h.logout))
	mux.HandleFunc("GET /v1/admin/me", h.Require(h.me))
	h.registerPanel(mux)
}

type ctxKey struct{}

// PrincipalFrom returns the signed-in admin of a request that passed Require.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// Require lets signed-in admins through; changes also need the CSRF header.
func (h *Handler) Require(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get(CSRFHeader) == "" {
			httpx.WriteError(w, r, http.StatusForbidden, httpx.CodeForbidden, "missing "+CSRFHeader+" header")
			return
		}
		c, _ := r.Cookie(CookieName)
		token := ""
		if c != nil {
			token = c.Value
		}
		p, err := h.svc.Authenticate(r.Context(), token)
		if errors.Is(err, ErrUnauthorized) {
			httpx.WriteError(w, r, http.StatusUnauthorized, httpx.CodeUnauthorized, "sign in again")
			return
		}
		if err != nil {
			h.internal(w, r, "authenticate admin", err)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)))
	}
}

// RequireEditor is Require for those who may edit practice content.
func (h *Handler) RequireEditor(next http.HandlerFunc) http.HandlerFunc {
	return h.Require(func(w http.ResponseWriter, r *http.Request) {
		if p, _ := PrincipalFrom(r.Context()); !p.CanEdit() {
			httpx.WriteError(w, r, http.StatusForbidden, httpx.CodeForbidden, "only editors can do this")
			return
		}
		next(w, r)
	})
}

// RequireSuper is Require for super admins only.
func (h *Handler) RequireSuper(next http.HandlerFunc) http.HandlerFunc {
	return h.Require(func(w http.ResponseWriter, r *http.Request) {
		if p, _ := PrincipalFrom(r.Context()); !p.Super {
			httpx.WriteError(w, r, http.StatusForbidden, httpx.CodeForbidden, "only super admins can do this")
			return
		}
		next(w, r)
	})
}

type loginRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

// Profile is what the panel shows about the signed-in admin.
type Profile struct {
	Name       string   `json:"name"`
	MemberType string   `json:"member_type"`
	MemberID   int32    `json:"member_id"`
	Super      bool     `json:"super"`
	Editor     bool     `json:"editor"`  // may edit practice tests (true for super admins)
	Offices    []Office `json:"offices"` // the branches they can see (all, for super admins)
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(CSRFHeader) == "" {
		httpx.WriteError(w, r, http.StatusForbidden, httpx.CodeForbidden, "missing "+CSRFHeader+" header")
		return
	}
	var req loginRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if req.Login == "" || req.Password == "" || len(req.Login) > 100 || len(req.Password) > 256 {
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "login and password are required")
		return
	}
	now := time.Now()
	if !h.byIP.Allow(httpx.ClientIP(r, h.trustProxy), now) || !h.byLogin.Allow(strings.ToLower(strings.TrimSpace(req.Login)), now) {
		w.Header().Set("Retry-After", strconv.Itoa(int(window.Seconds())))
		httpx.WriteError(w, r, http.StatusTooManyRequests, httpx.CodeRateLimited, "too many sign-in attempts; try again in 15 minutes")
		return
	}

	token, p, err := h.svc.Login(r.Context(), req.Login, req.Password)
	switch {
	case errors.Is(err, ErrBadCredentials):
		httpx.WriteError(w, r, http.StatusUnauthorized, CodeInvalidCredentials, ErrBadCredentials.Error())
		return
	case errors.Is(err, ErrNotAdmin):
		httpx.WriteError(w, r, http.StatusForbidden, CodeNotAdmin, ErrNotAdmin.Error()+"; ask a super admin")
		return
	case errors.Is(err, ErrUnavailable):
		httpx.Logger(r.Context()).Warn("admin sign-in: HolliHop unavailable", slog.Any("err", err))
		httpx.WriteError(w, r, http.StatusServiceUnavailable, httpx.CodeUnavailable, ErrUnavailable.Error())
		return
	case err != nil:
		h.internal(w, r, "admin sign-in", err)
		return
	}
	httpx.Logger(r.Context()).Info("admin signed in", slog.String("member_type", p.MemberType), slog.Int("member_id", int(p.MemberID)))
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: Secure is off only for local development over plain HTTP
		Name: CookieName, Value: token, Path: "/", MaxAge: int(SessionTTL.Seconds()),
		HttpOnly: true, Secure: h.secure, SameSite: http.SameSiteStrictMode,
	})
	h.writeProfile(w, r, p)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieName); err == nil {
		if err := h.svc.Logout(r.Context(), c.Value); err != nil {
			h.internal(w, r, "admin sign-out", err)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: as above
		Name: CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: h.secure, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	h.writeProfile(w, r, p)
}

func (h *Handler) writeProfile(w http.ResponseWriter, r *http.Request, p Principal) {
	all, err := h.svc.Offices(r.Context())
	if err != nil {
		h.internal(w, r, "offices", err)
		return
	}
	offices := []Office{}
	for _, o := range all {
		if p.CanSeeOffice(o.ID) {
			offices = append(offices, o)
		}
	}
	httpx.WriteJSON(w, r, http.StatusOK, Profile{Name: p.Name, MemberType: p.MemberType, MemberID: p.MemberID, Super: p.Super, Editor: p.CanEdit(), Offices: offices})
}

func (h *Handler) internal(w http.ResponseWriter, r *http.Request, what string, err error) {
	httpx.Logger(r.Context()).Error(what, slog.Any("err", err))
	httpx.WriteError(w, r, http.StatusInternalServerError, httpx.CodeInternal, "internal error")
}
