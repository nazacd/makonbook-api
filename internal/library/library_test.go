package library

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/r2"
	"github.com/nazacd/makonbook-api/internal/platform/testdb"
)

// memStore is the bucket, in memory.
type memStore struct {
	mu   sync.Mutex
	objs map[string]r2.Object
	puts int
}

func (m *memStore) Get(_ context.Context, key string) (r2.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.objs[key]
	if !ok {
		return r2.Object{}, errors.New("no such key")
	}
	return o, nil
}

func (m *memStore) Put(_ context.Context, key string, body []byte, contentType string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.objs == nil {
		m.objs = map[string]r2.Object{}
	}
	m.objs[key] = r2.Object{Body: body, ContentType: contentType}
	m.puts++
	return nil
}

// pdfOf is a made-up file that passes for a PDF.
func pdfOf(text string) []byte {
	return []byte("%PDF-1.4\n% " + text + "\n" + strings.Repeat("0123456789", 30) + "\n%%EOF\n")
}

func coverOf(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 30, 40))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// checkout writes a made-up makonbook-library checkout: files by their path.
func checkout(t *testing.T, files map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func metaOf(title, subject string, pages string) []byte {
	return []byte(`{"title":"` + title + `","author":"SAT MAKON","subject":"` + subject + `","description":"About ` + title + `.","pages":` + pages + `}`)
}

func TestLibrary(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	store := &memStore{}
	im := Importer{Pool: pool, Store: store}
	algebra, grammar := pdfOf("algebra"), pdfOf("grammar")
	files := map[string][]byte{
		"books/02-grammar-guide.json": metaOf("Grammar Guide", "english", "63"),
		"books/02-grammar-guide.pdf":  grammar,
		"books/01-algebra-guide.json": metaOf("Algebra Guide", "math", "79"),
		"books/01-algebra-guide.pdf":  algebra,
		"covers/01-algebra-guide.png": coverOf(t),
		"README.md":                   []byte("ignored"),
	}
	s, err := im.Import(ctx, checkout(t, files))
	if err != nil {
		t.Fatal(err)
	}
	if s.Listed != 2 || s.New != 2 || s.Uploaded != 3 {
		t.Fatalf("first import: %+v", s)
	}

	svc := NewService(pool, store)
	reader, other := Who{Kind: people.KindStudent, ID: 11}, Who{Kind: people.KindTeacher, ID: 11}
	books, err := svc.Books(ctx, reader)
	if err != nil || len(books) != 2 {
		t.Fatalf("books: %v %v", books, err)
	}
	// In the files' order, the number dropped from the id.
	a, g := books[0], books[1]
	if a.ID != "algebra-guide" || a.Title != "Algebra Guide" || a.Subject != "math" || a.Pages != 79 || a.Bytes != len(algebra) ||
		a.CoverURL == nil || a.Views != 0 || a.Progress != nil {
		t.Errorf("algebra: %+v", a)
	}
	if g.ID != "grammar-guide" || g.CoverURL != nil {
		t.Errorf("grammar: %+v", g)
	}

	// Opening a book remembers the page and counts a view, once per person.
	if p, err := svc.SetProgress(ctx, reader, "algebra-guide", 1); err != nil || p.Page != 1 {
		t.Fatalf("open: %+v %v", p, err)
	}
	if p, err := svc.SetProgress(ctx, reader, "algebra-guide", 40); err != nil || p.Page != 40 {
		t.Fatalf("turn: %+v %v", p, err)
	}
	if _, err := svc.SetProgress(ctx, other, "algebra-guide", 3); err != nil {
		t.Fatal(err)
	}
	for _, page := range []int{0, 80} {
		if _, err := svc.SetProgress(ctx, reader, "algebra-guide", page); !errors.Is(err, ErrBadPage) {
			t.Errorf("page %d: %v", page, err)
		}
	}
	if _, err := svc.SetProgress(ctx, reader, "no-such-book", 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown book: %v", err)
	}
	books, _ = svc.Books(ctx, reader)
	if b := books[0]; b.Views != 2 || b.Progress == nil || b.Progress.Page != 40 {
		t.Errorf("after reading: %+v", b)
	}
	if books, _ := svc.Books(ctx, other); books[0].Progress.Page != 3 || books[1].Progress != nil {
		t.Errorf("another reader: %+v", books)
	}

	// The files, over HTTP.
	pass := func(h http.HandlerFunc) http.HandlerFunc { return h }
	mux := http.NewServeMux()
	NewHandler(svc, pass).Register(mux)
	do := func(path string, header ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	rec := do("/v1/library/algebra-guide/file")
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), algebra) || rec.Header().Get("Content-Type") != "application/pdf" ||
		!strings.HasPrefix(rec.Header().Get("Content-Disposition"), "inline") || !strings.Contains(rec.Header().Get("Cache-Control"), "private") {
		t.Errorf("file: %d %v", rec.Code, rec.Header())
	}
	if r := do("/v1/library/algebra-guide/file?download=1"); r.Header().Get("Content-Disposition") != `attachment; filename="Algebra Guide.pdf"` {
		t.Errorf("download: %q", r.Header().Get("Content-Disposition"))
	}
	if r := do("/v1/library/algebra-guide/file", "Range", "bytes=0-7"); r.Code != http.StatusPartialContent || r.Body.String() != "%PDF-1.4" {
		t.Errorf("range: %d %q", r.Code, r.Body)
	}
	if r := do("/v1/library/algebra-guide/file", "If-None-Match", rec.Header().Get("ETag")); r.Code != http.StatusNotModified {
		t.Errorf("unchanged file: %d", r.Code)
	}
	if r := do("/v1/library/nowhere/file"); r.Code != http.StatusNotFound {
		t.Errorf("unknown file: %d", r.Code)
	}
	rec = do(*a.CoverURL)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("cover: %d %v", rec.Code, rec.Header())
	}
	if r := do("/v1/library/grammar-guide/cover"); r.Code != http.StatusNotFound {
		t.Errorf("no cover: %d", r.Code)
	}

	// The same folder again: nothing changes, nothing is uploaded.
	puts := store.puts
	if s, err = im.Import(ctx, checkout(t, files)); err != nil || s.New+s.Changed+s.Unlisted+s.Uploaded != 0 || store.puts != puts {
		t.Fatalf("again: %+v %v", s, err)
	}

	// A new edition of one book and the other gone: one changed (its readers
	// keep their place), one off the shelf.
	delete(files, "books/02-grammar-guide.json")
	delete(files, "books/02-grammar-guide.pdf")
	files["books/01-algebra-guide.pdf"] = pdfOf("algebra, second edition")
	s, err = im.Import(ctx, checkout(t, files))
	if err != nil || s.Listed != 1 || s.Changed != 1 || s.Unlisted != 1 || s.Uploaded != 1 {
		t.Fatalf("a new edition: %+v %v", s, err)
	}
	books, _ = svc.Books(ctx, reader)
	if len(books) != 1 || books[0].Progress.Page != 40 {
		t.Errorf("after the new edition: %+v", books)
	}
	if r := do("/v1/library/grammar-guide/file"); r.Code != http.StatusNotFound {
		t.Errorf("a book off the shelf: %d", r.Code)
	}

	// Bad folders: every problem listed, nothing stored.
	_, err = im.Import(ctx, checkout(t, map[string][]byte{
		"books/01-algebra-guide.json": metaOf("Algebra Guide", "physics", "79"),
		"books/01-algebra-guide.pdf":  algebra,
		"books/02-No Spaces.json":     metaOf("X", "math", "1"),
		"books/03-missing.json":       metaOf("Missing", "math", "5"),
		"books/04-not-a-pdf.json":     metaOf("Not", "math", "5"),
		"books/04-not-a-pdf.pdf":      []byte("<html>"),
		"books/05-algebra-guide.json": metaOf("Twice", "math", "5"),
		"books/06-typo.json":          []byte(`{"title":"T","writer":"x","subject":"math","description":"d","pages":1}`),
		"books/07-cover.json":         metaOf("Cover", "math", "5"),
		"books/07-cover.pdf":          algebra,
		"covers/07-cover.png":         []byte("not an image"),
	}))
	var ie *ImportError
	if !errors.As(err, &ie) || len(ie.Problems) != 7 {
		t.Fatalf("bad folder: %v", err)
	}
	if books, _ := svc.Books(ctx, reader); len(books) != 1 || books[0].Subject != "math" {
		t.Error("a bad folder was stored")
	}
	if _, err := im.Import(ctx, checkout(t, map[string][]byte{"README.md": nil})); err == nil {
		t.Error("an empty folder was imported")
	}
	// Files to upload without a bucket: refused before anything is stored.
	_, err = Importer{Pool: pool}.Import(ctx, checkout(t, map[string][]byte{
		"books/01-new.json": metaOf("New", "math", "5"), "books/01-new.pdf": pdfOf("new"),
	}))
	if err == nil || !strings.Contains(err.Error(), "R2_UPLOADS") {
		t.Errorf("no bucket: %v", err)
	}
}

