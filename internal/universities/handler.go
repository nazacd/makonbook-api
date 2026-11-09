package universities

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/nazacd/makonbook-api/internal/platform/httpx"
)

// Handler is the apps' universities API (spec 24.3).
type Handler struct {
	svc     *Service
	require func(http.HandlerFunc) http.HandlerFunc
}

// NewHandler: require is auth's Require (anyone signed in).
func NewHandler(svc *Service, require func(http.HandlerFunc) http.HandlerFunc) *Handler {
	return &Handler{svc: svc, require: require}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/universities", h.require(h.list))
	mux.HandleFunc("GET /v1/universities/{id}", h.require(h.get))
	mux.HandleFunc("GET /v1/universities/{id}/logo", h.logo)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	l, err := h.svc.List(r.Context())
	if err != nil {
		h.fail(w, r, "list", err)
		return
	}
	etag := `"` + l.Version + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, no-cache")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, l)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	d, err := h.svc.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, "get", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, d)
}

// logoCSP keeps an SVG logo from running anything, even opened on its own.
const logoCSP = "default-src 'none'; style-src 'unsafe-inline'; img-src data:; sandbox"

// logo serves a university's logo (no sign-in: the web shows it in <img>).
// Its URL carries the content's hash, so it can be kept for good.
func (h *Handler) logo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", logoCSP)
	l, err := h.svc.Logo(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, "logo", err)
		return
	}
	etag := `"` + l.SHA256 + `"`
	w.Header().Set("ETag", etag)
	if v := r.URL.Query().Get("v"); v != "" && len(l.SHA256) >= len(v) && l.SHA256[:len(v)] == v {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=86400")
	}
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", l.ContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(l.Body)))
	_, _ = w.Write(l.Body)
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, op string, err error) {
	if errors.Is(err, ErrNotFound) {
		httpx.WriteError(w, r, http.StatusNotFound, httpx.CodeNotFound, "no such university")
		return
	}
	slog.ErrorContext(r.Context(), "universities: "+op, "err", err)
	httpx.WriteError(w, r, http.StatusInternalServerError, httpx.CodeInternal, "something went wrong")
}
