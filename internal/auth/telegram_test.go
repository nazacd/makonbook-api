package auth

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	gosync "sync"
	"testing"
	"time"
)

// fakeTelegram is oauth.telegram.org: the token endpoint (codes handed out by
// the test, as the consent page would) and the JWKS, with an RSA key and an
// EC key; tokens are signed with the one in alg.
type fakeTelegram struct {
	t     *testing.T
	rsa   *rsa.PrivateKey
	ec    *ecdsa.PrivateKey
	mu    gosync.Mutex
	codes map[string]pendingCode
	alg   string // RS256 (default) or ES256
	forge bool   // sign with a key that isn't published
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	r, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	e, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeTelegram{t: t, rsa: r, ec: e, codes: map[string]pendingCode{}, alg: "RS256"}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (f *fakeTelegram) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/jwks":
		ecPub, err := f.ec.PublicKey.Bytes()
		if err != nil {
			f.t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{
			{"kty": "RSA", "kid": "r1", "alg": "RS256", "use": "sig", "n": b64(f.rsa.N.Bytes()), "e": b64(big.NewInt(int64(f.rsa.E)).Bytes())},
			{"kty": "EC", "kid": "e1", "crv": "P-256", "x": b64(ecPub[1:33]), "y": b64(ecPub[33:])},
		}})
	case "/token":
		if err := r.ParseForm(); err != nil {
			f.t.Error(err)
		}
		id, secret, ok := r.BasicAuth()
		fm := r.PostForm
		if !ok || id != "8123456789" || secret != "tg-secret" || fm.Get("grant_type") != "authorization_code" ||
			fm.Get("client_id") != "8123456789" || fm.Get("redirect_uri") != "https://sat.example/v1/auth/telegram/callback" {
			f.t.Errorf("token request: %v %v", r.Header.Get("Authorization"), fm)
		}
		f.mu.Lock()
		c, found := f.codes[fm.Get("code")]
		delete(f.codes, fm.Get("code"))
		f.mu.Unlock()
		sum := sha256.Sum256([]byte(fm.Get("code_verifier")))
		if !found || b64(sum[:]) != c.challenge {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "x", "token_type": "Bearer", "expires_in": 3600, "id_token": f.sign(c.claims)})
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeTelegram) sign(claims map[string]any) string {
	kid := "r1"
	if f.alg == "ES256" {
		kid = "e1"
	}
	header, _ := json.Marshal(map[string]string{"alg": f.alg, "kid": kid, "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	input := b64(header) + "." + b64(payload)
	digest := sha256.Sum256([]byte(input))
	var sig []byte
	if f.alg == "ES256" {
		rr, ss, err := ecdsa.Sign(rand.Reader, f.ec, digest[:])
		if err != nil {
			f.t.Fatal(err)
		}
		sig = append(append(make([]byte, 32-len(rr.Bytes())), rr.Bytes()...), append(make([]byte, 32-len(ss.Bytes())), ss.Bytes()...)...)
	} else {
		key := f.rsa
		if f.forge {
			key, _ = rsa.GenerateKey(rand.Reader, 2048)
		}
		var err error
		if sig, err = rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:]); err != nil {
			f.t.Fatal(err)
		}
	}
	return input + "." + b64(sig)
}

type tgAccounts struct {
	googleAccounts
	tg *fakeTelegram
}

func setupTelegram(t *testing.T) tgAccounts {
	a := setupGoogle(t)
	tg := newFakeTelegram(t)
	srv := httptest.NewServer(tg)
	t.Cleanup(srv.Close)
	a.svc.WithTelegram(TelegramConfig{ClientID: "8123456789", ClientSecret: "tg-secret", WebOrigin: "https://sat.example",
		AuthURL: "https://oauth.example/auth", TokenURL: srv.URL + "/token", JWKSURL: srv.URL + "/jwks"})
	a.svc.photos.allowHTTP = true
	return tgAccounts{googleAccounts: a, tg: tg}
}

func (a tgAccounts) tgClaims(id int64, phone string) map[string]any {
	c := map[string]any{"iss": "https://oauth.telegram.org", "aud": "8123456789", "sub": "x", "iat": a.clock.now().Unix(),
		"exp": a.clock.now().Add(time.Hour).Unix(), "id": id, "name": "Diyora Abdullayeva", "given_name": "Diyora",
		"family_name": "Abdullayeva", "preferred_username": "diyora"}
	if phone != "" {
		c["phone_number"], c["phone_number_verified"] = phone, true
	}
	return c
}

