package attendance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nazacd/makonbook-api/internal/attendance/attendancedb"
	"github.com/nazacd/makonbook-api/internal/platform/scope"
)

// The admin panel's side of attendance (spec 13): every call takes the
// admin's branches, and anything outside them is "not found".

// AdminSession is a session as the panel shows it: the teacher's view
// without the QR, plus who opened it, every change, and where each mark's
// write to HolliHop stands.
type AdminSession struct {
	Session
	OfficeID   *int32   `json:"office_id"`
	Office     string   `json:"office"`
	OpenedBy   string   `json:"opened_by"`   // the teacher who started it
	FinishedBy *string  `json:"finished_by"` // "teacher" or "auto"; null while open
	History    []Change `json:"history"`
}

// Change is one audited change of a mark.
type Change struct {
	ClientID    int32     `json:"client_id"`
	StudentName string    `json:"student_name"`
	ActorKind   string    `json:"actor_kind"` // student (scan), teacher, admin, system (finish)
	ActorName   string    `json:"actor_name"`
	From        string    `json:"from"`
	To          string    `json:"to"`
	At          time.Time `json:"at"`
}

// AdminSession returns a session in the admin's branches.
func (s *Service) AdminSession(ctx context.Context, offices scope.Offices, sessionID string) (AdminSession, error) {
	id, row, err := s.inScope(ctx, offices, sessionID)
	if err != nil {
		return AdminSession{}, err
	}
	sess, err := s.get(ctx, id)
	if err != nil {
		return AdminSession{}, err
	}
	sess.QR = nil // the panel never draws codes
	q := attendancedb.New(s.pool)

	writes, err := q.SessionWrites(ctx, id)
	if err != nil {
		return AdminSession{}, fmt.Errorf("load write-back: %w", err)
	}
	byStudent := make(map[int32]*Write, len(writes))
	for _, w := range writes {
		byStudent[w.StudentClientID] = &Write{Status: w.Status, Error: text(w.LastError)}
	}
	for i := range sess.Marks {
		sess.Marks[i].HolliHop = byStudent[sess.Marks[i].ClientID]
	}

	history, err := q.SessionHistory(ctx, id)
	if err != nil {
		return AdminSession{}, fmt.Errorf("load history: %w", err)
	}
	out := AdminSession{Session: sess, Office: row.Office, OpenedBy: row.TeacherName, History: make([]Change, 0, len(history))}
	if row.OfficeID.Valid {
		v := row.OfficeID.Int32
		out.OfficeID = &v
	}
	// Reload: get may just have finished the session.
	if fresh, err := q.GetSession(ctx, id); err == nil && fresh.FinishedBy.Valid {
		v := fresh.FinishedBy.String
		out.FinishedBy = &v
	}
	for _, h := range history {
		out.History = append(out.History, Change{
			ClientID: h.StudentClientID, StudentName: h.StudentName, ActorKind: h.ActorKind, ActorName: h.ActorName,
			From: h.FromState, To: h.ToState, At: h.At.Time,
		})
	}
	return out, nil
}

// AdminSetMark is an admin's correction: any time, open or finished, in their
// branches. A teacher can't change it back afterwards.
func (s *Service) AdminSetMark(ctx context.Context, offices scope.Offices, actor Actor, sessionID string, clientID int32, state string) (AdminSession, error) {
	if !validState(state) {
		return AdminSession{}, ErrBadState
	}
	id, _, err := s.inScope(ctx, offices, sessionID)
	if err != nil {
		return AdminSession{}, err
	}
	actor.Kind = ActorAdmin
	if err := s.setMark(ctx, id, clientID, state, actor); err != nil {
		return AdminSession{}, err
	}
	return s.AdminSession(ctx, offices, sessionID)
}

// inScope loads a session if its group is in the admin's branches.
func (s *Service) inScope(ctx context.Context, offices scope.Offices, sessionID string) (pgtype.UUID, attendancedb.GetSessionRow, error) {
	id, ok := parseUUID(sessionID)
	if !ok {
		return id, attendancedb.GetSessionRow{}, ErrNotFound
	}
	row, err := attendancedb.New(s.pool).GetSession(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return id, row, ErrNotFound
	}
	if err != nil {
		return id, row, fmt.Errorf("load session: %w", err)
	}
	if !offices.All && (!row.OfficeID.Valid || !offices.Has(row.OfficeID.Int32)) {
		return id, row, ErrNotFound
	}
	return id, row, nil
}

// TeacherCorrections is one teacher's line in the corrections report.
type TeacherCorrections struct {
	TeacherID        int32  `json:"teacher_id"`
	TeacherName      string `json:"teacher_name"`
	LessonsOpened    int32  `json:"lessons_opened"`    // sessions they started in the range
	LessonsCorrected int32  `json:"lessons_corrected"` // lessons with at least one correction
	Corrections      int32  `json:"corrections"`
	MadePresent      int32  `json:"made_present"` // absent or not scanned → present/late
}

// Correction is one teacher's or admin's change, for the report.
type Correction struct {
	ID          int64     `json:"id"`
	At          time.Time `json:"at"`
	ActorKind   string    `json:"actor_kind"` // teacher or admin
	ActorName   string    `json:"actor_name"`
	From        string    `json:"from"`
	To          string    `json:"to"`
	SessionID   string    `json:"session_id"`
	Date        string    `json:"date"`
	GroupID     int32     `json:"group_id"`
	GroupName   string    `json:"group_name"`
	ClientID    int32     `json:"client_id"`
	StudentName string    `json:"student_name"`
}

