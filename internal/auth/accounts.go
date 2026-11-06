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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nazacd/makonbook-api/internal/auth/authdb"
	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/sms"
)

// Accounts (spec 21): anyone can sign up as a student after proving a phone
// number with a code sent by SMS (or come with Google and then prove one);
// anyone who signs in with a phone can reset their password the same way.

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

// Code purposes.
const (
	PurposeSignup = "signup"
	PurposeReset  = "reset"
)

// Ticket purposes besides the code purposes (auth_tickets.purpose).
const (
	ticketGoogle       = "google"        // back from Google, for /v1/auth/google/ticket
	ticketGoogleSignup = "google_signup" // a Google account waiting for a phone, for sign-up
	ticketEmail        = "email"         // a proved email waiting for a phone, for sign-up
)

var (
	ErrSMSOff        = errors.New("sign-up and reset by SMS aren't available")
	ErrSMSFailed     = errors.New("the SMS couldn't be sent; try again in a minute")
	ErrInvalidPhone  = errors.New("an Uzbek mobile number is needed, like 90 123 45 67")
	ErrCodeExpired   = errors.New("this code has expired or was used; ask for a new one")
	ErrInvalidTicket = errors.New("this step took too long or was already done; start again")
	ErrNoAccount     = errors.New("nobody signs in with this phone number")
	ErrPhoneTaken    = errors.New("this phone number already has an account; sign in, or reset the password")
	ErrIdentitySpent = errors.New("this email or Google account was already used with another phone number; start again")
	ErrGoogleTaken   = errors.New("this Google account is linked to another account")
	errNeedPassword  = errors.New("choose a password (8 to 128 characters)")
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

// SignupEnabled: people can sign up: codes go by SMS, and there is a way to
// start (an email code or Google).
func (s *Service) SignupEnabled() bool { return s.sms != nil && (s.mail != nil || s.google != nil) }

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

// PhoneOwner is who already signs in with a phone, as sign-up shows it once
// the phone is verified (spec 21.3): a registered student (sign-up is
// refused), or an enrolled person, by kind and short name ("Diyora A."), who
// is asked whether that is them.
type PhoneOwner struct {
	Status string `json:"status"`         // StatusRegistered or StatusEnrolled
	Kind   string `json:"kind,omitempty"` // enrolled: student or teacher
	Name   string `json:"name,omitempty"` // enrolled: first name and the last name's initial
}

// phoneOwner is the active person who signs in with phone; found is false
// when nobody does.
func phoneOwner(ctx context.Context, p People, phone string) (people.Person, bool, error) {
	owner, err := p.ByLoginPhone(ctx, phone)
	if errors.Is(err, people.ErrNotFound) {
		return people.Person{}, false, nil
	}
	if err != nil {
		return people.Person{}, false, err
	}
	return owner, owner.Active, nil
}

// shortName is a first name and the last name's initial: "Diyora A.".
func shortName(first, last string) string {
	first, last = strings.TrimSpace(first), strings.TrimSpace(last)
	r, _ := utf8.DecodeRuneInString(last)
	if last == "" || r == utf8.RuneError {
		return first
	}
	return strings.TrimSpace(first + " " + string(unicode.ToUpper(r)) + ".")
}

// Identity is what a sign-up starts with (spec 21.3): a proved email
// (EmailTicket, from VerifyEmail) or a Google account (GoogleTicket, from
// GoogleTicket's needs_phone); exactly one.
type Identity struct {
	EmailTicket  *string `json:"email_ticket"`
	GoogleTicket *string `json:"google_ticket"`
}

// identityTicket is the identity's ticket and its purpose; ErrInvalidTicket
// unless exactly one is given.
func (id Identity) ticket() (ticket, purpose string, err error) {
	email := id.EmailTicket != nil && *id.EmailTicket != ""
	google := id.GoogleTicket != nil && *id.GoogleTicket != ""
	switch {
	case email && !google:
		return *id.EmailTicket, ticketEmail, nil
	case google && !email:
		return *id.GoogleTicket, ticketGoogleSignup, nil
	default:
		return "", "", ErrInvalidTicket
	}
}

// VerifyPhone checks a code sent by StartPhone and answers a ticket: proof,
// for 15 minutes and once, that this phone was verified for this purpose.
// A sign-up needs an identity, and the ticket is good only with it; the
// answer then also says who already signs in with the phone (nil: nobody).
// An identity shows the owner of one phone at most: another phone with an
// owner is ErrIdentitySpent (a phone nobody has doesn't count). For a reset,
// identity is ignored and owner is nil.
func (s *Service) VerifyPhone(ctx context.Context, raw, code, purpose string, identity Identity) (ticket string, owner *PhoneOwner, err error) {
	if s.sms == nil {
		return "", nil, ErrSMSOff
	}
	phone, ok := uzbekPhone(raw)
	if !ok {
		return "", nil, ErrInvalidPhone
	}
	now := s.now()
	var idHash []byte
	var idPurpose string
	if purpose == PurposeSignup {
		idTicket, p, err := identity.ticket()
		if err != nil {
			return "", nil, err
		}
		idHash, idPurpose = hashToken(idTicket), p
		it, err := s.q.PeekTicket(ctx, authdb.PeekTicketParams{TokenHash: idHash, Purpose: idPurpose, Now: timestamp(now)})
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil, ErrInvalidTicket
		}
		if err != nil {
			return "", nil, err
		}
		if it.RevealedPhone.Valid && it.RevealedPhone.String != phone {
			return "", nil, ErrIdentitySpent
		}
	}

	row, err := s.q.LatestPhoneCode(ctx, authdb.LatestPhoneCodeParams{Phone: phone, Purpose: purpose})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, ErrCodeExpired
	}
	if err != nil {
		return "", nil, fmt.Errorf("latest code: %w", err)
	}
	c := sentCode{id: row.ID, salt: row.Salt, hash: row.CodeHash, tries: row.Tries, expires: row.ExpiresAt, usedAt: row.UsedAt,
		countTry: s.q.CountPhoneCodeTry,
		use: func(ctx context.Context, id int64) (int64, error) {
			return s.q.UsePhoneCode(ctx, authdb.UsePhoneCodeParams{ID: id, Now: timestamp(now)})
		},
		waitForAnother: func() (time.Duration, error) { return s.codeWait(ctx, s.q, phone, "", now) },
	}
	if err := c.check(ctx, code, now); err != nil {
		return "", nil, err
	}

	if purpose == PurposeSignup {
		p, found, err := phoneOwner(ctx, s.people, phone)
		switch {
		case err != nil:
			return "", nil, err
		case !found:
		case p.Registered:
			owner = &PhoneOwner{Status: StatusRegistered}
		default:
			owner = &PhoneOwner{Status: StatusEnrolled, Kind: p.Kind, Name: shortName(p.FirstName, p.LastName)}
		}
		if owner != nil {
			n, err := s.q.RevealPhone(ctx, authdb.RevealPhoneParams{Phone: phone, TokenHash: idHash, Purpose: idPurpose})
			if err != nil {
				return "", nil, fmt.Errorf("reveal phone: %w", err)
			}
			if n == 0 { // another phone was revealed meanwhile, or the identity was used
				return "", nil, ErrIdentitySpent
			}
		}
	}
	ticket, err = s.newTicket(ctx, s.q, ticketData{purpose: purpose, phone: phone, identity: idHash}, PhoneTicketTTL)
	if err != nil {
		return "", nil, err
	}
	return ticket, owner, nil
}

