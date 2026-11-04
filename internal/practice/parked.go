package practice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nazacd/makonbook-api/internal/practice/practicedb"
)

// "Ask later" (spec 9.6): questions a student parks during a test to look at
// later. The device makes the id, so saving again is harmless.

// Limits of a parked question's texts, in characters.
const (
	MaxFolder = 40
	MaxNote   = 1000
	MaxFix    = 300
)

// DefaultFolder is where a parked question goes when no folder is given.
const DefaultFolder = "Unsorted"

// Why a student parked a question.
var parkedReasons = map[string]bool{
	"didnt_understand": true, "didnt_know_start": true, "lost_partway": true, "ran_out_of_time": true,
}

// ParkedError is a parked question that can't be saved as it is.
type ParkedError struct{ Reason string }

func (e *ParkedError) Error() string { return e.Reason }

// QuestionRef is a question at its place in a published version, as a
// device names it.
type QuestionRef struct {
	Test       string `json:"test"` // the code
	Version    int32  `json:"version"`
	Subject    string `json:"subject"`
	Module     int16  `json:"module"`
	Position   int16  `json:"position"`
	QuestionID string `json:"question_id"`
}

// check returns the question's id if the version has that question at that
// place; otherwise why not (reason), so the caller can say it.
func (r QuestionRef) check(ctx context.Context, q *practicedb.Queries) (id pgtype.UUID, reason string, err error) {
	switch {
	case r.Test == "" || r.Version <= 0:
		return id, "test and version are required", nil
	case r.Subject != "english" && r.Subject != "math":
		return id, "subject must be english or math", nil
	case r.Module != 1 && r.Module != 2:
		return id, "module must be 1 or 2", nil
	case r.Position <= 0:
		return id, "position must be a positive number", nil
	}
	id, ok := parseUUID(r.QuestionID)
	if !ok {
		return id, "question_id must be a UUID", nil
	}
	at, err := q.VersionSlot(ctx, practicedb.VersionSlotParams{
		Code: r.Test, Version: r.Version, Subject: r.Subject, Module: int32(r.Module), Position: int32(r.Position),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return id, "that test version has no such question", nil
	}
	if err != nil {
		return id, "", err
	}
	if at != uuidString(id) {
		return id, "question_id isn't the question at that place in that version", nil
	}
	return id, "", nil
}

// ParkedInput is a parked question as the device sends it.
type ParkedInput struct {
	QuestionRef
	Folder *string `json:"folder"` // null or blank: DefaultFolder
	Reason *string `json:"reason"`
	Note   string  `json:"note"`
	Fix    string  `json:"fix"`
	Status string  `json:"status"` // waiting (when left out) or sorted
}

// Parked is a stored parked question.
type Parked struct {
	ID string `json:"id"`
	QuestionRef
	Folder    string    `json:"folder"`
	Reason    *string   `json:"reason"`
	Note      string    `json:"note"`
	Fix       string    `json:"fix"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func parkedOf(r practicedb.ParkedQuestion) Parked {
	return Parked{
		ID: uuidString(r.ID),
		QuestionRef: QuestionRef{Test: r.Test, Version: r.Version, Subject: r.Subject, Module: r.Module, Position: r.Position,
			QuestionID: uuidString(r.QuestionID)},
		Folder: r.Folder, Reason: textPtr(r.Reason), Note: r.Note, Fix: r.Fix, Status: r.Status,
		CreatedAt: r.CreatedAt.Time.UTC(), UpdatedAt: r.UpdatedAt.Time.UTC(),
	}
}

// SaveParked creates or changes one of the student's parked questions (the
// id is the device's). ErrNotFound if the id is another student's;
// *ParkedError if a value is wrong.
func (s *Service) SaveParked(ctx context.Context, student int32, id string, in ParkedInput) (Parked, error) {
	uid, ok := parseUUID(id)
	if !ok {
		return Parked{}, &ParkedError{"the id must be a UUID"}
	}
	folder := DefaultFolder
	if in.Folder != nil && strings.TrimSpace(*in.Folder) != "" {
		folder = strings.TrimSpace(*in.Folder)
	}
	note, fix := strings.TrimSpace(in.Note), strings.TrimSpace(in.Fix)
	if in.Status == "" {
		in.Status = "waiting"
	}
	switch {
	case utf8.RuneCountInString(folder) > MaxFolder || strings.ContainsRune(folder, 0):
		return Parked{}, &ParkedError{fmt.Sprintf("the folder's name must be at most %d characters", MaxFolder)}
	case in.Reason != nil && !parkedReasons[*in.Reason]:
		return Parked{}, &ParkedError{"reason must be null, didnt_understand, didnt_know_start, lost_partway or ran_out_of_time"}
	case utf8.RuneCountInString(note) > MaxNote || strings.ContainsRune(note, 0):
		return Parked{}, &ParkedError{fmt.Sprintf("the note must be at most %d characters", MaxNote)}
	case utf8.RuneCountInString(fix) > MaxFix || strings.ContainsRune(fix, 0):
		return Parked{}, &ParkedError{fmt.Sprintf("the fix must be at most %d characters", MaxFix)}
	case in.Status != "waiting" && in.Status != "sorted":
		return Parked{}, &ParkedError{"status must be waiting or sorted"}
	}
	q := practicedb.New(s.pool)
	question, reason, err := in.check(ctx, q)
	if err != nil {
		return Parked{}, err
	}
	if reason != "" {
		return Parked{}, &ParkedError{reason}
	}
	var why pgtype.Text
	if in.Reason != nil {
		why = pgtype.Text{String: *in.Reason, Valid: true}
	}
	row, err := q.UpsertParked(ctx, practicedb.UpsertParkedParams{
		ID: uid, StudentClientID: student, Test: in.Test, Version: in.Version, Subject: in.Subject, Module: in.Module,
		Position: in.Position, QuestionID: question, Folder: folder, Reason: why, Note: note, Fix: fix, Status: in.Status,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Parked{}, ErrNotFound
	}
	if err != nil {
		return Parked{}, err
	}
	return parkedOf(row), nil
}

// MyParked lists the student's parked questions, newest first.
func (s *Service) MyParked(ctx context.Context, student int32) ([]Parked, error) {
	rows, err := practicedb.New(s.pool).ParkedOf(ctx, []int32{student})
	if err != nil {
		return nil, err
	}
	out := make([]Parked, 0, len(rows))
	for _, r := range rows {
		out = append(out, parkedOf(r))
	}
	return out, nil
}

// DeleteParked removes one of the student's parked questions; ErrNotFound if
// they have none with this id.
func (s *Service) DeleteParked(ctx context.Context, student int32, id string) error {
	uid, ok := parseUUID(id)
	if !ok {
		return ErrNotFound
	}
	n, err := practicedb.New(s.pool).DeleteParked(ctx, practicedb.DeleteParkedParams{ID: uid, StudentClientID: student})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Student is who a teacher sees beside a parked question.
type Student struct {
	ID        int32  `json:"id"` // the HolliHop client id
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

// GroupParkedQuestion is a parked question with its student, for the teacher.
type GroupParkedQuestion struct {
	Parked
	Student Student `json:"student"`
}

// GroupParked lists the parked questions of a group's current students,
// newest first, for one of its teachers (today: the school's date).
// ErrNotTeacher otherwise.
func (s *Service) GroupParked(ctx context.Context, teacher, group int32, today time.Time) ([]GroupParkedQuestion, error) {
	q := practicedb.New(s.pool)
	day := pgtype.Date{Time: today, Valid: true}
	ok, err := q.TeachesGroup(ctx, practicedb.TeachesGroupParams{GroupID: group, TeacherID: teacher, Today: day})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotTeacher
	}
	students, err := q.GroupStudents(ctx, practicedb.GroupStudentsParams{GroupID: group, Today: day})
	if err != nil {
		return nil, err
	}
	ids := make([]int32, 0, len(students))
	who := make(map[int32]Student, len(students))
	for _, st := range students {
		ids = append(ids, st.ClientID)
		who[st.ClientID] = Student{ID: st.ClientID, FirstName: st.FirstName, LastName: st.LastName}
	}
	rows, err := q.ParkedOf(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]GroupParkedQuestion, 0, len(rows))
	for _, r := range rows {
		out = append(out, GroupParkedQuestion{Parked: parkedOf(r), Student: who[r.StudentClientID]})
	}
	return out, nil
}
