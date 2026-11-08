package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nazacd/makonbook-api/internal/auth/authdb"
	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/sms"
)

// Accounts (spec 21): anyone can sign up as a student with an email code,
// Google or Telegram; a phone is added afterwards (an SMS code, or the one
// Telegram shares), and proving a number someone else has merges the two
// accounts (phone.go). Anyone who signs in with a phone can reset their
// password with an SMS code.

// Code and ticket rules.
const (
	CodeTTL          = 10 * time.Minute
	CodeTries        = 5
	ResendAfter      = time.Minute // per phone: one code a minute …
	CodesPerDay      = 5           // … and 5 a day
	CodesPerNetwork  = 20          // per network an hour
	PhoneTicketTTL   = 15 * time.Minute
	smsSendTimeout   = 30 * time.Second
	maxNameRunes     = 60
	uzbekCountryCode = "+998"
)

// Code purposes (phone_codes.purpose).
const (
	PurposeReset  = "reset"  // a forgotten password
	purposeVerify = "verify" // a signed-in person adding their phone
)

// Ticket purposes besides the code purposes (auth_tickets.purpose).
const (
	ticketGoogle         = "google"          // back from Google, for /v1/auth/google/ticket
	ticketEmail          = "email"           // a proved email nobody has, for sign-up
	ticketTelegramSignin = "telegram_signin" // back from Telegram, for /v1/auth/telegram/ticket
	ticketTelegramVerify = "telegram_verify" // back from Telegram, for /v1/me/phone/telegram
)

var (
	ErrSMSOff          = errors.New("sign-up and reset by SMS aren't available")
	ErrSMSFailed       = errors.New("the SMS couldn't be sent; try again in a minute")
	ErrInvalidPhone    = errors.New("an Uzbek mobile number is needed, like 90 123 45 67")
	ErrCodeExpired     = errors.New("this code has expired or was used; ask for a new one")
	ErrInvalidTicket   = errors.New("this step took too long or was already done; start again")
	ErrNoAccount       = errors.New("nobody signs in with this phone number")
	ErrGoogleTaken     = errors.New("this Google account is linked to another account")
	ErrPhoneSet        = errors.New("you already have a phone number")
	ErrNoTelegramPhone = errors.New("Telegram didn't share a phone number; allow it, or verify it by SMS") //nolint:staticcheck // a product name
)

// RateLimitedError: too many codes for a phone or from a network.
type RateLimitedError struct{ RetryAfter time.Duration }

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("too many codes asked for; try again in %s", e.RetryAfter.Round(time.Second))
}

// WrongCodeError: the code isn't right. With no tries left the code is
// spent, and RetryAfter says when a new one can be sent.
type WrongCodeError struct {
	TriesLeft  int
	RetryAfter time.Duration
}

func (e *WrongCodeError) Error() string {
	switch e.TriesLeft {
	case 0:
		return "wrong code, and no tries left; ask for a new code"
	case 1:
		return "wrong code; 1 try left"
	default:
		return fmt.Sprintf("wrong code; %d tries left", e.TriesLeft)
	}
}

// ProfileError: a name that can't be used.
type ProfileError struct{ Reason string }

func (e *ProfileError) Error() string { return e.Reason }

// WithSMS turns on sign-up and password reset: codes go through sender, in
// text (with {code}). log records sends that fail in the background.
func (s *Service) WithSMS(sender sms.Sender, text string, log *slog.Logger) *Service {
	s.sms, s.smsText, s.log = sender, text, log
	return s
}

// SignupEnabled: people can sign up with an email code (Google and Telegram
// make accounts on their own).
func (s *Service) SignupEnabled() bool { return s.mail != nil }

// ResetEnabled: people can reset their password with an SMS code.
func (s *Service) ResetEnabled() bool { return s.sms != nil }

// uzbekPhone normalises a number as the rest of the API does (spec 5) and
// accepts Uzbek numbers only: codes go by SMS through an Uzbek gateway.
func uzbekPhone(raw string) (string, bool) {
	phone, ok := people.NormalizePhone(raw)
	return phone, ok && strings.HasPrefix(phone, uzbekCountryCode)
}

