// Package attendance runs QR attendance (spec 8): a teacher opens a session
// for one of their lessons, students scan the rotating QR, the teacher
// finishes it (or it finishes on its own) and may correct marks for 24 hours.
//
// Nothing here talks to HolliHop; writing finished attendance back is a
// separate step (spec 8.6).
package attendance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nazacd/makonbook-api/internal/attendance/attendancedb"
	"github.com/nazacd/makonbook-api/internal/lessons"
)

// Timing rules (spec 8.1, D13).
const (
	// LateAfter: a scan this long after the lesson starts counts as late.
	LateAfter = 10 * time.Minute
	// EditWindow: teachers may correct marks until this long after the lesson.
	EditWindow = 24 * time.Hour
)

// Mark states.
const (
	StatePending = "pending"
	StatePresent = "present"
	StateLate    = "late"
	StateAbsent  = "absent"
)

var (
	ErrNotFound         = errors.New("no such session")
	ErrNoLesson         = errors.New("no such lesson of yours on that date")
	ErrDisabled         = errors.New("this lesson doesn't take attendance in the app")
	ErrLessonOver       = errors.New("the lesson is over")
	ErrSessionClosed    = errors.New("attendance for this lesson is closed")
	ErrInvalidCode      = errors.New("not a valid attendance code")
	ErrCodeExpired      = errors.New("this code has expired")
	ErrNotInRoster      = errors.New("not on this lesson's list")
	ErrDeviceUsed       = errors.New("this phone already checked in another student")
	ErrMarkedAbsent     = errors.New("the teacher marked this student absent")
	ErrEditWindowClosed = errors.New("marks can only be changed until 24 hours after the lesson")
	ErrBadState         = errors.New("state must be present, late or absent")
)

// TooEarlyError: attendance opens OpensBefore the lesson starts.
type TooEarlyError struct{ OpensAt time.Time }

func (e *TooEarlyError) Error() string { return "attendance opens 15 minutes before the lesson" }

// Session is what the teacher's screen shows.
type Session struct {
	ID            string     `json:"id"`
	GroupID       int32      `json:"group_id"`
	GroupName     string     `json:"group_name"`
	Date          string     `json:"date"`
	StartsAt      time.Time  `json:"starts_at"`
	EndsAt        time.Time  `json:"ends_at"`
	LateAfter     time.Time  `json:"late_after"`
	OpenedAt      time.Time  `json:"opened_at"`
	FinishedAt    *time.Time `json:"finished_at"`
	Status        string     `json:"status"` // "open" or "finished"
	EditableUntil time.Time  `json:"editable_until"`
	QR            *QR        `json:"qr,omitempty"` // only while open
	Counts        Counts     `json:"counts"`
	Marks         []Mark     `json:"marks"`
}

// QR is what the teacher's device needs to draw codes (see token.go).
type QR struct {
	Secret        string    `json:"secret"` // base64url, no padding
	WindowSeconds int       `json:"window_seconds"`
	ServerTime    time.Time `json:"server_time"` // lets the app correct its clock
}

type Counts struct {
	Total   int `json:"total"`
	Present int `json:"present"`
	Late    int `json:"late"`
	Absent  int `json:"absent"`
	Pending int `json:"pending"`
}

type Mark struct {
	ClientID  int32      `json:"client_id"`
	FirstName string     `json:"first_name"`
	LastName  string     `json:"last_name"`
	State     string     `json:"state"`
	Source    *string    `json:"source"` // "scan", "teacher", "auto"; null while pending
	ScannedAt *time.Time `json:"scanned_at"`
}

// ScanResult is what the student's screen shows after a scan.
type ScanResult struct {
	State     string    `json:"state"`   // "present" or "late"
	Already   bool      `json:"already"` // they had scanned before
	GroupName string    `json:"group_name"`
	At        time.Time `json:"at"`
}

type Service struct {
	pool    *pgxpool.Pool
	lessons *lessons.Service
	now     func() time.Time
}

func NewService(pool *pgxpool.Pool, lessonsSvc *lessons.Service) *Service {
	return &Service{pool: pool, lessons: lessonsSvc, now: time.Now}
}

