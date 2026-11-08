package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/nazacd/makonbook-api/internal/auth/authdb"
	"github.com/nazacd/makonbook-api/internal/people"
)

// Adding a phone (spec 21.4): someone who signed up with an email, Google or
// Telegram has no phone until they prove one, by an SMS code or the phone
// Telegram shares. If somebody else active already has that number, proving
// it is proof enough that they are the same person: the phoneless account
// is merged into theirs.

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

// phoneless is the signed-in person if they may add a phone: a registered
// student without one. Everyone else has theirs (HolliHop's for enrolled
// people): ErrPhoneSet.
func (s *Service) phoneless(ctx context.Context, pr Principal) (people.Person, error) {
	p, err := s.people.Get(ctx, pr.Kind, pr.ID)
	if err != nil {
		return people.Person{}, err
	}
	if !p.Registered || p.LoginPhone != "" {
		return people.Person{}, ErrPhoneSet
	}
	return p, nil
}

// StartMyPhone sends a code to the phone a signed-in person without one wants
// to add (the limits of StartPhone).
func (s *Service) StartMyPhone(ctx context.Context, pr Principal, raw, ip string) error {
	if s.sms == nil {
		return ErrSMSOff
	}
	if _, err := s.phoneless(ctx, pr); err != nil {
		return err
	}
	return s.StartPhone(ctx, raw, purposeVerify, ip)
}

// VerifyMyPhone checks that code and adds the phone (applyPhone).
func (s *Service) VerifyMyPhone(ctx context.Context, pr Principal, raw, code, deviceID string) (LoginResult, error) {
	u, err := s.phoneless(ctx, pr)
	if err != nil {
		return LoginResult{}, err
	}
	phone, err := s.checkPhoneCode(ctx, raw, code, purposeVerify)
	if err != nil {
		return LoginResult{}, err
	}
	return s.applyPhone(ctx, u, phone, nil, deviceID)
}