// StartPhone sends a code for purpose to the phone, if the limits allow: one
// a minute and 5 a day per phone, 20 an hour per network (ip). For a reset it
// answers the same whether or not anyone signs in with the number, and sends
// only if someone does (in the background, so the time taken says nothing
// either).
func (s *Service) StartPhone(ctx context.Context, raw, purpose, ip string) error {
	if s.sms == nil {
		return ErrSMSOff
	}
	phone, ok := uzbekPhone(raw)
	if !ok {
		return ErrInvalidPhone
	}
	send := true
	if purpose == PurposeReset {
		p, err := s.people.ByLoginPhone(ctx, phone)
		if err != nil && !errors.Is(err, people.ErrNotFound) {
			return err
		}
		send = err == nil && p.Active
	}

	code, err := newCode()
	if err != nil {
		return err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	var hash []byte
	if send {
		hash = codeHash(salt, code)
	}

	now := s.now()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := authdb.New(tx)
	if err := q.LockPhone(ctx, phone); err != nil {
		return fmt.Errorf("lock phone: %w", err)
	}
	if wait, err := s.codeWait(ctx, q, phone, ip, now); err != nil {
		return err
	} else if wait > 0 {
		return &RateLimitedError{RetryAfter: wait}
	}
	if err := q.InsertPhoneCode(ctx, authdb.InsertPhoneCodeParams{
		Phone: phone, Purpose: purpose, Salt: salt, CodeHash: hash, Ip: ip,
		CreatedAt: timestamp(now), ExpiresAt: timestamp(now.Add(CodeTTL)),
	}); err != nil {
		return fmt.Errorf("store code: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	if !send {
		return nil
	}
	text := strings.ReplaceAll(s.smsText, "{code}", code)
	if purpose == PurposeReset {
		go func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), smsSendTimeout)
			defer cancel()
			if err := s.sms.Send(ctx, phone, text); err != nil {
				s.log.Error("send reset code", slog.Any("err", err))
			}
		}()
		return nil
	}
	sendCtx, cancel := context.WithTimeout(ctx, smsSendTimeout)
	defer cancel()
	if err := s.sms.Send(sendCtx, phone, text); err != nil {
		return fmt.Errorf("%w: %w", ErrSMSFailed, err)
	}
	return nil
}

// codeWait is how long the phone (and, given ip, the network) must wait for
// another code; 0 when one can be sent now.
func (s *Service) codeWait(ctx context.Context, q *authdb.Queries, phone, ip string, now time.Time) (time.Duration, error) {
	var network []pgtype.Timestamptz
	if ip != "" {
		var err error
		if network, err = q.NetworkCodesSince(ctx, authdb.NetworkCodesSinceParams{Ip: ip, Since: timestamp(now.Add(-time.Hour))}); err != nil {
			return 0, fmt.Errorf("network codes: %w", err)
		}
	}
	day, err := q.PhoneCodesSince(ctx, authdb.PhoneCodesSinceParams{Phone: phone, Since: timestamp(now.Add(-24 * time.Hour))})
	if err != nil {
		return 0, fmt.Errorf("phone codes: %w", err)
	}
	return waitFor(day, network, now), nil
}

// waitFor applies the limits to the codes a phone or address had in the
// last day and a network in the last hour (oldest first): one a minute and
// CodesPerDay a day for the first, CodesPerNetwork an hour for the second.
func waitFor(day, network []pgtype.Timestamptz, now time.Time) time.Duration {
	var wait time.Duration
	if len(network) >= CodesPerNetwork {
		wait = max(wait, network[len(network)-CodesPerNetwork].Time.Add(time.Hour).Sub(now))
	}
	if n := len(day); n > 0 {
		wait = max(wait, day[n-1].Time.Add(ResendAfter).Sub(now))
		if n >= CodesPerDay {
			wait = max(wait, day[n-CodesPerDay].Time.Add(24*time.Hour).Sub(now))
		}
	}
	if wait > 0 && wait < time.Second {
		wait = time.Second
	}
	return max(wait, 0)
}

// sentCode is a code as stored (phone_codes or email_codes).
type sentCode struct {
	id              int64
	salt, hash      []byte
	tries           int16
	expires, usedAt pgtype.Timestamptz
	countTry        func(ctx context.Context, id int64) (int16, error)
	use             func(ctx context.Context, id int64) (int64, error)
	waitForAnother  func() (time.Duration, error)
}

