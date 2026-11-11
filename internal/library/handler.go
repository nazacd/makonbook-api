package library

import (
	"bytes"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"strconv"

	"github.com/nazacd/makonbook-api/internal/auth"
	"github.com/nazacd/makonbook-api/internal/platform/httpx"
)

// CodeInvalidPage is a page outside the book (see api/openapi.yaml).
const CodeInvalidPage = "invalid_page"

// Handler is the apps' library API (spec 25.3).
type Handler struct {
	svc *Service
	// requireContent lets in anyone signed in except registered students
	// without a phone (403 phone_required).
	requireContent func(http.HandlerFunc) http.HandlerFunc
}

// NewHandler: requireContent is auth's RequireContent.
func NewHandler(svc *Service, requireContent func(http.HandlerFunc) http.HandlerFunc) *Handler {
	return &Handler{svc: svc, requireContent: requireContent}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/library", h.requireContent(h.list))
	mux.HandleFunc("GET /v1/library/{id}/file", h.requireContent(h.file))
	mux.HandleFunc("PUT /v1/library/{id}/progress", h.requireContent(h.progress))
	mux.HandleFunc("GET /v1/library/{id}/cover", h.cover)
}

func who(r *http.Request) Who {
	p, _ := auth.PrincipalFrom(r.Context())
	return Who{Kind: p.Kind, ID: p.ID}
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	books, err := h.svc.Books(r.Context(), who(r))
	if err != nil {
		h.fail(w, r, "list", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"books": books})
}

// file serves a book's PDF: inline for the reader, or as a download with
// ?download=1. Ranges and If-None-Match work (http.ServeContent).
func (h *Handler) file(w http.ResponseWriter, r *http.Request) {
	f, err := h.svc.PDF(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, "file", err)
		return
	}
	disposition := "inline"
	if r.URL.Query().Get("download") != "" {
		disposition = "attachment"
	}
	w.Header().Set("Content-Type", f.ContentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": f.Title + ".pdf"}))
	w.Header().Set("ETag", `"`+f.SHA256+`"`)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "", f.ModTime, bytes.NewReader(f.Body))
}

// cover serves a book's cover (no sign-in: the web shows it in <img>). Its
// URL carries the content's hash, so it can be kept for good.
func (h *Handler) cover(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	f, err := h.svc.Cover(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, "cover", err)
		return
	}
	etag := `"` + f.SHA256 + `"`
	w.Header().Set("ETag", etag)
	if v := r.URL.Query().Get("v"); v != "" && len(f.SHA256) >= len(v) && f.SHA256[:len(v)] == v {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=86400")
	}
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", f.ContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(f.Body)))
	_, _ = w.Write(f.Body)
}

func (h *Handler) progress(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Page int `json:"page"`
	}
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	p, err := h.svc.SetProgress(r.Context(), who(r), r.PathValue("id"), in.Page)
	if err != nil {
		h.fail(w, r, "progress", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, p)
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, op string, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.WriteError(w, r, http.StatusNotFound, httpx.CodeNotFound, "no such book")
	case errors.Is(err, ErrBadPage):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeInvalidPage, "the book has no such page")
	default:
		slog.ErrorContext(r.Context(), "library: "+op, "err", err)
		httpx.WriteError(w, r, http.StatusInternalServerError, httpx.CodeInternal, "something went wrong")
	}
}
