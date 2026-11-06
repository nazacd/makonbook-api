package auth

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	gosync "sync"
	"testing"
	"time"
)

const (
	tgToken  = "123456:test-token"
	tgSecret = "webhook-secret-for-tests"
)

// fakeBotAPI is the Bot API: it records each call (method and JSON body).
type fakeBotAPI struct {
	mu    gosync.Mutex
	calls []botCall
}

type botCall struct {
	Method string
	Body   map[string]any
}

func (f *fakeBotAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := strings.TrimPrefix(r.URL.Path, "/bot"+tgToken+"/")
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.calls = append(f.calls, botCall{method, body})
	f.mu.Unlock()
	_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
}

func (f *fakeBotAPI) last(t *testing.T) botCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("no Bot API call")
	}
	return f.calls[len(f.calls)-1]
}

type tgAccounts struct {
	accounts
	bot *fakeBotAPI
}

func setupTelegram(t *testing.T) tgAccounts {
	a := setupAccounts(t, nil)
	bot := &fakeBotAPI{}
	srv := httptest.NewServer(bot)
	t.Cleanup(srv.Close)
	a.svc.WithTelegram(TelegramConfig{Token: tgToken, Username: "SatMakonBot", WebhookSecret: tgSecret, APIURL: srv.URL})
	return tgAccounts{accounts: a, bot: bot}
}

// signed is Login Widget data for a Telegram account, signed as Telegram
// signs it.
func signed(id int64, first string, at time.Time) map[string]any {
	f := map[string]any{"id": id, "first_name": first, "last_name": "Yusupova", "username": "malika_y", "auth_date": at.Unix()}
	var lines []string
	for k, v := range f {
		lines = append(lines, fmt.Sprintf("%s=%v", k, v))
	}
	sort.Strings(lines)
	key := sha256.Sum256([]byte(tgToken))
	m := hmac.New(sha256.New, key[:])
	m.Write([]byte(strings.Join(lines, "\n")))
	f["hash"] = hex.EncodeToString(m.Sum(nil))
	return f
}

func (a tgAccounts) signIn(auth map[string]any) response {
	a.t.Helper()
	return a.do("POST", "/v1/auth/telegram", "", map[string]any{"auth": auth})
}

