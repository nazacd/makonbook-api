package admin

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/nazacd/makonbook-api/internal/parents"
	"github.com/nazacd/makonbook-api/internal/platform/httpx"
)

// Parents' reports and the students' parent codes (spec 12), for the admins
// of the students' branches.
func (h *Handler) registerParents(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/admin/parent-reports", h.Require(h.parentReports))
	mux.HandleFunc("POST /v1/admin/parent-reports/{id}/handled", h.Require(h.handleParentReport))
	mux.HandleFunc("GET /v1/admin/parent-codes", h.Require(h.parentCodes))
}

func (h *Handler) parentReports(w http.ResponseWriter, r *http.Request) {
	sc, ok := scopeOf(r)
	status := r.URL.Query().Get("status")
	if !ok || (status != "" && status != "open" && status != "handled") {
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "status must be open or handled")
		return
	}
	list, err := h.panel.Parents.AdminReports(r.Context(), sc, status == "handled")
	if err != nil {
		h.internal(w, r, "parent reports", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"reports": list})
}

func (h *Handler) handleParentReport(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		httpx.NotFound(w, r)
		return
	}
	var req struct {
		Reply string `json:"reply"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	p, _ := PrincipalFrom(r.Context())
	err = h.panel.Parents.MarkHandled(r.Context(), p.Scope(), id, p.Name, req.Reply)
	var bad *parents.InvalidError
	switch {
	case errors.As(err, &bad):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, httpx.CodeBadRequest, bad.Reason)
	case errors.Is(err, parents.ErrNotFound):
		httpx.WriteError(w, r, http.StatusNotFound, httpx.CodeNotFound, "no such open report in your branches")
	case err != nil:
		h.internal(w, r, "handle parent report", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *Handler) parentCodes(w http.ResponseWriter, r *http.Request) {
	sc, ok := scopeOf(r)
	if !ok {
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "office_id must be a number")
		return
	}
	y, m, d := time.Now().In(h.panel.Location).Date()
	list, err := h.panel.Parents.SearchCodes(r.Context(), sc, r.URL.Query().Get("q"), time.Date(y, m, d, 0, 0, 0, 0, time.UTC))
	if err != nil {
		h.internal(w, r, "parent codes", err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"students": list})
}
