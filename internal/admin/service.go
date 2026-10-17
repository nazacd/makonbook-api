// Package admin is the admin panel's sign-in and access control (spec 13).
//
// Admins sign in with their HolliHop login and password, which HolliHop
// checks; MakonBook never stores them, nor the HolliHop key that comes back.
// HolliHop has no roles, so admin_access lists who may use the panel: super
// admins see every branch and manage the list, branch admins see only the
// offices they were given — enforced in every admin query, not in the page.
package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nazacd/makonbook-api/internal/admin/admindb"
	"github.com/nazacd/makonbook-api/internal/hollihop"
	"github.com/nazacd/makonbook-api/internal/platform/scope"
)

// SessionTTL: an admin session lasts one working day, then they sign in again.
const SessionTTL = 12 * time.Hour

var (
	ErrBadCredentials  = errors.New("wrong HolliHop login or password")
	ErrNotAdmin        = errors.New("this HolliHop account has no access to the admin panel")
	ErrUnavailable     = errors.New("HolliHop isn't answering; try again in a minute")
	ErrUnauthorized    = errors.New("not signed in")
	ErrNotFound        = errors.New("no such admin")
	ErrInvalid         = errors.New("give at least one branch, or the editor right")
	ErrUnknownTeacher  = errors.New("no such teacher in MakonBook (only teachers with classes are synced)")
	ErrUnknownEmployee = errors.New("no such employee in HolliHop")
	ErrFired           = errors.New("fired in HolliHop")
	ErrLastSuper       = errors.New("this is the last super admin; make someone else super admin first")
	ErrUnknownOffice   = errors.New("no such branch")
	ErrSuperProtected  = errors.New("super admins are managed on the server, not in the panel")
)

// Member types, as HolliHop reports them.
const (
	TypeEmployee = "Employee"
	TypeTeacher  = "Teacher"
)

// HolliHop is what admin needs from the HolliHop client.
type HolliHop interface {
	CheckLogin(ctx context.Context, login, password string) (hollihop.Member, error)
	GetEmployee(ctx context.Context, id int32) (hollihop.Employee, bool, error)
	FindEmployees(ctx context.Context, term string) ([]hollihop.Employee, error)
}

// Principal is a signed-in admin.
type Principal struct {
	MemberType string
	MemberID   int32
	Name       string
	Super      bool
	OfficeIDs  []int32 // ignored for super admins
	Editor     bool    // may edit practice content (super admins may too)
}

// CanEdit reports whether the admin may edit practice tests and questions.
func (p Principal) CanEdit() bool { return p.Super || p.Editor }

// Scope is the branches the admin may see and change.
func (p Principal) Scope() scope.Offices {
	if p.Super {
		return scope.Offices{All: true}
	}
	return scope.Offices{IDs: p.OfficeIDs}
}

// CanSeeOffice reports whether the admin may see and change lessons of an office.
func (p Principal) CanSeeOffice(officeID int32) bool { return p.Scope().Has(officeID) }

type Service struct {
	pool     *pgxpool.Pool
	hollihop HolliHop
	now      func() time.Time
}

func NewService(pool *pgxpool.Pool, hh HolliHop) *Service {
	return &Service{pool: pool, hollihop: hh, now: time.Now}
}

// Login checks the credentials with HolliHop, then the access list, and
// opens a session. It returns the session token for the cookie.
func (s *Service) Login(ctx context.Context, login, password string) (token string, p Principal, err error) {
	if s.hollihop == nil {
		return "", Principal{}, ErrUnavailable
	}
	m, err := s.hollihop.CheckLogin(ctx, strings.TrimSpace(login), password)
	if errors.Is(err, hollihop.ErrBadCredentials) {
		return "", Principal{}, ErrBadCredentials
	}
	if err != nil {
		return "", Principal{}, fmt.Errorf("%w (%w)", ErrUnavailable, err)
	}
	if m.Type != TypeEmployee && m.Type != TypeTeacher {
		return "", Principal{}, ErrNotAdmin
	}

	q := admindb.New(s.pool)
	access, err := q.GetAccess(ctx, admindb.GetAccessParams{MemberType: m.Type, MemberID: m.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", Principal{}, ErrNotAdmin
	}
	if err != nil {
		return "", Principal{}, fmt.Errorf("load access: %w", err)
	}
	if gone, err := s.gone(ctx, m); err != nil {
		return "", Principal{}, err
	} else if gone {
		return "", Principal{}, ErrNotAdmin
	}

	now := s.now()
	name := strings.TrimSpace(m.FirstName + " " + m.LastName)
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", Principal{}, err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := admindb.New(tx)
		if err := q.RecordLogin(ctx, admindb.RecordLoginParams{Name: name, At: ts(now), MemberType: m.Type, MemberID: m.ID}); err != nil {
			return err
		}
		return q.CreateSession(ctx, admindb.CreateSessionParams{
			TokenHash: hash(token), MemberType: m.Type, MemberID: m.ID, CreatedAt: ts(now), ExpiresAt: ts(now.Add(SessionTTL)),
		})
	})
	if err != nil {
		return "", Principal{}, fmt.Errorf("open session: %w", err)
	}
	access.Name = name
	return token, principal(access), nil
}