// check compares code with a sent code, counting the try, and uses the code
// when it is right.
func (c sentCode) check(ctx context.Context, code string, now time.Time) error {
	if c.usedAt.Valid || !c.expires.Time.After(now) {
		return ErrCodeExpired
	}
	spent := func() error {
		wait, err := c.waitForAnother()
		if err != nil {
			return err
		}
		return &WrongCodeError{TriesLeft: 0, RetryAfter: wait}
	}
	if c.tries >= CodeTries {
		return spent()
	}
	tries, err := c.countTry(ctx, c.id)
	if errors.Is(err, pgx.ErrNoRows) { // used up or used meanwhile
		return spent()
	}
	if err != nil {
		return fmt.Errorf("count try: %w", err)
	}
	code = strings.Join(strings.Fields(code), "")
	if c.hash == nil || subtle.ConstantTimeCompare(codeHash(c.salt, code), c.hash) != 1 {
		if left := CodeTries - int(tries); left > 0 {
			return &WrongCodeError{TriesLeft: left}
		}
		return spent()
	}
	if n, err := c.use(ctx, c.id); err != nil {
		return fmt.Errorf("use code: %w", err)
	} else if n == 0 {
		return ErrCodeExpired
	}
	return nil
}

// VerifyPhone checks a password-reset code sent by StartPhone and answers a
// ticket: proof, for 15 minutes and once, that this phone was verified.
func (s *Service) VerifyPhone(ctx context.Context, raw, code string) (string, error) {
	phone, err := s.checkPhoneCode(ctx, raw, code, PurposeReset)
	if err != nil {
		return "", err
	}
	return s.newTicket(ctx, s.q, ticketData{purpose: PurposeReset, phone: phone}, PhoneTicketTTL)
}

// checkPhoneCode checks the latest code sent to the phone for purpose and
// uses it; it answers the phone, normalised.
func (s *Service) checkPhoneCode(ctx context.Context, raw, code, purpose string) (string, error) {
	if s.sms == nil {
		return "", ErrSMSOff
	}
	phone, ok := uzbekPhone(raw)
	if !ok {
		return "", ErrInvalidPhone
	}
	now := s.now()
	row, err := s.q.LatestPhoneCode(ctx, authdb.LatestPhoneCodeParams{Phone: phone, Purpose: purpose})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrCodeExpired
	}
	if err != nil {
		return "", fmt.Errorf("latest code: %w", err)
	}
	c := sentCode{id: row.ID, salt: row.Salt, hash: row.CodeHash, tries: row.Tries, expires: row.ExpiresAt, usedAt: row.UsedAt,
		countTry: s.q.CountPhoneCodeTry,
		use: func(ctx context.Context, id int64) (int64, error) {
			return s.q.UsePhoneCode(ctx, authdb.UsePhoneCodeParams{ID: id, Now: timestamp(now)})
		},
		waitForAnother: func() (time.Duration, error) { return s.codeWait(ctx, s.q, phone, "", now) },
	}
	if err := c.check(ctx, code, now); err != nil {
		return "", err
	}
	return phone, nil
}

