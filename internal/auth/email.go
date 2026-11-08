package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nazacd/makonbook-api/internal/auth/authdb"
	"github.com/nazacd/makonbook-api/internal/people"
	mailer "github.com/nazacd/makonbook-api/internal/platform/mail"
)

// Email codes (spec 21.2): a code sent to an address signs in whoever has it
// (which also covers a forgotten password), or answers a ticket to sign up
// with it. The person's email is also a sign-in identifier.

// EmailTicketTTL is how long a proved email waits for the sign-up form.
const EmailTicketTTL = 30 * time.Minute

const maxEmailLength = 254

var (
	ErrMailOff      = errors.New("sign-up by email isn't available")
	ErrMailFailed   = errors.New("the email couldn't be sent; try again in a minute")
	ErrInvalidEmail = errors.New("this isn't an email address")
	ErrEmailTaken   = errors.New("an account already has this email; sign in, or reset the password")
)

// WithMail turns on email codes.
func (s *Service) WithMail(sender mailer.Sender) *Service {
	s.mail = sender
	return s
}

// MailEnabled: email codes can be sent.
func (s *Service) MailEnabled() bool { return s.mail != nil }

// normalizeEmail trims and lower-cases an address and checks it is a plain
// one (name@domain.tld).
func normalizeEmail(raw string) (string, bool) {
	e := strings.ToLower(strings.TrimSpace(raw))
	if e == "" || len(e) > maxEmailLength || strings.ContainsAny(e, " \t\r\n<>\"(),;:\\") {
		return "", false
	}
	a, err := mail.ParseAddress(e)
	if err != nil || a.Address != e {
		return "", false
	}
	at := strings.LastIndex(e, "@")
	domain := e[at+1:]
	if at < 1 || !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return "", false
	}
	return e, true
}