// CorrectionsReport covers lessons dated from..to (inclusive). teacherID 0
// lists everyone's changes; otherwise only that teacher's. At most the 500
// newest changes are listed.
func (s *Service) CorrectionsReport(ctx context.Context, offices scope.Offices, from, to time.Time, teacherID int32) ([]TeacherCorrections, []Correction, error) {
	q := attendancedb.New(s.pool)
	byTeacher, err := q.CorrectionsByTeacher(ctx, attendancedb.CorrectionsByTeacherParams{
		DateFrom: date(from), DateTo: date(to), AllOffices: offices.All, OfficeIds: offices.List(),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("corrections by teacher: %w", err)
	}
	rows, err := q.Corrections(ctx, attendancedb.CorrectionsParams{
		DateFrom: date(from), DateTo: date(to), AllOffices: offices.All, OfficeIds: offices.List(), TeacherID: teacherID,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("corrections: %w", err)
	}
	teachers := make([]TeacherCorrections, 0, len(byTeacher))
	for _, t := range byTeacher {
		teachers = append(teachers, TeacherCorrections{
			TeacherID: t.TeacherID, TeacherName: t.TeacherName, LessonsOpened: t.LessonsOpened,
			LessonsCorrected: t.LessonsCorrected, Corrections: t.Corrections, MadePresent: t.MadePresent,
		})
	}
	changes := make([]Correction, 0, len(rows))
	for _, r := range rows {
		changes = append(changes, Correction{
			ID: r.ID, At: r.At.Time, ActorKind: r.ActorKind, ActorName: r.ActorName, From: r.FromState, To: r.ToState,
			SessionID: uuidString(r.SessionID), Date: r.Date.Time.Format(time.DateOnly), GroupID: r.GroupID, GroupName: r.GroupName,
			ClientID: r.StudentClientID, StudentName: r.StudentName,
		})
	}
	return teachers, changes, nil
}

// Write is where a mark's write to HolliHop stands.
type Write struct {
	Status string  `json:"status"` // see WriteStatuses
	Error  *string `json:"error"`
}

// WriteStatuses are the states of a write to HolliHop (hollihop_outbox).
var WriteStatuses = []string{"pending", "sent", "unchanged", "set_manually", "dry_run", "superseded", "failed"}

// WriteRow is one write to HolliHop, for the panel's list.
type WriteRow struct {
	ID          int64      `json:"id"`
	SessionID   string     `json:"session_id"`
	Date        string     `json:"date"`
	GroupID     int32      `json:"group_id"`
	GroupName   string     `json:"group_name"`
	ClientID    int32      `json:"client_id"`
	StudentName string     `json:"student_name"`
	Absent      bool       `json:"absent"`
	Status      string     `json:"status"`
	Attempts    int32      `json:"attempts"`
	Error       *string    `json:"error"`
	CreatedAt   time.Time  `json:"created_at"`
	DoneAt      *time.Time `json:"done_at"`
}

// Writes counts the writes to HolliHop by status and lists the 500 newest
// with one of the given statuses.
func (s *Service) Writes(ctx context.Context, offices scope.Offices, statuses []string) (map[string]int32, []WriteRow, error) {
	q := attendancedb.New(s.pool)
	counts, err := q.WriteCounts(ctx, attendancedb.WriteCountsParams{AllOffices: offices.All, OfficeIds: offices.List()})
	if err != nil {
		return nil, nil, fmt.Errorf("write counts: %w", err)
	}
	byStatus := make(map[string]int32, len(WriteStatuses))
	for _, st := range WriteStatuses {
		byStatus[st] = 0
	}
	for _, c := range counts {
		byStatus[c.Status] = c.Count
	}
	rows, err := q.Writes(ctx, attendancedb.WritesParams{Statuses: statuses, AllOffices: offices.All, OfficeIds: offices.List()})
	if err != nil {
		return nil, nil, fmt.Errorf("writes: %w", err)
	}
	out := make([]WriteRow, 0, len(rows))
	for _, r := range rows {
		w := WriteRow{
			ID: r.ID, SessionID: uuidString(r.SessionID), Date: r.Date.Time.Format(time.DateOnly), GroupID: r.GroupID, GroupName: r.GroupName,
			ClientID: r.StudentClientID, StudentName: r.StudentName, Absent: r.Pass, Status: r.Status, Attempts: r.Attempts,
			Error: text(r.LastError), CreatedAt: r.CreatedAt.Time,
		}
		if r.DoneAt.Valid {
			t := r.DoneAt.Time
			w.DoneAt = &t
		}
		out = append(out, w)
	}
	return byStatus, out, nil
}

// RetryWrite puts a failed write back in the queue. Only the latest write of
// a mark can be retried; ErrNotFound otherwise.
func (s *Service) RetryWrite(ctx context.Context, offices scope.Offices, id int64) error {
	n, err := attendancedb.New(s.pool).RequeueFailed(ctx, attendancedb.RequeueFailedParams{
		At: ts(s.now()), ID: id, AllOffices: offices.All, OfficeIds: offices.List(),
	})
	if err != nil {
		return fmt.Errorf("requeue: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func text(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}
