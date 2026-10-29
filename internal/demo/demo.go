// Package demo keeps the demo student account app store reviewers sign in
// with (Google Play and the App Store ask for one): a fictional student with
// two classes, their schedule, eight weeks of attendance and a Day 1 practice
// result.
//
// It lives in the HolliHop mirror tables under negative ids, so every screen
// shows it like a real student, and stays apart from the school's data:
//   - the sync leaves negative ids alone (people and attendance queries);
//   - its groups have no lesson days (those come only from HolliHop), so no
//     teacher or admin sees its lessons, nobody can take attendance there,
//     and nothing about it is ever written to HolliHop;
//   - its branch isn't in the admin panel's branch list, and parents' reports
//     about it don't go to Telegram;
//   - it signs in with a username and the DEMO_PASSWORD setting, and has no
//     phone number.
//
// Setup is safe to run any time: the worker runs it every day, which moves
// the attendance along with the calendar and puts back the password and
// username if a reviewer changed them.
package demo

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nazacd/makonbook-api/internal/auth"
	"github.com/nazacd/makonbook-api/internal/demo/demodb"
	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/practice"
)

const (
	StudentID = -1
	Username  = "demo.student"

	// PracticeTest is the test the student has a result of.
	PracticeTest = "DAY1"
	// attemptID is the result's fixed id: setting up again keeps the one
	// result instead of adding another.
	attemptID = "de000000-0000-4000-8000-000000000001"

	officeID = -1
	weeks    = 8 // of attendance
)

// The fictional classes. Weekdays as in schedule_items: Mon=1 … Sun=64.
var classes = []struct {
	group, teacher            int32
	name, discipline          string
	teacherFirst, teacherLast string
	weekdays                  int16
	begin, end                time.Duration
	room                      string
}{
	{-1, -1, "SAT Math (demo)", "SAT Math", "Nodira", "Aliyeva", 1 | 4 | 16, 14 * time.Hour, 15*time.Hour + 30*time.Minute, "204"},
	{-2, -2, "SAT English (demo)", "SAT English", "Sardor", "Karimov", 2 | 8 | 32, 16 * time.Hour, 17*time.Hour + 30*time.Minute, "305"},
}

// ErrUsernameTaken: a real person chose the demo's username.
var ErrUsernameTaken = errors.New("someone already uses the username " + Username)

// Codes gives a student's parent code (parents.Service).
type Codes interface {
	Code(ctx context.Context, student int32) (string, error)
}

type Service struct {
	pool     *pgxpool.Pool
	practice *practice.Service
	codes    Codes
	hasher   *auth.Hasher
	location *time.Location
}

func NewService(pool *pgxpool.Pool, practice *practice.Service, codes Codes, location *time.Location) *Service {
	return &Service{pool: pool, practice: practice, codes: codes, hasher: auth.NewHasher(), location: location}
}

// Result says what Setup made.
type Result struct {
	Username   string
	ParentCode string
	// Practice is the Day 1 result ("1150 (English 560, Math 590)"), or why
	// there is none.
	Practice string
}

// Setup creates the demo account or brings it up to date (now: the time it
// runs; attendance is made for the eight weeks before that day).
func (s *Service) Setup(ctx context.Context, password string, now time.Time) (Result, error) {
	if n := utf8.RuneCountInString(password); n < 8 || n > 128 {
		return Result{}, errors.New("the demo password must be 8 to 128 characters")
	}
	hash, err := s.hasher.Hash(password)
	if err != nil {
		return Result{}, err
	}
	y, m, d := now.In(s.location).Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	start := today.AddDate(0, 0, -7*weeks)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // after Commit it does nothing
	q := demodb.New(tx)

	owner, err := q.UsernameOwner(ctx, pgtype.Text{String: Username, Valid: true})
	if err == nil && (owner.PersonKind != people.KindStudent || owner.PersonID != StudentID) {
		return Result{}, ErrUsernameTaken
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Result{}, err
	}

	if err := q.UpsertOffice(ctx, demodb.UpsertOfficeParams{ID: officeID, Name: "SAT MAKON (demo)"}); err != nil {
		return Result{}, fmt.Errorf("office: %w", err)
	}
	if err := q.DeleteScheduleItems(ctx); err != nil {
		return Result{}, err
	}
	for _, c := range classes {
		if err := q.UpsertTeacher(ctx, demodb.UpsertTeacherParams{ID: c.teacher, FirstName: c.teacherFirst, LastName: c.teacherLast}); err != nil {
			return Result{}, fmt.Errorf("teacher: %w", err)
		}
		if err := q.UpsertGroup(ctx, demodb.UpsertGroupParams{ID: c.group, Name: c.name, Discipline: c.discipline,
			Level: "Intermediate", OfficeID: pgtype.Int4{Int32: officeID, Valid: true}}); err != nil {
			return Result{}, fmt.Errorf("group: %w", err)
		}
		if err := q.InsertScheduleItem(ctx, demodb.InsertScheduleItemParams{
			ID: c.group, GroupID: c.group, BeginDate: date(start),
			BeginTime: clock(c.begin), EndTime: clock(c.end), Weekdays: c.weekdays, Classroom: c.room,
			TeacherIds: []int32{c.teacher},
		}); err != nil {
			return Result{}, fmt.Errorf("schedule: %w", err)
		}
	}

	// Active from the start of the attendance, so the password set below
	// (now) belongs to the current active period.
	if err := q.UpsertStudent(ctx, demodb.UpsertStudentParams{ClientID: StudentID, FirstName: "Demo", LastName: "Student",
		OfficeIds: []int32{officeID}, ActivatedAt: pgtype.Timestamptz{Time: start, Valid: true}}); err != nil {
		return Result{}, fmt.Errorf("student: %w", err)
	}
	if err := q.DeleteEnrollments(ctx, StudentID); err != nil {
		return Result{}, err
	}
	for _, c := range classes {
		if err := q.InsertEnrollment(ctx, demodb.InsertEnrollmentParams{GroupID: c.group, StudentClientID: StudentID, BeginDate: date(start)}); err != nil {
			return Result{}, fmt.Errorf("enrollment: %w", err)
		}
	}

	if err := q.DeleteRecords(ctx, StudentID); err != nil {
		return Result{}, err
	}
	rec := demodb.InsertRecordsParams{StudentClientID: StudentID}
	for _, c := range classes {
		n := 0
		for day := start; day.Before(today); day = day.AddDate(0, 0, 1) {
			if c.weekdays&weekdayBit(day) == 0 {
				continue
			}
			rec.GroupIds = append(rec.GroupIds, c.group)
			rec.Dates = append(rec.Dates, date(day))
			rec.Passes = append(rec.Passes, n%9 == 4) // missed one lesson in nine
			n++
		}
	}
	if err := q.InsertRecords(ctx, rec); err != nil {
		return Result{}, fmt.Errorf("attendance: %w", err)
	}

	if err := q.SetCredential(ctx, demodb.SetCredentialParams{PersonID: StudentID,
		Username: pgtype.Text{String: Username, Valid: true}, PasswordHash: pgtype.Text{String: hash, Valid: true}}); err != nil {
		return Result{}, fmt.Errorf("credential: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, err
	}

	res := Result{Username: Username, Practice: s.practiceResult(ctx, today)}
	if res.ParentCode, err = s.codes.Code(ctx, StudentID); err != nil {
		return res, fmt.Errorf("parent code: %w", err)
	}
	return res, nil
}

// practiceResult gives the student a result of PracticeTest, taken the day
// before: about three answers in four right, some blank. It goes through the
// same grading as a phone's upload, and stays the same once made.
func (s *Service) practiceResult(ctx context.Context, today time.Time) string {
	pkg, err := s.practice.LatestPackage(ctx, PracticeTest)
	if errors.Is(err, practice.ErrNotFound) {
		return PracticeTest + " isn't published yet: no practice result (setting up again after it is adds one)"
	}
	if err != nil {
		return "no practice result: " + err.Error()
	}
	started := today.AddDate(0, 0, -1).Add(15*time.Hour - 5*time.Hour) // 15:00 in Tashkent (UTC+5)
	in := practice.AttemptInput{ID: attemptID, Test: pkg.Code, Version: pkg.Version,
		StartedAt: started, FinishedAt: started.Add(2*time.Hour + 14*time.Minute)}
	i := 0
	for _, sec := range pkg.Sections {
		in.Sections = append(in.Sections, sec.Subject)
		for _, mod := range sec.Modules {
			for _, q := range mod.Questions {
				in.Answers = append(in.Answers, practice.Answer{Subject: sec.Subject, Module: mod.Module, Position: q.Position,
					Answer: answer(q, i), Seconds: int32(45 + (i*37)%80)})
				i++
			}
		}
	}
	a, err := s.practice.SaveAttempt(ctx, StudentID, in)
	if err != nil {
		return "no practice result: " + err.Error()
	}
	out := fmt.Sprintf("%s v%d", a.Test, a.Version)
	if a.Total != nil {
		out += fmt.Sprintf(": %d", *a.Total)
	}
	for _, sub := range a.Sections {
		out += fmt.Sprintf(", %s %d", sub, a.Scores[sub].Score)
	}
	return out
}

// answer is the demo's answer to the i-th question: blank now and then, a
// wrong one about one time in four, otherwise the right one.
func answer(q practice.PackageQuestion, i int) *string {
	if i%23 == 7 {
		return nil
	}
	right := ""
	if q.Type == "mcq" && q.Key != nil {
		right = *q.Key
	} else if len(q.Accepted) > 0 {
		right = q.Accepted[0]
	}
	if i%4 != 1 || right == "" {
		return &right
	}
	if q.Type == "mcq" {
		letters := make([]string, 0, len(q.Choices))
		for l := range q.Choices {
			letters = append(letters, l)
		}
		sort.Strings(letters)
		for _, l := range letters {
			if !q.Correct(l) {
				return &l
			}
		}
		return &right
	}
	wrong := "0"
	if q.Correct(wrong) {
		wrong = "1"
	}
	return &wrong
}

// Remove deletes the demo account and everything made with it.
func (s *Service) Remove(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // after Commit it does nothing
	q := demodb.New(tx)
	for _, del := range []func(context.Context) error{
		q.DeleteAttempts, q.DeleteVocabReviews, q.DeleteSessions, q.DeleteCredentials,
		q.DeleteStudents, q.DeleteGroups, q.DeleteTeachers, q.DeleteOffices,
	} {
		if err := del(ctx); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func weekdayBit(day time.Time) int16 {
	return 1 << ((int(day.Weekday()) + 6) % 7) // Monday = bit 0
}

func date(t time.Time) pgtype.Date { return pgtype.Date{Time: t, Valid: true} }

func clock(d time.Duration) pgtype.Time {
	return pgtype.Time{Microseconds: d.Microseconds(), Valid: true}
}
