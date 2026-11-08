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

// verify checks a reset code.
func (a accounts) verify(phone, code string) response {
	a.t.Helper()
	return a.do("POST", "/v1/auth/phone/verify", "", map[string]any{"phone": phone, "code": code, "purpose": PurposeReset})
}

// ticket proves phone (E.164) for a password reset, through an SMS code.
func (a accounts) ticket(phone string) string {
	a.t.Helper()
	a.clock.add(2 * time.Minute) // past the per-phone minute
	n := a.sms.count(phone)
	if r := a.start(phone, PurposeReset); r.status != http.StatusOK {
		a.t.Fatalf("start %s: %d %v", phone, r.status, r.body)
	}
	r := a.verify(phone, a.sms.code(a.t, phone, n+1))
	if r.status != http.StatusOK {
		a.t.Fatalf("verify %s: %d %v", phone, r.status, r.body)
	}
	return r.body["ticket"].(string)
}

func (a accounts) startEmail(email string) response {
	a.t.Helper()
	return a.do("POST", "/v1/auth/email/start", "", map[string]string{"email": email})
}

func (a accounts) verifyEmail(email, code string) response {
	a.t.Helper()
	return a.do("POST", "/v1/auth/email/verify", "", map[string]string{"email": email, "code": code})
}

// emailCode proves an email and returns the verify answer.
func (a accounts) emailCode(email string) response {
	a.t.Helper()
	a.clock.add(2 * time.Minute)
	key := strings.ToLower(strings.TrimSpace(email))
	n := a.mail.count(key)
	if r := a.startEmail(email); r.status != http.StatusOK {
		a.t.Fatalf("email start %s: %d %v", email, r.status, r.body)
	}
	r := a.verifyEmail(email, a.mail.code(a.t, key, n+1))
	if r.status != http.StatusOK {
		a.t.Fatalf("email verify %s: %d %v", email, r.status, r.body)
	}
	return r
}

// emailSignup makes a registered student with an email and no phone; it
// returns their token and profile.
func (a accounts) emailSignup(email, first string) (string, map[string]any) {
	a.t.Helper()
	v := a.emailCode(email)
	if v.body["status"] != "new" {
		a.t.Fatalf("email %s isn't new: %v", email, v.body)
	}
	r := a.do("POST", "/v1/auth/signup", "", map[string]any{"email_ticket": v.body["email_ticket"], "first_name": first,
		"last_name": "Web", "password": "a good password"})
	if r.status != http.StatusOK {
		a.t.Fatalf("signup %s: %d %v", email, r.status, r.body)
	}
	return r.body["token"].(string), r.body["user"].(map[string]any)
}

// addPhone proves phone by SMS for the signed-in person (token).
func (a accounts) addPhone(token, phone string) response {
	a.t.Helper()
	a.clock.add(2 * time.Minute)
	n := a.sms.count(phone)
	if r := a.do("POST", "/v1/me/phone/start", token, map[string]string{"phone": phone}); r.status != http.StatusOK {
		a.t.Fatalf("phone start %s: %d %v", phone, r.status, r.body)
	}
	return a.do("POST", "/v1/me/phone/verify", token, map[string]string{"phone": phone, "code": a.sms.code(a.t, phone, n+1)})
}

func retryAfterSeconds(t *testing.T, r response) int {
	t.Helper()
	n, err := strconv.Atoi(r.header.Get("Retry-After"))
	if err != nil {
		t.Fatalf("Retry-After = %q", r.header.Get("Retry-After"))
	}
	return n
}

func mustAtoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		panic(err)
	}
	return n
}

