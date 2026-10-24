// Package parents is parents' access (spec 12). There are no parent accounts:
// every student has a permanent 5-digit parent code; a parent who enters it in
// the app gets a read-only view of that child (classes, schedule, attendance)
// and can report a problem to the admins. Parent sessions are kept apart from
// students' and teachers' (auth), so a parent's token opens nothing else.
package parents

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nazacd/makonbook-api/internal/attendance"
	"github.com/nazacd/makonbook-api/internal/parents/parentsdb"
	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/alert"
	"github.com/nazacd/makonbook-api/internal/platform/scope"
)

var (
	// ErrBadCode means no active student has this code.
	ErrBadCode = errors.New("no student has this code")
	// ErrUnauthenticated means the token isn't a live parent session.
	ErrUnauthenticated = errors.New("not signed in")
	// ErrNotFound means no such report (in the admin's branches) or it is handled.
	ErrNotFound = errors.New("not found")
)

// InvalidError is a report that can't be sent as it is.
type InvalidError struct{ Reason string }

func (e *InvalidError) Error() string { return e.Reason }

// SessionTTL is how long a parent stays signed in without opening the app.
const SessionTTL = 180 * 24 * time.Hour

// Service is parents' access.
type Service struct {
	pool       *pgxpool.Pool
	people     *people.Directory
	attendance *attendance.Service
	notifier   alert.Notifier
	adminURL   string // the panel's address for links in alerts; "" for none
	now        func() time.Time
}

// NewService: notifier tells the admins about reports (Telegram, or the log);
// adminURL is the admin panel's address, e.g. https://admin.makonbook.uz.
func NewService(pool *pgxpool.Pool, dir *people.Directory, att *attendance.Service, notifier alert.Notifier, adminURL string) *Service {
	return &Service{pool: pool, people: dir, attendance: att, notifier: notifier, adminURL: adminURL, now: time.Now}
}

// Codes ----------------------------------------------------------------------

// newCode is a random 5-digit code.
func newCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(100_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%05d", n.Int64()), nil
}

