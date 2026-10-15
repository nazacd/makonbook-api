package hollihop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ErrBadCredentials means HolliHop refused the login and password.
var ErrBadCredentials = errors.New("hollihop: wrong login or password")

// CheckLogin asks HolliHop whose login and password these are, for signing
// staff in to the admin panel. It makes exactly one request — never retried,
// so a wrong password counts once against the person's HolliHop account —
// and sends the password in the body, never in the URL.
//
// HolliHop answers every error with HTTP 500 and {"Error": "…"}; for this
// method that means the credentials were refused.
func (c *Client) CheckLogin(ctx context.Context, login, password string) (Member, error) {
	form := url.Values{"login": {login}, "password": {password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/GetMemberAuthKey?"+url.Values{"authkey": {c.authKey}}.Encode(),
		strings.NewReader(form.Encode()))
	if err != nil {
		return Member{}, errors.New("hollihop GetMemberAuthKey: invalid base URL")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL carries the school's auth key
		}
		return Member{}, fmt.Errorf("hollihop GetMemberAuthKey: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Member{}, fmt.Errorf("hollihop GetMemberAuthKey: read response: %w", err)
	}

	var r struct {
		Member
		Error string `json:"Error"`
	}
	decodeErr := json.Unmarshal(body, &r)
	switch {
	case resp.StatusCode == http.StatusOK && decodeErr == nil && r.Error == "" && r.ID != 0:
		return r.Member, nil
	case resp.StatusCode == http.StatusInternalServerError && decodeErr == nil && r.Error != "":
		return Member{}, ErrBadCredentials
	case resp.StatusCode == http.StatusOK:
		// Don't quote it: a 200 may carry the person's own HolliHop key.
		return Member{}, errors.New("hollihop GetMemberAuthKey: unexpected 200 response")
	default:
		return Member{}, fmt.Errorf("hollihop GetMemberAuthKey: %w", statusError(resp.StatusCode, bytes.ReplaceAll(body, []byte(password), []byte("***"))))
	}
}

// GetEmployee returns one employee (fired ones included), or ok=false.
func (c *Client) GetEmployee(ctx context.Context, id int32) (e Employee, ok bool, err error) {
	body, err := c.get(ctx, "GetEmployees", url.Values{"id": {strconv.Itoa(int(id))}})
	if err != nil {
		return Employee{}, false, err
	}
	var r struct{ Employees []Employee }
	if err := json.Unmarshal(body, &r); err != nil {
		return Employee{}, false, fmt.Errorf("hollihop GetEmployees: decode response: %w", err)
	}
	if len(r.Employees) == 0 {
		return Employee{}, false, nil
	}
	return r.Employees[0], true, nil
}

// FindEmployees searches employees by name, phone or email (HolliHop's
// "term"), for granting panel access by name.
func (c *Client) FindEmployees(ctx context.Context, term string) ([]Employee, error) {
	body, err := c.get(ctx, "GetEmployees", url.Values{"term": {term}, "take": {"50"}})
	if err != nil {
		return nil, err
	}
	var r struct{ Employees []Employee }
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("hollihop GetEmployees: decode response: %w", err)
	}
	return r.Employees, nil
}