// The rules of SMS codes, on a signed-in person adding their phone.
func TestPhoneCodes(t *testing.T) {
	a := setupAccounts(t, nil)
	token, _ := a.emailSignup("phone.codes@example.com", "Codes")
	const phone = "+998903000001"
	start := func(p string) response {
		return a.do("POST", "/v1/me/phone/start", token, map[string]string{"phone": p})
	}
	verify := func(p, code string) response {
		return a.do("POST", "/v1/me/phone/verify", token, map[string]string{"phone": p, "code": code})
	}

	for _, bad := range []string{"+12025550123", "123", "+998 71 1"} {
		if r := start(bad); r.status != http.StatusUnprocessableEntity || r.code() != CodeInvalidPhone {
			t.Errorf("start %q: %d %v", bad, r.status, r.body)
		}
	}
	if r := a.start(phone, "signup"); r.status != http.StatusBadRequest {
		t.Errorf("purpose signup is gone: %d", r.status)
	}

	// Any common format; the answer says when to resend.
	r := start("90 300 00 01")
	if r.status != http.StatusOK || r.body["resend_in"] != float64(60) {
		t.Fatalf("start: %d %v", r.status, r.body)
	}
	if code := a.sms.code(t, phone, 1); len(code) != 6 {
		t.Errorf("SMS = %v", a.sms.sent[phone])
	}

	// One a minute.
	a.clock.add(30 * time.Second)
	r = start(phone)
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
		r := verify(phone, wrong)
		if r.status != http.StatusUnprocessableEntity || r.code() != CodeInvalidCode ||
			!strings.Contains(r.body["error"].(map[string]any)["message"].(string), strconv.Itoa(left)+" tr") {
			t.Fatalf("wrong code, %d left: %d %v", left, r.status, r.body)
		}
	}
	if r := verify(phone, wrong); r.code() != CodeTooManyTries {
		t.Fatalf("5th wrong code: %d %v", r.status, r.body)
	}
	if r := verify(phone, code); r.code() != CodeTooManyTries {
		t.Errorf("right code after 5 wrong: %d %v", r.status, r.body)
	}
	// A code for another purpose doesn't count.
	if r := a.verify(phone, code); r.code() != CodeCodeExpired {
		t.Errorf("code for another purpose: %d %v", r.status, r.body)
	}

	// A new code expires after 10 minutes.
	a.clock.add(time.Minute)
	if r := start(phone); r.status != http.StatusOK {
		t.Fatalf("second code: %d %v", r.status, r.body)
	}
	a.clock.add(10*time.Minute + time.Second)
	if r := verify(phone, a.sms.code(t, phone, 2)); r.code() != CodeCodeExpired {
		t.Errorf("expired code: %d %v", r.status, r.body)
	}

	// Five a day: the 6th waits until the first is a day old.
	for range 3 {
		a.clock.add(time.Minute)
		if r := start(phone); r.status != http.StatusOK {
			t.Fatalf("code: %d %v", r.status, r.body)
		}
	}
	a.clock.add(time.Minute)
	r = start(phone)
	if r.status != http.StatusTooManyRequests {
		t.Fatalf("6th code in a day: %d %v", r.status, r.body)
	}
	if s := retryAfterSeconds(t, r); s < 22*3600 || s > 24*3600 {
		t.Errorf("Retry-After for the day = %d s", s)
	}

	// 20 codes an hour from one network, whatever the phones.
	a.clock.add(24 * time.Hour)
	for i := range 20 {
		if r := start(fmt.Sprintf("+99890400%04d", i)); r.status != http.StatusOK {
			t.Fatalf("network code %d: %d %v", i, r.status, r.body)
		}
	}
	r = start("+998904009999")
	if r.status != http.StatusTooManyRequests || retryAfterSeconds(t, r) < 3500 {
		t.Errorf("21st code from one network: %d %v %v", r.status, r.body, r.header)
	}

	// The gateway fails: the person hears so.
	a.clock.add(2 * time.Hour)
	a.sms.fail = fmt.Errorf("eskiz down")
	if r := start("+998905000001"); r.status != http.StatusServiceUnavailable || r.code() != CodeSMSUnavailable {
		t.Errorf("gateway down: %d %v", r.status, r.body)
	}
}

