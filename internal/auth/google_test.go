package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
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

	// A new Google account: the web gets a ticket, and goes on to a phone.
	v := a.roundTrip("/practice/DAY1?tab=math", a.claims("g-1"))
	if v.Get("return") != "/practice/DAY1?tab=math" || v.Get("ticket") == "" {
		t.Fatalf("fragment = %v", v)
	}
	r := a.trade(v.Get("ticket"))
	if r.status != http.StatusOK || r.body["status"] != "needs_phone" || r.body["first_name"] != "Malika" ||
		r.body["last_name"] != "Yusupova" || r.body["email"] != "g-1@gmail.com" || r.body["google_ticket"] == "" {
		t.Fatalf("trade: %d %v", r.status, r.body)
	}
	if r := a.trade(v.Get("ticket")); r.code() != CodeInvalidTicket {
		t.Errorf("ticket traded twice: %d %v", r.status, r.body)
	}
	googleTicket := r.body["google_ticket"].(string)
	if r := a.trade(googleTicket); r.code() != CodeInvalidTicket {
		t.Errorf("a sign-up ticket traded as a callback ticket: %d %v", r.status, r.body)
	}

	// Sign-up with the phone and no password.
	r = a.do("POST", "/v1/auth/signup", "", signupBody(a.ticket("+998903000003", PurposeSignup, googleID(googleTicket)), googleID(googleTicket), "Malika", "Yusupova", nil))
	if r.status != http.StatusOK || r.body["existing"] != false {
		t.Fatalf("signup: %d %v", r.status, r.body)
	}
	user := r.body["user"].(map[string]any)
	if user["google"] != true || user["has_password"] != false || user["must_change_password"] != false || user["status"] != StatusRegistered {
		t.Errorf("user = %v", user)
	}
	id := user["id"]
	token := r.body["token"].(string)
	if r := a.do("PUT", "/v1/me/profile", token, map[string]any{"first_name": "Mali", "last_name": "Yusupova"}); r.status != http.StatusOK {
		t.Errorf("a Google-only account is fully signed in: %d %v", r.status, r.body)
	}

	// From then on Google signs them in.
	r = a.trade(a.roundTrip("/", a.claims("g-1")).Get("ticket"))
	if r.status != http.StatusOK || r.body["status"] != "signed_in" || r.body["token"] == "" ||
		r.body["user"].(map[string]any)["id"] != id || r.body["user"].(map[string]any)["first_name"] != "Mali" {
		t.Errorf("sign in: %d %v", r.status, r.body)
	}

	// Somewhere else than this site to go back to: home.
	if v := a.roundTrip("https://evil.example/", a.claims("g-1")); v.Get("return") != "/" {
		t.Errorf("return = %q", v.Get("return"))
	}
	if v := a.roundTrip("//evil.example/x", a.claims("g-1")); v.Get("return") != "/" {
		t.Errorf("return = %q", v.Get("return"))
	}

	// A password later: then phone + password works as well (reset by SMS).
	r = a.do("POST", "/v1/auth/reset", "", map[string]string{"ticket": a.ticket("+998903000003", PurposeReset, nil), "password": "now a password"})
	if r.status != http.StatusOK || r.body["user"].(map[string]any)["has_password"] != true || r.body["user"].(map[string]any)["google"] != true {
		t.Errorf("first password: %d %v", r.status, r.body)
	}
	a.mustLogin("+998903000003", "now a password")
}

func TestGoogleOnAnExistingAccount(t *testing.T) {
	a := setupGoogle(t)

	// Two tabs started with the same new Google account.
	first := a.trade(a.roundTrip("/", a.claims("g-2")).Get("ticket")).body["google_ticket"].(string)
	second := a.trade(a.roundTrip("/", a.claims("g-2")).Get("ticket")).body["google_ticket"].(string)
	if r := a.do("POST", "/v1/auth/signup", "", signupBody(a.ticket("+998903000004", PurposeSignup, googleID(first)), googleID(first), "A", "B", nil)); r.status != http.StatusOK {
		t.Fatalf("first signup: %d %v", r.status, r.body)
	}
	// The other links the same Google account to student 1: taken.
	ticket := a.ticket("+998902001234", PurposeSignup, googleID(second))
	if r := a.do("POST", "/v1/auth/signup", "", signupBody(ticket, googleID(second), "Aziz", "Karimov", nil)); r.status != http.StatusConflict || r.code() != CodeGoogleTaken {
		t.Fatalf("taken: %d %v", r.status, r.body)
	}

	// Their own Google account (and with it, the phone again).
	own := a.trade(a.roundTrip("/", a.claims("g-3")).Get("ticket")).body["google_ticket"].(string)
	r := a.do("POST", "/v1/auth/signup", "", signupBody(a.ticket("+998902001234", PurposeSignup, googleID(own)), googleID(own), nil, nil, nil))
	if r.status != http.StatusOK || r.body["existing"] != true {
		t.Fatalf("link to student 1: %d %v", r.status, r.body)
	}
	// Still on the default password: they must choose one, as at a login.
	if u := r.body["user"].(map[string]any); u["id"] != float64(1) || u["google"] != true || u["must_change_password"] != true || u["has_password"] != false {
		t.Errorf("user = %v", u)
	}
	r = a.trade(a.roundTrip("/", a.claims("g-3")).Get("ticket"))
	token, _ := r.body["token"].(string)
	if r.body["status"] != "signed_in" || r.body["user"].(map[string]any)["must_change_password"] != true {
		t.Errorf("google sign-in: %v", r.body)
	}
	if r := a.do("PUT", "/v1/auth/username", token, map[string]string{"username": "aziz"}); r.code() != CodePasswordChangeRequired {
		t.Errorf("default password still in use: %d %v", r.status, r.body)
	}
	if r := a.do("POST", "/v1/auth/password", token, map[string]string{"current_password": "1234", "new_password": "chosen at last"}); r.status != http.StatusNoContent {
		t.Errorf("change password: %d %v", r.status, r.body)
	}
}
