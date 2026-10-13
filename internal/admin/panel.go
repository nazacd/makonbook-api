package admin

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nazacd/makonbook-api/internal/attendance"
	"github.com/nazacd/makonbook-api/internal/lessons"
	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/httpx"
	"github.com/nazacd/makonbook-api/internal/platform/scope"
)

// Panel is what the admin pages read and change, through the features that
// own the data. Every call passes the admin's branches (Principal.Scope).
type Panel struct {
	Lessons    *lessons.Service
	Attendance *attendance.Service
	People     *people.Directory
	Location   *time.Location // school time zone, for "today"
	// WriteEnabled: attendance is really written to HolliHop (not dry run).
	WriteEnabled bool
}

// Error codes of the panel's pages (see api/openapi.yaml).
const (
	CodeLastSuper = "last_super_admin"
	CodeFired     = "fired"
)

// maxReportDays bounds a report's date range.
const maxReportDays = 366

func (h *Handler) registerPanel(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/admin/lessons", h.Require(h.lessons))
	mux.HandleFunc("GET /v1/admin/attendance/sessions/{id}", h.Require(h.session))
	mux.HandleFunc("PUT /v1/admin/attendance/sessions/{id}/marks/{client_id}", h.Require(h.setMark))
	mux.HandleFunc("GET /v1/admin/reports/corrections", h.Require(h.corrections))
	mux.HandleFunc("GET /v1/admin/hollihop/writes", h.Require(h.writes))
	mux.HandleFunc("POST /v1/admin/hollihop/writes/{id}/retry", h.Require(h.retryWrite))
	mux.HandleFunc("GET /v1/admin/data-quality", h.Require(h.dataQuality))

	mux.HandleFunc("GET /v1/admin/access", h.RequireSuper(h.accessList))
	mux.HandleFunc("PUT /v1/admin/access/{member_type}/{member_id}", h.RequireSuper(h.grant))
	mux.HandleFunc("DELETE /v1/admin/access/{member_type}/{member_id}", h.RequireSuper(h.revoke))
	mux.HandleFunc("GET /v1/admin/staff", h.RequireSuper(h.staff))
}

// scopeOf is the signed-in admin's branches, narrowed to ?office_id= if the
// page asks for one branch (one outside their branches yields nothing).
func scopeOf(r *http.Request) (scope.Offices, bool) {
	p, _ := PrincipalFrom(r.Context())
	sc := p.Scope()
	v := r.URL.Query().Get("office_id")
	if v == "" {
		return sc, true
	}
	id, err := strconv.ParseInt(v, 10, 32)
	if err != nil {
		return sc, false
	}
	if !sc.Has(int32(id)) {
		return scope.Offices{IDs: []int32{}}, true
	}
	return scope.Offices{IDs: []int32{int32(id)}}, true
}

type lessonsResponse struct {
	Date    string                 `json:"date"`
	Lessons []lessons.OfficeLesson `json:"lessons"`
}

func (h *Handler) lessons(w http.ResponseWriter, r *http.Request) {
	sc, ok := scopeOf(r)
	day, okDay := h.day(r.URL.Query().Get("date"))
	if !ok || !okDay {
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "date is YYYY-MM-DD; office_id is a number")
		return
	}
	list, err := h.panel.Lessons.OfficeLessons(r.Context(), sc, day)
	if err != nil {
		h.internal(w, r, "office lessons", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, lessonsResponse{Date: day.Format(time.DateOnly), Lessons: list})
}

func (h *Handler) session(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	s, err := h.panel.Attendance.AdminSession(r.Context(), p.Scope(), r.PathValue("id"))
	h.respondSession(w, r, s, err)
}

type markRequest struct {
	State string `json:"state"`
}

func (h *Handler) setMark(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	clientID, err := strconv.ParseInt(r.PathValue("client_id"), 10, 32)
	if err != nil {
		httpx.WriteError(w, r, http.StatusNotFound, httpx.CodeNotFound, "no such student")
		return
	}
	var req markRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	actor := attendance.Actor{ID: p.MemberID, MemberType: p.MemberType, Name: p.Name}
	s, err := h.panel.Attendance.AdminSetMark(r.Context(), p.Scope(), actor, r.PathValue("id"), int32(clientID), req.State)
	h.respondSession(w, r, s, err)
}

func (h *Handler) respondSession(w http.ResponseWriter, r *http.Request, s attendance.AdminSession, err error) {
	switch {
	case errors.Is(err, attendance.ErrNotFound):
		httpx.WriteError(w, r, http.StatusNotFound, httpx.CodeNotFound, err.Error())
	case errors.Is(err, attendance.ErrNotInRoster):
		httpx.WriteError(w, r, http.StatusNotFound, attendance.CodeNotInRoster, err.Error())
	case errors.Is(err, attendance.ErrBadState):
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
	case err != nil:
		h.internal(w, r, "admin attendance", err)
	default:
		httpx.WriteJSON(w, r, http.StatusOK, s)
	}
}