// tgRoundTrip starts the flow (token: for verify), lets "Telegram" approve it
// with claims, and returns the web page's query.
func (a tgAccounts) tgRoundTrip(purpose, token string, claims map[string]any) url.Values {
	a.t.Helper()
	r := a.do("POST", "/v1/auth/telegram/start", token, map[string]any{"purpose": purpose, "return": "/practice"})
	if r.status != http.StatusOK {
		a.t.Fatalf("start: %d %v", r.status, r.body)
	}
	at, _ := url.Parse(r.body["url"].(string))
	q := at.Query()
	if at.Host != "oauth.example" || q.Get("client_id") != "8123456789" || q.Get("scope") != "openid profile phone" ||
		q.Get("redirect_uri") != "https://sat.example/v1/auth/telegram/callback" || q.Get("code_challenge_method") != "S256" {
		a.t.Fatalf("auth url = %s", at)
	}
	code := "code-" + q.Get("state")[:8]
	a.tg.mu.Lock()
	a.tg.codes[code] = pendingCode{challenge: q.Get("code_challenge"), claims: claims}
	a.tg.mu.Unlock()
	return a.tgCallback("?state=" + url.QueryEscape(q.Get("state")) + "&code=" + code)
}

func (a tgAccounts) tgCallback(query string) url.Values {
	a.t.Helper()
	resp := a.get(TelegramCallbackPath+query, nil)
	page, rawQuery, _ := strings.Cut(resp.location, "?")
	if resp.status != http.StatusFound || page != "https://sat.example/auth/telegram" {
		a.t.Fatalf("callback: %d %q", resp.status, resp.location)
	}
	v, _ := url.ParseQuery(rawQuery)
	return v
}

func (a tgAccounts) tgSignIn(claims map[string]any) response {
	a.t.Helper()
	v := a.tgRoundTrip(TelegramSignIn, "", claims)
	if v.Get("purpose") != TelegramSignIn || v.Get("return") != "/practice" || v.Get("ticket") == "" {
		a.t.Fatalf("callback query = %v", v)
	}
	return a.do("POST", "/v1/auth/telegram/ticket", "", map[string]string{"ticket": v.Get("ticket")})
}

func TestTelegramCallbackChecks(t *testing.T) {
	a := setupTelegram(t)
	if v := a.tgCallback("?state=forged&code=x"); v.Get("error") != TelegramFailed {
		t.Errorf("unknown state: %v", v)
	}
	r := a.do("POST", "/v1/auth/telegram/start", "", map[string]any{"purpose": "signin"})
	st, _ := url.Parse(r.body["url"].(string))
	if v := a.tgCallback("?state=" + url.QueryEscape(st.Query().Get("state")) + "&error=access_denied"); v.Get("error") != TelegramDenied {
		t.Errorf("declined: %v", v)
	}
	if r := a.do("POST", "/v1/auth/telegram/start", "", map[string]any{"purpose": "verify"}); r.status != http.StatusUnauthorized {
		t.Errorf("verify without a session: %d", r.status)
	}
	for name, change := range map[string]func(map[string]any){
		"audience": func(c map[string]any) { c["aud"] = "999" },
		"issuer":   func(c map[string]any) { c["iss"] = "https://evil.example" },
		"expired":  func(c map[string]any) { c["exp"] = a.clock.now().Add(-time.Minute).Unix() },
		"no id":    func(c map[string]any) { delete(c, "id") },
	} {
		c := a.tgClaims(1, "")
		change(c)
		if v := a.tgRoundTrip(TelegramSignIn, "", c); v.Get("error") != TelegramFailed {
			t.Errorf("%s: %v", name, v)
		}
	}
	a.tg.forge = true
	if v := a.tgRoundTrip(TelegramSignIn, "", a.tgClaims(1, "")); v.Get("error") != TelegramFailed {
		t.Errorf("forged signature: %v", v)
	}
	a.tg.forge, a.tg.alg = false, "ES256"
	if r := a.tgSignIn(a.tgClaims(1, "")); r.status != http.StatusOK {
		t.Errorf("ES256: %d %v", r.status, r.body)
	}
}

