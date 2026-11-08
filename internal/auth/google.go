package auth

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nazacd/makonbook-api/internal/auth/authdb"
	"github.com/nazacd/makonbook-api/internal/people"
)

// Google sign-in (spec 21): the OAuth authorization code flow with PKCE, run
// by the API, so the web's pages load nothing from Google. The web sends the
// browser to /v1/auth/google/start; Google sends it back to
// /v1/auth/google/callback on the web's origin (whose /v1/ is this API),
// which hands the web a one-time ticket in the URL fragment; the web trades
// it at POST /v1/auth/google/ticket for a session, or for a Google ticket to
// sign up with once a phone is verified.

// Google's endpoints.
//
//nolint:gosec // G101: addresses, not credentials
const (
	GoogleAuthURL  = "https://accounts.google.com/o/oauth2/v2/auth"
	GoogleTokenURL = "https://oauth2.googleapis.com/token"
)

const (
	// GoogleCallbackPath is where Google sends people back, on WebOrigin.
	GoogleCallbackPath = "/v1/auth/google/callback"
	// googleWebPath is the web's page that finishes the sign-in.
	googleWebPath   = "/auth/google"
	googleCookie    = "mb_google"
	googleStateTTL  = 10 * time.Minute
	GoogleTicketTTL = 10 * time.Minute // from the callback to the web's POST
	GoogleSignupTTL = 30 * time.Minute // for the phone and the sign-up form
	maxReturnLength = 500
)

// Reasons the callback sends the web instead of a ticket (#error=…).
const (
	GoogleDenied = "google_denied" // the person cancelled at Google
	GoogleFailed = "google_failed" // anything else
)

// GoogleConfig turns on Google sign-in.
type GoogleConfig struct {
	ClientID, ClientSecret string
	// WebOrigin is the web app's origin, e.g. https://sat.makonbook.uz; its
	// /v1/ is this API.
	WebOrigin string
	// AuthURL and TokenURL are Google's (GoogleAuthURL, GoogleTokenURL)
	// unless set; tests use their own.
	AuthURL, TokenURL string
	Client            *http.Client // a client with a 15 s timeout when nil
}

// WithGoogle turns on Google sign-in.
func (s *Service) WithGoogle(c GoogleConfig) *Service {
	if c.AuthURL == "" {
		c.AuthURL = GoogleAuthURL
	}
	if c.TokenURL == "" {
		c.TokenURL = GoogleTokenURL
	}
	if c.Client == nil {
		c.Client = &http.Client{Timeout: 15 * time.Second}
	}
	s.google = &c
	return s
}

// GoogleEnabled: Google sign-in is configured.
func (s *Service) GoogleEnabled() bool { return s.google != nil }

// googleState is what the start keeps for the callback, in a cookie signed
// with a key derived from the client secret.
type googleState struct {
	State    string `json:"s"`
	Verifier string `json:"v"` // PKCE
	Return   string `json:"r"`
	Expires  int64  `json:"e"`
}

// GoogleStart begins a sign-in: the address at Google to send the browser
// to, and the cookie value the callback checks. returnPath is where the web
// goes afterwards: a path on the web's origin, "/" otherwise.
func (s *Service) GoogleStart(returnPath string) (redirect, cookie string, err error) {
	g := s.google
	if g == nil {
		return "", "", ErrGoogleOff
	}
	st := googleState{Return: safeReturn(returnPath), Expires: s.now().Add(googleStateTTL).Unix()}
	if st.State, err = randomString(); err != nil {
		return "", "", err
	}
	if st.Verifier, err = randomString(); err != nil {
		return "", "", err
	}
	challenge := sha256.Sum256([]byte(st.Verifier))
	q := url.Values{
		"client_id":             {g.ClientID},
		"redirect_uri":          {g.WebOrigin + GoogleCallbackPath},
		"response_type":         {"code"},
		"scope":                 {"openid email profile"},
		"state":                 {st.State},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
		"prompt":                {"select_account"},
	}
	payload, _ := json.Marshal(st)
	enc := base64.RawURLEncoding.EncodeToString(payload)
	return g.AuthURL + "?" + q.Encode(), enc + "." + base64.RawURLEncoding.EncodeToString(s.stateMAC(enc)), nil
}

