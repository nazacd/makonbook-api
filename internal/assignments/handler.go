package assignments

import (
	"errors"
	"log/slog"
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

// Error codes specific to assignments (see api/openapi.yaml).
const (
	CodeInvalidAssignment = "invalid_assignment"
	CodeAssignmentClosed  = "assignment_closed"
	CodeNotOpen           = "not_open"
	CodeNotAnAssessment   = "not_an_assessment"
	CodeHasWork           = "has_work"
	CodeNoQuestions       = roadmap.CodeNoQuestions
)

// Handler is the apps' assignments and notifications API.
type Handler struct {
	svc      *Service
	location *time.Location
	// require lets in anyone signed in; requireContent also keeps out
	// registered students without a phone (403 phone_required).
	require, requireContent func(http.HandlerFunc) http.HandlerFunc
}

// NewHandler: require is auth's Require, requireContent its RequireContent.
func NewHandler(svc *Service, location *time.Location, require, requireContent func(http.HandlerFunc) http.HandlerFunc) *Handler {
	return &Handler{svc: svc, location: location, require: require, requireContent: requireContent}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/me/assignments", h.requireContent(h.mine))
	mux.HandleFunc("POST /v1/me/assignments/{id}/start", h.requireContent(h.start))
	mux.HandleFunc("GET /v1/me/notifications", h.require(h.notifications))
	mux.HandleFunc("PUT /v1/me/notifications/read", h.require(h.markRead))
	mux.HandleFunc("GET /v1/teacher/assignments", h.require(h.teacher(h.list)))
	mux.HandleFunc("POST /v1/teacher/assignments", h.require(h.teacher(h.create)))
	mux.HandleFunc("POST /v1/teacher/assignments/draw", h.require(h.teacher(h.draw)))
	mux.HandleFunc("GET /v1/teacher/assignments/{id}", h.require(h.teacher(h.get)))
	mux.HandleFunc("PUT /v1/teacher/assignments/{id}", h.require(h.teacher(h.update)))
	mux.HandleFunc("DELETE /v1/teacher/assignments/{id}", h.require(h.teacher(h.remove)))
	mux.HandleFunc("GET /v1/teacher/assignments/{id}/results", h.require(h.teacher(h.results)))
	mux.HandleFunc("GET /v1/teacher/assignments/{id}/results/{client_id}", h.require(h.teacher(h.studentWork)))
	mux.HandleFunc("GET /v1/teacher/questions", h.require(h.teacher(h.questions)))
	mux.HandleFunc("GET /v1/teacher/taxonomy", h.require(h.teacher(h.taxonomy)))
	mux.HandleFunc("GET /v1/teacher/curriculum", h.require(h.teacher(h.curriculum)))
	mux.HandleFunc("GET /v1/teacher/groups/{id}/roadmap", h.require(h.teacher(h.groupRoadmap)))
}

// today is the school's date, as midnight UTC.
func (h *Handler) today() time.Time {
	y, m, d := time.Now().In(h.location).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// teacher lets only teachers through.
func (h *Handler) teacher(next func(http.ResponseWriter, *http.Request, int32)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, _ := auth.PrincipalFrom(r.Context())
		if p.Kind != people.KindTeacher {
			httpx.WriteError(w, r, http.StatusForbidden, httpx.CodeForbidden, "only teachers give assignments")
			return
		}
		next(w, r, p.ID)
	}
}

func (h *Handler) mine(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	list := []StudentAssignment{}
	if p.Kind == people.KindStudent && !p.Registered {
		var err error
		if list, err = h.svc.Mine(r.Context(), p.ID, h.today()); err != nil {
			h.fail(w, r, "my assignments", err)
			return
		}
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"assignments": list})
}

