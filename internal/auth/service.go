// Package auth handles login for students and teachers (spec 7): phone or
// username plus password, the default password, lockouts, and sessions.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nazacd/makonbook-api/internal/auth/authdb"
	"github.com/nazacd/makonbook-api/internal/people"
)

const (
	// SessionTTL is how long a session lives without use; every use (at most
	// hourly) extends it.
	SessionTTL    = 30 * 24 * time.Hour
	touchInterval = time.Hour
)

var (
	ErrInvalidCredentials = errors.New("invalid login or password")
	ErrUnauthenticated    = errors.New("not logged in")
	ErrWrongPassword      = errors.New("current password is wrong")
	ErrUsernameTaken      = errors.New("username is taken")
)

// LockedError means the account is temporarily locked after repeated
// failures.
type LockedError struct{ Until time.Time }

func (e *LockedError) Error() string { return "account temporarily locked" }

// ValidationError carries a user-facing reason a new password or username
// was rejected.
type ValidationError struct {
	Field string // "password" or "username"
	Err   error
}

func (e *ValidationError) Error() string { return e.Err.Error() }

// People is what auth needs from the people feature. Satisfied by
// *people.Directory.
type People interface {
	ByLoginPhone(ctx context.Context, phone string) (people.Person, error)
	Get(ctx context.Context, kind string, id int32) (people.Person, error)
	Groups(ctx context.Context, kind string, id int32, today time.Time) ([]people.GroupInfo, error)
}

// Principal is the logged-in person behind a request.
type Principal struct {
	SessionID int64
	Kind      string // people.KindStudent or people.KindTeacher
	ID        int32
	// MustChangePassword is true while the person uses the default password;
	// only a few endpoints are reachable until they choose their own.
	MustChangePassword bool
}

type Service struct {
	q      *authdb.Queries
	people People
	hasher *Hasher
	now    func() time.Time
}

func NewService(db authdb.DBTX, p People, h *Hasher) *Service {
	return &Service{q: authdb.New(db), people: p, hasher: h, now: time.Now}
}

// LoginResult is a new session.
type LoginResult struct {
	Token     string
	ExpiresAt time.Time
	Principal Principal
}

// Login checks a phone-or-username and password and opens a session.
//
// Every failure that isn't a lockout returns ErrInvalidCredentials, whether
// the account exists or not.
func (s *Service) Login(ctx context.Context, login, password, deviceID string) (LoginResult, error) {
	p, err := s.lookup(ctx, login)
	if errors.Is(err, people.ErrNotFound) || (err == nil && !p.Active) {
		s.hasher.Burn()
		return LoginResult{}, ErrInvalidCredentials
	}
	if err != nil {
		return LoginResult{}, err
	}

	cred, err := s.credential(ctx, p)
	if err != nil {
		return LoginResult{}, err
	}
	if cred.LockedUntil.Valid && cred.LockedUntil.Time.After(s.now()) {
		return LoginResult{}, &LockedError{Until: cred.LockedUntil.Time}
	}

	custom, ok := s.checkPassword(p, cred, password)
	if !ok {
		if err := s.recordFailure(ctx, p); err != nil {
			return LoginResult{}, err
		}
		return LoginResult{}, ErrInvalidCredentials
	}
	if err := s.q.ResetLoginFailures(ctx, authdb.ResetLoginFailuresParams{PersonKind: p.Kind, PersonID: p.ID}); err != nil {
		return LoginResult{}, fmt.Errorf("reset failures: %w", err)
	}

	token, hash, err := newToken()
	if err != nil {
		return LoginResult{}, err
	}
	expires := s.now().Add(SessionTTL)
	row, err := s.q.CreateSession(ctx, authdb.CreateSessionParams{
		TokenHash:  hash,
		PersonKind: p.Kind,
		PersonID:   p.ID,
		DeviceID:   deviceID,
		ExpiresAt:  pgtype.Timestamptz{Time: expires, Valid: true},
	})
	if err != nil {
		return LoginResult{}, fmt.Errorf("create session: %w", err)
	}
	return LoginResult{
		Token:     token,
		ExpiresAt: expires,
		Principal: Principal{SessionID: row.ID, Kind: p.Kind, ID: p.ID, MustChangePassword: !custom},
	}, nil
}

