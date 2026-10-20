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

func TestCardPage(t *testing.T) {
	fsys := fstest.MapFS{
		"dist/card/card.html": {Data: []byte(`<head><script type="module" crossorigin src="/assets/card-1.js"></script>` +
			`<link rel="stylesheet" crossorigin href="/assets/style-1.css"></head><body><div id="root"></div></body>`)},
		"dist/card/assets/card-1.js":   {Data: []byte(`window.renderCard = () => "</script>";`)},
		"dist/card/assets/style-1.css": {Data: []byte(`body{color:red}`)},
	}
	page, err := cardPage(fsys, "dist/card")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(page, "/assets/") || !strings.Contains(page, `<style>body{color:red}</style>`) ||
		!strings.Contains(page, `<script type="module">window.renderCard = () => "<\/script>";</script>`) {
		t.Errorf("page: %s", page)
	}
	if _, err := cardPage(fstest.MapFS{}, "dist/card"); err == nil {
		t.Error("no page: no error")
	}
	// The real build, when there is one.
	if real, err := CardPage(); err == nil && !strings.Contains(real, "renderCard") {
		t.Error("the built page has no renderCard")
	}
}
