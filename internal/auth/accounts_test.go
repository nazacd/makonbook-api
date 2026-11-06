package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/httpx"
	"github.com/nazacd/makonbook-api/internal/platform/testdb"
)

// fakeSMS records messages instead of sending them.
type fakeSMS struct {
	mu   gosync.Mutex
	sent map[string][]string // phone → texts
	fail error
}

func (f *fakeSMS) Send(_ context.Context, phone, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	if f.sent == nil {
		f.sent = map[string][]string{}
	}
	f.sent[phone] = append(f.sent[phone], text)
	return nil
}

func (f *fakeSMS) count(phone string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent[phone])
}

var sixDigits = regexp.MustCompile(`\b\d{6}\b`)

// code waits for the n-th message to phone (resets are sent in the
// background) and returns its code.
func (f *fakeSMS) code(t *testing.T, phone string, n int) string {
	t.Helper()
	for range 200 {
		f.mu.Lock()
		texts := f.sent[phone]
		f.mu.Unlock()
		if len(texts) >= n {
			return sixDigits.FindString(texts[n-1])
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no SMS %d to %s", n, phone)
	return ""
}

// clock is the service's time, moved by tests.
type clock struct {
	mu gosync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// fakeMail records emails as fakeSMS records messages (the body).
type fakeMail struct{ fakeSMS }

func (f *fakeMail) Send(ctx context.Context, to, _, body string) error {
	return f.fakeSMS.Send(ctx, to, body)
}

type accounts struct {
	client
	pool  *pgxpool.Pool
	svc   *Service
	sms   *fakeSMS
	mail  *fakeMail
	clock *clock
}

// setupAccounts: the auth API with sign-up by SMS (and Google, when g is
// set), over the snapshot of setup.
func setupAccounts(t *testing.T, g *GoogleConfig) accounts {
	pool := testdb.New(t)
	applySnapshot(t, pool, snapshot(people.StatusStudying))
	sms := &fakeSMS{}
	clk := &clock{t: time.Now()}
	svc := NewService(pool, people.NewDirectory(pool), NewHasher()).WithSMS(sms, "SATMAKON: your code is {code}", slogDiscard())
	svc.now = clk.now
	mail := &fakeMail{}
	svc.WithMail(mail)
	if g != nil {
		svc.WithGoogle(*g)
	}
	mux := http.NewServeMux()
	NewHandler(svc, tashkent, false).Register(mux)
	srv := httptest.NewServer(httpx.Chain(mux, httpx.WithRequestContext(slogDiscard()), httpx.Recover))
	t.Cleanup(srv.Close)
	return accounts{client: client{t, srv}, pool: pool, svc: svc, sms: sms, mail: mail, clock: clk}
}

func (a accounts) start(phone, purpose string) response {
	a.t.Helper()
	return a.do("POST", "/v1/auth/phone/start", "", map[string]string{"phone": phone, "purpose": purpose})
}

// verify checks a phone's code; a sign-up's needs an identity (id:
// {"email_ticket": …} or {"google_ticket": …}).
func (a accounts) verify(phone, code, purpose string, id map[string]any) response {
	a.t.Helper()
	body := map[string]any{"phone": phone, "code": code, "purpose": purpose}
	for k, v := range id {
		body[k] = v
	}
	return a.do("POST", "/v1/auth/phone/verify", "", body)
}

// ticket proves phone (E.164) for purpose, through an SMS code (with the
// identity id, for a sign-up).
func (a accounts) ticket(phone, purpose string, id map[string]any) string {
	a.t.Helper()
	return a.verified(phone, purpose, id).body["ticket"].(string)
}

// verified is the answer to the right code for phone.
func (a accounts) verified(phone, purpose string, id map[string]any) response {
	a.t.Helper()
	a.clock.add(2 * time.Minute) // past the per-phone minute
	n := a.sms.count(phone)
	if r := a.start(phone, purpose); r.status != http.StatusOK {
		a.t.Fatalf("start %s: %d %v", phone, r.status, r.body)
	}
	r := a.verify(phone, a.sms.code(a.t, phone, n+1), purpose, id)
	if r.status != http.StatusOK {
		a.t.Fatalf("verify %s: %d %v", phone, r.status, r.body)
	}
	return r
}

func (a accounts) startEmail(email string) response {
	a.t.Helper()
	return a.do("POST", "/v1/auth/email/start", "", map[string]string{"email": email})
}

func (a accounts) verifyEmail(email, code string) response {
	a.t.Helper()
	return a.do("POST", "/v1/auth/email/verify", "", map[string]string{"email": email, "code": code})
}

// emailID proves an email and returns it as an identity for sign-up.
func (a accounts) emailID(email string) map[string]any {
	a.t.Helper()
	a.clock.add(2 * time.Minute)
	key := strings.ToLower(email)
	n := a.mail.count(key)
	if r := a.startEmail(email); r.status != http.StatusOK {
		a.t.Fatalf("email start %s: %d %v", email, r.status, r.body)
	}
	r := a.verifyEmail(email, a.mail.code(a.t, key, n+1))
	if r.status != http.StatusOK {
		a.t.Fatalf("email verify %s: %d %v", email, r.status, r.body)
	}
	return map[string]any{"email_ticket": r.body["email_ticket"]}
}

func googleID(ticket string) map[string]any { return map[string]any{"google_ticket": ticket} }

func retryAfterSeconds(t *testing.T, r response) int {
	t.Helper()
	n, err := strconv.Atoi(r.header.Get("Retry-After"))
	if err != nil {
		t.Fatalf("Retry-After = %q", r.header.Get("Retry-After"))
	}
	return n
}

func TestPhoneCodes(t *testing.T) {
	a := setupAccounts(t, nil)
	const phone = "+998903000001"
	id := a.emailID("phone.codes@example.com")

	for _, bad := range []string{"+12025550123", "123", "+998 71 1"} {
		if r := a.start(bad, PurposeSignup); r.status != http.StatusUnprocessableEntity || r.code() != CodeInvalidPhone {
			t.Errorf("start %q: %d %v", bad, r.status, r.body)
		}
	}
	if r := a.start(phone, "login"); r.status != http.StatusBadRequest {
		t.Errorf("unknown purpose: %d", r.status)
	}

	// Any common format; the answer says when to resend.
	r := a.start("90 300 00 01", PurposeSignup)
	if r.status != http.StatusOK || r.body["resend_in"] != float64(60) || r.body["expires_in"] != float64(600) {
		t.Fatalf("start: %d %v", r.status, r.body)
	}
	if text := a.sms.sent[phone][0]; !strings.HasPrefix(text, "SATMAKON: your code is ") || len(a.sms.code(t, phone, 1)) != 6 {
		t.Errorf("SMS = %q", text)
	}

	// One a minute.
	a.clock.add(30 * time.Second)
	r = a.start(phone, PurposeSignup)
	if r.status != http.StatusTooManyRequests || r.code() != "rate_limited" {
		t.Fatalf("second start within a minute: %d %v", r.status, r.body)
	}
	if s := retryAfterSeconds(t, r); s < 29 || s > 31 {
		t.Errorf("Retry-After = %d, want 30", s)
	}

	// Wrong codes: 4 tell the tries left, the 5th spends the code.
	code := a.sms.code(t, phone, 1)
	wrong := fmt.Sprintf("%06d", (mustAtoi(code)+1)%1_000_000)
	for left := 4; left > 0; left-- {
		r := a.verify(phone, wrong, PurposeSignup, id)
		if r.status != http.StatusUnprocessableEntity || r.code() != CodeInvalidCode ||
			!strings.Contains(r.body["error"].(map[string]any)["message"].(string), strconv.Itoa(left)+" tr") {
			t.Fatalf("wrong code, %d left: %d %v", left, r.status, r.body)
		}
	}
	if r := a.verify(phone, wrong, PurposeSignup, id); r.status != http.StatusTooManyRequests || r.code() != CodeTooManyTries {
		t.Fatalf("5th wrong code: %d %v", r.status, r.body)
	}
	if r := a.verify(phone, code, PurposeSignup, id); r.status != http.StatusTooManyRequests || r.code() != CodeTooManyTries {
		t.Errorf("right code after 5 wrong: %d %v", r.status, r.body)
	}
	// A code for another purpose doesn't count.
	if r := a.verify(phone, code, PurposeReset, nil); r.status != http.StatusGone || r.code() != CodeCodeExpired {
		t.Errorf("code for another purpose: %d %v", r.status, r.body)
	}

	// A new code expires after 10 minutes.
	a.clock.add(time.Minute)
	if r := a.start(phone, PurposeSignup); r.status != http.StatusOK {
		t.Fatalf("second code: %d %v", r.status, r.body)
	}
	a.clock.add(10*time.Minute + time.Second)
	if r := a.verify(phone, a.sms.code(t, phone, 2), PurposeSignup, id); r.status != http.StatusGone || r.code() != CodeCodeExpired {
		t.Errorf("expired code: %d %v", r.status, r.body)
	}

	// The right code gives a ticket, once; spaces don't matter.
	if r := a.start(phone, PurposeSignup); r.status != http.StatusOK {
		t.Fatalf("third code: %d %v", r.status, r.body)
	}
	code = a.sms.code(t, phone, 3)
	r = a.verify("903000001", code[:3]+" "+code[3:], PurposeSignup, id)
	if account, said := r.body["account"]; r.status != http.StatusOK || len(r.body["ticket"].(string)) < 40 || !said || account != nil {
		t.Fatalf("right code: %d %v", r.status, r.body)
	}
	if r := a.verify(phone, code, PurposeSignup, id); r.status != http.StatusGone {
		t.Errorf("code used twice: %d %v", r.status, r.body)
	}

	// Five a day: the 6th waits until the first is a day old.
	for range 2 {
		a.clock.add(time.Minute)
		if r := a.start(phone, PurposeSignup); r.status != http.StatusOK {
			t.Fatalf("code: %d %v", r.status, r.body)
		}
	}
	a.clock.add(time.Minute)
	r = a.start(phone, PurposeSignup)
	if r.status != http.StatusTooManyRequests {
		t.Fatalf("6th code in a day: %d %v", r.status, r.body)
	}
	if s := retryAfterSeconds(t, r); s < 22*3600 || s > 24*3600 {
		t.Errorf("Retry-After for the day = %d s", s)
	}
	a.clock.add(24 * time.Hour)
	if r := a.start(phone, PurposeSignup); r.status != http.StatusOK {
		t.Errorf("next day: %d %v", r.status, r.body)
	}

	// 20 codes an hour from one network, whatever the phones.
	for i := range 19 {
		if r := a.start(fmt.Sprintf("+99890400%04d", i), PurposeSignup); r.status != http.StatusOK {
			t.Fatalf("network code %d: %d %v", i, r.status, r.body)
		}
	}
	r = a.start("+998904009999", PurposeSignup)
	if r.status != http.StatusTooManyRequests || retryAfterSeconds(t, r) < 3500 {
		t.Errorf("21st code from one network: %d %v %v", r.status, r.body, r.header)
	}

	// The gateway fails: the person hears so.
	a.clock.add(2 * time.Hour)
	a.sms.fail = fmt.Errorf("eskiz down")
	if r := a.start("+998905000001", PurposeSignup); r.status != http.StatusServiceUnavailable || r.code() != CodeSMSUnavailable {
		t.Errorf("gateway down: %d %v", r.status, r.body)
	}
}

func mustAtoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		panic(err)
	}
	return n
}

func TestSMSOff(t *testing.T) {
	c, _, _ := setup(t)
	r := c.do("POST", "/v1/auth/phone/start", "", map[string]string{"phone": "+998903000001", "purpose": PurposeSignup})
	if r.status != http.StatusServiceUnavailable || r.code() != CodeSMSUnavailable {
		t.Errorf("start: %d %v", r.status, r.body)
	}
	if r := c.do("POST", "/v1/auth/email/start", "", map[string]string{"email": "a@example.com"}); r.status != http.StatusServiceUnavailable || r.code() != CodeEmailUnavailable {
		t.Errorf("email: %d %v", r.status, r.body)
	}
	if r := c.do("GET", "/v1/auth/google/start", "", nil); r.status != http.StatusServiceUnavailable || r.code() != CodeGoogleUnavailable {
		t.Errorf("google: %d %v", r.status, r.body)
	}
}

// signupBody is a sign-up with the phone ticket and the identity it was
// verified with.
func signupBody(ticket string, id map[string]any, first, last, password any) map[string]any {
	body := map[string]any{"ticket": ticket, "first_name": first, "last_name": last, "password": password,
		"email_ticket": nil, "google_ticket": nil}
	for k, v := range id {
		body[k] = v
	}
	return body
}

func TestEmailCodes(t *testing.T) {
	a := setupAccounts(t, nil)
	const email = "malika@example.com"
	for _, bad := range []string{"", "malika", "malika@example", "a b@example.com", "Malika <malika@example.com>", "@example.com"} {
		if r := a.startEmail(bad); r.status != http.StatusUnprocessableEntity || r.code() != CodeInvalidEmail {
			t.Errorf("start %q: %d %v", bad, r.status, r.body)
		}
	}
	r := a.startEmail("  Malika@Example.COM ")
	if r.status != http.StatusOK || r.body["resend_in"] != float64(60) || r.body["expires_in"] != float64(600) {
		t.Fatalf("start: %d %v", r.status, r.body)
	}
	code := a.mail.code(t, email, 1)
	if r := a.startEmail(email); r.status != http.StatusTooManyRequests || r.code() != "rate_limited" {
		t.Errorf("second code within a minute: %d %v", r.status, r.body)
	}
	wrong := fmt.Sprintf("%06d", (mustAtoi(code)+1)%1_000_000)
	for left := 4; left > 0; left-- {
		if r := a.verifyEmail(email, wrong); r.code() != CodeInvalidCode {
			t.Fatalf("wrong code: %d %v", r.status, r.body)
		}
	}
	if r := a.verifyEmail(email, wrong); r.code() != CodeTooManyTries {
		t.Errorf("5th wrong code: %d %v", r.status, r.body)
	}
	a.clock.add(time.Minute)
	if r := a.startEmail(email); r.status != http.StatusOK {
		t.Fatal(r.status)
	}
	a.clock.add(11 * time.Minute)
	if r := a.verifyEmail(email, a.mail.code(t, email, 2)); r.code() != CodeCodeExpired {
		t.Errorf("expired: %d %v", r.status, r.body)
	}
	id := a.emailID("MALIKA@example.com")
	if len(id["email_ticket"].(string)) < 40 {
		t.Errorf("ticket = %v", id)
	}

	// Once an account has the email, its code says so.
	if r := a.do("POST", "/v1/auth/signup", "", signupBody(a.ticket("+998903000005", PurposeSignup, id), id, "Malika", "Y", "a good password")); r.status != http.StatusOK {
		t.Fatalf("signup: %d %v", r.status, r.body)
	}
	a.clock.add(2 * time.Minute)
	a.startEmail(email)
	if r := a.verifyEmail(email, a.mail.code(t, email, 4)); r.status != http.StatusConflict || r.code() != CodeEmailTaken {
		t.Errorf("taken: %d %v", r.status, r.body)
	}
}

// Verifying a phone for sign-up needs an identity, and shows at most one
// phone's owner per identity.
func TestPhoneVerifyNeedsAnIdentity(t *testing.T) {
	a := setupAccounts(t, nil)
	const phone = "+998903000006"
	id, other := a.emailID("one@example.com"), a.emailID("two@example.com")
	a.start(phone, PurposeSignup)
	code := a.sms.code(t, phone, 1)
	for _, bad := range []map[string]any{
		nil,
		{"email_ticket": "nope"},
		{"email_ticket": id["email_ticket"], "google_ticket": "x"},
		{"google_ticket": id["email_ticket"]}, // an email ticket isn't a Google one
	} {
		if r := a.verify(phone, code, PurposeSignup, bad); r.status != http.StatusUnprocessableEntity || r.code() != CodeInvalidTicket {
			t.Errorf("%v: %d %v", bad, r.status, r.body)
		}
	}
	// None of that spent a try.
	r := a.verify(phone, code, PurposeSignup, id)
	if account, said := r.body["account"]; r.status != http.StatusOK || !said || account != nil {
		t.Fatalf("verify: %d %v", r.status, r.body)
	}
	// A phone nobody has doesn't spend the identity; one owner does.
	if r := a.verified("+998902001234", PurposeSignup, id); r.body["account"].(map[string]any)["name"] != "Aziz K." {
		t.Errorf("enrolled: %v", r.body)
	}
	if r := a.verified("+998902001234", PurposeSignup, id); r.body["account"] == nil {
		t.Errorf("the same phone again: %v", r.body)
	}
	a.clock.add(2 * time.Minute)
	a.start("+998901000010", PurposeSignup)
	r = a.verify("+998901000010", a.sms.code(t, "+998901000010", 1), PurposeSignup, id)
	if r.status != http.StatusConflict || r.code() != CodeIdentitySpent {
		t.Errorf("a second owner: %d %v", r.status, r.body)
	}
	// Spent: not even a phone nobody has.
	a.clock.add(2 * time.Minute)
	a.start("+998903000007", PurposeSignup)
	if r := a.verify("+998903000007", a.sms.code(t, "+998903000007", 1), PurposeSignup, id); r.code() != CodeIdentitySpent {
		t.Errorf("spent identity, new phone: %d %v", r.status, r.body)
	}
	// The phone ticket goes with its identity only.
	ticket := a.ticket("+998903000008", PurposeSignup, other)
	if r := a.do("POST", "/v1/auth/signup", "", signupBody(ticket, a.emailID("three@example.com"), "A", "B", "a good password")); r.code() != CodeInvalidTicket {
		t.Errorf("another identity: %d %v", r.status, r.body)
	}
	if r := a.do("POST", "/v1/auth/signup", "", signupBody(ticket, nil, "A", "B", "a good password")); r.code() != CodeInvalidTicket {
		t.Errorf("no identity: %d %v", r.status, r.body)
	}
}

func TestSignupNewAccount(t *testing.T) {
	a := setupAccounts(t, nil)
	const phone = "+998903000002"
	id := a.emailID("Malika.Y@Example.com")
	ticket := a.ticket(phone, PurposeSignup, id)

	// Nothing is spent by a request that is turned down.
	for _, c := range []struct {
		body map[string]any
		code string
	}{
		{signupBody(ticket, id, "Malika", "Yusupova", "short"), CodeWeakPassword},
		{signupBody(ticket, id, "Malika", "Yusupova", "pass903000002"), CodeWeakPassword}, // contains the phone
		{signupBody(ticket, id, "Malika", "Yusupova", nil), CodeWeakPassword},             // email needs a password
		{signupBody(ticket, id, "  ", "Yusupova", "a good password"), CodeInvalidProfile},
		{signupBody(ticket, id, nil, nil, "a good password"), CodeInvalidProfile},
		{signupBody(ticket, id, strings.Repeat("я", 61), "Yusupova", "a good password"), CodeInvalidProfile},
		{signupBody("nope", id, "Malika", "Yusupova", "a good password"), CodeInvalidTicket},
	} {
		if r := a.do("POST", "/v1/auth/signup", "", c.body); r.status != http.StatusUnprocessableEntity || r.code() != c.code {
			t.Errorf("%v: %d %v, want %s", c.body, r.status, r.body, c.code)
		}
	}

	r := a.do("POST", "/v1/auth/signup", "", signupBody(ticket, id, " Malika ", "Yusupova", "a good password"))
	if r.status != http.StatusOK || r.body["existing"] != false {
		t.Fatalf("signup: %d %v", r.status, r.body)
	}
	user := r.body["user"].(map[string]any)
	if user["kind"] != "student" || user["id"].(float64) > -1000001 || user["first_name"] != "Malika" ||
		user["status"] != StatusRegistered || user["phone"] != phone || user["email"] != "malika.y@example.com" || user["google"] != false ||
		user["has_password"] != true || user["must_change_password"] != false || len(user["groups"].([]any)) != 0 {
		t.Errorf("user = %v", user)
	}
	token := r.body["token"].(string)
	if r := a.do("GET", "/v1/me", token, nil); r.status != http.StatusOK {
		t.Errorf("me: %d %v", r.status, r.body)
	}
	if r := a.do("POST", "/v1/auth/signup", "", signupBody(ticket, id, "Malika", "Yusupova", "a good password")); r.code() != CodeInvalidTicket {
		t.Errorf("tickets used twice: %d %v", r.status, r.body)
	}

	// Phone or email + password from then on; no default password.
	if _, u := a.mustLogin("90 300 00 02", "a good password"); u["status"] != StatusRegistered {
		t.Errorf("login: %v", u)
	}
	if _, u := a.mustLogin(" MALIKA.Y@example.com", "a good password"); u["phone"] != phone {
		t.Errorf("login by email: %v", u)
	}
	if r := a.login("malika.y@example.com", "wrong password"); r.code() != CodeInvalidCredentials {
		t.Errorf("wrong password by email: %d %v", r.status, r.body)
	}
	if r := a.login(phone, "0002"); r.status != http.StatusUnauthorized {
		t.Errorf("default password works for a registered student: %d", r.status)
	}

	// Their own name: both needed, as they have no other.
	r = a.do("PUT", "/v1/me/profile", token, map[string]any{"first_name": "Malik", "last_name": "Yusupov"})
	if r.status != http.StatusOK || r.body["first_name"] != "Malik" || r.body["last_name"] != "Yusupov" {
		t.Errorf("profile: %d %v", r.status, r.body)
	}
	if r := a.do("PUT", "/v1/me/profile", token, map[string]any{"first_name": nil, "last_name": "Yusupov"}); r.code() != CodeInvalidProfile {
		t.Errorf("clearing a registered student's name: %d %v", r.status, r.body)
	}

	// Signing up again with the number, by email or Google: the code says
	// whose it is, and sign-up is refused; nothing changes.
	var sessions int
	if err := a.pool.QueryRow(t.Context(), "SELECT count(*) FROM auth_sessions").Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	google, err := a.svc.newTicket(t.Context(), a.svc.q, ticketData{purpose: ticketGoogleSignup,
		google: &GoogleAccount{Sub: "g-9", Email: "g9@gmail.com"}}, GoogleSignupTTL)
	if err != nil {
		t.Fatal(err)
	}
	for _, again := range []map[string]any{a.emailID("someone@example.com"), googleID(google)} {
		v := a.verified(phone, PurposeSignup, again)
		if account, _ := v.body["account"].(map[string]any); len(account) != 1 || account["status"] != StatusRegistered {
			t.Errorf("verify a registered number: %v", v.body)
		}
		r := a.do("POST", "/v1/auth/signup", "", signupBody(v.body["ticket"].(string), again, nil, nil, "another password"))
		if r.status != http.StatusConflict || r.code() != CodePhoneTaken || r.body["token"] != nil {
			t.Errorf("signup with a registered number: %d %v", r.status, r.body)
		}
	}
	var after, links int
	if err := a.pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM auth_sessions), (SELECT count(*) FROM google_links)").Scan(&after, &links); err != nil {
		t.Fatal(err)
	}
	if after != sessions || links != 0 {
		t.Errorf("sessions %d → %d, Google links %d", sessions, after, links)
	}
	if r := a.do("GET", "/v1/me", token, nil); r.status != http.StatusOK || r.body["google"] != false || r.body["email"] != "malika.y@example.com" {
		t.Errorf("the account changed: %d %v", r.status, r.body)
	}
	a.mustLogin(phone, "a good password")
	// A reset's answer says nothing about the account.
	if r := a.verified(phone, PurposeReset, nil); r.body["account"] != nil || len(r.body) != 1 {
		t.Errorf("verify for a reset: %v", r.body)
	}
}

