package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAdminHost(t *testing.T) {
	answer := func(s string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, s) })
	}
	h := adminHost("admin.makonbook.uz", answer("panel"), answer("api"))
	for _, tc := range []struct{ host, path, want string }{
		{"admin.makonbook.uz", "/", "panel"},
		{"admin.makonbook.uz", "/lessons", "panel"},
		{"Admin.MakonBook.uz:443", "/sessions/x", "panel"},
		{"admin.makonbook.uz", "/v1/admin/me", "api"},
		{"admin.makonbook.uz", "/healthz", "api"},
		{"admin.makonbook.uz", "/v1/auth/login", ""}, // only the admin API on the admin host
		{"api.makonbook.uz", "/", "api"},
		{"api.makonbook.uz", "/v1/auth/login", "api"},
		{"api.makonbook.uz", "/v1/admin/me", "api"},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, tc.path, nil)
		r.Host = tc.host
		h.ServeHTTP(w, r)
		if got := w.Body.String(); (tc.want == "" && w.Code != 404) || (tc.want != "" && got != tc.want) {
			t.Errorf("%s%s: %d %q, want %q", tc.host, tc.path, w.Code, got, tc.want)
		}
	}
}
