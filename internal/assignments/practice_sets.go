package assignments

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/nazacd/makonbook-api/internal/auth"
	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/httpx"
	"github.com/nazacd/makonbook-api/internal/practice"
	"github.com/nazacd/makonbook-api/internal/roadmap"
)

// Students' practice (spec 22.8): the bank's topics and questions to browse,
// and practice sets built from them, run and graded as assessments.

// Error codes of practice sets.
const (
	CodeInvalidPractice = "invalid_practice"
	CodePracticeOpen    = "practice_open"
	CodePracticeLimit   = "practice_limit"
)

func (h *Handler) registerPractice(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/me/practice/topics", h.requireContent(h.student(h.practiceTopics)))
	mux.HandleFunc("GET /v1/me/practice/questions", h.requireContent(h.student(h.practiceQuestions)))
	mux.HandleFunc("GET /v1/me/practice/sets", h.requireContent(h.student(h.practiceSets)))
	mux.HandleFunc("POST /v1/me/practice/sets", h.requireContent(h.student(h.startPractice)))
	mux.HandleFunc("DELETE /v1/me/practice/sets/{id}", h.requireContent(h.student(h.discardPractice)))
}

// student lets only students through.
func (h *Handler) student(next func(http.ResponseWriter, *http.Request, auth.Principal)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, _ := auth.PrincipalFrom(r.Context())
		if p.Kind != people.KindStudent {
			httpx.WriteError(w, r, http.StatusForbidden, httpx.CodeForbidden, "only students practise")
			return
		}
		next(w, r, p)
	}
}

func (h *Handler) practiceTopics(w http.ResponseWriter, r *http.Request, _ auth.Principal) {
	h.taxonomy(w, r, 0)
}

// practiceQuestions is the teachers' question search for students, each
// question marked `done` once they submitted it in any assessment.
func (h *Handler) practiceQuestions(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	v := r.URL.Query()
	f := practice.QuestionFilter{Subject: v.Get("subject"), Skill: v.Get("skill"), Difficulty: v.Get("difficulty"), Text: v.Get("q"), Limit: 30}
	bad := func(msg string) { httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, msg) }
	switch {
	case !slices.Contains(roadmap.Subjects, f.Subject):
		bad("subject must be math or english")
		return
	case f.Difficulty != "" && !slices.Contains(roadmap.Difficulties, f.Difficulty):
		bad("difficulty must be easy, medium or hard")
		return
	case utf8.RuneCountInString(f.Text) > 100:
		bad("q is at most 100 characters")
		return
	}
	for name, dst := range map[string]*int32{"limit": &f.Limit, "offset": &f.Offset} {
		if s := v.Get(name); s != "" {
			n, err := strconv.ParseInt(s, 10, 32)
			if err != nil || n < 0 || (name == "limit" && (n < 1 || n > 100)) {
				bad(name + " must be a number in range (limit 1 to 100)")
				return
			}
			*dst = int32(n)
		}
	}
	list, total, err := h.svc.Questions(r.Context(), f)
	if err != nil {
		h.fail(w, r, "practice questions", err)
		return
	}
	done, err := h.svc.roadmap.FinishedQuestions(r.Context(), p.ID)
	if err != nil {
		h.fail(w, r, "finished questions", err)
		return
	}
	ids := []string{}
	for _, q := range list {
		if done[q.ID] {
			ids = append(ids, q.ID)
		}
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"questions": list, "total": total, "done": ids})
}

func (h *Handler) practiceSets(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	sets, err := h.svc.roadmap.PracticeSets(r.Context(), p.ID, 50)
	if err != nil {
		h.fail(w, r, "practice sets", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"sets": sets, "free_questions_a_day": freeLimit(p)})
}

func freeLimit(p auth.Principal) *int {
	if !p.Registered {
		return nil
	}
	n := roadmap.FreeQuestionsADay
	return &n
}

func (h *Handler) startPractice(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	var in roadmap.PracticeRequest
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	local := time.Now().In(h.location)
	dayStart := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, h.location)
	a, err := h.svc.roadmap.StartPractice(r.Context(), p.ID, p.Registered, in, dayStart)
	var bad *roadmap.PracticeError
	switch {
	case errors.Is(err, roadmap.ErrPracticeOpen):
		// The open set comes with the error, to continue or discard.
		body, merr := json.Marshal(map[string]any{"error": map[string]any{"code": CodePracticeOpen, "message": err.Error()}, "open": a})
		if merr != nil {
			h.fail(w, r, "start practice", merr)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write(body)
	case errors.Is(err, roadmap.ErrPracticeLimit):
		httpx.WriteError(w, r, http.StatusForbidden, CodePracticeLimit, err.Error())
	case errors.As(err, &bad):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeInvalidPractice, bad.Reason)
	case err != nil:
		h.fail(w, r, "start practice", err)
	default:
		httpx.WriteJSON(w, r, http.StatusCreated, a)
	}
}

func (h *Handler) discardPractice(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	if err := h.svc.roadmap.DiscardPractice(r.Context(), p.ID, r.PathValue("id")); err != nil {
		h.fail(w, r, "discard practice", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
