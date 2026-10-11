package httpx

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestHandler(h http.Handler, logs *bytes.Buffer) http.Handler {
	log := slog.New(slog.NewJSONHandler(logs, nil))
	return Chain(h, WithRequestContext(log), AccessLog, Recover)
}

func TestRequestIDIsGeneratedAndEchoed(t *testing.T) {
	var seen string
	h := newTestHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestID(r.Context())
	}), &bytes.Buffer{})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if len(seen) != 32 || rec.Header().Get(RequestIDHeader) != seen {
		t.Errorf("request id %q, header %q", seen, rec.Header().Get(RequestIDHeader))
	}
}

func TestRequestIDReusesWellFormedIncomingID(t *testing.T) {
	h := newTestHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), &bytes.Buffer{})

	for in, wantReuse := range map[string]bool{
		"client-abc-12345":         true,
		"short":                    false,
		"has spaces in it, sadly!": false,
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(RequestIDHeader, in)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if got := rec.Header().Get(RequestIDHeader) == in; got != wantReuse {
			t.Errorf("incoming %q reused = %v, want %v", in, got, wantReuse)
		}
	}
}

func TestRecoverReturnsJSONErrorAndLogs(t *testing.T) {
	var logs bytes.Buffer
	h := newTestHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}), &logs)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x?token=secret", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Error.Code != CodeInternal || body.Error.RequestID == "" {
		t.Errorf("unexpected body: %+v", body)
	}
	if !strings.Contains(logs.String(), "panic in handler") || !strings.Contains(logs.String(), `"status":500`) {
		t.Errorf("panic or access log line missing:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), "secret") {
		t.Error("query string leaked into logs")
	}
}