// A number that belongs to a SAT MAKON student signs into their account,
// with the password chosen at sign-up, and gets the email.
func TestSignupExistingAccount(t *testing.T) {
	a := setupAccounts(t, nil)
	const phone = "+998902001234" // student 1, on the default password
	old, _ := a.mustLogin(phone, "1234")
	// Locked out by someone guessing.
	for range 5 {
		a.login(phone, "0000")
	}
	if r := a.login(phone, "1234"); r.code() != CodeAccountLocked {
		t.Fatalf("not locked: %d %v", r.status, r.body)
	}

	// The code tells whose number it is: the client asks "is this you?".
	id := a.emailID("aziz@example.com")
	v := a.verified(phone, PurposeSignup, id)
	if account, _ := v.body["account"].(map[string]any); account["status"] != StatusEnrolled || account["kind"] != "student" || account["name"] != "Aziz K." {
		t.Errorf("verify an enrolled student's number: %v", v.body)
	}
	// Yes: their account, with the password chosen; names aren't needed.
	r := a.do("POST", "/v1/auth/signup", "", signupBody(v.body["ticket"].(string), id, nil, "", "my own password"))
	if r.status != http.StatusOK || r.body["existing"] != true {
		t.Fatalf("signup: %d %v", r.status, r.body)
	}
	user := r.body["user"].(map[string]any)
	if user["id"] != float64(1) || user["first_name"] != "Aziz" || user["status"] != StatusEnrolled || user["email"] != "aziz@example.com" ||
		user["must_change_password"] != false || user["has_password"] != true || len(user["groups"].([]any)) != 1 {
		t.Errorf("user = %v", user)
	}
	if r := a.do("GET", "/v1/me", old, nil); r.status != http.StatusUnauthorized {
		t.Errorf("other sessions survived: %d", r.status)
	}
	if _, err := a.svc.Login(t.Context(), "aziz@example.com", "my own password", "phone"); err != nil {
		t.Errorf("login by email with the new password: %v", err)
	}
	var n int
	if err := a.pool.QueryRow(t.Context(), "SELECT count(*) FROM students WHERE source = 'self'").Scan(&n); err != nil || n != 0 {
		t.Errorf("self-registered students: %d %v", n, err)
	}

	// A teacher's number too, with the name they chose for themselves. The
	// email belongs to someone else by then: they sign in without it.
	first := "Alisher"
	if err := people.NewDirectory(a.pool).SetNames(t.Context(), people.KindTeacher, 10, &first, nil); err != nil {
		t.Fatal(err)
	}
	taken := a.emailID("teacher@example.com")
	if _, err := a.pool.Exec(t.Context(), "UPDATE credentials SET email = 'teacher@example.com' WHERE person_kind = 'student' AND person_id = 1"); err != nil {
		t.Fatal(err)
	}
	v = a.verified("+998901000010", PurposeSignup, taken)
	if account, _ := v.body["account"].(map[string]any); account["status"] != StatusEnrolled || account["kind"] != "teacher" || account["name"] != "Alisher V." {
		t.Errorf("verify a teacher's number: %v", v.body)
	}
	r = a.do("POST", "/v1/auth/signup", "", signupBody(v.body["ticket"].(string), taken, nil, nil, "teacher password"))
	if u, _ := r.body["user"].(map[string]any); r.status != http.StatusOK || r.body["existing"] != true || u["kind"] != "teacher" || u["email"] != nil {
		t.Errorf("teacher: %d %v", r.status, r.body)
	}
}

