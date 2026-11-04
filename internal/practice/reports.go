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

// Issue reports (spec 9.6): "Report an issue" on a question of the test
// screen, for the admins. The device makes the id, so sending it again is
// harmless.

// ErrReportTaken means another student already sent a report with this id.
var ErrReportTaken = errors.New("a report with this id belongs to someone else")

// MaxReportText bounds what a student writes in a report.
const MaxReportText = 2000

var reportReasons = map[string]bool{
	"answer_key_wrong": true, "typo": true, "unclear": true, "image_broken": true, "formatting": true, "other": true,
}

// ReportError is a report that can't be stored as it is.
type ReportError struct{ Reason string }

func (e *ReportError) Error() string { return e.Reason }

// ReportInput is a report as the device sends it.
type ReportInput struct {
	ID string `json:"id"` // the device's UUID
	QuestionRef
	Kind   string  `json:"kind"`   // question | technical
	Reason *string `json:"reason"` // optional
	Text   string  `json:"text"`
}

// SendReport stores a student's report about a question. created is false
// when this report was stored before (a repeat); ErrReportTaken if the id is
// another student's; *ReportError if a value is wrong.
func (s *Service) SendReport(ctx context.Context, student int32, in ReportInput) (id string, created bool, err error) {
	uid, ok := parseUUID(in.ID)
	if !ok {
		return "", false, &ReportError{"id must be a UUID"}
	}
	q := practicedb.New(s.pool)
	if owner, err := q.ReportOwner(ctx, uid); err == nil {
		if owner != student {
			return "", false, ErrReportTaken
		}
		return uuidString(uid), false, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, err
	}
	text := strings.TrimSpace(in.Text)
	switch {
	case in.Kind != "question" && in.Kind != "technical":
		return "", false, &ReportError{"kind must be question or technical"}
	case in.Reason != nil && !reportReasons[*in.Reason]:
		return "", false, &ReportError{"reason must be null, answer_key_wrong, typo, unclear, image_broken, formatting or other"}
	case utf8.RuneCountInString(text) > MaxReportText || strings.ContainsRune(text, 0):
		return "", false, &ReportError{fmt.Sprintf("the text must be at most %d characters", MaxReportText)}
	case in.Reason == nil && text == "":
		return "", false, &ReportError{"say what is wrong: a reason or a text"}
	}
	question, reason, err := in.check(ctx, q)
	if err != nil {
		return "", false, err
	}
	if reason != "" {
		return "", false, &ReportError{reason}
	}
	var why pgtype.Text
	if in.Reason != nil {
		why = pgtype.Text{String: *in.Reason, Valid: true}
	}
	n, err := q.InsertReport(ctx, practicedb.InsertReportParams{
		ID: uid, StudentClientID: student, Test: in.Test, Version: in.Version, Subject: in.Subject, Module: in.Module,
		Position: in.Position, QuestionID: question, Kind: in.Kind, Reason: why, Text: text,
	})
	if err != nil {
		return "", false, err
	}
	if n == 0 { // another request stored it first
		owner, err := q.ReportOwner(ctx, uid)
		if err != nil {
			return "", false, err
		}
		if owner != student {
			return "", false, ErrReportTaken
		}
	}
	return uuidString(uid), n == 1, nil
}

// AdminReport is a report as the admin panel shows it.
type AdminReport struct {
	ID        string `json:"id"`
	TestTitle string `json:"test_title"` // empty if the test is gone
	QuestionRef
	Kind       string     `json:"kind"`
	Reason     *string    `json:"reason"`
	Text       string     `json:"text"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	ResolvedAt *time.Time `json:"resolved_at"`
	ResolvedBy *string    `json:"resolved_by"` // the admin's name
	Student    Student    `json:"student"`
}

func adminReportOf(r practicedb.AdminReportRow) AdminReport {
	a := AdminReport{
		ID: uuidString(r.ID), TestTitle: r.TestTitle,
		QuestionRef: QuestionRef{Test: r.Test, Version: r.Version, Subject: r.Subject, Module: r.Module, Position: r.Position,
			QuestionID: uuidString(r.QuestionID)},
		Kind: r.Kind, Reason: textPtr(r.Reason), Text: r.Text, Status: r.Status, CreatedAt: r.CreatedAt.Time.UTC(),
		ResolvedBy: textPtr(r.ResolvedBy),
		Student:    Student{ID: r.StudentClientID, FirstName: r.FirstName, LastName: r.LastName},
	}
	if r.ResolvedAt.Valid {
		t := r.ResolvedAt.Time.UTC()
		a.ResolvedAt = &t
	}
	return a
}

// Reports lists the open or resolved reports, newest first, at most 200.
func (s *Service) Reports(ctx context.Context, status string) ([]AdminReport, error) {
	rows, err := practicedb.New(s.pool).AdminReports(ctx, status)
	if err != nil {
		return nil, err
	}
	out := make([]AdminReport, 0, len(rows))
	for _, r := range rows {
		out = append(out, adminReportOf(practicedb.AdminReportRow(r)))
	}
	return out, nil
}

// SetReportStatus opens or resolves a report (by: the admin's name) and
// returns it. Resolving keeps who resolved it first. ErrNotFound if there is
// no such report.
func (s *Service) SetReportStatus(ctx context.Context, id, status, by string) (AdminReport, error) {
	uid, ok := parseUUID(id)
	if !ok {
		return AdminReport{}, ErrNotFound
	}
	if status != "open" && status != "resolved" {
		return AdminReport{}, &ReportError{"status must be open or resolved"}
	}
	q := practicedb.New(s.pool)
	n, err := q.SetReportStatus(ctx, practicedb.SetReportStatusParams{ID: uid, Status: status, ResolvedBy: by})
	if err != nil {
		return AdminReport{}, err
	}
	if n == 0 {
		return AdminReport{}, ErrNotFound
	}
	r, err := q.AdminReport(ctx, uid)
	if err != nil {
		return AdminReport{}, err
	}
	return adminReportOf(r), nil
}