// lookup resolves a login identifier: phone numbers go through people's login
// phone, anything else is a username.
func (s *Service) lookup(ctx context.Context, login string) (people.Person, error) {
	if looksLikePhone(login) {
		phone, ok := people.NormalizePhone(login)
		if !ok {
			return people.Person{}, people.ErrNotFound
		}
		return s.people.ByLoginPhone(ctx, phone)
	}
	username, err := normalizeUsername(login)
	if err != nil {
		return people.Person{}, people.ErrNotFound
	}
	c, err := s.q.GetCredentialByUsername(ctx, pgtype.Text{String: username, Valid: true})
	if errors.Is(err, pgx.ErrNoRows) {
		return people.Person{}, people.ErrNotFound
	}
	if err != nil {
		return people.Person{}, fmt.Errorf("find username: %w", err)
	}
	return s.people.Get(ctx, c.PersonKind, c.PersonID)
}

// credential returns the person's credentials row, or an empty one.
func (s *Service) credential(ctx context.Context, p people.Person) (authdb.Credential, error) {
	c, err := s.q.GetCredential(ctx, authdb.GetCredentialParams{PersonKind: p.Kind, PersonID: p.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return authdb.Credential{PersonKind: p.Kind, PersonID: p.ID}, nil
	}
	if err != nil {
		return authdb.Credential{}, fmt.Errorf("get credential: %w", err)
	}
	return c, nil
}

// ownPassword returns the person's own password hash if they set one during
// their current active period. Otherwise the default password applies.
func ownPassword(hash pgtype.Text, changedAt pgtype.Timestamptz, p people.Person) (string, bool) {
	if !hash.Valid || !changedAt.Valid || changedAt.Time.Before(p.ActivatedAt) {
		return "", false
	}
	return hash.String, true
}

// checkPassword verifies password against the person's own password or, if
// they have none, the default one. custom reports which applied.
func (s *Service) checkPassword(p people.Person, c authdb.Credential, password string) (custom, ok bool) {
	if hash, has := ownPassword(c.PasswordHash, c.PasswordChangedAt, p); has {
		return true, s.hasher.Verify(hash, password)
	}
	s.hasher.Burn() // same cost as a real check
	def := defaultPassword(p.LoginPhone)
	if def == "" {
		return false, false // no valid phone: no default password
	}
	return false, subtle.ConstantTimeCompare([]byte(password), []byte(def)) == 1
}

func (s *Service) recordFailure(ctx context.Context, p people.Person) error {
	if _, err := s.q.RecordLoginFailure(ctx, authdb.RecordLoginFailureParams{PersonKind: p.Kind, PersonID: p.ID}); err != nil {
		return fmt.Errorf("record login failure: %w", err)
	}
	return nil
}

// Authenticate resolves a bearer token. Sessions die with deactivation and
// don't survive into a later active period.
func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	if token == "" {
		return Principal{}, ErrUnauthenticated
	}
	row, err := s.q.GetLiveSession(ctx, hashToken(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, fmt.Errorf("get session: %w", err)
	}

	p, err := s.people.Get(ctx, row.PersonKind, row.PersonID)
	if errors.Is(err, people.ErrNotFound) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, err
	}
	if !p.Active || row.CreatedAt.Time.Before(p.ActivatedAt) {
		return Principal{}, ErrUnauthenticated
	}

	if now := s.now(); now.Sub(row.LastSeenAt.Time) > touchInterval {
		err := s.q.TouchSession(ctx, authdb.TouchSessionParams{ID: row.ID, ExpiresAt: pgtype.Timestamptz{Time: now.Add(SessionTTL), Valid: true}})
		if err != nil {
			return Principal{}, fmt.Errorf("touch session: %w", err)
		}
	}

	_, custom := ownPassword(row.PasswordHash, row.PasswordChangedAt, p)
	return Principal{SessionID: row.ID, Kind: p.Kind, ID: p.ID, MustChangePassword: !custom}, nil
}