// Open starts attendance for one of the teacher's lessons, or returns the
// session already started for it (by this or another teacher of the lesson),
// so reopening the app never loses the QR.
func (s *Service) Open(ctx context.Context, teacherID, groupID int32, day time.Time) (Session, error) {
	lesson, err := s.teacherLesson(ctx, teacherID, groupID, day)
	if err != nil {
		return Session{}, err
	}
	q := attendancedb.New(s.pool)
	if existing, err := q.SessionByLesson(ctx, attendancedb.SessionByLessonParams{GroupID: groupID, Date: date(day)}); err == nil {
		return s.get(ctx, existing.ID)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Session{}, fmt.Errorf("find session: %w", err)
	}

	if !lesson.Attendance {
		return Session{}, ErrDisabled
	}
	now := s.now()
	if opens := lesson.StartsAt.Add(-lessons.OpensBefore); now.Before(opens) {
		return Session{}, &TooEarlyError{OpensAt: opens}
	}
	if now.After(lesson.EndsAt) {
		return Session{}, ErrLessonOver
	}

	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return Session{}, err
	}
	var id pgtype.UUID
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := attendancedb.New(tx)
		row, err := q.CreateSession(ctx, attendancedb.CreateSessionParams{
			GroupID: groupID, Date: date(day), TeacherID: teacherID, Secret: secret,
			StartsAt: ts(lesson.StartsAt), EndsAt: ts(lesson.EndsAt), OpenedAt: ts(now),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // created concurrently; picked up below
		}
		if err != nil {
			return fmt.Errorf("create session: %w", err)
		}
		id = row.ID
		_, err = q.InsertRoster(ctx, attendancedb.InsertRosterParams{SessionID: row.ID, At: ts(now), GroupID: groupID, Date: date(day)})
		if err != nil {
			return fmt.Errorf("roster: %w", err)
		}
		return nil
	})
	if err != nil {
		return Session{}, err
	}
	if !id.Valid {
		existing, err := q.SessionByLesson(ctx, attendancedb.SessionByLessonParams{GroupID: groupID, Date: date(day)})
		if err != nil {
			return Session{}, fmt.Errorf("find session: %w", err)
		}
		id = existing.ID
	}
	return s.get(ctx, id)
}

// Get returns a session to a teacher of its lesson.
func (s *Service) Get(ctx context.Context, teacherID int32, sessionID string) (Session, error) {
	id, err := s.authorize(ctx, teacherID, sessionID)
	if err != nil {
		return Session{}, err
	}
	return s.get(ctx, id)
}

// Finish closes the session: everyone who hasn't scanned is marked absent.
// Finishing twice is harmless.
func (s *Service) Finish(ctx context.Context, teacherID int32, sessionID string) (Session, error) {
	id, err := s.authorize(ctx, teacherID, sessionID)
	if err != nil {
		return Session{}, err
	}
	if err := s.finish(ctx, id, "teacher"); err != nil {
		return Session{}, err
	}
	return s.get(ctx, id)
}

// SetMark is a teacher's correction (a dead phone, a mistake), allowed while
// the session is open and until EditWindow after the lesson. Every change is
// audited.
func (s *Service) SetMark(ctx context.Context, teacherID int32, sessionID string, clientID int32, state string) (Session, error) {
	if state != StatePresent && state != StateLate && state != StateAbsent {
		return Session{}, ErrBadState
	}
	id, err := s.authorize(ctx, teacherID, sessionID)
	if err != nil {
		return Session{}, err
	}
	now := s.now()
	row, err := attendancedb.New(s.pool).GetSession(ctx, id)
	if err != nil {
		return Session{}, fmt.Errorf("load session: %w", err)
	}
	if now.After(row.EndsAt.Time.Add(EditWindow)) {
		return Session{}, ErrEditWindowClosed
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := attendancedb.New(tx)
		mark, err := q.GetMarkForUpdate(ctx, attendancedb.GetMarkForUpdateParams{SessionID: id, StudentClientID: clientID})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotInRoster
		}
		if err != nil {
			return fmt.Errorf("load mark: %w", err)
		}
		if mark.State == state {
			return nil
		}
		if err := q.TeacherSetMark(ctx, attendancedb.TeacherSetMarkParams{State: state, At: ts(now), SessionID: id, StudentClientID: clientID}); err != nil {
			return fmt.Errorf("set mark: %w", err)
		}
		return q.InsertAudit(ctx, attendancedb.InsertAuditParams{
			SessionID: id, StudentClientID: clientID, ActorKind: "teacher", ActorID: pgtype.Int4{Int32: teacherID, Valid: true},
			FromState: mark.State, ToState: state, At: ts(now),
		})
	})
	if err != nil {
		return Session{}, err
	}
	return s.get(ctx, id)
}