// giveCode gives a student a code if they have none (a code another student
// has is drawn again).
func giveCode(ctx context.Context, q *parentsdb.Queries, student int32) error {
	for range 50 {
		code, err := newCode()
		if err != nil {
			return err
		}
		if _, err := q.InsertCode(ctx, parentsdb.InsertCodeParams{StudentClientID: student, Code: code}); err != nil {
			return err
		}
		if _, err := q.CodeOf(ctx, student); err == nil {
			return nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	return errors.New("no free parent code found")
}

// EnsureCodes gives every student without a parent code one (the worker runs
// it after each sync, so new students get theirs). It returns how many.
func (s *Service) EnsureCodes(ctx context.Context) (int, error) {
	q := parentsdb.New(s.pool)
	ids, err := q.StudentsWithoutCode(ctx)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := giveCode(ctx, q, id); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

// Code is a student's parent code (given now if they have none yet).
func (s *Service) Code(ctx context.Context, student int32) (string, error) {
	q := parentsdb.New(s.pool)
	code, err := q.CodeOf(ctx, student)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := giveCode(ctx, q, student); err != nil {
			return "", err
		}
		return q.CodeOf(ctx, student)
	}
	return code, err
}

// Sessions -------------------------------------------------------------------

// Child is the child a parent sees.
type Child struct {
	ClientID  int32  `json:"client_id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

// Login signs a parent's phone in with a child's code. ErrBadCode if no active
// student has it.
func (s *Service) Login(ctx context.Context, code, deviceID string) (string, Child, error) {
	code = strings.TrimSpace(code)
	if len(code) != 5 || strings.Trim(code, "0123456789") != "" {
		return "", Child{}, ErrBadCode
	}
	q := parentsdb.New(s.pool)
	st, err := q.StudentByCode(ctx, code)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !st.Active {
		return "", Child{}, ErrBadCode
	}
	if err != nil {
		return "", Child{}, err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", Child{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	err = q.InsertSession(ctx, parentsdb.InsertSessionParams{
		TokenHash: hash(token), StudentClientID: st.ClientID, DeviceID: deviceID,
		ExpiresAt: pgtype.Timestamptz{Time: s.now().Add(SessionTTL), Valid: true},
	})
	if err != nil {
		return "", Child{}, err
	}
	return token, Child{ClientID: st.ClientID, FirstName: st.FirstName, LastName: st.LastName}, nil
}

// Session is a signed-in parent's phone.
type Session struct {
	ID       int64
	Student  int32
	DeviceID string
}

// Authenticate finds the live session of a token. ErrUnauthenticated if none
// (or the child left).
func (s *Service) Authenticate(ctx context.Context, token string) (Session, error) {
	if token == "" {
		return Session{}, ErrUnauthenticated
	}
	q := parentsdb.New(s.pool)
	row, err := q.LiveSession(ctx, hash(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrUnauthenticated
	}
	if err != nil {
		return Session{}, err
	}
	if s.now().Sub(row.LastSeenAt.Time) > 24*time.Hour {
		if err := q.TouchSession(ctx, parentsdb.TouchSessionParams{ID: row.ID,
			ExpiresAt: pgtype.Timestamptz{Time: s.now().Add(SessionTTL), Valid: true}}); err != nil {
			return Session{}, err
		}
	}
	return Session{ID: row.ID, Student: row.StudentClientID, DeviceID: row.DeviceID}, nil
}

func (s *Service) Logout(ctx context.Context, sess Session) error {
	return parentsdb.New(s.pool).RevokeSession(ctx, sess.ID)
}

// ChildView is the child with their classes (today: the school's date).
type ChildView struct {
	Child  Child              `json:"child"`
	Groups []people.GroupInfo `json:"groups"`
}

func (s *Service) Child(ctx context.Context, sess Session, today time.Time) (ChildView, error) {
	c, err := parentsdb.New(s.pool).Child(ctx, sess.Student)
	if err != nil {
		return ChildView{}, err
	}
	groups, err := s.people.Groups(ctx, people.KindStudent, sess.Student, today)
	if err != nil {
		return ChildView{}, err
	}
	if groups == nil {
		groups = []people.GroupInfo{}
	}
	return ChildView{Child: Child{ClientID: c.ClientID, FirstName: c.FirstName, LastName: c.LastName}, Groups: groups}, nil
}

// Attendance is the child's attendance table, as the student sees it.
func (s *Service) Attendance(ctx context.Context, sess Session, today time.Time) ([]attendance.GroupAttendance, error) {
	return s.attendance.StudentTable(ctx, sess.Student, today)
}

// Reports --------------------------------------------------------------------

// Report is a parent's report as their phone lists it.
type Report struct {
	ID        int64      `json:"id"`
	GroupID   *int32     `json:"group_id"`
	GroupName string     `json:"group_name"`
	Message   string     `json:"message"`
	CreatedAt time.Time  `json:"created_at"`
	HandledAt *time.Time `json:"handled_at"`
	Reply     *string    `json:"reply"`
}

// MaxMessage bounds a report.
const MaxMessage = 2000

// SendReport stores a parent's report about the child (optionally about one of
// their classes) and tells the admins.
func (s *Service) SendReport(ctx context.Context, sess Session, groupID *int32, message, lang string, today time.Time) (Report, error) {
	message = strings.TrimSpace(message)
	switch {
	case message == "" || utf8.RuneCountInString(message) > MaxMessage:
		return Report{}, &InvalidError{fmt.Sprintf("the message must be 1–%d characters", MaxMessage)}
	case lang != "uz" && lang != "ru":
		return Report{}, &InvalidError{"lang must be uz or ru"}
	}
	q := parentsdb.New(s.pool)
	group := pgtype.Int4{}
	if groupID != nil {
		ok, err := q.InGroup(ctx, parentsdb.InGroupParams{ClientID: sess.Student, GroupID: *groupID, Today: pgtype.Date{Time: today, Valid: true}})
		if err != nil {
			return Report{}, err
		}
		if !ok {
			return Report{}, &InvalidError{"the child isn't in this class"}
		}
		group = pgtype.Int4{Int32: *groupID, Valid: true}
	}
	row, err := q.InsertReport(ctx, parentsdb.InsertReportParams{
		StudentClientID: sess.Student, DeviceID: sess.DeviceID, GroupID: group, Message: message, Lang: lang,
	})
	if err != nil {
		return Report{}, err
	}
	rc, err := q.ReportContext(ctx, parentsdb.ReportContextParams{ClientID: sess.Student, GroupID: group})
	if err == nil {
		s.alert(ctx, row.ID, rc, lang, message)
	}
	r := Report{ID: row.ID, GroupID: groupID, GroupName: rc.GroupName, Message: message, CreatedAt: row.CreatedAt.Time}
	return r, nil
}

var langName = map[string]string{"uz": "Uzbek", "ru": "Russian"}

// alert tells the admins' chat about a new report; a failure is only logged
// by the notifier's caller (the report is stored either way).
func (s *Service) alert(ctx context.Context, id int64, rc parentsdb.ReportContextRow, lang, message string) {
	var b strings.Builder
	fmt.Fprintf(&b, "📩 Parent report #%d\nStudent: %s %s\n", id, rc.LastName, rc.FirstName)
	if rc.GroupName != "" {
		fmt.Fprintf(&b, "Class: %s\n", rc.GroupName)
	}
	if rc.Offices != "" {
		fmt.Fprintf(&b, "Branch: %s\n", rc.Offices)
	}
	fmt.Fprintf(&b, "Written in %s:\n\n%s", langName[lang], message)
	if s.adminURL != "" {
		fmt.Fprintf(&b, "\n\nAnswer it in the admin panel: %s/parents", strings.TrimRight(s.adminURL, "/"))
	}
	_ = s.notifier.Notify(ctx, b.String())
}

// Reports lists this phone's reports about the child, newest first.
func (s *Service) Reports(ctx context.Context, sess Session) ([]Report, error) {
	rows, err := parentsdb.New(s.pool).DeviceReports(ctx, parentsdb.DeviceReportsParams{ClientID: sess.Student, DeviceID: sess.DeviceID})
	if err != nil {
		return nil, err
	}
	out := make([]Report, 0, len(rows))
	for _, r := range rows {
		rep := Report{ID: r.ID, GroupName: r.GroupName, Message: r.Message, CreatedAt: r.CreatedAt.Time}
		if r.GroupID.Valid {
			rep.GroupID = &r.GroupID.Int32
		}
		if r.HandledAt.Valid {
			rep.HandledAt = &r.HandledAt.Time
		}
		if r.Reply.Valid {
			rep.Reply = &r.Reply.String
		}
		out = append(out, rep)
	}
	return out, nil
}

// Admin panel ----------------------------------------------------------------

// AdminReport is a report as admins see it.
type AdminReport struct {
	ID        int64      `json:"id"`
	ClientID  int32      `json:"client_id"`
	FirstName string     `json:"first_name"`
	LastName  string     `json:"last_name"`
	GroupID   *int32     `json:"group_id"`
	GroupName string     `json:"group_name"`
	Message   string     `json:"message"`
	Lang      string     `json:"lang"`
	CreatedAt time.Time  `json:"created_at"`
	HandledAt *time.Time `json:"handled_at"`
	HandledBy *string    `json:"handled_by"`
	Reply     *string    `json:"reply"`
}

// AdminReports lists the reports about students of the admin's branches:
// open ones, or handled ones.
func (s *Service) AdminReports(ctx context.Context, sc scope.Offices, handled bool) ([]AdminReport, error) {
	rows, err := parentsdb.New(s.pool).AdminReports(ctx, parentsdb.AdminReportsParams{Handled: handled, AllOffices: sc.All, OfficeIds: sc.List()})
	if err != nil {
		return nil, err
	}
	out := make([]AdminReport, 0, len(rows))
	for _, r := range rows {
		a := AdminReport{ID: r.ID, ClientID: r.StudentClientID, FirstName: r.FirstName, LastName: r.LastName, GroupName: r.GroupName,
			Message: r.Message, Lang: r.Lang, CreatedAt: r.CreatedAt.Time}
		if r.GroupID.Valid {
			a.GroupID = &r.GroupID.Int32
		}
		if r.HandledAt.Valid {
			a.HandledAt = &r.HandledAt.Time
		}
		if r.HandledBy.Valid {
			a.HandledBy = &r.HandledBy.String
		}
		if r.Reply.Valid {
			a.Reply = &r.Reply.String
		}
		out = append(out, a)
	}
	return out, nil
}

// MaxReply bounds an admin's answer to a parent.
const MaxReply = 1000

// MarkHandled marks a report handled by the admin, with an optional answer the
// parent sees. ErrNotFound if it isn't an open report of their branches.
func (s *Service) MarkHandled(ctx context.Context, sc scope.Offices, id int64, by, reply string) error {
	reply = strings.TrimSpace(reply)
	if utf8.RuneCountInString(reply) > MaxReply {
		return &InvalidError{fmt.Sprintf("the answer must be at most %d characters", MaxReply)}
	}
	n, err := parentsdb.New(s.pool).MarkHandled(ctx, parentsdb.MarkHandledParams{
		ID: id, HandledBy: pgtype.Text{String: by, Valid: true}, Reply: pgtype.Text{String: reply, Valid: reply != ""}, AllOffices: sc.All, OfficeIds: sc.List(),
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// StudentCode is a student with their parent code, for the office.
type StudentCode struct {
	ClientID  int32  `json:"client_id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Groups    string `json:"groups"`
	Code      string `json:"code"`
}

// SearchCodes finds active students of the admin's branches by name, with
// their parent codes.
func (s *Service) SearchCodes(ctx context.Context, sc scope.Offices, query string, today time.Time) ([]StudentCode, error) {
	query = strings.TrimSpace(query)
	if utf8.RuneCountInString(query) < 2 {
		return []StudentCode{}, nil
	}
	if _, err := s.EnsureCodes(ctx); err != nil {
		return nil, err
	}
	rows, err := parentsdb.New(s.pool).SearchCodes(ctx, parentsdb.SearchCodesParams{
		Today: pgtype.Date{Time: today, Valid: true}, Q: query, AllOffices: sc.All, OfficeIds: sc.List(),
	})
	if err != nil {
		return nil, err
	}
	out := make([]StudentCode, 0, len(rows))
	for _, r := range rows {
		out = append(out, StudentCode{ClientID: r.ClientID, FirstName: r.FirstName, LastName: r.LastName, Groups: r.Groups, Code: r.Code})
	}
	return out, nil
}

func hash(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}
