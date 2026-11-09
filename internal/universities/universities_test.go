package universities

import (
	"bytes"
	"context"
	"encoding/json"
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

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// checkout writes a made-up makonbook-universities checkout.
func checkout(t *testing.T, list, records, countries []string, logos map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	for _, d := range []string{"data", "logos"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(name string, lines []string) {
		if lines == nil {
			return
		}
		if err := os.WriteFile(filepath.Join(dir, "data", name), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("list.jsonl", list)
	write("universities.jsonl", records)
	write("countries.jsonl", countries)
	for name, body := range logos {
		if err := os.WriteFile(filepath.Join(dir, "logos", name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const (
	northbridge = `{"id":"northbridge-university","name":"Northbridge University","country":"GB","city":"London",` +
		`"location":{"lat":51.5,"lng":-0.12},"ids":{},"list_reason":"qs","qs":{"rank":2,"rank_display":"2","edition":"QS 2027","url":"https://x"},` +
		`"logo":{"file":"logos/northbridge-university.svg","origin":"qs"}}`
	lakeside = `{"id":"lakeside-institute","name":"Lakeside Institute","country":"US","city":"Chicago",` +
		`"location":{"lat":41.8,"lng":-87.6},"ids":{},"list_reason":"qs","qs":{"rank":601,"rank_display":"601-610","edition":"QS 2027","url":"https://x"},` +
		`"logo":{"file":"logos/lakeside-institute.png","origin":"qs"}}`
	silkroad = `{"id":"silk-road-state-university","name":"Silk Road State University","country":"UZ","city":"Samarkand",` +
		`"location":null,"ids":{},"list_reason":"uzbekistan","qs":null,"logo":null}`
	// Northbridge's whole record: its fields win over the list's.
	northbridgeRecord = `{"id":"northbridge-university","name":"Northbridge University","short_name":"NBU","country":"GB",` +
		`"city":"London","location":{"lat":51.51,"lng":-0.13},"list_reason":"qs","qs":{"rank":2,"rank_display":"2","edition":"QS 2027","url":"https://x"},` +
		`"tuition":{"amount":40000,"currency":"GBP","usd":51000,"year":"2026-27"},` +
		`"admissions":{"sat":{"policy":"accepted","total":null,"math":null,"rw":null,"minimum":{"total":1470}}},` +
		`"logo":{"file":"logos/northbridge-university.svg","origin":"qs"},"doubts":["a note to ourselves"]}`
	gb = `{"code":"GB","name":"United Kingdom","currency":"GBP","usd_rate":{"per_usd":0.78},"doubts":["x"]}`
)

const svgLogo = `<?xml version="1.0" encoding="UTF-8"?><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"/>`

func TestImportAndServe(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	store := &memStore{}
	im := Importer{Pool: pool, Store: store}
	logos := map[string][]byte{"northbridge-university.svg": []byte(svgLogo), "lakeside-institute.png": pngOf(t, 64, 64)}
	dir := checkout(t, []string{northbridge, lakeside, silkroad}, []string{northbridgeRecord}, []string{gb}, logos)

	s, err := im.Import(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Listed != 3 || s.New != 3 || s.WithDetails != 1 || s.WithLogo != 2 || s.LogosUploaded != 2 || s.Countries != 1 {
		t.Fatalf("first import: %+v", s)
	}

	pass := func(h http.HandlerFunc) http.HandlerFunc { return h }
	mux := http.NewServeMux()
	NewHandler(NewService(pool, store), pass).Register(mux)
	do := func(path string, header ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// The list: the data's order, the record's fields over the list's.
	rec := do("/v1/universities")
	var l List
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &l) != nil || len(l.Universities) != 3 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	nb, lk, sr := l.Universities[0], l.Universities[1], l.Universities[2]
	if nb.ID != "northbridge-university" || *nb.ShortName != "NBU" || nb.Location.Lat != 51.51 || nb.QS.Display != "2" ||
		!nb.HasDetails || !strings.Contains(string(nb.SAT), `"minimum":{"total":1470}`) || nb.LogoURL == nil {
		t.Errorf("northbridge: %+v", nb)
	}
	if lk.QS.Rank != 601 || lk.QS.Display != "601-610" || lk.HasDetails || string(lk.SAT) != "null" {
		t.Errorf("lakeside: %+v", lk)
	}
	if sr.Location != nil || sr.QS != nil || sr.LogoURL != nil {
		t.Errorf("silk road: %+v", sr)
	}
	etag := rec.Header().Get("ETag")
	if r := do("/v1/universities", "If-None-Match", etag); r.Code != http.StatusNotModified {
		t.Errorf("unchanged list: %d", r.Code)
	}

	// A card: the record and its country's, without the doubts.
	rec = do("/v1/universities/northbridge-university")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `"amount":40000`) || !strings.Contains(body, `"country_details":{`) ||
		strings.Contains(body, "doubts") {
		t.Errorf("card: %d %s", rec.Code, body)
	}
	if r := do("/v1/universities/lakeside-institute"); !strings.Contains(r.Body.String(), `"details":null`) ||
		!strings.Contains(r.Body.String(), `"country_details":null`) {
		t.Errorf("a listed-only card: %s", r.Body)
	}
	if r := do("/v1/universities/nowhere"); r.Code != http.StatusNotFound {
		t.Errorf("unknown: %d", r.Code)
	}

	// The logo, from the bucket, kept for good at its versioned URL.
	rec = do(*nb.LogoURL)
	if rec.Code != http.StatusOK || rec.Body.String() != svgLogo || rec.Header().Get("Content-Type") != "image/svg+xml" ||
		!strings.Contains(rec.Header().Get("Cache-Control"), "immutable") || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("logo: %d %v", rec.Code, rec.Header())
	}
	if r := do("/v1/universities/silk-road-state-university/logo"); r.Code != http.StatusNotFound {
		t.Errorf("no logo: %d", r.Code)
	}

	// The same data again: nothing changes, nothing is uploaded, the list's version stays.
	if s, err = im.Import(ctx, dir); err != nil || s.New+s.Changed+s.Unlisted+s.LogosUploaded != 0 {
		t.Fatalf("again: %+v %v", s, err)
	}
	if r := do("/v1/universities", "If-None-Match", etag); r.Code != http.StatusNotModified {
		t.Error("the version moved without a change")
	}

	// Lakeside leaves the data and Silk Road moves: one unlisted, one changed.
	moved := strings.Replace(silkroad, `"location":null`, `"location":{"lat":39.65,"lng":66.96}`, 1)
	s, err = im.Import(ctx, checkout(t, []string{northbridge, moved}, []string{northbridgeRecord}, []string{gb}, logos))
	if err != nil || s.Listed != 2 || s.Changed != 1 || s.Unlisted != 1 {
		t.Fatalf("a changed list: %+v %v", s, err)
	}
	if r := do("/v1/universities"); strings.Contains(r.Body.String(), "lakeside") {
		t.Error("an unlisted university is still in the list")
	}
	if r := do("/v1/universities/lakeside-institute"); r.Code != http.StatusOK {
		t.Error("an unlisted university's card is gone (students may have chosen it)")
	}

	// Bad data: every problem listed, nothing stored.
	_, err = im.Import(ctx, checkout(t, []string{
		strings.Replace(northbridge, `"country":"GB"`, `"country":"gb"`, 1),
		lakeside, silkroad, silkroad,
	}, nil, nil, map[string][]byte{"northbridge-university.svg": []byte(svgLogo), "lakeside-institute.png": pngOf(t, 64, 40)}))
	var ie *ImportError
	if !errors.As(err, &ie) || len(ie.Problems) != 3 {
		t.Fatalf("bad data: %v", err)
	}
	if r := do("/v1/universities"); !strings.Contains(r.Body.String(), `"country":"GB"`) {
		t.Error("bad data was stored")
	}

	// A logo that isn't square (not padded) is refused.
	_, err = im.Import(ctx, checkout(t, []string{lakeside}, nil, nil, map[string][]byte{"lakeside-institute.png": pngOf(t, 64, 40)}))
	if !errors.As(err, &ie) || !strings.Contains(ie.Problems[0], "not square") {
		t.Errorf("an unpadded logo: %v", err)
	}

	// Logos to upload without a bucket: refused before anything is stored.
	other := strings.Replace(svgLogo, `0 0 10 10`, `0 0 20 20`, 1)
	_, err = Importer{Pool: pool}.Import(ctx, checkout(t, []string{northbridge}, nil, nil,
		map[string][]byte{"northbridge-university.svg": []byte(other)}))
	if err == nil || !strings.Contains(err.Error(), "R2_UPLOADS") {
		t.Errorf("no bucket: %v", err)
	}
}