// SignupInput is a sign-up: a verified phone (Ticket), verified together with
// the identity (an email or a Google account), the person's names (needed
// only for a new account) and password (needed with an email).
type SignupInput struct {
	Ticket string `json:"ticket"`
	Identity
	FirstName *string `json:"first_name"`
	LastName  *string `json:"last_name"`
	Password  *string `json:"password"`
	DeviceID  string  `json:"device_id"`
}

// Signup creates a registered student with the verified phone and the
// identity, and signs them in. A phone an enrolled person (a student or
// teacher in HolliHop) signs in with signs into that account instead (the
// client asked them first; see VerifyPhone): the password, if given, replaces
// theirs (ending their other sessions), the email is added if nobody active
// has it, the Google account is linked, and the names are ignored. existing
// says which. A phone a registered student already signs in with is
// ErrPhoneTaken: they sign in, or reset their password.
func (s *Service) Signup(ctx context.Context, in SignupInput) (res LoginResult, existing bool, err error) {
	now := s.now()
	idTicket, idPurpose, err := in.ticket()
	if err != nil {
		return LoginResult{}, false, err
	}
	pt, err := s.q.PeekTicket(ctx, authdb.PeekTicketParams{TokenHash: hashToken(in.Ticket), Purpose: PurposeSignup, Now: timestamp(now)})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && subtle.ConstantTimeCompare(pt.Identity, hashToken(idTicket)) != 1) {
		return LoginResult{}, false, ErrInvalidTicket // unknown, or verified with another identity
	}
	if err != nil {
		return LoginResult{}, false, err
	}
	phone := pt.Phone.String
	it, err := s.q.PeekTicket(ctx, authdb.PeekTicketParams{TokenHash: hashToken(idTicket), Purpose: idPurpose, Now: timestamp(now)})
	if errors.Is(err, pgx.ErrNoRows) {
		return LoginResult{}, false, ErrInvalidTicket
	}
	if err != nil {
		return LoginResult{}, false, err
	}
	var email string
	var google *GoogleAccount
	if idPurpose == ticketEmail {
		email = it.Email.String
	} else {
		google = &GoogleAccount{Sub: it.GoogleSub.String, Email: it.Email.String}
	}

	if owner, found, err := phoneOwner(ctx, s.people, phone); err != nil {
		return LoginResult{}, false, err
	} else if found && owner.Registered {
		return LoginResult{}, false, ErrPhoneTaken
	}
	var hash string
	if in.Password == nil {
		if google == nil {
			return LoginResult{}, false, &ValidationError{Field: "password", Err: errNeedPassword}
		}
	} else {
		if err := checkNewPassword(*in.Password, "", phone); err != nil {
			return LoginResult{}, false, &ValidationError{Field: "password", Err: err}
		}
		if hash, err = s.hasher.Hash(*in.Password); err != nil {
			return LoginResult{}, false, fmt.Errorf("hash password: %w", err)
		}
	}

	// Two sign-ups with one number at once: the second finds the first's.
	for attempt := 0; ; attempt++ {
		res, existing, err = s.signup(ctx, in, idTicket, idPurpose, phone, hash, email, google)
		var pgErr *pgconn.PgError
		if attempt == 0 && errors.As(err, &pgErr) && pgErr.Code == "23505" {
			continue
		}
		return res, existing, err
	}
}

