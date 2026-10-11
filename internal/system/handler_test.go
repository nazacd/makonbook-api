package system_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nazacd/makonbook-api/internal/platform/httpx"
	"github.com/nazacd/makonbook-api/internal/platform/testdb"
	"github.com/nazacd/makonbook-api/internal/system"
	"github.com/nazacd/makonbook-api/internal/system/systemdb"
)

type fakeStore struct {
	value []byte
	err   error
}

func (f fakeStore) GetAppSetting(context.Context, string) ([]byte, error) { return f.value, f.err }

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func serve(t *testing.T, h *system.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	h.Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestHealthIgnoresDatabase(t *testing.T) {
	rec := serve(t, system.NewHandler(fakeStore{}, fakePinger{err: errors.New("down")}), "/healthz")
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestReadyReflectsDatabase(t *testing.T) {
	if rec := serve(t, system.NewHandler(fakeStore{}, fakePinger{}), "/readyz"); rec.Code != http.StatusOK {
		t.Errorf("healthy db: status = %d, want 200", rec.Code)
	}
	rec := serve(t, system.NewHandler(fakeStore{}, fakePinger{err: errors.New("down")}), "/readyz")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("db down: status = %d, want 503", rec.Code)
	}
}

func TestAppConfigStoreError(t *testing.T) {
	rec := serve(t, system.NewHandler(fakeStore{err: errors.New("db")}, fakePinger{}), "/v1/app/config")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// Runs against the real migration, so it also checks the seeded value.
func TestAppConfigFromDatabase(t *testing.T) {
	pool := testdb.New(t)
	rec := serve(t, system.NewHandler(systemdb.New(pool), pool), "/v1/app/config")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}

	var got system.AppConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := system.MinAppVersion{Android: "1.0.0", IOS: "1.0.0"}
	if got.MinAppVersion != want {
		t.Errorf("min_app_version = %+v, want %+v", got.MinAppVersion, want)
	}
}

func TestUnknownRouteIsJSON404(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", httpx.NotFound)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Errorf("got %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}