func TestSMSOff(t *testing.T) {
	c, _, _ := setup(t)
	r := c.do("POST", "/v1/auth/phone/start", "", map[string]string{"phone": "+998903000001", "purpose": PurposeReset})
	if r.status != http.StatusServiceUnavailable || r.code() != CodeSMSUnavailable {
		t.Errorf("start: %d %v", r.status, r.body)
	}
	if r := c.do("POST", "/v1/auth/email/start", "", map[string]string{"email": "a@example.com"}); r.code() != CodeEmailUnavailable {
		t.Errorf("email: %d %v", r.status, r.body)
	}
	if r := c.do("GET", "/v1/auth/google/start", "", nil); r.code() != CodeGoogleUnavailable {
		t.Errorf("google: %d %v", r.status, r.body)
	}
	if r := c.do("POST", "/v1/auth/telegram/start", "", map[string]string{"purpose": "signin"}); r.code() != CodeTelegramUnavailable {
		t.Errorf("telegram: %d %v", r.status, r.body)
	}
}

// Email codes: a new address signs up (no phone yet), a known one signs in.
func TestEmailSignupAndSignIn(t *testing.T) {
	a := setupAccounts(t, nil)
	for _, bad := range []string{"", "malika", "malika@example", "a b@example.com", "Malika <malika@example.com>"} {
		if r := a.startEmail(bad); r.code() != CodeInvalidEmail {
			t.Errorf("start %q: %d %v", bad, r.status, r.body)
		}
	}
	if r := a.startEmail("malika@example.com"); r.status != http.StatusOK || r.body["expires_in"] != float64(600) {
		t.Fatalf("start: %d %v", r.status, r.body)
	}
	if r := a.startEmail("malika@example.com"); r.code() != "rate_limited" {
		t.Errorf("second code within a minute: %d %v", r.status, r.body)
	}
	if r := a.verifyEmail("malika@example.com", "000000"); r.code() != CodeInvalidCode && a.mail.code(t, "malika@example.com", 1) != "000000" {
		t.Errorf("wrong code: %d %v", r.status, r.body)
	}

	v := a.emailCode("  Malika@Example.COM ")
	ticket, _ := v.body["email_ticket"].(string)
	if v.body["status"] != "new" || len(ticket) < 40 {
		t.Fatalf("new: %v", v.body)
	}
	signup := func(body map[string]any) response { return a.do("POST", "/v1/auth/signup", "", body) }
	for _, c := range []struct {
		body map[string]any
		code string
	}{
		{map[string]any{"email_ticket": ticket, "first_name": "Malika", "last_name": "Y", "password": "short"}, CodeWeakPassword},
		{map[string]any{"email_ticket": ticket, "first_name": "Malika", "last_name": "Y"}, CodeWeakPassword},
		{map[string]any{"email_ticket": ticket, "first_name": " ", "last_name": "Y", "password": "a good password"}, CodeInvalidProfile},
		{map[string]any{"email_ticket": "nope", "first_name": "Malika", "last_name": "Y", "password": "a good password"}, CodeInvalidTicket},
	} {
		if r := signup(c.body); r.status != http.StatusUnprocessableEntity || r.code() != c.code {
			t.Errorf("%v: %d %v, want %s", c.body, r.status, r.body, c.code)
		}
	}
	r := signup(map[string]any{"email_ticket": ticket, "first_name": "Malika", "last_name": "Yusupova", "password": "a good password"})
	u, _ := r.body["user"].(map[string]any)
	if r.status != http.StatusOK || u["status"] != StatusRegistered || u["phone"] != nil || u["email"] != "malika@example.com" ||
		u["has_password"] != true || u["id"].(float64) > -1000001 || u["photo_url"] != nil {
		t.Fatalf("signup: %d %v", r.status, r.body)
	}
	if r := signup(map[string]any{"email_ticket": ticket, "first_name": "M", "last_name": "Y", "password": "a good password"}); r.code() != CodeInvalidTicket {
		t.Errorf("ticket twice: %d %v", r.status, r.body)
	}

	// The address signs in from then on: by its code (a forgotten password)
	// or with the password.
	v = a.emailCode("malika@example.com")
	if v.body["status"] != "signed_in" || v.body["token"] == "" || v.body["user"].(map[string]any)["id"] != u["id"] {
		t.Errorf("code for a known email: %v", v.body)
	}
	if _, me := a.mustLogin(" MALIKA@example.com", "a good password"); me["id"] != u["id"] {
		t.Errorf("login by email: %v", me)
	}
	if r := a.login("malika@example.com", "wrong password"); r.code() != CodeInvalidCredentials {
		t.Errorf("wrong password: %d %v", r.status, r.body)
	}
}

