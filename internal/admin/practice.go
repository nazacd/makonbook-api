package admin

import (
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/nazacd/makonbook-api/internal/platform/httpx"
	"github.com/nazacd/makonbook-api/internal/platform/r2"
	"github.com/nazacd/makonbook-api/internal/practice"
)

// Error codes of the practice editor (see api/openapi.yaml).
const (
	CodeConflict        = "version_conflict"
	CodeInvalidQuestion = "invalid_question"
)

// Figures reads question images (SAT MAKON's R2 bucket, read only).
type Figures interface {
	Get(ctx context.Context, key string) (r2.Object, error)
}

// figurePrefixes: the bank stores keys without the "media/" its bucket uses;
// a few objects sit at the bare key.
var figurePrefixes = []string{"media/", ""}

var figureKey = regexp.MustCompile(`^[\p{L}\p{N}._/ ()\-]+$`)

func (h *Handler) registerPractice(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/admin/practice/tests", h.RequireEditor(h.practiceTests))
	mux.HandleFunc("GET /v1/admin/practice/tests/{code}", h.RequireEditor(h.practiceTest))
	mux.HandleFunc("GET /v1/admin/practice/questions", h.RequireEditor(h.practiceQuestions))
	mux.HandleFunc("GET /v1/admin/practice/questions/{id}", h.RequireEditor(h.practiceQuestion))
	mux.HandleFunc("PUT /v1/admin/practice/questions/{id}", h.RequireEditor(h.practiceUpdate))
	mux.HandleFunc("GET /v1/admin/practice/taxonomy", h.RequireEditor(h.practiceTaxonomy))
	mux.HandleFunc("GET /v1/admin/practice/figures", h.RequireEditor(h.practiceFigure))
	mux.HandleFunc("POST /v1/admin/practice/images", h.RequireEditor(h.practiceUpload))
}

// practiceUpload stores an image an editor uploads (multipart field "file").
func (h *Handler) practiceUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, practice.MaxImageBytes+64<<10)
	file, _, err := r.FormFile("file")
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			httpx.WriteError(w, r, http.StatusRequestEntityTooLarge, httpx.CodeBadRequest, practice.ErrBadImage.Error())
			return
		}
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "send the image as the form field \"file\"")
		return
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, practice.MaxImageBytes+1))
	if err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "couldn't read the image")
		return
	}
	p, _ := PrincipalFrom(r.Context())
	img, err := h.panel.Practice.UploadImage(r.Context(), data, p.Name)
	switch {
	case errors.Is(err, practice.ErrBadImage):
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	case errors.Is(err, practice.ErrUploadsOff):
		httpx.WriteError(w, r, http.StatusServiceUnavailable, httpx.CodeUnavailable, err.Error())
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("upload image", "err", err)
		httpx.WriteError(w, r, http.StatusBadGateway, httpx.CodeUnavailable, "couldn't store the image; try again")
		return
	}
	httpx.WriteJSON(w, r, http.StatusCreated, img)
}

type practiceTestsResponse struct {
	Tests []practice.TestSummary `json:"tests"`
}

func (h *Handler) practiceTests(w http.ResponseWriter, r *http.Request) {
	tests, err := h.panel.Practice.Tests(r.Context())
	if err != nil {
		h.internal(w, r, "practice tests", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, practiceTestsResponse{Tests: tests})
}

func (h *Handler) practiceTest(w http.ResponseWriter, r *http.Request) {
	t, err := h.panel.Practice.Test(r.Context(), r.PathValue("code"))
	if errors.Is(err, practice.ErrNotFound) {
		httpx.WriteError(w, r, http.StatusNotFound, httpx.CodeNotFound, "no such test")
		return
	}
	if err != nil {
		h.internal(w, r, "practice test", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, t)
}

type practiceQuestionsResponse struct {
	Questions []practice.QuestionSummary `json:"questions"`
	Total     int32                      `json:"total"`
}

func (h *Handler) practiceQuestions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := practice.Filter{Risk: q.Get("risk"), Status: q.Get("status"), Subject: q.Get("subject"), Limit: 50}
	if !oneOf(f.Risk, "", "red", "yellow", "green") || !oneOf(f.Status, "", "draft", "review", "approved") ||
		!oneOf(f.Subject, "", "math", "english") {
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "risk, status or subject has an unknown value")
		return
	}
	for name, dst := range map[string]*int32{"limit": &f.Limit, "offset": &f.Offset} {
		if v := q.Get(name); v != "" {
			n, err := strconv.ParseInt(v, 10, 32)
			if err != nil || n < 0 || (name == "limit" && (n < 1 || n > 200)) {
				httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, name+" is out of range")
				return
			}
			*dst = int32(n)
		}
	}
	list, total, err := h.panel.Practice.Questions(r.Context(), f)
	if err != nil {
		h.internal(w, r, "practice questions", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, practiceQuestionsResponse{Questions: list, Total: total})
}