// gone reports whether the person has left: fired in HolliHop (employees),
// or fired in the sync's mirror (teachers). A teacher with no classes right
// now is still staff — an admin may teach nothing.
func (s *Service) gone(ctx context.Context, m hollihop.Member) (bool, error) {
	if m.Type == TypeTeacher {
		t, err := admindb.New(s.pool).GetTeacher(ctx, m.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			return true, nil
		}
		return t.Fired, err
	}
	e, ok, err := s.hollihop.GetEmployee(ctx, m.ID)
	if err != nil {
		return false, fmt.Errorf("%w (%w)", ErrUnavailable, err)
	}
	return !ok || e.Fired, nil
}

// Authenticate resolves a session token. Taking someone off the access list
// ends their sessions at once.
func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	if token == "" {
		return Principal{}, ErrUnauthorized
	}
	a, err := admindb.New(s.pool).SessionAccess(ctx, admindb.SessionAccessParams{TokenHash: hash(token), Now: ts(s.now())})
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, ErrUnauthorized
	}
	if err != nil {
		return Principal{}, fmt.Errorf("load session: %w", err)
	}
	return principal(a), nil
}

func (s *Service) Logout(ctx context.Context, token string) error {
	return admindb.New(s.pool).RevokeSession(ctx, admindb.RevokeSessionParams{At: ts(s.now()), TokenHash: hash(token)})
}

// Office is a branch.
type Office struct {
	ID   int32  `json:"id"`
	Name string `json:"name"`
}

// Offices lists every branch.
func (s *Service) Offices(ctx context.Context) ([]Office, error) {
	rows, err := admindb.New(s.pool).Offices(ctx)
	if err != nil {
		return nil, fmt.Errorf("offices: %w", err)
	}
	out := make([]Office, len(rows))
	for i, r := range rows {
		out[i] = Office{ID: r.ID, Name: r.Name}
	}
	return out, nil
}

// Grant gives (or changes) panel access; the server command's way, the only
// one that can make, change or remove super admins. Super admins need no
// branches. The person must exist and not be fired: teachers in our copy of
// HolliHop (sign-in checks them there), employees in HolliHop itself when
// it's configured. name, if empty, comes from there too.
func (s *Service) Grant(ctx context.Context, memberType string, memberID int32, name string, super bool, officeIDs []int32) (admindb.AdminAccess, error) {
	if super {
		officeIDs = []int32{}
	}
	name, err := s.checkGrant(ctx, memberType, memberID, name, super || len(officeIDs) > 0, officeIDs)
	if err != nil {
		return admindb.AdminAccess{}, err
	}
	var a admindb.AdminAccess
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := admindb.New(tx)
		if !super {
			if err := keepASuper(ctx, q, memberType, memberID); err != nil {
				return err
			}
		}
		var err error
		a, err = q.UpsertAccess(ctx, admindb.UpsertAccessParams{
			MemberType: memberType, MemberID: memberID, Name: name, Super: super, OfficeIds: officeIDs,
		})
		return err
	})
	return a, err
}

