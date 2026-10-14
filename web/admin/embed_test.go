package adminweb

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestHandler(t *testing.T) {
	h := handler(fstest.MapFS{
		"index.html":           {Data: []byte("<html>panel</html>")},
		"favicon.png":          {Data: []byte("PNG")},
		"assets/index-abc.js":  {Data: []byte("console.log(1)")},
		"assets/index-abc.css": {Data: []byte("body{}")},
	})
	for _, tc := range []struct {
		method, path string
		status       int
		body, cache  string
	}{
		{"GET", "/", 200, "panel", "no-cache"},
		{"GET", "/lessons", 200, "panel", "no-cache"},                // a page route: the page
		{"GET", "/sessions/1234-5678", 200, "panel", "no-cache"},     // ditto
		{"GET", "/assets/index-abc.js", 200, "console", "immutable"}, // hashed build file
		{"GET", "/favicon.png", 200, "PNG", "no-cache"},
		{"GET", "/assets/index-old.js", 404, "", ""}, // an old build's file: not the page
		{"GET", "/../../etc/passwd", 400, "", ""},    // never outside the build
		{"POST", "/lessons", 405, "", ""},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.body) || !strings.Contains(w.Header().Get("Cache-Control"), tc.cache) {
			t.Errorf("%s %s: %d %q cache %q", tc.method, tc.path, w.Code, w.Body.String(), w.Header().Get("Cache-Control"))
		}
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s %s: security headers %v", tc.method, tc.path, w.Header())
		}
	}

	w := httptest.NewRecorder()
	handler(nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("not built: %d", w.Code)
	}
}