type correctionsResponse struct {
	From     string                          `json:"from"`
	To       string                          `json:"to"`
	Teachers []attendance.TeacherCorrections `json:"teachers"`
	Changes  []attendance.Correction         `json:"changes"`
}

// corrections: lessons from..to (default: the last 30 days).
func (h *Handler) corrections(w http.ResponseWriter, r *http.Request) {
	sc, ok := scopeOf(r)
	q := r.URL.Query()
	to, okTo := h.day(q.Get("to"))
	from := to.AddDate(0, 0, -29)
	okFrom := true
	if q.Get("from") != "" {
		from, okFrom = h.day(q.Get("from"))
	}
	var teacherID int64
	var err error
	if v := q.Get("teacher_id"); v != "" {
		teacherID, err = strconv.ParseInt(v, 10, 32)
	}
	if !ok || !okTo || !okFrom || err != nil || from.After(to) || to.Sub(from) > maxReportDays*24*time.Hour {
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "from and to are YYYY-MM-DD, from ≤ to, at most a year apart")
		return
	}
	teachers, changes, err := h.panel.Attendance.CorrectionsReport(r.Context(), sc, from, to, int32(teacherID))
	if err != nil {
		h.internal(w, r, "corrections report", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, correctionsResponse{
		From: from.Format(time.DateOnly), To: to.Format(time.DateOnly), Teachers: teachers, Changes: changes,
	})
}

type writesResponse struct {
	WritingEnabled bool                  `json:"writing_enabled"` // false: dry run, nothing is sent
	Counts         map[string]int32      `json:"counts"`
	Writes         []attendance.WriteRow `json:"writes"`
}

// writes lists writes to HolliHop with ?status= (comma-separated; default
// the ones that need a person: failed, set_manually).
func (h *Handler) writes(w http.ResponseWriter, r *http.Request) {
	sc, ok := scopeOf(r)
	statuses := []string{"failed", "set_manually"}
	if v := r.URL.Query().Get("status"); v != "" {
		statuses = strings.Split(v, ",")
		for _, st := range statuses {
			if !validWriteStatus(st) {
				ok = false
			}
		}
	}
	if !ok {
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "status is a comma-separated list of: "+strings.Join(attendance.WriteStatuses, ", "))
		return
	}
	counts, rows, err := h.panel.Attendance.Writes(r.Context(), sc, statuses)
	if err != nil {
		h.internal(w, r, "writes", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, writesResponse{WritingEnabled: h.panel.WriteEnabled, Counts: counts, Writes: rows})
}

func validWriteStatus(s string) bool {
	for _, v := range attendance.WriteStatuses {
		if s == v {
			return true
		}
	}
	return false
}

