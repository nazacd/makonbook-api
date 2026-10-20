package r2

import (
	"errors"
	"io"
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
	c.sign(req, path, hexSHA(""))
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

// A PUT signs the SHA-256 of its body; checked against botocore too.
func TestSignPut(t *testing.T) {
	c := &Client{Region: "auto", AccessKey: "AKIDEXAMPLE", SecretKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
		Now: func() time.Time { return time.Date(2026, 10, 17, 9, 30, 0, 0, time.UTC) }}
	path := "/makonbook/mb/abc.png"
	req, _ := http.NewRequest(http.MethodPut, "https://acct.r2.cloudflarestorage.com"+path, nil)
	c.sign(req, path, hexSHA("PNG"))
	if got := req.Header.Get("Authorization"); !strings.HasSuffix(got, "Signature=6072288ec9e8344263bb17ece31d59b358d57aaaad0734ec347665d50dc47737") {
		t.Errorf("Authorization: %s", got)
	}
}

func TestPut(t *testing.T) {
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		if r.Method != http.MethodPut || r.URL.EscapedPath() != "/makonbook/mb/a.png" || r.Header.Get("Content-Type") != "image/png" ||
			r.Header.Get("x-amz-content-sha256") != hexSHA("PNG") {
			t.Errorf("request: %s %s %v", r.Method, r.URL.EscapedPath(), r.Header)
		}
	}))
	defer srv.Close()
	c := &Client{Endpoint: srv.URL, Bucket: "makonbook", AccessKey: "k", SecretKey: "s"}
	if err := c.Put(t.Context(), "mb/a.png", []byte("PNG"), "image/png"); err != nil || string(got) != "PNG" {
		t.Errorf("put: %v %q", err, got)
	}
}

// The example in AWS's S3 documentation ("Authenticating Requests: Using
// Query Parameters"), with its published signature.
func TestPresign(t *testing.T) {
	c := &Client{Region: "us-east-1", AccessKey: "AKIAIOSFODNN7EXAMPLE", SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		Now: func() time.Time { return time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC) }}
	q := c.presignQuery("examplebucket.s3.amazonaws.com", "/test.txt", 24*time.Hour)
	want := "X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20130524%2Fus-east-1%2Fs3%2Faws4_request" +
		"&X-Amz-Date=20130524T000000Z&X-Amz-Expires=86400&X-Amz-SignedHeaders=host" +
		"&X-Amz-Signature=aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404"
	if q != want {
		t.Errorf("query:\n got %s\nwant %s", q, want)
	}
	c.Endpoint, c.Bucket = "https://acct.r2.cloudflarestorage.com", "makonbook"
	u, err := c.PresignGet("p/img/a b.png", time.Hour)
	if err != nil || !strings.HasPrefix(u, "https://acct.r2.cloudflarestorage.com/makonbook/p/img/a%20b.png?X-Amz-Algorithm=") {
		t.Errorf("url: %s %v", u, err)
	}
}
