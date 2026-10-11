// Package hollihop is a read-only client for the HolliHop CRM API v2, limited
// to the methods the sync needs.
//
// The auth key travels in the query string, so this package never lets a
// request URL reach an error message or a log line.
package hollihop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// PageSize is how many rows each paged request asks for (HolliHop allows up
// to 10 000; smaller pages keep single responses modest).
const PageSize = 1000

// Client calls HolliHop. Create it with New.
type Client struct {
	baseURL  string
	authKey  string
	http     *http.Client
	retries  int
	backoff  time.Duration
	pageSize int
}

// Option customises a Client.
type Option func(*Client)

// WithHTTPClient replaces the default HTTP client (60 s timeout).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithRetry sets how many times a failed request is retried and the initial
// backoff, which doubles on each attempt. Defaults: 3 retries, 2 s.
func WithRetry(retries int, backoff time.Duration) Option {
	return func(c *Client) { c.retries, c.backoff = retries, backoff }
}

// WithPageSize overrides PageSize; used by tests to exercise paging.
func WithPageSize(n int) Option { return func(c *Client) { c.pageSize = n } }

// New returns a client for baseURL, e.g. "https://school.t8s.ru/Api/V2".
func New(baseURL, authKey string, opts ...Option) *Client {
	c := &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		authKey:  authKey,
		http:     &http.Client{Timeout: 60 * time.Second},
		retries:  3,
		backoff:  2 * time.Second,
		pageSize: PageSize,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// GetTeachers returns every teacher, fired ones included.
func (c *Client) GetTeachers(ctx context.Context) ([]Teacher, error) {
	var out []Teacher
	err := c.paged(ctx, "GetTeachers", nil, func(body []byte) (int, error) {
		var r struct{ Teachers []Teacher }
		if err := json.Unmarshal(body, &r); err != nil {
			return 0, err
		}
		out = append(out, r.Teachers...)
		return len(r.Teachers), nil
	})
	return out, err
}

// GetEdUnits returns every group with its schedule items.
func (c *Client) GetEdUnits(ctx context.Context) ([]EdUnit, error) {
	var out []EdUnit
	err := c.paged(ctx, "GetEdUnits", nil, func(body []byte) (int, error) {
		var r struct{ EdUnits []EdUnit }
		if err := json.Unmarshal(body, &r); err != nil {
			return 0, err
		}
		out = append(out, r.EdUnits...)
		return len(r.EdUnits), nil
	})
	return out, err
}

// GetStudents returns students of every status. With a non-empty
// updatedSince (a value previously returned as `now`), only students whose
// status, contacts or assignees changed since then are returned — HolliHop
// does not count name changes.
//
// `now` is HolliHop's server time at the first page; pass it back as the next
// updatedSince so nothing that changes during paging is missed.
func (c *Client) GetStudents(ctx context.Context, updatedSince string) (students []Student, now string, err error) {
	params := url.Values{}
	if updatedSince != "" {
		params.Set("lastUpdatedFrom", updatedSince)
	}
	err = c.paged(ctx, "GetStudents", params, func(body []byte) (int, error) {
		var r struct {
			Students []Student
			Now      string
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return 0, err
		}
		if now == "" {
			now = r.Now
		}
		students = append(students, r.Students...)
		return len(r.Students), nil
	})
	if err == nil && now == "" {
		err = errors.New("hollihop: GetStudents response has no Now timestamp")
	}
	return students, now, err
}

// GetEdUnitStudents returns enrollments with status Normal. HolliHop returns
// the full history; callers filter by date.
func (c *Client) GetEdUnitStudents(ctx context.Context) ([]EdUnitStudent, error) {
	var out []EdUnitStudent
	params := url.Values{"statuses": {"Normal"}}
	err := c.paged(ctx, "GetEdUnitStudents", params, func(body []byte) (int, error) {
		var r struct{ EdUnitStudents []EdUnitStudent }
		if err := json.Unmarshal(body, &r); err != nil {
			return 0, err
		}
		out = append(out, r.EdUnitStudents...)
		return len(r.EdUnitStudents), nil
	})
	return out, err
}

// paged calls method with skip/take until a page comes back short. decode
// appends the page's rows and returns how many there were.
func (c *Client) paged(ctx context.Context, method string, params url.Values, decode func([]byte) (int, error)) error {
	for skip := 0; ; skip += c.pageSize {
		p := url.Values{}
		for k, v := range params {
			p[k] = v
		}
		p.Set("skip", strconv.Itoa(skip))
		p.Set("take", strconv.Itoa(c.pageSize))

		body, err := c.get(ctx, method, p)
		if err != nil {
			return err
		}
		n, err := decode(body)
		if err != nil {
			return fmt.Errorf("hollihop %s: decode response: %w", method, err)
		}
		if n < c.pageSize {
			return nil
		}
	}
}

// get performs one GET with retries on network errors, 5xx, 429 and
// HolliHop's rate-limit message.
func (c *Client) get(ctx context.Context, method string, params url.Values) ([]byte, error) {
	params.Set("authkey", c.authKey)
	u := c.baseURL + "/" + method + "?" + params.Encode()

	backoff := c.backoff
	var lastErr error
	for attempt := 0; attempt <= c.retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("hollihop %s: %w (last error: %w)", method, ctx.Err(), lastErr)
			case <-time.After(backoff):
			}
			backoff *= 2
		}

		body, retry, err := c.do(ctx, u)
		if err == nil {
			return body, nil
		}
		lastErr = fmt.Errorf("hollihop %s: %w", method, err)
		if !retry || ctx.Err() != nil {
			return nil, lastErr
		}
	}
	return nil, lastErr
}

// do performs a single request. Errors never contain the URL.
func (c *Client) do(ctx context.Context, u string) (body []byte, retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, false, errors.New("build request: invalid base URL")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // drop the URL, which carries the auth key
		}
		return nil, true, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err = io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return nil, true, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		retry := resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests
		return nil, retry, fmt.Errorf("status %d: %s", resp.StatusCode, snippet(body))
	}
	if strings.Contains(string(body[:min(len(body), 512)]), "Requests limit is exceeded") {
		return nil, true, errors.New("rate limit exceeded")
	}
	return body, false, nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