func (h *Handler) start(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	if p.Kind != people.KindStudent {
		httpx.WriteError(w, r, http.StatusForbidden, httpx.CodeForbidden, "only students take assignments")
		return
	}
	a, err := h.svc.Start(r.Context(), p.ID, r.PathValue("id"), h.today())
	if err != nil {
		h.fail(w, r, "start assignment", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, a)
}

func (h *Handler) notifications(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	items, unread, err := h.svc.Notifications(r.Context(), p.Kind, p.ID)
	if err != nil {
		h.fail(w, r, "notifications", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"items": items, "unread": unread})
}

func (h *Handler) markRead(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	var in struct {
		IDs []int64 `json:"ids"`
		All bool    `json:"all"`
	}
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	if len(in.IDs) == 0 && !in.All {
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, `send "ids" or "all": true`)
		return
	}
	unread, err := h.svc.MarkRead(r.Context(), p.Kind, p.ID, in.IDs, in.All)
	if err != nil {
		h.fail(w, r, "read notifications", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"unread": unread})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request, teacher int32) {
	var group *int32
	if v := r.URL.Query().Get("group_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 32)
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "group_id must be a number")
			return
		}
		g := int32(id)
		group = &g
	}
	list, err := h.svc.List(r.Context(), teacher, group, h.today())
	if err != nil {
		h.fail(w, r, "assignments", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"assignments": list})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request, teacher int32) {
	var in Input
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	d, err := h.svc.Create(r.Context(), teacher, in, h.today())
	if err != nil {
		h.fail(w, r, "create assignment", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusCreated, d)
}

func (h *Handler) draw(w http.ResponseWriter, r *http.Request, teacher int32) {
	var in DrawInput
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	d, err := h.svc.Draw(r.Context(), teacher, in, h.today())
	if err != nil {
		h.fail(w, r, "draw", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, d)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request, teacher int32) {
	d, err := h.svc.Get(r.Context(), teacher, r.PathValue("id"), h.today())
	if err != nil {
		h.fail(w, r, "assignment", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, d)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request, teacher int32) {
	var in Update
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	d, err := h.svc.Update(r.Context(), teacher, r.PathValue("id"), in, h.today())
	if err != nil {
		h.fail(w, r, "update assignment", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, d)
}

func (h *Handler) remove(w http.ResponseWriter, r *http.Request, teacher int32) {
	if err := h.svc.Delete(r.Context(), teacher, r.PathValue("id"), h.today()); err != nil {
		h.fail(w, r, "delete assignment", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) results(w http.ResponseWriter, r *http.Request, teacher int32) {
	res, err := h.svc.Results(r.Context(), teacher, r.PathValue("id"), h.today())
	if err != nil {
		h.fail(w, r, "results", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, res)
}

func (h *Handler) studentWork(w http.ResponseWriter, r *http.Request, teacher int32) {
	student, err := strconv.ParseInt(r.PathValue("client_id"), 10, 32)
	if err != nil {
		httpx.WriteError(w, r, http.StatusNotFound, httpx.CodeNotFound, "not found")
		return
	}
	res, err := h.svc.StudentWork(r.Context(), teacher, r.PathValue("id"), int32(student), h.today())
	if err != nil {
		h.fail(w, r, "student work", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, res)
}

func (h *Handler) questions(w http.ResponseWriter, r *http.Request, _ int32) {
	v := r.URL.Query()
	f := practice.QuestionFilter{Subject: v.Get("subject"), Skill: v.Get("skill"), Difficulty: v.Get("difficulty"), Text: v.Get("q"), Limit: 30}
	bad := func(msg string) { httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, msg) }
	if !slices.Contains(roadmap.Subjects, f.Subject) {
		bad("subject must be math or english")
		return
	}
	if f.Difficulty != "" && !slices.Contains(roadmap.Difficulties, f.Difficulty) {
		bad("difficulty must be easy, medium or hard")
		return
	}
	if utf8.RuneCountInString(f.Text) > 100 {
		bad("q is at most 100 characters")
		return
	}
	if s := v.Get("limit"); s != "" {
		n, err := strconv.ParseInt(s, 10, 32)
		if err != nil || n < 1 || n > 100 {
			bad("limit must be 1 to 100")
			return
		}
		f.Limit = int32(n)
	}
	if s := v.Get("offset"); s != "" {
		n, err := strconv.ParseInt(s, 10, 32)
		if err != nil || n < 0 {
			bad("offset must be 0 or more")
			return
		}
		f.Offset = int32(n)
	}
	list, total, err := h.svc.Questions(r.Context(), f)
	if err != nil {
		h.fail(w, r, "questions", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"questions": list, "total": total})
}

func (h *Handler) taxonomy(w http.ResponseWriter, r *http.Request, _ int32) {
	t, err := h.svc.Taxonomy(r.Context())
	if err != nil {
		h.fail(w, r, "taxonomy", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"subjects": t})
}

func (h *Handler) curriculum(w http.ResponseWriter, r *http.Request, _ int32) {
	c, err := h.svc.Curriculum(r.Context())
	if err != nil {
		h.fail(w, r, "curriculum", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"courses": c})
}

func (h *Handler) groupRoadmap(w http.ResponseWriter, r *http.Request, teacher int32) {
	group, err := strconv.ParseInt(r.PathValue("id"), 10, 32)
	if err != nil {
		httpx.WriteError(w, r, http.StatusForbidden, httpx.CodeForbidden, ErrForbidden.Error())
		return
	}
	g, err := h.svc.GroupRoadmap(r.Context(), teacher, int32(group), h.today())
	if err != nil {
		h.fail(w, r, "group roadmap", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, g)
}

// fail answers an error from the service.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	var bad *InvalidError
	var answers *roadmap.AnswersError
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, roadmap.ErrNotFound):
		httpx.WriteError(w, r, http.StatusNotFound, httpx.CodeNotFound, "not found")
	case errors.Is(err, ErrForbidden):
		httpx.WriteError(w, r, http.StatusForbidden, httpx.CodeForbidden, err.Error())
	case errors.Is(err, ErrNotAnAssessment):
		httpx.WriteError(w, r, http.StatusConflict, CodeNotAnAssessment, err.Error())
	case errors.Is(err, ErrNotOpen):
		httpx.WriteError(w, r, http.StatusConflict, CodeNotOpen, err.Error())
	case errors.Is(err, ErrClosed):
		httpx.WriteError(w, r, http.StatusConflict, CodeAssignmentClosed, err.Error())
	case errors.Is(err, ErrHasWork):
		httpx.WriteError(w, r, http.StatusConflict, CodeHasWork, err.Error())
	case errors.Is(err, roadmap.ErrNoQuestions):
		httpx.WriteError(w, r, http.StatusConflict, CodeNoQuestions, err.Error())
	case errors.As(err, &bad):
		httpx.WriteFieldErrors(w, r, http.StatusUnprocessableEntity, CodeInvalidAssignment, bad.Error(), bad.Fields)
	case errors.As(err, &answers):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, roadmap.CodeInvalidAnswers, answers.Reason)
	default:
		slog.ErrorContext(r.Context(), "assignments: "+what, "err", err)
		httpx.WriteError(w, r, http.StatusInternalServerError, httpx.CodeInternal, "something went wrong")
	}
}
