package roadmap

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/nazacd/makonbook-api/internal/auth"
	"github.com/nazacd/makonbook-api/internal/platform/httpx"
)

// Error codes specific to roadmaps (see api/openapi.yaml).
const (
	CodeNotNeeded         = "not_needed"
	CodeInvalidOnboarding = "invalid_onboarding"
	CodeLessonLocked      = "lesson_locked"
	CodeEnrolledOnly      = "enrolled_only"
	CodeNoQuestions       = "no_questions"
	CodeFinished          = "finished"
	CodeInvalidAnswers    = "invalid_answers"
	CodeInvalidAssessment = "invalid_assessment"
)

// ProfileFunc builds the person's profile (auth's Service.Profile).
type ProfileFunc func(ctx context.Context, p auth.Principal, today time.Time) (auth.Profile, error)

// Handler is the apps' roadmap API.
type Handler struct {
	svc      *Service
	location *time.Location
	profile  ProfileFunc
	// require lets in anyone signed in; requireContent also keeps out
	// registered students without a phone (403 phone_required).
	require, requireContent func(http.HandlerFunc) http.HandlerFunc
}

// NewHandler: require is auth's Require, requireContent its RequireContent.
func NewHandler(svc *Service, location *time.Location, profile ProfileFunc, require, requireContent func(http.HandlerFunc) http.HandlerFunc) *Handler {
	return &Handler{svc: svc, location: location, profile: profile, require: require, requireContent: requireContent}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/me/onboarding", h.require(h.getOnboarding))
	mux.HandleFunc("POST /v1/me/onboarding", h.require(h.postOnboarding))
	mux.HandleFunc("GET /v1/me/roadmaps", h.requireContent(h.list))
	mux.HandleFunc("GET /v1/me/roadmaps/{id}", h.requireContent(h.detail))
	mux.HandleFunc("GET /v1/me/roadmaps/{id}/lessons/{lesson}", h.requireContent(h.lesson))
	mux.HandleFunc("POST /v1/me/roadmaps/{id}/lessons/{lesson}/assessments", h.requireContent(h.start))
	mux.HandleFunc("GET /v1/me/assessments/{id}", h.requireContent(h.assessment))
	mux.HandleFunc("PUT /v1/me/assessments/{id}/answers", h.requireContent(h.saveAnswers))
	mux.HandleFunc("POST /v1/me/assessments/{id}/submit", h.requireContent(h.submit))
	mux.HandleFunc("GET /v1/logos/{slug}", h.logo)
}

// logoCSP keeps an SVG logo from running anything, even opened on its own.
const logoCSP = "default-src 'none'; style-src 'unsafe-inline'; img-src data:; sandbox"

// logo serves a university's logo (no sign-in: the web shows it in <img>).
func (h *Handler) logo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", logoCSP)
	l, err := h.svc.Logo(r.Context(), r.PathValue("slug"))
	if err != nil {
		h.fail(w, r, "logo", err)
		return
	}
	etag := `"` + l.SHA256 + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", l.ContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(l.Body)))
	_, _ = w.Write(l.Body)
}

func (h *Handler) who(r *http.Request) (Who, auth.Principal) {
	p, _ := auth.PrincipalFrom(r.Context())
	return Who{Kind: p.Kind, ID: p.ID, Registered: p.Registered}, p
}