func TestShortName(t *testing.T) {
	for in, want := range map[[2]string]string{
		{"Diyora", "Abdullayeva"}: "Diyora A.", {" Ali ", "valiyev"}: "Ali V.", {"Ali", ""}: "Ali", {"Шахзода", "Юсупова"}: "Шахзода Ю.",
	} {
		if got := shortName(in[0], in[1]); got != want {
			t.Errorf("shortName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReset(t *testing.T) {
	a := setupAccounts(t, nil)
	const phone = "+998902001234" // student 1
	old, _ := a.mustLogin(phone, "1234")

	// Nobody signs in with this number: the same answer, and no SMS.
	const nobody = "+998903000009"
	if r := a.start(nobody, PurposeReset); r.status != http.StatusOK || r.body["resend_in"] != float64(60) {
		t.Fatalf("reset for nobody: %d %v", r.status, r.body)
	}
	if r := a.verify(nobody, "123456", PurposeReset, nil); r.code() != CodeInvalidCode {
		t.Errorf("verify for nobody: %d %v", r.status, r.body)
	}
	if r := a.start(nobody, PurposeReset); r.status != http.StatusTooManyRequests {
		t.Errorf("limits apply to nobody's number too: %d", r.status)
	}

	ticket := a.ticket(phone, PurposeReset, nil)
	if a.sms.count(nobody) != 0 {
		t.Error("a code went to a number nobody signs in with")
	}
	if r := a.do("POST", "/v1/auth/reset", "", map[string]string{"ticket": a.ticket(phone, PurposeSignup, a.emailID("r@example.com")), "password": "a new password"}); r.code() != CodeInvalidTicket {
		t.Errorf("sign-up ticket for a reset: %d %v", r.status, r.body)
	}
	if r := a.do("POST", "/v1/auth/reset", "", map[string]string{"ticket": ticket, "password": "short"}); r.code() != CodeWeakPassword {
		t.Errorf("weak: %d %v", r.status, r.body)
	}
	r := a.do("POST", "/v1/auth/reset", "", map[string]string{"ticket": ticket, "password": "a new password"})
	if r.status != http.StatusOK || r.body["user"].(map[string]any)["must_change_password"] != false || r.body["token"] == "" {
		t.Fatalf("reset: %d %v", r.status, r.body)
	}
	if r := a.do("GET", "/v1/me", old, nil); r.status != http.StatusUnauthorized {
		t.Errorf("old session after reset: %d", r.status)
	}
	if r := a.do("POST", "/v1/auth/reset", "", map[string]string{"ticket": ticket, "password": "a new password"}); r.code() != CodeInvalidTicket {
		t.Errorf("ticket used twice: %d %v", r.status, r.body)
	}
	a.mustLogin(phone, "a new password")

	// The person stopped signing in with the number since the code.
	ticket = a.ticket(phone, PurposeReset, nil)
	if _, err := a.pool.Exec(t.Context(), "UPDATE students SET login_phone = NULL WHERE client_id = 1"); err != nil {
		t.Fatal(err)
	}
	if r := a.do("POST", "/v1/auth/reset", "", map[string]string{"ticket": ticket, "password": "another one"}); r.status != http.StatusNotFound || r.code() != CodeNoAccount {
		t.Errorf("no account: %d %v", r.status, r.body)
	}
}

func TestProfileNamesForEnrolledPeople(t *testing.T) {
	a := setupAccounts(t, nil)
	token, _ := a.mustLogin("+998902001234", "1234")
	if r := a.do("PUT", "/v1/me/profile", token, map[string]any{"first_name": "X", "last_name": "Y"}); r.code() != CodePasswordChangeRequired {
		t.Errorf("before choosing a password: %d %v", r.status, r.body)
	}
	if r := a.do("POST", "/v1/auth/password", token, map[string]string{"current_password": "1234", "new_password": "a good password"}); r.status != http.StatusNoContent {
		t.Fatal(r.status)
	}
	r := a.do("PUT", "/v1/me/profile", token, map[string]any{"first_name": "Azizbek", "last_name": nil})
	if r.status != http.StatusOK || r.body["first_name"] != "Azizbek" || r.body["last_name"] != "Karimov" || r.body["status"] != StatusEnrolled {
		t.Errorf("override: %d %v", r.status, r.body)
	}
	// The sync renames them in HolliHop: they keep theirs.
	s := snapshot(people.StatusStudying)
	s.Students[0].FirstName = "Azizjon"
	applySnapshot(t, a.pool, s)
	if r := a.do("GET", "/v1/me", token, nil); r.body["first_name"] != "Azizbek" {
		t.Errorf("after a sync: %v", r.body)
	}
	if r := a.do("PUT", "/v1/me/profile", token, map[string]any{"first_name": "a\tb", "last_name": nil}); r.code() != CodeInvalidProfile {
		t.Errorf("control character: %d %v", r.status, r.body)
	}
	if r := a.do("PUT", "/v1/me/profile", token, map[string]any{"first_name": nil, "last_name": nil}); r.body["first_name"] != "Azizjon" {
		t.Errorf("cleared: %v", r.body)
	}
}