// Reset sets a new password for whoever signs in with the phone a reset
// ticket proves, unlocks the account, ends their other sessions and signs
// them in.
func (s *Service) Reset(ctx context.Context, ticket, password, deviceID string) (LoginResult, error) {
	now := s.now()
	pt, err := s.q.PeekTicket(ctx, authdb.PeekTicketParams{TokenHash: hashToken(ticket), Purpose: PurposeReset, Now: timestamp(now)})
	if errors.Is(err, pgx.ErrNoRows) {
		return LoginResult{}, ErrInvalidTicket
	}
	if err != nil {
		return LoginResult{}, err
	}
	phone := pt.Phone.String
	p, err := s.people.ByLoginPhone(ctx, phone)
	if errors.Is(err, people.ErrNotFound) || (err == nil && !p.Active) {
		return LoginResult{}, ErrNoAccount
	}
	if err != nil {
		return LoginResult{}, err
	}
	if err := checkNewPassword(password, "", phone); err != nil {
		return LoginResult{}, &ValidationError{Field: "password", Err: err}
	}
	hash, err := s.hasher.Hash(password)
	if err != nil {
		return LoginResult{}, fmt.Errorf("hash password: %w", err)
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return LoginResult{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := authdb.New(tx)
	if _, err := s.useTicket(ctx, q, ticket, PurposeReset, now); err != nil {
		return LoginResult{}, err
	}
	// SetPassword also clears failures and the lockout.
	if err := q.SetPassword(ctx, authdb.SetPasswordParams{PersonKind: p.Kind, PersonID: p.ID, PasswordHash: pgtype.Text{String: hash, Valid: true}}); err != nil {
		return LoginResult{}, fmt.Errorf("set password: %w", err)
	}
	if _, err := q.RevokeAllSessions(ctx, authdb.RevokeAllSessionsParams{PersonKind: p.Kind, PersonID: p.ID}); err != nil {
		return LoginResult{}, fmt.Errorf("revoke sessions: %w", err)
	}
	res, err := s.openSession(ctx, q, p, deviceID, false)
	if err != nil {
		return LoginResult{}, err
	}
	return res, tx.Commit(ctx)
}

// SetNames sets the names the person sees for themselves (spec 21); nil
// clears one, back to HolliHop's, which only people in HolliHop have.
func (s *Service) SetNames(ctx context.Context, pr Principal, first, last *string) error {
	p, err := s.people.Get(ctx, pr.Kind, pr.ID)
	if err != nil {
		return err
	}
	for _, n := range []*string{first, last} {
		if n == nil {
			if p.Registered {
				return &ProfileError{"a first name and a last name are needed"}
			}
			continue
		}
		v, err := name(*n)
		if err != nil {
			return err
		}
		*n = v
	}
	return s.people.SetNames(ctx, p.Kind, p.ID, first, last)
}

// names checks a first and a last name.
func names(first, last string) (string, string, error) {
	f, err := name(first)
	if err != nil {
		return "", "", err
	}
	l, err := name(last)
	if err != nil {
		return "", "", err
	}
	return f, l, nil
}

// name trims a name and checks it: 1 to 60 characters, no control
// characters.
func name(s string) (string, error) {
	s = strings.TrimSpace(s)
	if n := utf8.RuneCountInString(s); n == 0 || n > maxNameRunes {
		return "", &ProfileError{"names must be 1 to 60 characters"}
	}
	if strings.IndexFunc(s, unicode.IsControl) >= 0 || !utf8.ValidString(s) {
		return "", &ProfileError{"names can't contain control characters"}
	}
	return s, nil
}

// GoogleAccount is a Google account as its ID token describes it.
type GoogleAccount struct {
	Sub       string // Google's id for the account, never reused
	Email     string // verified
	FirstName string
	LastName  string
	PhotoURL  string
}

// linkGoogle links the Google account to p (inside the caller's
// transaction). An account linked to someone else who is still active is
// ErrGoogleTaken; one linked to someone who left moves to p. A person may
// have several Google accounts.
func (s *Service) linkGoogle(ctx context.Context, q *authdb.Queries, dir *people.Directory, p people.Person, g GoogleAccount) error {
	link, err := q.GoogleLinkBySub(ctx, g.Sub)
	switch {
	case err == nil && link.PersonKind == p.Kind && link.PersonID == p.ID:
		return q.SetGoogleEmail(ctx, authdb.SetGoogleEmailParams{Sub: g.Sub, Email: g.Email})
	case err == nil:
		other, err := dir.Get(ctx, link.PersonKind, link.PersonID)
		if err != nil && !errors.Is(err, people.ErrNotFound) {
			return err
		}
		if err == nil && other.Active {
			return ErrGoogleTaken
		}
		if err := q.DeleteGoogleLink(ctx, g.Sub); err != nil {
			return fmt.Errorf("unlink google: %w", err)
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("google link: %w", err)
	}
	if err := q.InsertGoogleLink(ctx, authdb.InsertGoogleLinkParams{Sub: g.Sub, PersonKind: p.Kind, PersonID: p.ID, Email: g.Email}); err != nil {
		return fmt.Errorf("link google: %w", err)
	}
	return nil
}

// ticketData is what a ticket proves.
type ticketData struct {
	purpose  string
	phone    string           // reset
	email    string           // email
	google   *GoogleAccount   // google
	telegram *TelegramAccount // telegram_signin, telegram_verify
	// telegram_verify: who started the flow.
	personKind string
	personID   int32
}

func optText(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

// newTicket stores a single-use ticket and returns it.
func (s *Service) newTicket(ctx context.Context, q *authdb.Queries, t ticketData, ttl time.Duration) (string, error) {
	token, hash, err := newToken()
	if err != nil {
		return "", err
	}
	now := s.now()
	p := authdb.InsertTicketParams{TokenHash: hash, Purpose: t.purpose, CreatedAt: timestamp(now), ExpiresAt: timestamp(now.Add(ttl)),
		Phone: optText(t.phone), Email: optText(t.email), PersonKind: optText(t.personKind)}
	if t.personKind != "" {
		p.PersonID = pgtype.Int4{Int32: t.personID, Valid: true}
	}
	if tg := t.telegram; tg != nil {
		p.TelegramID = pgtype.Int8{Int64: tg.ID, Valid: true}
		p.TelegramUsername = optText(tg.Username)
		p.FirstName, p.LastName = pgtype.Text{String: tg.FirstName, Valid: true}, pgtype.Text{String: tg.LastName, Valid: true}
		p.SharedPhone, p.PhotoUrl = optText(tg.Phone), optText(tg.PhotoURL)
	}
	if g := t.google; g != nil {
		p.GoogleSub = pgtype.Text{String: g.Sub, Valid: true}
		p.Email = pgtype.Text{String: g.Email, Valid: true}
		p.FirstName, p.LastName = pgtype.Text{String: g.FirstName, Valid: true}, pgtype.Text{String: g.LastName, Valid: true}
		p.PhotoUrl = optText(g.PhotoURL)
	}
	if err := q.InsertTicket(ctx, p); err != nil {
		return "", fmt.Errorf("store ticket: %w", err)
	}
	return token, nil
}

// useTicket spends a ticket: ErrInvalidTicket if it is unknown, used,
// expired or for another purpose.
func (s *Service) useTicket(ctx context.Context, q *authdb.Queries, ticket, purpose string, now time.Time) (ticketData, error) {
	row, err := q.UseTicket(ctx, authdb.UseTicketParams{TokenHash: hashToken(ticket), Purpose: purpose, Now: timestamp(now)})
	if errors.Is(err, pgx.ErrNoRows) {
		return ticketData{}, ErrInvalidTicket
	}
	if err != nil {
		return ticketData{}, fmt.Errorf("use ticket: %w", err)
	}
	t := ticketData{purpose: purpose, phone: row.Phone.String, email: row.Email.String,
		personKind: row.PersonKind.String, personID: row.PersonID.Int32}
	if row.GoogleSub.Valid {
		t.google = &GoogleAccount{Sub: row.GoogleSub.String, Email: row.Email.String, FirstName: row.FirstName.String,
			LastName: row.LastName.String, PhotoURL: row.PhotoUrl.String}
	}
	if row.TelegramID.Valid {
		t.telegram = &TelegramAccount{ID: row.TelegramID.Int64, Username: row.TelegramUsername.String, FirstName: row.FirstName.String,
			LastName: row.LastName.String, Phone: row.SharedPhone.String, PhotoURL: row.PhotoUrl.String}
	}
	return t, nil
}

// CleanUp deletes codes older than two days (the limits look back one) and
// tickets that expired a day ago.
func CleanUp(ctx context.Context, db authdb.DBTX, now time.Time) (int64, error) {
	q := authdb.New(db)
	codes, err := q.DeleteOldPhoneCodes(ctx, timestamp(now.Add(-48*time.Hour)))
	if err != nil {
		return 0, err
	}
	tickets, err := q.DeleteOldTickets(ctx, timestamp(now.Add(-24*time.Hour)))
	if err != nil {
		return 0, err
	}
	emails, err := q.DeleteOldEmailCodes(ctx, timestamp(now.Add(-48*time.Hour)))
	if err != nil {
		return 0, err
	}
	states, err := q.DeleteOldOAuthStates(ctx, timestamp(now.Add(-24*time.Hour)))
	return codes + tickets + emails + states, err
}

// newCode is 6 random digits.
func newCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func codeHash(salt []byte, code string) []byte {
	h := sha256.New()
	h.Write(salt)
	h.Write([]byte(code))
	return h.Sum(nil)
}

func timestamp(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}