// GrantBranch is the panel's grant: it makes or changes a branch admin and/or
// an editor (editing needs no branches), and never touches a super admin
// (ErrSuperProtected) — a super admin can't demote, remove or create another,
// or themselves, from the panel.
func (s *Service) GrantBranch(ctx context.Context, memberType string, memberID int32, officeIDs []int32, editor bool) (admindb.AdminAccess, error) {
	if officeIDs == nil {
		officeIDs = []int32{}
	}
	name, err := s.checkGrant(ctx, memberType, memberID, "", editor || len(officeIDs) > 0, officeIDs)
	if err != nil {
		return admindb.AdminAccess{}, err
	}
	a, err := admindb.New(s.pool).UpsertBranchAccess(ctx, admindb.UpsertBranchAccessParams{
		MemberType: memberType, MemberID: memberID, Name: name, OfficeIds: officeIDs, Editor: editor,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return admindb.AdminAccess{}, ErrSuperProtected
	}
	return a, err
}

// checkGrant validates a grant and returns the person's name.
// granted says the grant gives some right (super, a branch, or editing).
func (s *Service) checkGrant(ctx context.Context, memberType string, memberID int32, name string, granted bool, officeIDs []int32) (string, error) {
	if memberType != TypeEmployee && memberType != TypeTeacher {
		return "", fmt.Errorf("member type must be %s or %s", TypeEmployee, TypeTeacher)
	}
	if !granted {
		return "", ErrInvalid
	}
	if err := s.checkOffices(ctx, officeIDs); err != nil {
		return "", err
	}
	switch memberType {
	case TypeTeacher:
		t, err := admindb.New(s.pool).GetTeacher(ctx, memberID)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrUnknownTeacher
		} else if err != nil {
			return "", fmt.Errorf("load teacher: %w", err)
		}
		if t.Fired {
			return "", ErrFired
		}
		if name == "" {
			name = t.Name
		}
	case TypeEmployee:
		if s.hollihop != nil {
			e, ok, err := s.hollihop.GetEmployee(ctx, memberID)
			if err != nil {
				return "", fmt.Errorf("%w (%w)", ErrUnavailable, err)
			}
			if !ok {
				return "", ErrUnknownEmployee
			}
			if e.Fired {
				return "", ErrFired
			}
			if name == "" {
				name = strings.TrimSpace(e.FirstName + " " + e.LastName)
			}
		}
	}
	return name, nil
}

// Revoke takes panel access away (the server command); their sessions end
// with it.
func (s *Service) Revoke(ctx context.Context, memberType string, memberID int32) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := admindb.New(tx)
		if err := keepASuper(ctx, q, memberType, memberID); err != nil {
			return err
		}
		n, err := q.DeleteAccess(ctx, admindb.DeleteAccessParams{MemberType: memberType, MemberID: memberID})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// RevokeBranch is the panel's revoke: branch admins only (ErrSuperProtected).
func (s *Service) RevokeBranch(ctx context.Context, memberType string, memberID int32) error {
	q := admindb.New(s.pool)
	n, err := q.DeleteBranchAccess(ctx, admindb.DeleteBranchAccessParams{MemberType: memberType, MemberID: memberID})
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if a, err := q.GetAccess(ctx, admindb.GetAccessParams{MemberType: memberType, MemberID: memberID}); err == nil && a.Super {
		return ErrSuperProtected
	}
	return ErrNotFound
}

// keepASuper refuses to take super admin away from the last one, which
// would leave nobody able to manage admins from the panel.
func keepASuper(ctx context.Context, q *admindb.Queries, memberType string, memberID int32) error {
	supers, err := q.LockSupers(ctx)
	if err != nil {
		return fmt.Errorf("load super admins: %w", err)
	}
	if len(supers) == 1 && supers[0].MemberType == memberType && supers[0].MemberID == memberID {
		return ErrLastSuper
	}
	return nil
}

func (s *Service) checkOffices(ctx context.Context, ids []int32) error {
	all, err := s.Offices(ctx)
	if err != nil {
		return err
	}
	known := make(map[int32]bool, len(all))
	for _, o := range all {
		known[o.ID] = true
	}
	for _, id := range ids {
		if !known[id] {
			return fmt.Errorf("%w: %d", ErrUnknownOffice, id)
		}
	}
	return nil
}

// FindStaff searches HolliHop's employees, to grant one access.
func (s *Service) FindStaff(ctx context.Context, term string) ([]hollihop.Employee, error) {
	if s.hollihop == nil {
		return nil, ErrUnavailable
	}
	found, err := s.hollihop.FindEmployees(ctx, term)
	if err != nil {
		return nil, fmt.Errorf("%w (%w)", ErrUnavailable, err)
	}
	return found, nil
}

// List returns the access list.
func (s *Service) List(ctx context.Context) ([]admindb.AdminAccess, error) {
	return admindb.New(s.pool).ListAccess(ctx)
}

func principal(a admindb.AdminAccess) Principal {
	return Principal{MemberType: a.MemberType, MemberID: a.MemberID, Name: a.Name, Super: a.Super, OfficeIDs: a.OfficeIds, Editor: a.Editor}
}

func hash(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