// ErrGoogleOff: Google sign-in isn't configured.
var ErrGoogleOff = errors.New("sign-in with Google isn't available")

// GoogleCallback finishes the round trip at Google: query is what Google
// sent back, cookie what GoogleStart set. It returns where to send the
// browser: the web's page with #ticket=…&return=… or #error=…; reason
// explains a failure, for the log.
func (s *Service) GoogleCallback(ctx context.Context, cookie string, query url.Values) (location string, reason error) {
	g := s.google
	fail := func(code string, err error) (string, error) {
		return g.WebOrigin + googleWebPath + "#error=" + code, err
	}
	if e := query.Get("error"); e != "" {
		if e == "access_denied" {
			return fail(GoogleDenied, nil)
		}
		return fail(GoogleFailed, fmt.Errorf("google answered %q", e))
	}
	st, err := s.readState(cookie)
	if err != nil {
		return fail(GoogleFailed, err)
	}
	if subtle.ConstantTimeCompare([]byte(query.Get("state")), []byte(st.State)) != 1 {
		return fail(GoogleFailed, errors.New("state doesn't match the cookie's"))
	}
	code := query.Get("code")
	if code == "" {
		return fail(GoogleFailed, errors.New("no code"))
	}
	account, err := s.exchange(ctx, code, st.Verifier)
	if err != nil {
		return fail(GoogleFailed, err)
	}
	ticket, err := s.newTicket(ctx, s.q, ticketData{purpose: ticketGoogle, google: &account}, GoogleTicketTTL)
	if err != nil {
		return fail(GoogleFailed, err)
	}
	return g.WebOrigin + googleWebPath + "#ticket=" + ticket + "&return=" + url.QueryEscape(st.Return), nil
}

func (s *Service) stateMAC(payload string) []byte {
	k := hmac.New(sha256.New, []byte(s.google.ClientSecret))
	k.Write([]byte("makonbook google sign-in state v1"))
	m := hmac.New(sha256.New, k.Sum(nil))
	m.Write([]byte(payload))
	return m.Sum(nil)
}

func (s *Service) readState(cookie string) (googleState, error) {
	enc, sig, ok := strings.Cut(cookie, ".")
	if !ok {
		return googleState{}, errors.New("no sign-in cookie (expired, or another browser)")
	}
	mac, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(mac, s.stateMAC(enc)) {
		return googleState{}, errors.New("sign-in cookie isn't ours")
	}
	payload, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return googleState{}, err
	}
	var st googleState
	if err := json.Unmarshal(payload, &st); err != nil {
		return googleState{}, err
	}
	if s.now().Unix() > st.Expires {
		return googleState{}, errors.New("sign-in cookie expired")
	}
	return st, nil
}