// No phone: the content is locked; the account itself works.
func TestPhoneRequired(t *testing.T) {
	a := setupAccounts(t, nil)
	mux := http.NewServeMux()
	h := NewHandler(a.svc, tashkent, false)
	h.Register(mux)
	mux.HandleFunc("GET /content", h.RequireContent(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	srv := httptest.NewServer(httpx.Chain(mux, httpx.WithRequestContext(slogDiscard())))
	defer srv.Close()
	c := client{t, srv}

	token, _ := a.emailSignup("locked@example.com", "Locked")
	if r := c.do("GET", "/content", token, nil); r.status != http.StatusForbidden || r.code() != CodePhoneRequired {
		t.Errorf("content without a phone: %d %v", r.status, r.body)
	}
	for _, path := range []string{"/v1/me"} {
		if r := c.do("GET", path, token, nil); r.status != http.StatusOK {
			t.Errorf("%s: %d", path, r.status)
		}
	}
	r := a.addPhone(token, "+998903000030")
	if r.status != http.StatusOK || r.body["user"].(map[string]any)["phone"] != "+998903000030" {
		t.Fatalf("add phone: %d %v", r.status, r.body)
	}
	if r := c.do("GET", "/content", r.body["token"].(string), nil); r.status != http.StatusNoContent {
		t.Errorf("content with a phone: %d %v", r.status, r.body)
	}
	if r := c.do("GET", "/content", token, nil); r.status != http.StatusNoContent {
		t.Errorf("the old session sees it too: %d", r.status)
	}
	// Once set, no other.
	if r := a.do("POST", "/v1/me/phone/start", token, map[string]string{"phone": "+998903000031"}); r.status != http.StatusConflict || r.code() != CodePhoneAlreadySet {
		t.Errorf("second phone: %d %v", r.status, r.body)
	}
	// HolliHop people have theirs.
	enrolled, _ := a.mustLogin("+998902001234", "1234")
	a.do("POST", "/v1/auth/password", enrolled, map[string]string{"current_password": "1234", "new_password": "a good password"})
	if r := a.do("POST", "/v1/me/phone/start", enrolled, map[string]string{"phone": "+998903000031"}); r.code() != CodePhoneAlreadySet {
		t.Errorf("enrolled: %d %v", r.status, r.body)
	}
	if r := c.do("GET", "/content", enrolled, nil); r.status != http.StatusNoContent {
		t.Errorf("enrolled content: %d", r.status)
	}
}

// Proving a number someone else has merges the phoneless account into theirs.
func TestAddPhoneMerges(t *testing.T) {
	a := setupAccounts(t, nil)
	ctx := t.Context()
	token, u := a.emailSignup("aziz.web@example.com", "Aziz")
	uid := int32(u["id"].(float64))
	for _, sql := range []string{
		`INSERT INTO google_links (sub, person_kind, person_id, email) VALUES ('g-u', 'student', $1, 'u@gmail.com')`,
		`INSERT INTO telegram_links (telegram_id, person_kind, person_id) VALUES (555, 'student', $1)`,
		`INSERT INTO student_goals (student_client_id, target_math) VALUES ($1, 700)`,
		`INSERT INTO parked_questions (id, student_client_id, test, version, subject, module, position, question_id)
		 VALUES (gen_random_uuid(), $1, 'DAY1', 1, 'math', 1, 3, gen_random_uuid())`,
	} {
		if _, err := a.pool.Exec(ctx, sql, uid); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	// Student 1's number: U goes into student 1, who keeps the default
	// password (must change it, as at any login).
	r := a.addPhone(token, "+998902001234")
	o, _ := r.body["user"].(map[string]any)
	if r.status != http.StatusOK || o["id"] != float64(1) || o["email"] != "aziz.web@example.com" || o["google"] != true ||
		o["telegram"] != true || o["must_change_password"] != true || o["status"] != StatusEnrolled {
		t.Fatalf("merge: %d %v", r.status, r.body)
	}
	for sql, want := range map[string]int{
		"SELECT count(*) FROM students WHERE client_id = $1":                                      0,
		"SELECT count(*) FROM credentials WHERE person_id = $1":                                   0,
		"SELECT count(*) FROM student_goals WHERE student_client_id = 1":                          1,
		"SELECT count(*) FROM parked_questions WHERE student_client_id = 1":                       1,
		"SELECT count(*) FROM google_links WHERE person_id = 1":                                   1,
		"SELECT count(*) FROM telegram_links WHERE person_id = 1 AND telegram_id = 555":           1,
		"SELECT count(*) FROM credentials WHERE person_id = 1 AND email = 'aziz.web@example.com'": 1,
	} {
		var n int
		args := []any{}
		if strings.Contains(sql, "$1") {
			args = append(args, uid)
		}
		if err := a.pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil || n != want {
			t.Errorf("%s = %d (%v), want %d", sql, n, err, want)
		}
	}
	if r := a.do("GET", "/v1/me", token, nil); r.status != http.StatusUnauthorized {
		t.Errorf("U's session lives on: %d", r.status)
	}
	if _, me := a.mustLogin("aziz.web@example.com", "1234"); me["id"] != float64(1) {
		t.Errorf("email now signs in student 1: %v", me)
	}

	// Into a teacher: U's practice can't move, and goes.
	token, u = a.emailSignup("teacher.web@example.com", "T")
	if _, err := a.pool.Exec(ctx, `INSERT INTO student_goals (student_client_id, target_math) VALUES ($1, 600)`, int32(u["id"].(float64))); err != nil {
		t.Fatal(err)
	}
	if r := a.addPhone(token, "+998901000010"); r.status != http.StatusOK || r.body["user"].(map[string]any)["kind"] != "teacher" {
		t.Errorf("into a teacher: %d %v", r.status, r.body)
	}

	// Into another registered student.
	first, _ := a.emailSignup("first@example.com", "First")
	if r := a.addPhone(first, "+998903000040"); r.status != http.StatusOK {
		t.Fatal(r.status)
	}
	second, _ := a.emailSignup("second@example.com", "Second")
	r = a.addPhone(second, "+998903000040")
	if u := r.body["user"].(map[string]any); r.status != http.StatusOK || u["first_name"] != "First" || u["email"] != "second@example.com" {
		t.Errorf("into a registered student: %d %v", r.status, r.body)
	}
}

// Someone without a password (Google, Telegram) sets one without a current one.
func TestFirstPasswordWithoutCurrent(t *testing.T) {
	a := setupAccounts(t, nil)
	_, u := a.emailSignup("pw@example.com", "Pw")
	if _, err := a.pool.Exec(t.Context(), "UPDATE credentials SET password_hash = NULL, password_changed_at = NULL WHERE person_id = $1", int32(u["id"].(float64))); err != nil {
		t.Fatal(err)
	}
	v := a.emailCode("pw@example.com")
	token := v.body["token"].(string)
	if v.body["user"].(map[string]any)["has_password"] != false {
		t.Fatalf("has_password: %v", v.body)
	}
	if r := a.do("POST", "/v1/auth/password", token, map[string]string{"new_password": "first password"}); r.status != http.StatusNoContent {
		t.Fatalf("first password: %d %v", r.status, r.body)
	}
	if r := a.do("POST", "/v1/auth/password", token, map[string]string{"new_password": "second password"}); r.code() != CodeWrongPassword {
		t.Errorf("then the current one is needed: %d %v", r.status, r.body)
	}
	a.mustLogin("pw@example.com", "first password")
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
	if r := a.verify(nobody, "123456"); r.code() != CodeInvalidCode {
		t.Errorf("verify for nobody: %d %v", r.status, r.body)
	}
	if r := a.start(nobody, PurposeReset); r.status != http.StatusTooManyRequests {
		t.Errorf("limits apply to nobody's number too: %d", r.status)
	}

	ticket := a.ticket(phone)
	if a.sms.count(nobody) != 0 {
		t.Error("a code went to a number nobody signs in with")
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
	ticket = a.ticket(phone)
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