// Scan records a student's scan of the teacher's QR (spec 8.3).
func (s *Service) Scan(ctx context.Context, clientID int32, sessionID string, window int64, sig, deviceID string) (ScanResult, error) {
	id, ok := parseUUID(sessionID)
	if !ok {
		return ScanResult{}, ErrInvalidCode
	}
	row, err := attendancedb.New(s.pool).GetSession(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ScanResult{}, ErrInvalidCode
	}
	if err != nil {
		return ScanResult{}, fmt.Errorf("load session: %w", err)
	}
	if !validSig(row.Secret, uuidString(id), window, sig) {
		return ScanResult{}, ErrInvalidCode
	}
	now := s.now()
	if !freshWindow(window, now) {
		return ScanResult{}, ErrCodeExpired
	}
	if row.FinishedAt.Valid || now.After(row.EndsAt.Time.Add(lessons.ClosesAfter)) {
		return ScanResult{}, ErrSessionClosed
	}

	res := ScanResult{GroupName: row.GroupName, At: now}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := attendancedb.New(tx)
		mark, err := q.GetMarkForUpdate(ctx, attendancedb.GetMarkForUpdateParams{SessionID: id, StudentClientID: clientID})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotInRoster
		}
		if err != nil {
			return fmt.Errorf("load mark: %w", err)
		}
		switch mark.State {
		case StatePresent, StateLate:
			res.State, res.Already = mark.State, true
			return nil
		case StateAbsent:
			return ErrMarkedAbsent
		}

		res.State = StatePresent
		if now.After(row.StartsAt.Time.Add(LateAfter)) {
			res.State = StateLate
		}
		n, err := q.ScanMark(ctx, attendancedb.ScanMarkParams{
			State: res.State, At: ts(now), DeviceID: pgtype.Text{String: deviceID, Valid: true},
			SessionID: id, StudentClientID: clientID,
		})
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrDeviceUsed
		}
		if err != nil {
			return fmt.Errorf("record scan: %w", err)
		}
		if n == 0 {
			return ErrSessionClosed // finished between the checks above and now
		}
		return q.InsertAudit(ctx, attendancedb.InsertAuditParams{
			SessionID: id, StudentClientID: clientID, ActorKind: "student", ActorID: pgtype.Int4{Int32: clientID, Valid: true},
			FromState: StatePending, ToState: res.State, At: ts(now),
		})
	})
	if err != nil {
		return ScanResult{}, err
	}
	return res, nil
}

// FinishDue finishes open sessions whose lesson ended more than ClosesAfter
// ago (spec 8.1). The worker calls it every minute.
func (s *Service) FinishDue(ctx context.Context) (int, error) {
	ids, err := attendancedb.New(s.pool).DueSessions(ctx, ts(s.now().Add(-lessons.ClosesAfter)))
	if err != nil {
		return 0, fmt.Errorf("due sessions: %w", err)
	}
	for _, id := range ids {
		if err := s.finish(ctx, id, "auto"); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

func (s *Service) finish(ctx context.Context, id pgtype.UUID, by string) error {
	now := ts(s.now())
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := attendancedb.New(tx)
		n, err := q.FinishSession(ctx, attendancedb.FinishSessionParams{At: now, FinishedBy: pgtype.Text{String: by, Valid: true}, ID: id})
		if err != nil {
			return fmt.Errorf("finish session: %w", err)
		}
		if n == 0 {
			return nil // already finished
		}
		absent, err := q.MarkPendingAbsent(ctx, attendancedb.MarkPendingAbsentParams{At: now, SessionID: id})
		if err != nil {
			return fmt.Errorf("mark absent: %w", err)
		}
		if len(absent) == 0 {
			return nil
		}
		return q.InsertAbsentAudit(ctx, attendancedb.InsertAbsentAuditParams{SessionID: id, StudentClientIds: absent, At: now})
	})
}

