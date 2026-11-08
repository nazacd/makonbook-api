package auth

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	gosync "sync"
	"testing"
	"time"
)

// fakeGoogle is Google's token endpoint: codes it handed out (as Google's
// sign-in page would), each with its PKCE challenge and the ID token claims.
type fakeGoogle struct {
	t     *testing.T
	mu    gosync.Mutex
	codes map[string]pendingCode
}

type pendingCode struct {
	challenge string
	claims    map[string]any
}

func (g *fakeGoogle) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		g.t.Error(err)
	}
	f := r.PostForm
	if f.Get("grant_type") != "authorization_code" || f.Get("client_id") != "web-client" || f.Get("client_secret") != "shh" ||
		f.Get("redirect_uri") != "https://sat.example/v1/auth/google/callback" {
		g.t.Errorf("token request: %v", f)
	}
	g.mu.Lock()
	c, ok := g.codes[f.Get("code")]
	delete(g.codes, f.Get("code"))
	g.mu.Unlock()
	sum := sha256.Sum256([]byte(f.Get("code_verifier")))
	if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != c.challenge {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		return
	}
	claims, _ := json.Marshal(c.claims)
	enc := base64.RawURLEncoding.EncodeToString
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "ya29.x", "token_type": "Bearer", "expires_in": 3599,
		"id_token": enc([]byte(`{"alg":"RS256","kid":"k"}`)) + "." + enc(claims) + ".c2ln",
	})
}

type googleAccounts struct {
	accounts
	google *fakeGoogle
}

func setupGoogle(t *testing.T) googleAccounts {
	g := &fakeGoogle{t: t, codes: map[string]pendingCode{}}
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	a := setupAccounts(t, &GoogleConfig{ClientID: "web-client", ClientSecret: "shh", WebOrigin: "https://sat.example",
		AuthURL: "https://accounts.example/o/oauth2/v2/auth", TokenURL: srv.URL})
	return googleAccounts{accounts: a, google: g}
}

// redirect is an answer that sends the browser elsewhere.
type redirect struct {
	status   int
	location string
	cookies  []*http.Cookie
}