func (h *Handler) retryWrite(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err == nil {
		err = h.panel.Attendance.RetryWrite(r.Context(), p.Scope(), id)
	} else {
		err = attendance.ErrNotFound
	}
	switch {
	case errors.Is(err, attendance.ErrNotFound):
		httpx.WriteError(w, r, http.StatusNotFound, httpx.CodeNotFound, "no failed write with this id (a newer one may have replaced it)")
	case err != nil:
		h.internal(w, r, "retry write", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

type dataQualityResponse struct {
	Issues []people.OpenIssue `json:"issues"`
}

func (h *Handler) dataQuality(w http.ResponseWriter, r *http.Request) {
	sc, ok := scopeOf(r)
	if !ok {
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "office_id is a number")
		return
	}
	issues, err := h.panel.People.OpenIssues(r.Context(), sc)
	if err != nil {
		h.internal(w, r, "data quality", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, dataQualityResponse{Issues: issues})
}

// Access is one entry of the access list.
type Access struct {
	MemberType  string     `json:"member_type"`
	MemberID    int32      `json:"member_id"`
	Name        string     `json:"name"`
	Super       bool       `json:"super"`
	Offices     []Office   `json:"offices"` // empty for super admins
	GrantedAt   time.Time  `json:"granted_at"`
	LastLoginAt *time.Time `json:"last_login_at"`
}

type accessResponse struct {
	Access []Access `json:"access"`
}

func (h *Handler) accessList(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.List(r.Context())
	if err != nil {
		h.internal(w, r, "access list", err)
		return
	}
	offices, err := h.svc.Offices(r.Context())
	if err != nil {
		h.internal(w, r, "offices", err)
		return
	}
	names := make(map[int32]string, len(offices))
	for _, o := range offices {
		names[o.ID] = o.Name
	}
	out := make([]Access, 0, len(list))
	for _, a := range list {
		e := Access{MemberType: a.MemberType, MemberID: a.MemberID, Name: a.Name, Super: a.Super, Offices: []Office{}, GrantedAt: a.GrantedAt.Time}
		for _, id := range a.OfficeIds {
			e.Offices = append(e.Offices, Office{ID: id, Name: names[id]})
		}
		if a.LastLoginAt.Valid {
			t := a.LastLoginAt.Time
			e.LastLoginAt = &t
		}
		out = append(out, e)
	}
	httpx.WriteJSON(w, r, http.StatusOK, accessResponse{Access: out})
}

type grantRequest struct {
	Super     bool    `json:"super"`
	OfficeIDs []int32 `json:"office_ids"`
}

// member reads {member_type}/{member_id}: "employee" or "teacher", and an id.
func member(r *http.Request) (string, int32, bool) {
	var typ string
	switch r.PathValue("member_type") {
	case "employee":
		typ = TypeEmployee
	case "teacher":
		typ = TypeTeacher
	}
	id, err := strconv.ParseInt(r.PathValue("member_id"), 10, 32)
	return typ, int32(id), typ != "" && err == nil && id > 0
}

func (h *Handler) grant(w http.ResponseWriter, r *http.Request) {
	typ, id, ok := member(r)
	if !ok {
		httpx.WriteError(w, r, http.StatusNotFound, httpx.CodeNotFound, "use /v1/admin/access/employee/{id} or /teacher/{id}")
		return
	}
	var req grantRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if _, err := h.svc.Grant(r.Context(), typ, id, "", req.Super, req.OfficeIDs); err != nil {
		h.accessError(w, r, err)
		return
	}
	h.accessList(w, r)
}

func (h *Handler) revoke(w http.ResponseWriter, r *http.Request) {
	typ, id, ok := member(r)
	if !ok {
		httpx.WriteError(w, r, http.StatusNotFound, httpx.CodeNotFound, "use /v1/admin/access/employee/{id} or /teacher/{id}")
		return
	}
	if err := h.svc.Revoke(r.Context(), typ, id); err != nil {
		h.accessError(w, r, err)
		return
	}
	h.accessList(w, r)
}

func (h *Handler) accessError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrUnknownTeacher), errors.Is(err, ErrUnknownEmployee):
		httpx.WriteError(w, r, http.StatusNotFound, httpx.CodeNotFound, err.Error())
	case errors.Is(err, ErrFired):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, CodeFired, err.Error())
	case errors.Is(err, ErrLastSuper):
		httpx.WriteError(w, r, http.StatusConflict, CodeLastSuper, err.Error())
	case errors.Is(err, ErrInvalid), errors.Is(err, ErrUnknownOffice):
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
	case errors.Is(err, ErrUnavailable):
		httpx.WriteError(w, r, http.StatusServiceUnavailable, httpx.CodeUnavailable, ErrUnavailable.Error())
	default:
		h.internal(w, r, "access", err)
	}
}

// StaffMember is a HolliHop employee found by name, to grant access to.
type StaffMember struct {
	ID       int32    `json:"id"`
	Name     string   `json:"name"`
	Position string   `json:"position"`
	Fired    bool     `json:"fired"`
	Offices  []string `json:"offices"`
}

type staffResponse struct {
	Staff []StaffMember `json:"staff"`
}

// staff searches HolliHop's employees (?q= name, phone or email).
func (h *Handler) staff(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) < 2 || len(q) > 100 {
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "q: 2 to 100 characters")
		return
	}
	found, err := h.svc.FindStaff(r.Context(), q)
	if errors.Is(err, ErrUnavailable) {
		httpx.WriteError(w, r, http.StatusServiceUnavailable, httpx.CodeUnavailable, ErrUnavailable.Error())
		return
	}
	if err != nil {
		h.internal(w, r, "find staff", err)
		return
	}
	out := make([]StaffMember, 0, len(found))
	for _, e := range found {
		m := StaffMember{ID: e.ID, Name: strings.TrimSpace(e.FirstName + " " + e.LastName), Position: e.Position, Fired: e.Fired, Offices: []string{}}
		for _, o := range e.Offices {
			m.Offices = append(m.Offices, o.Name)
		}
		out = append(out, m)
	}
	httpx.WriteJSON(w, r, http.StatusOK, staffResponse{Staff: out})
}

// day parses a date, defaulting to today at the school.
func (h *Handler) day(s string) (time.Time, bool) {
	if s == "" {
		y, m, d := time.Now().In(h.panel.Location).Date()
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC), true
	}
	t, err := time.Parse(time.DateOnly, s)
	return t, err == nil
}