// ChangePassword replaces the password (the default one included) after
// checking the current one, and logs out every other session.
func (s *Service) ChangePassword(ctx context.Context, pr Principal, current, next string) error {
	p, err := s.people.Get(ctx, pr.Kind, pr.ID)
	if err != nil {
		return err
	}
	cred, err := s.credential(ctx, p)
	if err != nil {
		return err
	}
	if cred.LockedUntil.Valid && cred.LockedUntil.Time.After(s.now()) {
		return &LockedError{Until: cred.LockedUntil.Time}
	}
	if _, ok := s.checkPassword(p, cred, current); !ok {
		if err := s.recordFailure(ctx, p); err != nil {
			return err
		}
		return ErrWrongPassword
	}
	if err := checkNewPassword(next, current, p.LoginPhone); err != nil {
		return &ValidationError{Field: "password", Err: err}
	}

	hash, err := s.hasher.Hash(next)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	if err := s.q.SetPassword(ctx, authdb.SetPasswordParams{PersonKind: p.Kind, PersonID: p.ID, PasswordHash: pgtype.Text{String: hash, Valid: true}}); err != nil {
		return fmt.Errorf("set password: %w", err)
	}
	if _, err := s.q.RevokeOtherSessions(ctx, authdb.RevokeOtherSessionsParams{PersonKind: p.Kind, PersonID: p.ID, KeepID: pr.SessionID}); err != nil {
		return fmt.Errorf("revoke other sessions: %w", err)
	}
	return nil
}

// SetUsername sets (or changes) the person's optional login name.
func (s *Service) SetUsername(ctx context.Context, pr Principal, username string) (string, error) {
	u, err := normalizeUsername(username)
	if err != nil {
		return "", &ValidationError{Field: "username", Err: err}
	}
	err = s.q.SetUsername(ctx, authdb.SetUsernameParams{PersonKind: pr.Kind, PersonID: pr.ID, Username: pgtype.Text{String: u, Valid: true}})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
		return "", ErrUsernameTaken
	}
	if err != nil {
		return "", fmt.Errorf("set username: %w", err)
	}
	return u, nil
}

// Logout ends the current session.
func (s *Service) Logout(ctx context.Context, pr Principal) error {
	return s.q.RevokeSession(ctx, pr.SessionID)
}

// ResetToDefault forgets a person's own password (they log in with the last
// 4 digits of their phone again and must choose a new one), unlocks the
// account and ends all their sessions. For admins helping someone who forgot
// their password.
func (s *Service) ResetToDefault(ctx context.Context, kind string, id int32) (people.Person, error) {
	p, err := s.people.Get(ctx, kind, id)
	if err != nil {
		return people.Person{}, err
	}
	if _, err := s.q.ResetPasswordToDefault(ctx, authdb.ResetPasswordToDefaultParams{PersonKind: kind, PersonID: id}); err != nil {
		return people.Person{}, fmt.Errorf("reset password: %w", err)
	}
	if _, err := s.q.RevokeAllSessions(ctx, authdb.RevokeAllSessionsParams{PersonKind: kind, PersonID: id}); err != nil {
		return people.Person{}, fmt.Errorf("revoke sessions: %w", err)
	}
	return p, nil
}

// Profile is the logged-in person as shown to the app.
type Profile struct {
	Kind               string             `json:"kind"`
	ID                 int32              `json:"id"`
	FirstName          string             `json:"first_name"`
	LastName           string             `json:"last_name"`
	Username           *string            `json:"username"`
	MustChangePassword bool               `json:"must_change_password"`
	Groups             []people.GroupInfo `json:"groups"`
}

// Profile returns the principal's profile with their groups as of today
// (a date in the school's time zone, as midnight UTC).
func (s *Service) Profile(ctx context.Context, pr Principal, today time.Time) (Profile, error) {
	p, err := s.people.Get(ctx, pr.Kind, pr.ID)
	if err != nil {
		return Profile{}, err
	}
	cred, err := s.credential(ctx, p)
	if err != nil {
		return Profile{}, err
	}
	groups, err := s.people.Groups(ctx, p.Kind, p.ID, today)
	if err != nil {
		return Profile{}, err
	}
	prof := Profile{
		Kind:               p.Kind,
		ID:                 p.ID,
		FirstName:          p.FirstName,
		LastName:           p.LastName,
		MustChangePassword: pr.MustChangePassword,
		Groups:             groups,
	}
	if cred.Username.Valid {
		prof.Username = &cred.Username.String
	}
	return prof, nil
}

// newToken returns a random bearer token and the hash stored for it. Tokens
// are 256-bit random values, so a fast hash is enough.
func newToken() (token string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, hashToken(token), nil
}

func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}
