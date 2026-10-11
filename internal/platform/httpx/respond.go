// Package httpx holds the HTTP server, middleware and JSON response helpers
// shared by every feature.
package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// Stable error codes returned in the "code" field. Clients branch on these,
// never on the message text.
const (
	CodeNotFound    = "not_found"
	CodeInternal    = "internal"
	CodeUnavailable = "unavailable"
)

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

// WriteJSON writes v as a JSON response with the given status.
func WriteJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		Logger(r.Context()).Warn("write response", slog.Any("err", err))
	}
}

// WriteError writes the standard error envelope:
//
//	{"error": {"code": "...", "message": "...", "request_id": "..."}}
func WriteError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	WriteJSON(w, r, status, errorBody{Error: errorDetail{
		Code:      code,
		Message:   message,
		RequestID: RequestID(r.Context()),
	}})
}

// NotFound answers any request that matched no route.
func NotFound(w http.ResponseWriter, r *http.Request) {
	WriteError(w, r, http.StatusNotFound, CodeNotFound, "no such endpoint")
}
