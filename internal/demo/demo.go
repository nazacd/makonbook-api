// Package demo keeps the demo student account app store reviewers sign in
// with (Google Play and the App Store ask for one): a fictional student with
// two classes, their schedule, eight weeks of attendance and practice results
// that get better over the weeks.
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

// The student's practice results, oldest first: the test, its estimated
// total, and how many days before today it was taken. Each has a fixed id, so
// setting up again replaces it instead of adding another.
var results = []struct {
	id, test string
	total    int
	daysAgo  int
}{
	{"de000000-0000-4000-8000-000000000001", "DAY1", 1240, 45},
	{"de000000-0000-4000-8000-000000000002", "DAY2", 1290, 41},
	{"de000000-0000-4000-8000-000000000003", "DAY2", 1340, 37},
	{"de000000-0000-4000-8000-000000000006", "DAY3", 1310, 33},
	{"de000000-0000-4000-8000-000000000007", "DAY4", 1360, 29},
	{"de000000-0000-4000-8000-000000000008", "DAY5", 1330, 25},
	{"de000000-0000-4000-8000-000000000009", "DAY6", 1390, 21},
	{"de000000-0000-4000-8000-00000000000a", "DAY7", 1370, 17},
	{"de000000-0000-4000-8000-000000000004", "DAY8", 1380, 12},
	{"de000000-0000-4000-8000-000000000005", "DAY9", 1410, 7},
	{"de000000-0000-4000-8000-00000000000b", "DAY9", 1520, 2},
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
	// Practice is each practice result ("DAY2 v1: 1290, english 630, math
	// 660"), or why it is missing.
	Practice []string
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

	res := Result{Username: Username, Practice: s.practiceResults(ctx, today)}
	if res.ParentCode, err = s.codes.Code(ctx, StudentID); err != nil {
		return res, fmt.Errorf("parent code: %w", err)
	}
	return res, nil
}

// practiceResults gives the student their results (see results), made
// again each time so the dates move with the calendar. Each goes through the
// same grading as a phone's upload, with answers chosen to reach its total. A
// test that isn't published is skipped (setting up again later adds it).
func (s *Service) practiceResults(ctx context.Context, today time.Time) []string {
	ids := make([]pgtype.UUID, len(results))
	for i, r := range results {
		_ = ids[i].Scan(r.id)
	}
	if err := demodb.New(s.pool).DeleteSeededAttempts(ctx, ids); err != nil {
		return []string{"no practice results: " + err.Error()}
	}
	var out []string
	for _, r := range results {
		pkg, err := s.practice.LatestPackage(ctx, r.test)
		if errors.Is(err, practice.ErrNotFound) {
			out = append(out, r.test+": not published yet, no result")
			continue
		}
		if err != nil {
			out = append(out, r.test+": "+err.Error())
			continue
		}
		started := today.AddDate(0, 0, -r.daysAgo).Add(15*time.Hour - 5*time.Hour) // 15:00 in Tashkent (UTC+5)
		in := practice.AttemptInput{ID: r.id, Test: pkg.Code, Version: pkg.Version,
			StartedAt: started, FinishedAt: started.Add(2*time.Hour + 14*time.Minute), Answers: answers(pkg, r.total)}
		for _, sec := range pkg.Sections {
			in.Sections = append(in.Sections, sec.Subject)
		}
		a, err := s.practice.SaveAttempt(ctx, StudentID, in)
		if err != nil {
			out = append(out, r.test+": "+err.Error())
			continue
		}
		line := fmt.Sprintf("%s v%d", a.Test, a.Version)
		if a.Total != nil {
			line += fmt.Sprintf(": %d", *a.Total)
		}
		for _, sub := range a.Sections {
			line += fmt.Sprintf(", %s %d", sub, a.Scores[sub].Score)
		}
		out = append(out, line)
	}
	return out
}

// answers are a student's answers to every question of pkg that score total
// (or as near as the score scales allow), Math a little ahead of English.
// The right ones are spread through each section; of the rest, most are a
// wrong choice and some are left blank.
func answers(pkg practice.Package, total int) []practice.Answer {
	right := map[string]int{}
	if len(pkg.Sections) == 2 {
		e, m := pkg.Sections[0], pkg.Sections[1]
		best := -1
		for re := range e.Scale {
			for rm := range m.Scale {
				miss := abs(e.Scale[re]+m.Scale[rm]-total)*1000 + abs(m.Scale[rm]-e.Scale[re]-20)
				if best < 0 || miss < best {
					best, right[e.Subject], right[m.Subject] = miss, re, rm
				}
			}
		}
	}
	var out []practice.Answer
	i := 0
	for _, sec := range pkg.Sections {
		n := 0
		for _, mod := range sec.Modules {
			n += len(mod.Questions)
		}
		k, j, wrong := right[sec.Subject], 0, 0
		for _, mod := range sec.Modules {
			for _, q := range mod.Questions {
				var a *string
				if (j+1)*k/n > j*k/n { // k of the n questions, evenly spread
					a = rightAnswer(q)
				} else {
					if wrong%4 != 3 {
						a = wrongAnswer(q)
					}
					wrong++
				}
				out = append(out, practice.Answer{Subject: sec.Subject, Module: mod.Module, Position: q.Position,
					Answer: a, Seconds: int32(45 + (i*37)%80)})
				i++
				j++
			}
		}
	}
	return out
}

func rightAnswer(q practice.PackageQuestion) *string {
	if q.Type == "mcq" && q.Key != nil {
		return q.Key
	}
	if len(q.Accepted) > 0 {
		return &q.Accepted[0]
	}
	return nil
}

func wrongAnswer(q practice.PackageQuestion) *string {
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
		return nil
	}
	wrong := "0"
	if q.Correct(wrong) {
		wrong = "1"
	}
	return &wrong
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
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