// applyPhone gives the proved phone to u, a registered student without one,
// in one transaction (consume, if not nil, spends what proved it there and
// may name a Telegram account to link):
//   - nobody else active has it: it becomes u's;
//   - someone else active (o) has it: u is merged into o (Google accounts,
//     the email, a Telegram account o lacks, the photo if o has none, and
//     everything u did move; u is removed) and o is signed in.
//
// It answers a new session for whoever has the phone afterwards.
func (s *Service) applyPhone(ctx context.Context, u people.Person, phone string,
	consume func(*authdb.Queries) (*TelegramAccount, error), deviceID string) (LoginResult, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return LoginResult{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := authdb.New(tx)
	dir := people.NewDirectory(tx)
	var tg *TelegramAccount
	if consume != nil {
		if tg, err = consume(q); err != nil {
			return LoginResult{}, err
		}
	}

	var target people.Person
	o, owned, err := phoneOwner(ctx, dir, phone)
	switch {
	case err != nil:
		return LoginResult{}, err
	case !owned || (o.Kind == u.Kind && o.ID == u.ID):
		if err := people.SetSelfPhone(ctx, tx, u.ID, phone); errors.Is(err, people.ErrNotFound) {
			return LoginResult{}, ErrPhoneSet
		} else if err != nil {
			return LoginResult{}, err
		}
		if target, err = dir.Get(ctx, u.Kind, u.ID); err != nil {
			return LoginResult{}, err
		}
	default:
		if err := mergeInto(ctx, q, tx, u, o); err != nil {
			return LoginResult{}, err
		}
		target = o
	}
	if tg != nil {
		if err := linkTelegramIfFree(ctx, q, dir, target, *tg); err != nil {
			return LoginResult{}, err
		}
	}
	res, err := s.signInPerson(ctx, q, target, deviceID)
	if err != nil {
		return LoginResult{}, err
	}
	return res, tx.Commit(ctx)
}

// mergeInto folds u (a registered student) into o, inside tx.
func mergeInto(ctx context.Context, q *authdb.Queries, tx pgx.Tx, u, o people.Person) error {
	if err := q.MoveGoogleLinks(ctx, authdb.MoveGoogleLinksParams{FromKind: u.Kind, FromID: u.ID, ToKind: o.Kind, ToID: o.ID}); err != nil {
		return fmt.Errorf("merge: google: %w", err)
	}
	uc, err := q.GetCredential(ctx, authdb.GetCredentialParams{PersonKind: u.Kind, PersonID: u.ID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("merge: credential: %w", err)
	}
	oc, err := q.GetCredential(ctx, authdb.GetCredentialParams{PersonKind: o.Kind, PersonID: o.ID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("merge: credential: %w", err)
	}
	if uc.Email.Valid { // u's email replaces o's
		if err := q.ClearEmail(ctx, uc.Email); err != nil {
			return fmt.Errorf("merge: email: %w", err)
		}
		if err := q.SetEmail(ctx, authdb.SetEmailParams{PersonKind: o.Kind, PersonID: o.ID, Email: uc.Email}); err != nil {
			return fmt.Errorf("merge: email: %w", err)
		}
	}
	if uc.PhotoID.Valid && !oc.PhotoID.Valid {
		if err := q.SetPhoto(ctx, authdb.SetPhotoParams{PersonKind: o.Kind, PersonID: o.ID, PhotoID: uc.PhotoID}); err != nil {
			return fmt.Errorf("merge: photo: %w", err)
		}
	}
	oHas, err := q.HasTelegramLink(ctx, authdb.HasTelegramLinkParams{PersonKind: o.Kind, PersonID: o.ID})
	if err != nil {
		return fmt.Errorf("merge: telegram: %w", err)
	}
	if oHas {
		err = q.DeletePersonTelegram(ctx, authdb.DeletePersonTelegramParams{PersonKind: u.Kind, PersonID: u.ID})
	} else {
		err = q.MoveTelegramLink(ctx, authdb.MoveTelegramLinkParams{FromKind: u.Kind, FromID: u.ID, ToKind: o.Kind, ToID: o.ID})
	}
	if err != nil {
		return fmt.Errorf("merge: telegram: %w", err)
	}
	if _, err := q.RevokeAllSessions(ctx, authdb.RevokeAllSessionsParams{PersonKind: u.Kind, PersonID: u.ID}); err != nil {
		return fmt.Errorf("merge: sessions: %w", err)
	}
	if err := q.DeleteCredential(ctx, authdb.DeleteCredentialParams{PersonKind: u.Kind, PersonID: u.ID}); err != nil {
		return fmt.Errorf("merge: credential: %w", err)
	}
	to := int32(0)
	if o.Kind == people.KindStudent {
		to = o.ID
	}
	return people.MergeSelfStudent(ctx, tx, u.ID, to)
}

// signInPerson opens a session for p (inside q's transaction), with
// must_change_password as at any login.
func (s *Service) signInPerson(ctx context.Context, q *authdb.Queries, p people.Person, deviceID string) (LoginResult, error) {
	c, err := q.GetCredential(ctx, authdb.GetCredentialParams{PersonKind: p.Kind, PersonID: p.ID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return LoginResult{}, fmt.Errorf("get credential: %w", err)
	}
	_, custom := ownPassword(c.PasswordHash, c.PasswordChangedAt, p)
	return s.openSession(ctx, q, p, deviceID, usesDefault(p, custom))
}

// personNames makes usable names from what Google or Telegram says: trimmed,
// at most 60 characters, no control characters; the first name falls back to
// fallback, then "Student".
func personNames(first, last, fallback string) (string, string) {
	clean := func(s string) string {
		s = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, strings.TrimSpace(s))
		for utf8.RuneCountInString(s) > maxNameRunes {
			_, size := utf8.DecodeLastRuneInString(s)
			s = s[:len(s)-size]
		}
		return strings.TrimSpace(s)
	}
	f, l := clean(first), clean(last)
	if f == "" {
		f = clean(fallback)
	}
	if f == "" {
		f = "Student"
	}
	return f, l
}