// get asks without following redirects.
func (a googleAccounts) get(path string, cookie *http.Cookie) redirect {
	a.t.Helper()
	req, _ := http.NewRequestWithContext(a.t.Context(), http.MethodGet, a.srv.URL+path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	return redirect{status: resp.StatusCode, location: resp.Header.Get("Location"), cookies: resp.Cookies()}
}

func (a googleAccounts) claims(sub string) map[string]any {
	return map[string]any{"iss": "https://accounts.google.com", "aud": "web-client", "exp": a.clock.now().Add(time.Hour).Unix(),
		"sub": sub, "email": sub + "@gmail.com", "email_verified": true, "given_name": "Malika", "family_name": "Yusupova"}
}

// roundTrip goes to Google and back, Google answering with these claims,
// and returns the fragment the web gets.
func (a googleAccounts) roundTrip(returnPath string, claims map[string]any) url.Values {
	a.t.Helper()
	start := a.get("/v1/auth/google/start?return="+url.QueryEscape(returnPath), nil)
	if start.status != http.StatusFound {
		a.t.Fatalf("start: %d", start.status)
	}
	at, _ := url.Parse(start.location)
	cookies := start.cookies
	if len(cookies) != 1 {
		a.t.Fatalf("cookies = %v", cookies)
	}
	code := "code-" + at.Query().Get("state")[:8]
	a.google.mu.Lock()
	a.google.codes[code] = pendingCode{challenge: at.Query().Get("code_challenge"), claims: claims}
	a.google.mu.Unlock()
	return a.callback("?state="+url.QueryEscape(at.Query().Get("state"))+"&code="+code, cookies[0])
}

func (a googleAccounts) callback(query string, cookie *http.Cookie) url.Values {
	a.t.Helper()
	resp := a.get(GoogleCallbackPath+query, cookie)
	page, fragment, _ := strings.Cut(resp.location, "#")
	if resp.status != http.StatusFound || page != "https://sat.example/auth/google" {
		a.t.Fatalf("callback: %d %q", resp.status, resp.location)
	}
	v, err := url.ParseQuery(fragment)
	if err != nil {
		a.t.Fatal(err)
	}
	return v
}

func (a googleAccounts) trade(ticket string) response {
	a.t.Helper()
	return a.do("POST", "/v1/auth/google/ticket", "", map[string]string{"ticket": ticket})
}

func TestGoogleSignUpAndSignIn(t *testing.T) {
	a := setupGoogle(t)

	// Start: off to Google with PKCE, the state in a cookie only the
	// callback gets.
	start := a.get("/v1/auth/google/start?return=/practice", nil)
	at, _ := url.Parse(start.location)
	q := at.Query()
	if start.status != http.StatusFound || at.Host != "accounts.example" || q.Get("client_id") != "web-client" ||
		q.Get("redirect_uri") != "https://sat.example/v1/auth/google/callback" || q.Get("response_type") != "code" ||
		q.Get("scope") != "openid email profile" || q.Get("code_challenge_method") != "S256" || len(q.Get("state")) < 40 {
		t.Fatalf("start: %d %s", start.status, at)
	}
	c := start.cookies[0]
	if c.Name != googleCookie || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/v1/auth/google/" || c.MaxAge != 600 {
		t.Errorf("cookie = %+v", c)
	}

	// The way back can fail.
	if v := a.callback("?error=access_denied&state="+q.Get("state"), c); v.Get("error") != GoogleDenied {
		t.Errorf("denied: %v", v)
	}
	if v := a.callback("?code=x&state="+q.Get("state"), nil); v.Get("error") != GoogleFailed {
		t.Errorf("no cookie: %v", v)
	}
	if v := a.callback("?code=x&state=forged", c); v.Get("error") != GoogleFailed {
		t.Errorf("wrong state: %v", v)
	}
	if v := a.callback("?code=never-issued&state="+q.Get("state"), c); v.Get("error") != GoogleFailed {
		t.Errorf("Google refuses the code: %v", v)
	}
	forged := *c
	forged.Value = strings.Replace(c.Value, ".", "x.", 1)
	if v := a.callback("?code=x&state="+q.Get("state"), &forged); v.Get("error") != GoogleFailed {
		t.Errorf("forged cookie: %v", v)
	}
	for name, change := range map[string]func(map[string]any){
		"audience":   func(m map[string]any) { m["aud"] = "someone-else" },
		"issuer":     func(m map[string]any) { m["iss"] = "https://evil.example" },
		"expired":    func(m map[string]any) { m["exp"] = a.clock.now().Add(-time.Minute).Unix() },
		"unverified": func(m map[string]any) { m["email_verified"] = false },
	} {
		claims := a.claims("g-bad")
		change(claims)
		if v := a.roundTrip("/", claims); v.Get("error") != GoogleFailed || v.Get("ticket") != "" {
			t.Errorf("%s: %v", name, v)
		}
	}

	// A new Google account: an account at once, with no phone (the content
	// waits for one), the email and a copy of the photo.
	img := photoServer(t)
	a.svc.photos.allowHTTP = true
	claims := a.claims("g-1")
	claims["picture"] = img.URL + "/me.png"
	v := a.roundTrip("/practice/DAY1?tab=math", claims)
	if v.Get("return") != "/practice/DAY1?tab=math" || v.Get("ticket") == "" {
		t.Fatalf("fragment = %v", v)
	}
	r := a.trade(v.Get("ticket"))
	user, _ := r.body["user"].(map[string]any)
	if r.status != http.StatusOK || r.body["token"] == "" || user["status"] != StatusRegistered || user["phone"] != nil ||
		user["email"] != "g-1@gmail.com" || user["google"] != true || user["has_password"] != false || user["first_name"] != "Malika" {
		t.Fatalf("trade: %d %v", r.status, r.body)
	}
	if r := a.trade(v.Get("ticket")); r.code() != CodeInvalidTicket {
		t.Errorf("ticket traded twice: %d %v", r.status, r.body)
	}
	token := r.body["token"].(string)
	me := a.do("GET", "/v1/me", token, nil)
	photo, _ := me.body["photo_url"].(string)
	if !strings.HasPrefix(photo, "/v1/photos/") {
		t.Fatalf("photo_url = %v", me.body["photo_url"])
	}
	resp, err := a.srv.Client().Get(a.srv.URL + photo) //nolint:noctx // test
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/png" || !bytes.Equal(body, pngBytes) ||
		!strings.Contains(resp.Header.Get("Cache-Control"), "max-age=31536000") {
		t.Errorf("photo: %d %v", resp.StatusCode, resp.Header)
	}

	// From then on Google signs them in.
	id := user["id"]
	r = a.trade(a.roundTrip("/", a.claims("g-1")).Get("ticket"))
	if r.status != http.StatusOK || r.body["user"].(map[string]any)["id"] != id {
		t.Errorf("sign in: %d %v", r.status, r.body)
	}

	// Somewhere else than this site to go back to: home.
	if v := a.roundTrip("https://evil.example/", a.claims("g-1")); v.Get("return") != "/" {
		t.Errorf("return = %q", v.Get("return"))
	}
	if v := a.roundTrip("//evil.example/x", a.claims("g-1")); v.Get("return") != "/" {
		t.Errorf("return = %q", v.Get("return"))
	}

	// A first password, in Profile.
	if r := a.do("POST", "/v1/auth/password", token, map[string]string{"current_password": "", "new_password": "now a password"}); r.status != http.StatusNoContent {
		t.Errorf("first password: %d %v", r.status, r.body)
	}
	a.mustLogin("g-1@gmail.com", "now a password")
}

// A Google account whose email someone has: linked to them; several Google
// accounts per person.
func TestGoogleOnAnExistingAccount(t *testing.T) {
	a := setupGoogle(t)
	_, u := a.emailSignup("g-2@gmail.com", "Email")
	r := a.trade(a.roundTrip("/", a.claims("g-2")).Get("ticket"))
	if r.status != http.StatusOK || r.body["user"].(map[string]any)["id"] != u["id"] || r.body["user"].(map[string]any)["google"] != true {
		t.Fatalf("by email: %d %v", r.status, r.body)
	}
	if _, err := a.pool.Exec(t.Context(), "UPDATE credentials SET email = 'g-3@gmail.com' WHERE person_id = $1", int32(u["id"].(float64))); err != nil {
		t.Fatal(err)
	}
	r = a.trade(a.roundTrip("/", a.claims("g-3")).Get("ticket"))
	if r.body["user"].(map[string]any)["id"] != u["id"] {
		t.Errorf("second Google account: %v", r.body)
	}
	var n int
	if err := a.pool.QueryRow(t.Context(), "SELECT count(*) FROM google_links WHERE person_id = $1", int32(u["id"].(float64))).Scan(&n); err != nil || n != 2 {
		t.Errorf("Google links = %d %v", n, err)
	}
	// Both sign in.
	if r := a.trade(a.roundTrip("/", a.claims("g-2")).Get("ticket")); r.body["user"].(map[string]any)["id"] != u["id"] {
		t.Errorf("first again: %v", r.body)
	}
}

var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{7}, 64)...)

// photoServer serves a small PNG at any path, like a provider's CDN.
func photoServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream") // what the bytes are counts
		_, _ = w.Write(pngBytes)
	}))
	t.Cleanup(srv.Close)
	return srv
}