func (s *Service) signup(ctx context.Context, in SignupInput, idTicket, idPurpose, phone, hash, email string, google *GoogleAccount) (LoginResult, bool, error) {
	now := s.now()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return LoginResult{}, false, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := authdb.New(tx)
	if _, err := s.useTicket(ctx, q, in.Ticket, PurposeSignup, now); err != nil {
		return LoginResult{}, false, err
	}
	if _, err := s.useTicket(ctx, q, idTicket, idPurpose, now); err != nil {
		return LoginResult{}, false, err
	}

	dir := people.NewDirectory(tx)
	p, err := dir.ByLoginPhone(ctx, phone)
	existing := err == nil
	switch {
	case existing && !p.Active: // the sync clears a leaver's login phone: not expected
		return LoginResult{}, false, fmt.Errorf("sign-up: %s %d has the phone but isn't active", p.Kind, p.ID)
	case existing && p.Registered: // signed up meanwhile
		return LoginResult{}, false, ErrPhoneTaken
	case errors.Is(err, people.ErrNotFound):
		first, last, err := names(deref(in.FirstName), deref(in.LastName))
		if err != nil {
			return LoginResult{}, false, err
		}
		id, err := people.CreateSelfStudent(ctx, tx, first, last, phone)
		if err != nil {
			return LoginResult{}, false, err
		}
		if p, err = dir.Get(ctx, people.KindStudent, id); err != nil {
			return LoginResult{}, false, err
		}
	case err != nil:
		return LoginResult{}, false, err
	}

	custom := false
	if hash != "" {
		if err := q.SetPassword(ctx, authdb.SetPasswordParams{PersonKind: p.Kind, PersonID: p.ID, PasswordHash: pgtype.Text{String: hash, Valid: true}}); err != nil {
			return LoginResult{}, false, fmt.Errorf("set password: %w", err)
		}
		if _, err := q.RevokeAllSessions(ctx, authdb.RevokeAllSessionsParams{PersonKind: p.Kind, PersonID: p.ID}); err != nil {
			return LoginResult{}, false, fmt.Errorf("revoke sessions: %w", err)
		}
		custom = true
	} else if existing {
		c, err := q.GetCredential(ctx, authdb.GetCredentialParams{PersonKind: p.Kind, PersonID: p.ID})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return LoginResult{}, false, fmt.Errorf("get credential: %w", err)
		}
		_, custom = ownPassword(c.PasswordHash, c.PasswordChangedAt, p)
	}
	if email != "" {
		// A new account must get it; an enrolled one gets it if it's free.
		if err := linkEmail(ctx, q, dir, p, email); err != nil && (!existing || !errors.Is(err, ErrEmailTaken)) {
			return LoginResult{}, false, err
		}
	}
	if google != nil {
		if err := s.linkGoogle(ctx, q, dir, p, *google); err != nil {
			return LoginResult{}, false, err
		}
	}
	res, err := s.openSession(ctx, q, p, in.DeviceID, usesDefault(p, custom))
	if err != nil {
		return LoginResult{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return LoginResult{}, false, err
	}
	return res, existing, nil
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

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
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
	Email     string
	FirstName string
	LastName  string
}