// webhook posts an update as Telegram would.
func (a tgAccounts) webhook(secret string, update map[string]any) int {
	a.t.Helper()
	b, _ := json.Marshal(update)
	req, _ := http.NewRequestWithContext(a.t.Context(), http.MethodPost, a.srv.URL+TelegramWebhookPath, bytes.NewReader(b))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
	resp, err := a.srv.Client().Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func message(from int64, text string, contact map[string]any) map[string]any {
	m := map[string]any{"message_id": 1, "from": map[string]any{"id": from, "first_name": "M"},
		"chat": map[string]any{"id": from, "type": "private"}, "text": text}
	if contact != nil {
		m["contact"] = contact
	}
	return map[string]any{"update_id": 1, "message": m}
}

func TestTelegramAuthHash(t *testing.T) {
	a := setupTelegram(t)
	now := a.clock.now()
	raw := func(m map[string]any) json.RawMessage { b, _ := json.Marshal(m); return b }
	if acc, err := a.svc.checkTelegramAuth(raw(signed(777, "Malika", now.Add(-time.Hour)))); err != nil || acc.ID != 777 || acc.Username != "malika_y" {
		t.Errorf("good: %+v %v", acc, err)
	}
	tampered := signed(777, "Malika", now)
	tampered["first_name"] = "Someone"
	stale := signed(777, "Malika", now.Add(-25*time.Hour))
	noHash := signed(777, "Malika", now)
	delete(noHash, "hash")
	for name, m := range map[string]map[string]any{"tampered": tampered, "stale": stale, "no hash": noHash} {
		if _, err := a.svc.checkTelegramAuth(raw(m)); !errors.Is(err, ErrInvalidTelegram) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if r := a.signIn(tampered); r.status != http.StatusUnprocessableEntity || r.code() != CodeInvalidTelegram {
		t.Errorf("tampered over HTTP: %d %v", r.status, r.body)
	}
}

func TestTelegramSignIn(t *testing.T) {
	a := setupTelegram(t)
	r := a.signIn(signed(777, "Malika", a.clock.now()))
	if r.status != http.StatusOK || r.body["status"] != "needs_phone" || r.body["first_name"] != "Malika" ||
		r.body["last_name"] != "Yusupova" || r.body["telegram_ticket"] == "" {
		t.Fatalf("needs phone: %d %v", r.status, r.body)
	}
	id := map[string]any{"telegram_ticket": r.body["telegram_ticket"]}

	// Telegram as the identity; no password needed.
	ticket := a.ticket("+998903000011", PurposeSignup, id)
	r = a.do("POST", "/v1/auth/signup", "", signupBody(ticket, id, "Malika", "Yusupova", nil))
	if u, _ := r.body["user"].(map[string]any); r.status != http.StatusOK || u["telegram"] != true || u["has_password"] != false || u["status"] != StatusRegistered {
		t.Fatalf("signup: %d %v", r.status, r.body)
	}
	userID := r.body["user"].(map[string]any)["id"]

	// Linked: Telegram signs them in.
	r = a.signIn(signed(777, "Malika", a.clock.now()))
	if r.status != http.StatusOK || r.body["status"] != "signed_in" || r.body["token"] == "" || r.body["user"].(map[string]any)["id"] != userID {
		t.Errorf("signed in: %d %v", r.status, r.body)
	}

	// A new Telegram account shows one phone's owner at most.
	other := map[string]any{"telegram_ticket": a.signIn(signed(888, "Aziz", a.clock.now())).body["telegram_ticket"]}
	if r := a.verified("+998902001234", PurposeSignup, other); r.body["account"].(map[string]any)["status"] != StatusEnrolled {
		t.Errorf("enrolled: %v", r.body)
	}
	a.clock.add(2 * time.Minute)
	a.start("+998901000010", PurposeSignup)
	if r := a.verify("+998901000010", a.sms.code(t, "+998901000010", 1), PurposeSignup, other); r.code() != CodeIdentitySpent {
		t.Errorf("spent: %d %v", r.status, r.body)
	}

	// Account 777 again, from a second tab before the link: taken by then.
	stale := map[string]any{"telegram_ticket": a.signIn(signed(999, "X", a.clock.now())).body["telegram_ticket"]}
	if r := a.do("POST", "/v1/auth/signup", "", signupBody(a.ticket("+998903000012", PurposeSignup, stale), stale, "X", "Y", nil)); r.status != http.StatusOK {
		t.Fatalf("999's signup: %d %v", r.status, r.body)
	}
	if _, err := a.pool.Exec(t.Context(), "INSERT INTO auth_tickets (token_hash, purpose, telegram_id, created_at, expires_at) VALUES ($1, 'telegram_signup', 999, now(), now() + interval '1 hour')",
		hashToken("second-tab")); err != nil {
		t.Fatal(err)
	}
	second := map[string]any{"telegram_ticket": "second-tab"}
	ticket = a.ticket("+998902001234", PurposeSignup, second)
	if r := a.do("POST", "/v1/auth/signup", "", signupBody(ticket, second, nil, nil, nil)); r.status != http.StatusConflict || r.code() != CodeTelegramTaken {
		t.Errorf("taken: %d %v", r.status, r.body)
	}
}

func TestTelegramPhoneCheck(t *testing.T) {
	a := setupTelegram(t)
	start := func(body map[string]any) response { return a.do("POST", "/v1/auth/phone/telegram/start", "", body) }
	check := func(nonce any) response {
		return a.do("POST", "/v1/auth/phone/telegram/check", "", map[string]any{"nonce": nonce})
	}

	if r := start(map[string]any{"purpose": "signup"}); r.code() != CodeInvalidTicket {
		t.Errorf("no identity: %d %v", r.status, r.body)
	}
	id := a.emailID("aziz@example.com")
	body := map[string]any{"purpose": "signup"}
	for k, v := range id {
		body[k] = v
	}
	r := start(body)
	nonce, _ := r.body["nonce"].(string)
	if r.status != http.StatusOK || r.body["link"] != "https://t.me/SatMakonBot?start="+nonce || r.body["expires_in"] != float64(600) {
		t.Fatalf("start: %d %v", r.status, r.body)
	}
	if r := check(nonce); r.status != http.StatusOK || r.body["status"] != "pending" {
		t.Errorf("pending: %d %v", r.status, r.body)
	}

	// The webhook: only Telegram, with the secret.
	if code := a.webhook("wrong", message(42, "/start "+nonce, nil)); code != http.StatusForbidden {
		t.Errorf("wrong secret: %d", code)
	}
	if code := a.webhook(tgSecret, message(42, "/start "+nonce, nil)); code != http.StatusOK {
		t.Fatalf("start: %d", code)
	}
	call := a.bot.last(t)
	kb, _ := json.Marshal(call.Body["reply_markup"])
	if call.Method != "sendMessage" || call.Body["chat_id"] != float64(42) || !strings.Contains(string(kb), `"request_contact":true`) ||
		!strings.Contains(string(kb), "Share my number") {
		t.Errorf("start reply: %+v", call)
	}
	// Someone else's contact, a foreign number, then their own.
	a.webhook(tgSecret, message(42, "", map[string]any{"phone_number": "998902001234", "first_name": "A", "user_id": 43}))
	if c := a.bot.last(t); c.Body["text"] != tgNotOwn {
		t.Errorf("another's contact: %+v", c)
	}
	a.webhook(tgSecret, message(42, "", map[string]any{"phone_number": "12025550123", "first_name": "A", "user_id": 42}))
	if c := a.bot.last(t); c.Body["text"] != tgNotUzbek {
		t.Errorf("foreign number: %+v", c)
	}
	if r := check(nonce); r.body["status"] != "pending" {
		t.Errorf("still pending: %v", r.body)
	}
	a.webhook(tgSecret, message(42, "", map[string]any{"phone_number": "998902001234", "first_name": "A", "user_id": 42}))
	if c := a.bot.last(t); c.Body["text"] != tgDone || c.Body["reply_markup"].(map[string]any)["remove_keyboard"] != true {
		t.Errorf("own contact: %+v", c)
	}
	a.webhook(tgSecret, message(42, "hello", nil))
	if c := a.bot.last(t); c.Body["text"] != tgOther {
		t.Errorf("anything else: %+v", c)
	}

	// Verified: the phone ticket (with whose it is), once.
	r = check(nonce)
	account, _ := r.body["account"].(map[string]any)
	if r.status != http.StatusOK || r.body["status"] != "verified" || r.body["phone"] != "+998902001234" || r.body["ticket"] == "" ||
		account["status"] != StatusEnrolled || account["name"] != "Aziz K." {
		t.Fatalf("verified: %d %v", r.status, r.body)
	}
	if r := check(nonce); r.status != http.StatusGone || r.code() != CodeCodeExpired {
		t.Errorf("used twice: %d %v", r.status, r.body)
	}
	if r := a.do("POST", "/v1/auth/signup", "", signupBody(r.body["ticket"].(string), id, nil, nil, "aziz password")); r.status != http.StatusOK || r.body["existing"] != true {
		t.Errorf("signup with the bot's ticket: %d %v", r.status, r.body)
	}

	// Expired.
	r = start(map[string]any{"purpose": "reset"})
	a.clock.add(11 * time.Minute)
	if r := check(r.body["nonce"]); r.code() != CodeCodeExpired {
		t.Errorf("expired: %d %v", r.status, r.body)
	}
}

// A password reset with the bot instead of an SMS code.
func TestTelegramReset(t *testing.T) {
	a := setupTelegram(t)
	viaBot := func(user int64, phone string) map[string]any {
		r := a.do("POST", "/v1/auth/phone/telegram/start", "", map[string]any{"purpose": "reset"})
		nonce := r.body["nonce"].(string)
		a.webhook(tgSecret, message(user, "/start "+nonce, nil))
		a.webhook(tgSecret, message(user, "", map[string]any{"phone_number": phone, "user_id": user}))
		r = a.do("POST", "/v1/auth/phone/telegram/check", "", map[string]any{"nonce": nonce})
		if r.body["status"] != "verified" {
			t.Fatalf("check: %d %v", r.status, r.body)
		}
		if _, said := r.body["account"]; said {
			t.Errorf("a reset says whose the phone is: %v", r.body)
		}
		return r.body
	}
	v := viaBot(50, "+998902001234")
	r := a.do("POST", "/v1/auth/reset", "", map[string]string{"ticket": v["ticket"].(string), "password": "reset by telegram"})
	if r.status != http.StatusOK {
		t.Fatalf("reset: %d %v", r.status, r.body)
	}
	a.mustLogin("+998902001234", "reset by telegram")
	// Nobody's number: verified all the same; the reset says no_account.
	v = viaBot(51, "998903000099")
	if r := a.do("POST", "/v1/auth/reset", "", map[string]string{"ticket": v["ticket"].(string), "password": "whatever pass"}); r.code() != CodeNoAccount {
		t.Errorf("nobody's: %d %v", r.status, r.body)
	}
}

func TestTelegramWebhookCommand(t *testing.T) {
	a := setupTelegram(t)
	hook, err := a.svc.SetTelegramWebhook(t.Context(), "https://api.makonbook.uz/")
	if err != nil || hook != "https://api.makonbook.uz/v1/telegram/webhook" {
		t.Fatalf("%q %v", hook, err)
	}
	if c := a.bot.last(t); c.Method != "setWebhook" || c.Body["url"] != hook || c.Body["secret_token"] != tgSecret {
		t.Errorf("call: %+v", c)
	}
}