// today is the school's date, as midnight UTC.
func (h *Handler) today() time.Time {
	y, m, d := time.Now().In(h.location).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func (h *Handler) getOnboarding(w http.ResponseWriter, r *http.Request) {
	who, _ := h.who(r)
	o, err := h.svc.Onboarding(r.Context(), who)
	if err != nil {
		h.fail(w, r, "onboarding", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, o)
}

func (h *Handler) postOnboarding(w http.ResponseWriter, r *http.Request) {
	who, p := h.who(r)
	var raw json.RawMessage
	if !httpx.DecodeJSON(w, r, &raw) {
		return
	}
	var o Onboarding
	if err := json.Unmarshal(raw, &o); err != nil {
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeInvalidOnboarding, "the answers don't have the expected fields and types")
		return
	}
	today := h.today()
	if err := h.svc.SaveOnboarding(r.Context(), who, o, today); err != nil {
		h.fail(w, r, "save onboarding", err)
		return
	}
	prof, err := h.profile(r.Context(), p, today)
	if err != nil {
		h.fail(w, r, "profile", err)
		return
	}
	maps, err := h.svc.Roadmaps(r.Context(), who, today)
	if err != nil {
		h.fail(w, r, "roadmaps", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"profile": prof, "roadmaps": maps})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	who, _ := h.who(r)
	maps, err := h.svc.Roadmaps(r.Context(), who, h.today())
	if err != nil {
		h.fail(w, r, "roadmaps", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"roadmaps": maps})
}

func (h *Handler) detail(w http.ResponseWriter, r *http.Request) {
	who, _ := h.who(r)
	d, err := h.svc.Roadmap(r.Context(), who, r.PathValue("id"), h.today())
	if err != nil {
		h.fail(w, r, "roadmap", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, d)
}

func (h *Handler) lesson(w http.ResponseWriter, r *http.Request) {
	who, _ := h.who(r)
	d, err := h.svc.Lesson(r.Context(), who, r.PathValue("id"), r.PathValue("lesson"), h.today())
	if err != nil {
		h.fail(w, r, "lesson", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, d)
}

func (h *Handler) start(w http.ResponseWriter, r *http.Request) {
	who, _ := h.who(r)
	var in struct {
		Kind  string `json:"kind"`
		Index *int   `json:"index"`
	}
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	index := 0
	switch {
	case in.Kind == kindLevelCheck:
	case in.Kind == kindHomework && in.Index != nil && *in.Index >= 0:
		index = *in.Index
	default:
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeInvalidAssessment,
			`kind must be "level_check", or "homework" with the set's index`)
		return
	}
	a, err := h.svc.Start(r.Context(), who, r.PathValue("id"), r.PathValue("lesson"), in.Kind, index, h.today())
	if err != nil {
		h.fail(w, r, "start assessment", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, a)
}

func (h *Handler) assessment(w http.ResponseWriter, r *http.Request) {
	who, _ := h.who(r)
	a, err := h.svc.Assessment(r.Context(), who, r.PathValue("id"))
	if err != nil {
		h.fail(w, r, "assessment", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, a)
}

type answersBody struct {
	Answers map[string]*string `json:"answers"`
}

func (h *Handler) saveAnswers(w http.ResponseWriter, r *http.Request) {
	who, _ := h.who(r)
	var in answersBody
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	if err := h.svc.SaveAnswers(r.Context(), who, r.PathValue("id"), in.Answers); err != nil {
		h.fail(w, r, "save answers", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) submit(w http.ResponseWriter, r *http.Request) {
	who, _ := h.who(r)
	var in answersBody
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	a, err := h.svc.Submit(r.Context(), who, r.PathValue("id"), in.Answers)
	if err != nil {
		h.fail(w, r, "submit assessment", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, a)
}

// fail answers an error from the service.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	var onboarding *OnboardingError
	var answers *AnswersError
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.WriteError(w, r, http.StatusNotFound, httpx.CodeNotFound, "not found")
	case errors.Is(err, ErrLocked):
		httpx.WriteError(w, r, http.StatusForbidden, CodeLessonLocked, err.Error())
	case errors.Is(err, ErrEnrolledOnly):
		httpx.WriteError(w, r, http.StatusForbidden, CodeEnrolledOnly, err.Error())
	case errors.Is(err, ErrNoQuestions):
		httpx.WriteError(w, r, http.StatusConflict, CodeNoQuestions, err.Error())
	case errors.Is(err, ErrFinished):
		httpx.WriteError(w, r, http.StatusConflict, CodeFinished, err.Error())
	case errors.Is(err, ErrNotNeeded):
		httpx.WriteError(w, r, http.StatusConflict, CodeNotNeeded, err.Error())
	case errors.As(err, &onboarding):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeInvalidOnboarding, onboarding.Reason)
	case errors.As(err, &answers):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeInvalidAnswers, answers.Reason)
	default:
		slog.ErrorContext(r.Context(), "roadmap: "+what, "err", err)
		httpx.WriteError(w, r, http.StatusInternalServerError, httpx.CodeInternal, "something went wrong")
	}
}