func TestTelegramSignIn(t *testing.T) {
	a := setupTelegram(t)
	img := photoServer(t)

	// c) New, with the phone shared (any country) and a photo.
	c := a.tgClaims(7001, "971501234567")
	c["picture"] = img.URL + "/tg.jpg"
	r := a.tgSignIn(c)
	u, _ := r.body["user"].(map[string]any)
	if r.status != http.StatusOK || u["status"] != StatusRegistered || u["phone"] != "+971501234567" || u["telegram"] != true ||
		u["first_name"] != "Diyora" || u["last_name"] != "Abdullayeva" || u["has_password"] != false || u["photo_url"] == nil {
		t.Fatalf("new: %d %v", r.status, r.body)
	}
	if me := a.do("GET", "/v1/me", r.body["token"].(string), nil); me.body["photo_url"] == nil {
		t.Errorf("no photo: %v", me.body)
	}
	// a) Linked: the same account.
	if r := a.tgSignIn(a.tgClaims(7001, "")); r.body["user"].(map[string]any)["id"] != u["id"] {
		t.Errorf("linked: %v", r.body)
	}

	// b) The phone is student 1's: Telegram is linked to them; must still
	// replace the default password.
	r = a.tgSignIn(a.tgClaims(7002, "998902001234"))
	if u := r.body["user"].(map[string]any); u["id"] != float64(1) || u["telegram"] != true || u["must_change_password"] != true {
		t.Errorf("by phone: %v", r.body)
	}
	// and a teacher's.
	if r := a.tgSignIn(a.tgClaims(7003, "998901000010")); r.body["user"].(map[string]any)["kind"] != "teacher" {
		t.Errorf("teacher: %v", r.body)
	}

	// No phone shared: an account without one (the content is locked).
	r = a.tgSignIn(a.tgClaims(7004, ""))
	u = r.body["user"].(map[string]any)
	if u["phone"] != nil || u["status"] != StatusRegistered {
		t.Fatalf("no phone: %v", r.body)
	}
	// a) again, now sharing a free phone: it becomes theirs.
	if r := a.tgSignIn(a.tgClaims(7004, "998903000050")); r.body["user"].(map[string]any)["phone"] != "+998903000050" ||
		r.body["user"].(map[string]any)["id"] != u["id"] {
		t.Errorf("phone added at sign-in: %v", r.body)
	}
	if r := a.do("POST", "/v1/auth/telegram/ticket", "", map[string]string{"ticket": "nope"}); r.code() != CodeInvalidTicket {
		t.Errorf("bad ticket: %d %v", r.status, r.body)
	}
}

// A signed-in person without a phone adds the one Telegram shares.
func TestTelegramVerifyPhone(t *testing.T) {
	a := setupTelegram(t)
	token, u := a.emailSignup("tg.verify@example.com", "Verify")

	// Telegram didn't share it.
	v := a.tgRoundTrip(TelegramVerify, token, a.tgClaims(8001, ""))
	if v.Get("purpose") != TelegramVerify {
		t.Fatalf("callback: %v", v)
	}
	if r := a.do("POST", "/v1/me/phone/telegram", token, map[string]string{"ticket": v.Get("ticket")}); r.status != http.StatusUnprocessableEntity || r.code() != CodeTelegramNoPhone {
		t.Errorf("no phone: %d %v", r.status, r.body)
	}
	// Another person's ticket.
	other, _ := a.emailSignup("other@example.com", "Other")
	v = a.tgRoundTrip(TelegramVerify, other, a.tgClaims(8002, "998903000060"))
	if r := a.do("POST", "/v1/me/phone/telegram", token, map[string]string{"ticket": v.Get("ticket")}); r.code() != CodeInvalidTicket {
		t.Errorf("someone else's ticket: %d %v", r.status, r.body)
	}
	// Their own, with a free number: theirs, Telegram linked, the photo copied.
	img := photoServer(t)
	c := a.tgClaims(8003, "998903000061")
	c["picture"] = img.URL + "/tg.jpg"
	v = a.tgRoundTrip(TelegramVerify, token, c)
	r := a.do("POST", "/v1/me/phone/telegram", token, map[string]string{"ticket": v.Get("ticket")})
	if me := r.body["user"]; r.status != http.StatusOK || me.(map[string]any)["phone"] != "+998903000061" || me.(map[string]any)["telegram"] != true ||
		me.(map[string]any)["id"] != u["id"] || me.(map[string]any)["photo_url"] == nil {
		t.Fatalf("verify: %d %v", r.status, r.body)
	}
	if r := a.do("POST", "/v1/auth/telegram/start", token, map[string]any{"purpose": "verify"}); r.code() != CodePhoneAlreadySet {
		t.Errorf("verify with a phone: %d %v", r.status, r.body)
	}
	// Student 1's number: merged into them; they get the Telegram photo.
	c = a.tgClaims(8004, "998902001234")
	c["picture"] = img.URL + "/tg2.jpg"
	v = a.tgRoundTrip(TelegramVerify, other, c)
	r = a.do("POST", "/v1/me/phone/telegram", other, map[string]string{"ticket": v.Get("ticket")})
	if me := r.body["user"].(map[string]any); r.status != http.StatusOK || me["id"] != float64(1) || me["email"] != "other@example.com" ||
		me["telegram"] != true || me["photo_url"] == nil {
		t.Errorf("merge: %d %v", r.status, r.body)
	}
}