// teacherLesson finds the teacher's lesson of a group on a date. A teacher
// who isn't on the lesson's schedule item has none (spec D12).
func (s *Service) teacherLesson(ctx context.Context, teacherID, groupID int32, day time.Time) (lessons.Lesson, error) {
	list, err := s.lessons.TeacherLessons(ctx, teacherID, day)
	if err != nil {
		return lessons.Lesson{}, err
	}
	for _, l := range list {
		if l.GroupID == groupID {
			return l, nil
		}
	}
	return lessons.Lesson{}, ErrNoLesson
}

// authorize returns the session id if the teacher teaches its lesson.
// Unknown and other teachers' sessions look the same: not found.
func (s *Service) authorize(ctx context.Context, teacherID int32, sessionID string) (pgtype.UUID, error) {
	id, ok := parseUUID(sessionID)
	if !ok {
		return id, ErrNotFound
	}
	row, err := attendancedb.New(s.pool).GetSession(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return id, ErrNotFound
	}
	if err != nil {
		return id, fmt.Errorf("load session: %w", err)
	}
	if _, err := s.teacherLesson(ctx, teacherID, row.GroupID, row.Date.Time); errors.Is(err, ErrNoLesson) {
		return id, ErrNotFound
	} else if err != nil {
		return id, err
	}
	return id, nil
}

// get builds the teacher's view, finishing the session first if it is due —
// so nobody sees an open session the worker hasn't caught up with yet.
func (s *Service) get(ctx context.Context, id pgtype.UUID) (Session, error) {
	q := attendancedb.New(s.pool)
	row, err := q.GetSession(ctx, id)
	if err != nil {
		return Session{}, fmt.Errorf("load session: %w", err)
	}
	now := s.now()
	if !row.FinishedAt.Valid && now.After(row.EndsAt.Time.Add(lessons.ClosesAfter)) {
		if err := s.finish(ctx, id, "auto"); err != nil {
			return Session{}, err
		}
		if row, err = q.GetSession(ctx, id); err != nil {
			return Session{}, fmt.Errorf("load session: %w", err)
		}
	}
	marks, err := q.SessionMarks(ctx, id)
	if err != nil {
		return Session{}, fmt.Errorf("load marks: %w", err)
	}

	out := Session{
		ID:            uuidString(row.ID),
		GroupID:       row.GroupID,
		GroupName:     row.GroupName,
		Date:          row.Date.Time.Format(time.DateOnly),
		StartsAt:      row.StartsAt.Time,
		EndsAt:        row.EndsAt.Time,
		LateAfter:     row.StartsAt.Time.Add(LateAfter),
		OpenedAt:      row.OpenedAt.Time,
		Status:        "open",
		EditableUntil: row.EndsAt.Time.Add(EditWindow),
		Marks:         make([]Mark, 0, len(marks)),
	}
	if row.FinishedAt.Valid {
		t := row.FinishedAt.Time
		out.FinishedAt, out.Status = &t, "finished"
	} else {
		out.QR = &QR{Secret: encodeSecret(row.Secret), WindowSeconds: WindowSeconds, ServerTime: now}
	}
	for _, m := range marks {
		mark := Mark{ClientID: m.StudentClientID, FirstName: m.FirstName, LastName: m.LastName, State: m.State}
		if m.Source.Valid {
			src := m.Source.String
			mark.Source = &src
		}
		if m.ScannedAt.Valid {
			t := m.ScannedAt.Time
			mark.ScannedAt = &t
		}
		out.Marks = append(out.Marks, mark)
		out.Counts.Total++
		switch m.State {
		case StatePresent:
			out.Counts.Present++
		case StateLate:
			out.Counts.Late++
		case StateAbsent:
			out.Counts.Absent++
		default:
			out.Counts.Pending++
		}
	}
	return out, nil
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func date(t time.Time) pgtype.Date { return pgtype.Date{Time: t, Valid: true} }

func parseUUID(s string) (pgtype.UUID, bool) {
	var u pgtype.UUID
	if len(s) != 36 || u.Scan(s) != nil {
		return pgtype.UUID{}, false
	}
	return u, true
}

func uuidString(u pgtype.UUID) string {
	b := hex.EncodeToString(u.Bytes[:])
	return b[0:8] + "-" + b[8:12] + "-" + b[12:16] + "-" + b[16:20] + "-" + b[20:32]
}