// A registered student who merges into another student takes their place in
// the books along (the further page where both read one); nothing is left behind.
func TestMergeMovesProgress(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	store := &memStore{}
	if _, err := (Importer{Pool: pool, Store: store}).Import(ctx, checkout(t, map[string][]byte{
		"books/01-one.json": metaOf("One", "math", "50"), "books/01-one.pdf": pdfOf("one"),
		"books/02-two.json": metaOf("Two", "math", "50"), "books/02-two.pdf": pdfOf("two"),
	})); err != nil {
		t.Fatal(err)
	}
	from, err := people.CreateSelfStudent(ctx, pool, "Web", "Student", "")
	if err != nil {
		t.Fatal(err)
	}
	to, err := people.CreateSelfStudent(ctx, pool, "Other", "Student", "+998935550011")
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(pool, store)
	a, b := Who{Kind: people.KindStudent, ID: from}, Who{Kind: people.KindStudent, ID: to}
	for _, step := range []struct {
		who  Who
		book string
		page int
	}{{a, "one", 30}, {b, "one", 12}, {a, "two", 5}} {
		if _, err := svc.SetProgress(ctx, step.who, step.book, step.page); err != nil {
			t.Fatal(err)
		}
	}
	if err := people.MergeSelfStudent(ctx, pool, from, to); err != nil {
		t.Fatal(err)
	}
	books, err := svc.Books(ctx, b)
	if err != nil || books[0].Progress.Page != 30 || books[0].Views != 1 || books[1].Progress.Page != 5 || books[1].Views != 1 {
		t.Errorf("after the merge: %+v %v", books, err)
	}
}