// exchange trades the code for Google's tokens and reads the ID token. The
// token comes straight from Google over TLS, authenticated with the client
// secret, so its claims are checked (issuer, audience, expiry, a verified
// email) but not its signature (OpenID Connect Core 3.1.3.7).
func (s *Service) exchange(ctx context.Context, code, verifier string) (GoogleAccount, error) {
	g := s.google
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {g.ClientID},
		"client_secret": {g.ClientSecret},
		"redirect_uri":  {g.WebOrigin + GoogleCallbackPath},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return GoogleAccount{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := g.Client.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return GoogleAccount{}, fmt.Errorf("google token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return GoogleAccount{}, fmt.Errorf("google token: %w", err)
	}
	var tok struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(body, &tok); err != nil || resp.StatusCode != http.StatusOK || tok.IDToken == "" {
		return GoogleAccount{}, fmt.Errorf("google token: status %d %s", resp.StatusCode, tok.Error)
	}
	return s.idToken(tok.IDToken)
}

// idToken reads and checks an ID token's claims.
func (s *Service) idToken(token string) (GoogleAccount, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return GoogleAccount{}, errors.New("id token: not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return GoogleAccount{}, fmt.Errorf("id token: %w", err)
	}
	var c struct {
		Iss           string          `json:"iss"`
		Aud           json.RawMessage `json:"aud"`
		Exp           json.Number     `json:"exp"`
		Sub           string          `json:"sub"`
		Email         string          `json:"email"`
		EmailVerified any             `json:"email_verified"` // true, or "true" in older tokens
		GivenName     string          `json:"given_name"`
		Picture       string          `json:"picture"`
		FamilyName    string          `json:"family_name"`
		Name          string          `json:"name"`
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	if err := dec.Decode(&c); err != nil {
		return GoogleAccount{}, fmt.Errorf("id token: %w", err)
	}
	if c.Iss != "https://accounts.google.com" && c.Iss != "accounts.google.com" {
		return GoogleAccount{}, fmt.Errorf("id token: issuer %q", c.Iss)
	}
	if !audience(c.Aud, s.google.ClientID) {
		return GoogleAccount{}, errors.New("id token: not for this client")
	}
	if exp, err := c.Exp.Int64(); err != nil || s.now().Unix() >= exp {
		return GoogleAccount{}, errors.New("id token: expired")
	}
	if c.Sub == "" || c.Email == "" {
		return GoogleAccount{}, errors.New("id token: no account or email")
	}
	if v := fmt.Sprint(c.EmailVerified); v != "true" {
		return GoogleAccount{}, errors.New("id token: email not verified")
	}
	first, last := strings.TrimSpace(c.GivenName), strings.TrimSpace(c.FamilyName)
	if first == "" && last == "" {
		first, last, _ = strings.Cut(strings.TrimSpace(c.Name), " ")
	}
	acc := GoogleAccount{Sub: c.Sub, Email: strings.ToLower(c.Email), FirstName: first, LastName: strings.TrimSpace(last)}
	if strings.HasPrefix(c.Picture, "https://") || strings.HasPrefix(c.Picture, "http://") { // copyPhoto takes https only
		acc.PhotoURL = c.Picture
	}
	return acc, nil
}

func audience(raw json.RawMessage, clientID string) bool {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one == clientID
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		for _, a := range many {
			if a == clientID {
				return true
			}
		}
	}
	return false
}

// GoogleTicket signs in with the ticket the callback gave the web:
//   - the Google account is linked to someone active: them;
//   - else someone active has its (verified) email: Google is linked to them;
//   - else a registered student is made with no phone: names, the email, the
//     photo, Google linked.
func (s *Service) GoogleTicket(ctx context.Context, ticket, deviceID string) (LoginResult, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return LoginResult{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := authdb.New(tx)
	t, err := s.useTicket(ctx, q, ticket, ticketGoogle, s.now())
	if err != nil {
		return LoginResult{}, err
	}
	g := *t.google
	dir := people.NewDirectory(tx)

	var p people.Person
	found := false
	if link, err := q.GoogleLinkBySub(ctx, g.Sub); err == nil {
		lp, err := dir.Get(ctx, link.PersonKind, link.PersonID)
		if err != nil && !errors.Is(err, people.ErrNotFound) {
			return LoginResult{}, err
		}
		if err == nil && lp.Active {
			p, found = lp, true
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return LoginResult{}, fmt.Errorf("google link: %w", err)
	}
	newEmail := false
	if !found {
		o, active, err := emailOwner(ctx, q, dir, g.Email)
		if err != nil {
			return LoginResult{}, err
		}
		if active {
			p = o
		} else {
			first, last := personNames(g.FirstName, g.LastName, strings.Split(g.Email, "@")[0])
			id, err := people.CreateSelfStudent(ctx, tx, first, last, "")
			if err != nil {
				return LoginResult{}, err
			}
			if p, err = dir.Get(ctx, people.KindStudent, id); err != nil {
				return LoginResult{}, err
			}
			newEmail = true
		}
	}
	if newEmail {
		if err := linkEmail(ctx, q, dir, p, g.Email); err != nil {
			return LoginResult{}, err
		}
	}
	if err := s.linkGoogle(ctx, q, dir, p, g); err != nil {
		return LoginResult{}, err
	}
	res, err := s.signInPerson(ctx, q, p, deviceID)
	if err != nil {
		return LoginResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return LoginResult{}, err
	}
	s.ensurePhoto(ctx, p, g.PhotoURL)
	return res, nil
}

// safeReturn keeps a return address that is a path on the web's own origin;
// anything else (another site, "//host", a backslash trick) becomes "/".
func safeReturn(p string) string {
	if p == "" || len(p) > maxReturnLength || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") ||
		strings.ContainsAny(p, "\\\r\n\t") {
		return "/"
	}
	u, err := url.Parse(p)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return "/"
	}
	return p
}

func randomString() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
