package roadmap

import (
	"bytes"
	"encoding/json"
	"image"
	"image/gif"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/testdb"
	"github.com/nazacd/makonbook-api/internal/practice"
)

func TestSlug(t *testing.T) {
	for name, want := range map[string]string{
		"University of California, Berkeley":                         "university-of-california-berkeley",
		"KAIST (Korea Advanced Institute of Science and Technology)": "kaist-korea-advanced-institute-of-science-and-technology",
		"Boğaziçi University":                                        "bogazici-university",
		"EPFL (École Polytechnique Fédérale de Lausanne)":            "epfl-ecole-polytechnique-federale-de-lausanne",
		"Queen's University":                                         "queen-s-university",
		"Universität Zürich":                                         "universitat-zurich",
		"İstanbul Teknik Üniversitesi":                               "istanbul-teknik-universitesi",
		"  -- MIT --  ":                                              "mit",
		"Ørsted Institute 2":                                         "orsted-institute-2",
	} {
		if got := Slug(name); got != want {
			t.Errorf("%q: %q, want %q", name, got, want)
		}
	}
}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, 8, 4))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

const svgLogo = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE svg PUBLIC "-//W3C//DTD SVG 1.1//EN" "http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd">
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><circle cx="5" cy="5" r="4"/></svg>`

func TestCheckLogo(t *testing.T) {
	var g bytes.Buffer
	if err := gif.Encode(&g, image.NewGray(image.Rect(0, 0, 2, 2)), nil); err != nil {
		t.Fatal(err)
	}
	ok := map[string]struct {
		data []byte
		ext  string
		want string
	}{
		"svg":         {[]byte(svgLogo), ".svg", "image/svg+xml"},
		"bare svg":    {[]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), ".SVG", "image/svg+xml"},
		"png":         {pngBytes(t), ".png", "image/png"},
		"png as webp": {pngBytes(t), ".webp", "image/png"}, // the bytes decide
	}
	for name, tc := range ok {
		if got, err := CheckLogo(tc.data, tc.ext); err != nil || got != tc.want {
			t.Errorf("%s: %q %v", name, got, err)
		}
	}
	bad := map[string]struct {
		data []byte
		ext  string
	}{
		"malformed svg":   {[]byte(`<svg><g></svg>`), ".svg"},
		"html as svg":     {[]byte(`<html><body><svg/></body></html>`), ".svg"},
		"png as svg":      {pngBytes(t), ".svg"},
		"text":            {[]byte("hello"), ".svg"},
		"svg as png":      {[]byte(svgLogo), ".png"},
		"truncated png":   {pngBytes(t)[:20], ".png"},
		"gif":             {g.Bytes(), ".png"},
		"empty":           {nil, ".png"},
		"over 1 MB (svg)": {append([]byte("<svg>"), append(bytes.Repeat([]byte(" "), MaxLogoBytes), "</svg>"...)...), ".svg"},
	}
	for name, tc := range bad {
		if got, err := CheckLogo(tc.data, tc.ext); err == nil {
			t.Errorf("%s: accepted as %s", name, got)
		}
	}
}

func TestLogos(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	svc := NewService(pool, practice.NewService(pool, nil), nil)
	c := fixture("math", "foundation", "m-")
	c.Units[1].Stop.University = "Boğaziçi University"
	c.Units[2].Stop.University = "Nowhere College"
	if _, err := svc.Import(ctx, []Course{c}); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(t.TempDir(), "logos")
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("bogazici-university.png", pngBytes(t))
	write("example-university.svg", []byte(svgLogo))
	write("example-university.webp", pngBytes(t)) // a second file for the same university
	write("kaist.png", pngBytes(t))               // no such university
	write("readme.txt", []byte("hi"))
	write("nowhere-college.svg", []byte("<svg>")) // not well-formed

	skipped := func(r LogoReport) []string {
		var out []string
		for _, s := range r.Skipped {
			out = append(out, s.File)
		}
		slices.Sort(out)
		return out
	}
	r, err := svc.ImportLogos(ctx, []string{dir, filepath.Join(t.TempDir(), "none")}, []Course{c})
	if err != nil || !slices.Equal(r.Loaded, []string{"bogazici-university", "example-university"}) || len(r.Unchanged) != 0 ||
		!slices.Equal(skipped(r), []string{"example-university.webp", "kaist.png", "nowhere-college.svg", "readme.txt"}) ||
		!slices.Equal(r.Missing, []string{"Nowhere College"}) {
		t.Fatalf("first load: %+v %v", r, err)
	}

	// Again: unchanged. A new file: replaced.
	if r, err := svc.ImportLogos(ctx, []string{dir}, []Course{c}); err != nil || len(r.Loaded) != 0 || len(r.Unchanged) != 2 {
		t.Errorf("again: %+v %v", r, err)
	}
	newSVG := strings.Replace(svgLogo, `r="4"`, `r="3"`, 1)
	write("example-university.svg", []byte(newSVG))
	if r, err := svc.ImportLogos(ctx, []string{dir}, []Course{c}); err != nil || !slices.Equal(r.Loaded, []string{"example-university"}) || len(r.Unchanged) != 1 {
		t.Errorf("changed: %+v %v", r, err)
	}
	us, err := svc.Universities(ctx)
	if err != nil || len(us) != 3 || us[0] != (University{Name: "Example University", Slug: "example-university", HasLogo: true}) || us[2].HasLogo {
		t.Errorf("universities: %+v %v", us, err)
	}

	// Served with its type, a long cache and a policy that runs nothing.
	pass := func(next http.HandlerFunc) http.HandlerFunc { return next }
	mux := http.NewServeMux()
	NewHandler(svc, time.UTC, nil, pass, pass).Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	type got struct {
		status int
		header http.Header
		body   string
	}
	get := func(path, etag string) got {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+path, nil)
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var b bytes.Buffer
		_, _ = b.ReadFrom(resp.Body)
		return got{resp.StatusCode, resp.Header, b.String()}
	}
	logo, err := svc.Logo(ctx, "example-university")
	if err != nil {
		t.Fatal(err)
	}
	resp := get("/v1/logos/example-university", "")
	h := resp.header
	if resp.status != http.StatusOK || h.Get("Content-Type") != "image/svg+xml" || resp.body != newSVG ||
		h.Get("ETag") != `"`+logo.SHA256+`"` || h.Get("Cache-Control") != "public, max-age=86400" {
		t.Errorf("logo: %d %v", resp.status, h)
	}
	for _, path := range []string{"/v1/logos/example-university", "/v1/logos/kaist"} {
		h := get(path, "").header
		if h.Get("X-Content-Type-Options") != "nosniff" ||
			h.Get("Content-Security-Policy") != "default-src 'none'; style-src 'unsafe-inline'; img-src data:; sandbox" {
			t.Errorf("%s headers: %v", path, h)
		}
	}
	if resp := get("/v1/logos/kaist", ""); resp.status != http.StatusNotFound {
		t.Errorf("unknown: %d", resp.status)
	}
	if resp := get("/v1/logos/example-university", `"`+logo.SHA256+`"`); resp.status != http.StatusNotModified {
		t.Errorf("not modified: %d", resp.status)
	}

	// The roadmap links each stop's logo, versioned by its bytes.
	student, err := people.CreateSelfStudent(ctx, pool, "Web", "Student", "+998935550011")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO roadmap_choices (student_client_id, roadmap_id) VALUES ($1, 'math-foundation')`, student); err != nil {
		t.Fatal(err)
	}
	d, err := svc.Roadmap(ctx, Who{Kind: people.KindStudent, ID: student, Registered: true}, "math-foundation", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	want := "/v1/logos/example-university?v=" + logo.SHA256[:8]
	if u := d.Units; u[0].Stop.LogoURL == nil || *u[0].Stop.LogoURL != want || u[1].Stop.LogoURL == nil || u[2].Stop.LogoURL != nil ||
		d.Current == nil || d.Current.LogoURL == nil || *d.Current.LogoURL != want {
		t.Errorf("logo urls: %+v %+v", d.Units, d.Current)
	}
	b, _ := json.Marshal(d.Units[0].Stop)
	if !strings.Contains(string(b), `"university":"Example University"`) || !strings.Contains(string(b), `"logo_url":"`+want+`"`) {
		t.Errorf("stop json: %s", b)
	}
}
