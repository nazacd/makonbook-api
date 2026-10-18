// Package r2 reads and writes objects in Cloudflare R2 (S3-compatible),
// signing each request with AWS Signature Version 4. Writing (Put) is only
// ever done to MakonBook's own bucket; SAT MAKON's bucket gets a read-only key.
package r2

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrNotFound means the bucket has no such object.
var ErrNotFound = errors.New("r2: no such object")

// maxObject bounds what is read into memory (figures are small screenshots).
const maxObject = 15 << 20

// Client reads one bucket.
type Client struct {
	Endpoint  string // e.g. https://<account>.r2.cloudflarestorage.com
	Bucket    string
	Region    string // "auto" for R2
	AccessKey string
	SecretKey string
	HTTP      *http.Client
	Now       func() time.Time // for tests
}

// Object is a downloaded object.
type Object struct {
	Body        []byte
	ContentType string
}

// Get downloads an object.
func (c *Client) Get(ctx context.Context, key string) (Object, error) {
	resp, err := c.do(ctx, http.MethodGet, key, nil, "")
	if err != nil {
		return Object{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return Object{}, ErrNotFound
	case resp.StatusCode != http.StatusOK:
		return Object{}, fmt.Errorf("r2: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxObject+1))
	if err != nil {
		return Object{}, fmt.Errorf("r2: %w", err)
	}
	if len(body) > maxObject {
		return Object{}, errors.New("r2: object too large")
	}
	return Object{Body: body, ContentType: resp.Header.Get("Content-Type")}, nil
}

// Put uploads an object, replacing any with the same key.
func (c *Client) Put(ctx context.Context, key string, body []byte, contentType string) error {
	resp, err := c.do(ctx, http.MethodPut, key, body, contentType)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("r2: put: status %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, key string, body []byte, contentType string) (*http.Response, error) {
	base, err := url.Parse(strings.TrimRight(c.Endpoint, "/"))
	if err != nil || base.Host == "" {
		return nil, errors.New("r2: invalid endpoint")
	}
	path := "/" + escapePath(c.Bucket) + "/" + escapePath(key)
	u := *base
	u.Path, _ = url.PathUnescape(path)
	u.RawPath = path
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return nil, fmt.Errorf("r2: %w", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	c.sign(req, path, hexSHA(string(body)))

	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // drop the URL
		}
		return nil, fmt.Errorf("r2: %w", err)
	}
	return resp, nil
}

// sign adds the SigV4 headers for a request to the escaped path whose body
// has the given SHA-256 (hex).
func (c *Client) sign(req *http.Request, escapedPath, payload string) {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	t := now().UTC()
	stamp, day := t.Format("20060102T150405Z"), t.Format("20060102")
	region := c.Region
	if region == "" {
		region = "auto"
	}
	req.Header.Set("x-amz-date", stamp)
	req.Header.Set("x-amz-content-sha256", payload)

	signed := "host;x-amz-content-sha256;x-amz-date"
	canonical := strings.Join([]string{
		req.Method, escapedPath, "",
		"host:" + req.URL.Host, "x-amz-content-sha256:" + payload, "x-amz-date:" + stamp, "",
		signed, payload,
	}, "\n")
	scope := day + "/" + region + "/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + hexSHA(canonical)

	key := mac([]byte("AWS4"+c.SecretKey), day)
	for _, part := range []string{region, "s3", "aws4_request"} {
		key = mac(key, part)
	}
	req.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		c.AccessKey, scope, signed, hex.EncodeToString(mac(key, toSign))))
}

// escapePath percent-encodes everything but unreserved characters and "/".
func escapePath(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '/' || ch == '-' || ch == '_' || ch == '.' || ch == '~' ||
			('A' <= ch && ch <= 'Z') || ('a' <= ch && ch <= 'z') || ('0' <= ch && ch <= '9') {
			b.WriteByte(ch)
		} else {
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}

func mac(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func hexSHA(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
