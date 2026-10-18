// Package adminweb is the admin panel's built page (spec 13), embedded in
// the API binary and served on the admin host.
package adminweb

import (
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// dist/app is written by `npm run build` (the Docker image does it). Without
// it, the panel answers that it isn't built.
//
//go:embed all:dist
var files embed.FS

// Locked down: the page loads only its own files and talks only to its own
// origin, and no other site may frame it.
// style-src allows inline styles: the math editor (MathLive) positions its
// glyphs with style attributes. Scripts stay limited to the page's own files.
const csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; " +
	"connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// Handler serves the panel: its files, and index.html for any other path
// (the page routes in the browser). Built assets have hashed names and are
// cached for good; index.html is always revalidated, so a deploy shows at once.
func Handler() http.Handler {
	app, err := fs.Sub(files, "dist/app")
	if err != nil {
		return handler(nil)
	}
	return handler(app)
}

// handler serves app; nil (or no index.html) means the panel isn't built.
func handler(app fs.FS) http.Handler {
	var err error = fs.ErrNotExist
	if app != nil {
		_, err = fs.Stat(app, "index.html")
	}
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "The admin panel isn't built into this binary (run npm run build in web/admin).", http.StatusServiceUnavailable)
		})
	}
	files := http.FileServerFS(app)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name != "" && name != "index.html" {
			if info, err := fs.Stat(app, name); err == nil && !info.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					h.Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					h.Set("Cache-Control", "no-cache")
				}
				files.ServeHTTP(w, r)
				return
			} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			// A missing file under assets/ is a stale page asking for an old
			// build's file: a 404, not the page.
			if strings.HasPrefix(name, "assets/") {
				http.NotFound(w, r)
				return
			}
		}
		h.Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, app, "index.html")
	})
}