func (h *Handler) practiceQuestion(w http.ResponseWriter, r *http.Request) {
	q, err := h.panel.Practice.Question(r.Context(), r.PathValue("id"))
	h.respondQuestion(w, r, q, err)
}

func (h *Handler) practiceUpdate(w http.ResponseWriter, r *http.Request) {
	var e practice.Edit
	if !httpx.DecodeJSON(w, r, &e) {
		return
	}
	p, _ := PrincipalFrom(r.Context())
	q, err := h.panel.Practice.Update(r.Context(), r.PathValue("id"), p.Name, e)
	h.respondQuestion(w, r, q, err)
}

func (h *Handler) respondQuestion(w http.ResponseWriter, r *http.Request, q practice.Question, err error) {
	var invalid *practice.InvalidError
	switch {
	case err == nil:
		httpx.WriteJSON(w, r, http.StatusOK, q)
	case errors.Is(err, practice.ErrNotFound):
		httpx.WriteError(w, r, http.StatusNotFound, httpx.CodeNotFound, "no such question")
	case errors.Is(err, practice.ErrConflict):
		httpx.WriteError(w, r, http.StatusConflict, CodeConflict, "someone saved this question after you opened it; reload to see their change")
	case errors.As(err, &invalid):
		httpx.WriteFieldErrors(w, r, http.StatusUnprocessableEntity, CodeInvalidQuestion, "fix the marked fields", invalid.Fields)
	default:
		h.internal(w, r, "practice question", err)
	}
}

type taxonomyResponse struct {
	Subjects map[string]map[string][]string `json:"subjects"` // subject → domain → skills
}

func (h *Handler) practiceTaxonomy(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, r, http.StatusOK, taxonomyResponse{Subjects: practice.Taxonomy})
}

// practiceFigure serves a question image from R2 to the editor's page.
func (h *Handler) practiceFigure(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if len(key) > 300 || !figureKey.MatchString(key) || strings.Contains(key, "..") || strings.HasPrefix(key, "/") {
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "not an image key")
		return
	}
	if strings.HasPrefix(key, practice.UploadPrefix) {
		obj, err := h.panel.Practice.GetImage(r.Context(), key)
		switch {
		case errors.Is(err, practice.ErrNotFound):
			httpx.WriteError(w, r, http.StatusNotFound, httpx.CodeNotFound, "no such image")
		case errors.Is(err, practice.ErrUploadsOff):
			httpx.WriteError(w, r, http.StatusServiceUnavailable, httpx.CodeUnavailable, err.Error())
		case err != nil:
			httpx.Logger(r.Context()).Warn("uploaded image", "key", key, "err", err)
			httpx.WriteError(w, r, http.StatusBadGateway, httpx.CodeUnavailable, "the image store didn't answer")
		default:
			writeImage(w, obj.ContentType, obj.Body)
		}
		return
	}
	if h.panel.Figures == nil {
		httpx.WriteError(w, r, http.StatusServiceUnavailable, httpx.CodeUnavailable, "images are not configured on the server")
		return
	}
	for _, prefix := range figurePrefixes {
		obj, err := h.panel.Figures.Get(r.Context(), prefix+key)
		if errors.Is(err, r2.ErrNotFound) {
			continue
		}
		if err != nil {
			httpx.Logger(r.Context()).Warn("figure", "key", key, "err", err)
			httpx.WriteError(w, r, http.StatusBadGateway, httpx.CodeUnavailable, "the image store didn't answer")
			return
		}
		writeImage(w, obj.ContentType, obj.Body)
		return
	}
	httpx.WriteError(w, r, http.StatusNotFound, httpx.CodeNotFound, "no such image")
}

func oneOf(s string, options ...string) bool {
	for _, o := range options {
		if s == o {
			return true
		}
	}
	return false
}

func writeImage(w http.ResponseWriter, contentType string, data []byte) {
	if !strings.HasPrefix(contentType, "image/") || strings.Contains(contentType, "svg") {
		contentType = "application/octet-stream" // never let a stored file run as a page
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	_, _ = w.Write(data) //nolint:gosec // image bytes: image/* only (never SVG), nosniff, and a CSP that runs nothing
}