// StartEmail sends a sign-up code to an email, if the limits allow: one a
// minute and 5 a day per address, 20 an hour per network (ip).
func (s *Service) StartEmail(ctx context.Context, raw, ip string) error {
	if s.mail == nil {
		return ErrMailOff
	}
	email, ok := normalizeEmail(raw)
	if !ok {
		return ErrInvalidEmail
	}
	code, err := newCode()
	if err != nil {
		return err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	now := s.now()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := authdb.New(tx)
	if err := q.LockEmail(ctx, email); err != nil {
		return fmt.Errorf("lock email: %w", err)
	}
	if wait, err := emailWait(ctx, q, email, ip, now); err != nil {
		return err
	} else if wait > 0 {
		return &RateLimitedError{RetryAfter: wait}
	}
	if err := q.InsertEmailCode(ctx, authdb.InsertEmailCodeParams{
		Email: email, Salt: salt, CodeHash: codeHash(salt, code), Ip: ip,
		CreatedAt: timestamp(now), ExpiresAt: timestamp(now.Add(CodeTTL)),
	}); err != nil {
		return fmt.Errorf("store code: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	sendCtx, cancel := context.WithTimeout(ctx, smsSendTimeout)
	defer cancel()
	subject := "Your SATMAKON code: " + code
	body := fmt.Sprintf("Your SATMAKON code is %s.\n\nIt works for 10 minutes. If you didn't ask for it, ignore this email.\n", code)
	if err := s.mail.Send(sendCtx, email, subject, body); err != nil {
		return fmt.Errorf("%w: %w", ErrMailFailed, err)
	}
	return nil
}

// emailWait is StartEmail's limits, as codeWait is StartPhone's.
func emailWait(ctx context.Context, q *authdb.Queries, email, ip string, now time.Time) (time.Duration, error) {
	var netCodes []pgtype.Timestamptz
	if ip != "" {
		var err error
		if netCodes, err = q.NetworkEmailCodesSince(ctx, authdb.NetworkEmailCodesSinceParams{Ip: ip, Since: timestamp(now.Add(-time.Hour))}); err != nil {
			return 0, fmt.Errorf("network codes: %w", err)
		}
	}
	day, err := q.EmailCodesSince(ctx, authdb.EmailCodesSinceParams{Email: email, Since: timestamp(now.Add(-24 * time.Hour))})
	if err != nil {
		return 0, fmt.Errorf("email codes: %w", err)
	}
	return waitFor(day, netCodes, now), nil
}

// EmailResult is a verified email code: a session when someone active has
// the address, else a ticket to sign up with it.
type EmailResult struct {
	SignedIn *LoginResult
	Ticket   string
}

// VerifyEmail checks a code sent by StartEmail: whoever active has the
// address is signed in (the code proved it, as a password would); otherwise
// it answers an email ticket (30 minutes, once) for Signup.
func (s *Service) VerifyEmail(ctx context.Context, raw, code, deviceID string) (EmailResult, error) {
	if s.mail == nil {
		return EmailResult{}, ErrMailOff
	}
	email, ok := normalizeEmail(raw)
	if !ok {
		return EmailResult{}, ErrInvalidEmail
	}
	now := s.now()
	row, err := s.q.LatestEmailCode(ctx, email)
	if errors.Is(err, pgx.ErrNoRows) {
		return EmailResult{}, ErrCodeExpired
	}
	if err != nil {
		return EmailResult{}, fmt.Errorf("latest code: %w", err)
	}
	c := sentCode{id: row.ID, salt: row.Salt, hash: row.CodeHash, tries: row.Tries, expires: row.ExpiresAt, usedAt: row.UsedAt,
		countTry: s.q.CountEmailCodeTry,
		use: func(ctx context.Context, id int64) (int64, error) {
			return s.q.UseEmailCode(ctx, authdb.UseEmailCodeParams{ID: id, Now: timestamp(now)})
		},
		waitForAnother: func() (time.Duration, error) { return emailWait(ctx, s.q, email, "", now) },
	}
	if err := c.check(ctx, code, now); err != nil {
		return EmailResult{}, err
	}
	p, active, err := emailOwner(ctx, s.q, s.people, email)
	if err != nil {
		return EmailResult{}, err
	}
	if active {
		res, err := s.signInPerson(ctx, s.q, p, deviceID)
		if err != nil {
			return EmailResult{}, err
		}
		return EmailResult{SignedIn: &res}, nil
	}
	ticket, err := s.newTicket(ctx, s.q, ticketData{purpose: ticketEmail, email: email}, EmailTicketTTL)
	return EmailResult{Ticket: ticket}, err
}

// SignupInput is a sign-up with a proved email: names and a password.
type SignupInput struct {
	EmailTicket string `json:"email_ticket"`
	FirstName   string `json:"first_name"`
	LastName    string `json:"last_name"`
	Password    string `json:"password"`
	DeviceID    string `json:"device_id"`
}

// Signup creates a registered student with the email, names and password,
// and no phone yet (they add it afterwards; until then the content is
// locked), and signs them in.
func (s *Service) Signup(ctx context.Context, in SignupInput) (LoginResult, error) {
	now := s.now()
	if _, err := s.q.PeekTicket(ctx, authdb.PeekTicketParams{TokenHash: hashToken(in.EmailTicket), Purpose: ticketEmail, Now: timestamp(now)}); errors.Is(err, pgx.ErrNoRows) {
		return LoginResult{}, ErrInvalidTicket
	} else if err != nil {
		return LoginResult{}, err
	}
	first, last, err := names(in.FirstName, in.LastName)
	if err != nil {
		return LoginResult{}, err
	}
	if err := checkNewPassword(in.Password, "", ""); err != nil {
		return LoginResult{}, &ValidationError{Field: "password", Err: err}
	}
	hash, err := s.hasher.Hash(in.Password)
	if err != nil {
		return LoginResult{}, fmt.Errorf("hash password: %w", err)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return LoginResult{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := authdb.New(tx)
	t, err := s.useTicket(ctx, q, in.EmailTicket, ticketEmail, now)
	if err != nil {
		return LoginResult{}, err
	}
	dir := people.NewDirectory(tx)
	id, err := people.CreateSelfStudent(ctx, tx, first, last, "")
	if err != nil {
		return LoginResult{}, err
	}
	p, err := dir.Get(ctx, people.KindStudent, id)
	if err != nil {
		return LoginResult{}, err
	}
	if err := linkEmail(ctx, q, dir, p, t.email); err != nil {
		return LoginResult{}, err // ErrEmailTaken: someone took it meanwhile
	}
	if err := q.SetPassword(ctx, authdb.SetPasswordParams{PersonKind: p.Kind, PersonID: p.ID, PasswordHash: pgtype.Text{String: hash, Valid: true}}); err != nil {
		return LoginResult{}, fmt.Errorf("set password: %w", err)
	}
	res, err := s.openSession(ctx, q, p, in.DeviceID, false)
	if err != nil {
		return LoginResult{}, err
	}
	return res, tx.Commit(ctx)
}

// emailOwner is who has an email; active says whether they still use the
// app (someone who left gives it up).
func emailOwner(ctx context.Context, q *authdb.Queries, dir personGetter, email string) (people.Person, bool, error) {
	o, err := q.EmailOwner(ctx, pgText(email))
	if errors.Is(err, pgx.ErrNoRows) {
		return people.Person{}, false, nil
	}
	if err != nil {
		return people.Person{}, false, fmt.Errorf("email owner: %w", err)
	}
	p, err := dir.Get(ctx, o.PersonKind, o.PersonID)
	if errors.Is(err, people.ErrNotFound) {
		return people.Person{}, false, nil
	}
	if err != nil {
		return people.Person{}, false, err
	}
	return p, p.Active, nil
}

type personGetter interface {
	Get(ctx context.Context, kind string, id int32) (people.Person, error)
}

// linkEmail makes email p's (inside the caller's transaction): taken from
// someone who left, ErrEmailTaken if someone else active has it.
func linkEmail(ctx context.Context, q *authdb.Queries, dir *people.Directory, p people.Person, email string) error {
	o, active, err := emailOwner(ctx, q, dir, email)
	if err != nil {
		return err
	}
	if active && o.Kind == p.Kind && o.ID == p.ID {
		return nil
	}
	if active {
		return ErrEmailTaken
	}
	if err := q.ClearEmail(ctx, pgText(email)); err != nil {
		return fmt.Errorf("clear email: %w", err)
	}
	if err := q.SetEmail(ctx, authdb.SetEmailParams{PersonKind: p.Kind, PersonID: p.ID, Email: pgText(email)}); err != nil {
		return fmt.Errorf("set email: %w", err)
	}
	return nil
}

func pgText(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }
