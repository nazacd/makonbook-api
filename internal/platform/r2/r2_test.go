package r2

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The expected signature comes from botocore's S3SigV4Auth for the same
// request, so ours matches what AWS's own SDK sends.
func TestSign(t *testing.T) {
	c := &Client{Region: "auto", AccessKey: "AKIDEXAMPLE", SecretKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
		Now: func() time.Time { return time.Date(2026, 10, 17, 9, 30, 0, 0, time.UTC) }}
	path := "/bucket/" + escapePath("media/sat/Снимок (1).PNG")
	if path != "/bucket/media/sat/%D0%A1%D0%BD%D0%B8%D0%BC%D0%BE%D0%BA%20%281%29.PNG" {
		t.Fatalf("escaped path: %s", path)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://acct.r2.cloudflarestorage.com"+path, nil)
	c.sign(req, path)
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261017/auto/s3/aws4_request, " +
		"SignedHeaders=host;x-amz-content-sha256;x-amz-date, " +
		"Signature=480aaf8160aa3d57f072a26fb670ae864c556de78d53e9ceb259499254bd9da1"
	if got := req.Header.Get("Authorization"); got != want {
		t.Errorf("Authorization:\n got %s\nwant %s", got, want)
	}
}

func TestGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Errorf("request: %s %v", r.Method, r.Header)
		}
		switch r.URL.EscapedPath() {
		case "/bucket/media/a%20b.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("PNG"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := &Client{Endpoint: srv.URL, Bucket: "bucket", AccessKey: "k", SecretKey: "s"}
	o, err := c.Get(t.Context(), "media/a b.png")
	if err != nil || string(o.Body) != "PNG" || o.ContentType != "image/png" {
		t.Errorf("get: %+v %v", o, err)
	}
	if _, err := c.Get(t.Context(), "media/missing.png"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
}