// linkGoogle links the Google account to p (inside the caller's
// transaction). An account linked to someone else who is still active is
// ErrGoogleTaken; one linked to someone who left moves to p. A person has one
// Google account: a new one replaces theirs.
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
	if err := q.UnlinkGooglePerson(ctx, authdb.UnlinkGooglePersonParams{PersonKind: p.Kind, PersonID: p.ID}); err != nil {
		return fmt.Errorf("unlink google: %w", err)
	}
	if err := q.InsertGoogleLink(ctx, authdb.InsertGoogleLinkParams{Sub: g.Sub, PersonKind: p.Kind, PersonID: p.ID, Email: g.Email}); err != nil {
		return fmt.Errorf("link google: %w", err)
	}
	return nil
}

// ticketData is what a ticket proves.
type ticketData struct {
	purpose  string
	phone    string         // signup, reset
	email    string         // email
	identity []byte         // signup: the identity ticket's hash
	google   *GoogleAccount // google, google_signup
}

// newTicket stores a single-use ticket and returns it.
func (s *Service) newTicket(ctx context.Context, q *authdb.Queries, t ticketData, ttl time.Duration) (string, error) {
	token, hash, err := newToken()
	if err != nil {
		return "", err
	}
	now := s.now()
	p := authdb.InsertTicketParams{TokenHash: hash, Purpose: t.purpose, CreatedAt: timestamp(now), ExpiresAt: timestamp(now.Add(ttl))}
	if t.phone != "" {
		p.Phone = pgtype.Text{String: t.phone, Valid: true}
	}
	if t.email != "" {
		p.Email = pgtype.Text{String: t.email, Valid: true}
	}
	p.Identity = t.identity
	if g := t.google; g != nil {
		p.GoogleSub = pgtype.Text{String: g.Sub, Valid: true}
		p.Email = pgtype.Text{String: g.Email, Valid: true}
		p.FirstName = pgtype.Text{String: g.FirstName, Valid: true}
		p.LastName = pgtype.Text{String: g.LastName, Valid: true}
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
	t := ticketData{purpose: purpose, phone: row.Phone.String}
	if row.GoogleSub.Valid {
		t.google = &GoogleAccount{Sub: row.GoogleSub.String, Email: row.Email.String, FirstName: row.FirstName.String, LastName: row.LastName.String}
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
	return codes + tickets + emails, err
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
