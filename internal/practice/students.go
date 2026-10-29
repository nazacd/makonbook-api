package practice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nazacd/makonbook-api/internal/practice/practicedb"
)

// What the apps read and send: the catalog, a test's package, attempts
// (spec 9.3, 9.5).

var (
	// ErrAttemptTaken means another student already sent an attempt with this id.
	ErrAttemptTaken = errors.New("an attempt with this id belongs to someone else")
	// ErrNotTeacher means the teacher doesn't teach the group.
	ErrNotTeacher = errors.New("you don't teach this group")
)

// AttemptError is an attempt that doesn't fit its test version.
type AttemptError struct{ Reason string }

func (e *AttemptError) Error() string { return e.Reason }

// LinkTTL is how long a download's image links work.
const LinkTTL = 6 * time.Hour

// Signer makes download links to MakonBook's bucket.
type Signer interface {
	PresignGet(key string, expires time.Duration) (string, error)
}

// CatalogTest is a test as students see it in the list.
type CatalogTest struct {
	Code  string `json:"code"`
	Title string `json:"title"`
	// English and Math are how many questions each section has.
	English int32 `json:"english"`
	Math    int32 `json:"math"`
	// Approved of Questions: how far its review has come.
	Questions int32 `json:"questions"`
	Approved  int32 `json:"approved"`
	// Version is the latest published version; null while the test is
	// locked (never published). Bytes is its download size.
	Version     *int32     `json:"version"`
	Bytes       *int32     `json:"bytes"`
	PublishedAt *time.Time `json:"published_at"`
}

// Catalog lists every test: published ones are open, the rest locked with
// their progress.
func (s *Service) Catalog(ctx context.Context) ([]CatalogTest, error) {
	rows, err := practicedb.New(s.pool).Catalog(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]CatalogTest, 0, len(rows))
	for _, r := range rows {
		t := CatalogTest{Code: r.Code, Title: r.Title, English: r.English, Math: r.Math, Questions: r.Questions, Approved: r.Approved}
		if r.Version > 0 {
			t.Version, t.Bytes, t.PublishedAt = &r.Version, &r.Bytes, &r.PublishedAt.Time
		}
		out = append(out, t)
	}
	return out, nil
}

// Download is a published version with links to its images.
type Download struct {
	Package json.RawMessage `json:"package"`
	// Images maps each image file to a link valid until ExpiresAt.
	Images    map[string]string `json:"images"`
	ExpiresAt time.Time         `json:"expires_at"`
}

// Download returns a test's package (version 0: the latest) and its image
// links. ErrNotFound if the test or version isn't published.
func (s *Service) Download(ctx context.Context, code string, version int32) (Download, error) {
	row, err := practicedb.New(s.pool).TestVersion(ctx, practicedb.TestVersionParams{Code: code, Version: version})
	if errors.Is(err, pgx.ErrNoRows) {
		return Download{}, ErrNotFound
	}
	if err != nil {
		return Download{}, err
	}
	var pkg struct {
		Images map[string]PackageImage `json:"images"`
	}
	if err := json.Unmarshal(row.Package, &pkg); err != nil {
		return Download{}, fmt.Errorf("package %s v%d: %w", code, row.Version, err)
	}
	d := Download{Package: row.Package, Images: map[string]string{}, ExpiresAt: time.Now().Add(LinkTTL).UTC().Truncate(time.Second)}
	if len(pkg.Images) == 0 {
		return d, nil
	}
	if s.signer == nil {
		return Download{}, ErrUploadsOff
	}
	for _, img := range pkg.Images {
		if d.Images[img.File], err = s.signer.PresignGet(ImagePrefix+img.File, LinkTTL); err != nil {
			return Download{}, err
		}
	}
	return d, nil
}

// LatestPackage is a published test's latest version (ErrNotFound if it was
// never published).
func (s *Service) LatestPackage(ctx context.Context, code string) (Package, error) {
	row, err := practicedb.New(s.pool).TestVersion(ctx, practicedb.TestVersionParams{Code: code, Version: 0})
	if errors.Is(err, pgx.ErrNoRows) {
		return Package{}, ErrNotFound
	}
	if err != nil {
		return Package{}, err
	}
	var pkg Package
	if err := json.Unmarshal(row.Package, &pkg); err != nil {
		return Package{}, fmt.Errorf("package %s v%d: %w", code, row.Version, err)
	}
	return pkg, nil
}

// AttemptInput is an attempt as the device sends it.
type AttemptInput struct {
	ID         string    `json:"id"` // the device's UUID
	Test       string    `json:"test"`
	Version    int32     `json:"version"`
	Sections   []string  `json:"sections"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Answers    []Answer  `json:"answers"`
}

// Answer is a student's answer to one question (null: left blank).
type Answer struct {
	Subject  string  `json:"subject"`
	Module   int16   `json:"module"`
	Position int16   `json:"position"`
	Answer   *string `json:"answer"`
	Seconds  int32   `json:"seconds"`
}

// Score is one section's result.
type Score struct {
	Raw       int `json:"raw"`       // questions right
	Questions int `json:"questions"` // in the section
	Score     int `json:"score"`     // estimated, 200–800
}

// Attempt is a stored attempt with its scores.
type Attempt struct {
	ID         string           `json:"id"`
	Test       string           `json:"test"`
	Title      string           `json:"title"`
	Version    int32            `json:"version"`
	Sections   []string         `json:"sections"`
	StartedAt  time.Time        `json:"started_at"`
	FinishedAt time.Time        `json:"finished_at"`
	Scores     map[string]Score `json:"scores"`
	// Total is the two sections' sum when both were taken.
	Total *int `json:"total"`
}

const maxSeconds = 6 * 60 * 60

// SaveAttempt grades an attempt from its version's keys and stores it. The
// same attempt sent again returns the stored one; an id another student used
// is ErrAttemptTaken.
func (s *Service) SaveAttempt(ctx context.Context, student int32, in AttemptInput) (Attempt, error) {
	id, ok := parseUUID(in.ID)
	if !ok {
		return Attempt{}, &AttemptError{"id must be a UUID"}
	}
	q := practicedb.New(s.pool)
	if a, err := s.attempt(ctx, q, id); err == nil {
		if a.student != student {
			return Attempt{}, ErrAttemptTaken
		}
		return a.Attempt, nil
	} else if !errors.Is(err, ErrNotFound) {
		return Attempt{}, err
	}
	if in.Version <= 0 {
		return Attempt{}, &AttemptError{"version is required"}
	}
	row, err := q.TestVersion(ctx, practicedb.TestVersionParams{Code: in.Test, Version: in.Version})
	if errors.Is(err, pgx.ErrNoRows) {
		return Attempt{}, &AttemptError{"no such test version"}
	}
	if err != nil {
		return Attempt{}, err
	}
	var pkg Package
	if err := json.Unmarshal(row.Package, &pkg); err != nil {
		return Attempt{}, err
	}
	scores, err := grade(pkg, in)
	if err != nil {
		return Attempt{}, err
	}
	answers, _ := json.Marshal(in.Answers)
	scoresJSON, _ := json.Marshal(scores)
	_, err = q.InsertAttempt(ctx, practicedb.InsertAttemptParams{
		ID: id, StudentClientID: student, TestID: row.ID, TestVersion: row.Version, Sections: in.Sections,
		StartedAt:  pgtype.Timestamptz{Time: in.StartedAt, Valid: true},
		FinishedAt: pgtype.Timestamptz{Time: in.FinishedAt, Valid: true},
		Answers:    answers, Scores: scoresJSON,
	})
	if err != nil {
		return Attempt{}, err
	}
	a, err := s.attempt(ctx, q, id) // also when another request stored it first
	if err != nil {
		return Attempt{}, err
	}
	if a.student != student {
		return Attempt{}, ErrAttemptTaken
	}
	return a.Attempt, nil
}

// grade checks an attempt against its package and scores each section taken.
func grade(pkg Package, in AttemptInput) (map[string]Score, error) {
	if in.StartedAt.IsZero() || in.FinishedAt.Before(in.StartedAt) {
		return nil, &AttemptError{"started_at and finished_at are required, in order"}
	}
	if in.FinishedAt.After(time.Now().Add(24 * time.Hour)) {
		return nil, &AttemptError{"finished_at is in the future"}
	}
	if len(in.Sections) == 0 {
		return nil, &AttemptError{"sections must name english and/or math"}
	}
	type slot struct {
		subject          string
		module, position int16
	}
	questions := map[slot]PackageQuestion{}
	sizes := map[string]int{}
	scales := map[string][]int{}
	for _, sec := range pkg.Sections {
		scales[sec.Subject] = sec.Scale
		for _, m := range sec.Modules {
			for _, pq := range m.Questions {
				questions[slot{sec.Subject, m.Module, pq.Position}] = pq
				sizes[sec.Subject]++
			}
		}
	}
	scores := map[string]Score{}
	for _, sub := range in.Sections {
		if _, dup := scores[sub]; dup || sizes[sub] == 0 {
			return nil, &AttemptError{fmt.Sprintf("section %q isn't in the test or is named twice", sub)}
		}
		scores[sub] = Score{Questions: sizes[sub]}
	}
	seen := map[slot]bool{}
	for _, a := range in.Answers {
		k := slot{a.Subject, a.Module, a.Position}
		pq, ok := questions[k]
		if _, taken := scores[a.Subject]; !ok || !taken || seen[k] {
			return nil, &AttemptError{fmt.Sprintf("answer to %s module %d question %d: no such question in the sections taken, or answered twice",
				a.Subject, a.Module, a.Position)}
		}
		if a.Seconds < 0 || a.Seconds > maxSeconds {
			return nil, &AttemptError{"seconds must be between 0 and 21600"}
		}
		seen[k] = true
		if a.Answer != nil && pq.Correct(*a.Answer) {
			sc := scores[a.Subject]
			sc.Raw++
			scores[a.Subject] = sc
		}
	}
	for sub, sc := range scores {
		if sc.Raw < len(scales[sub]) {
			sc.Score = scales[sub][sc.Raw]
		}
		scores[sub] = sc
	}
	return scores, nil
}

type storedAttempt struct {
	Attempt
	student int32
}

func (s *Service) attempt(ctx context.Context, q *practicedb.Queries, id pgtype.UUID) (storedAttempt, error) {
	r, err := q.GetAttempt(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return storedAttempt{}, ErrNotFound
	}
	if err != nil {
		return storedAttempt{}, err
	}
	a, err := attemptOf(practicedb.StudentAttemptsRow(r))
	return storedAttempt{a, r.StudentClientID}, err
}

func attemptOf(r practicedb.StudentAttemptsRow) (Attempt, error) {
	a := Attempt{
		ID: uuidString(r.ID), Test: r.Code, Title: r.Title, Version: r.TestVersion, Sections: r.Sections,
		StartedAt: r.StartedAt.Time.UTC(), FinishedAt: r.FinishedAt.Time.UTC(),
	}
	if err := json.Unmarshal(r.Scores, &a.Scores); err != nil {
		return Attempt{}, fmt.Errorf("attempt %s: %w", a.ID, err)
	}
	if e, ok := a.Scores["english"]; ok {
		if m, ok := a.Scores["math"]; ok {
			total := e.Score + m.Score
			a.Total = &total
		}
	}
	return a, nil
}

// OwnAttempt is an attempt with the student's answers, for the student
// themselves: a phone that doesn't have it (a new one) can show its review.
type OwnAttempt struct {
	Attempt
	Answers []Answer `json:"answers"`
}

// StudentAttempts lists a student's attempts, latest first, with answers.
func (s *Service) StudentAttempts(ctx context.Context, student int32) ([]OwnAttempt, error) {
	rows, err := practicedb.New(s.pool).OwnAttempts(ctx, student)
	if err != nil {
		return nil, err
	}
	out := make([]OwnAttempt, 0, len(rows))
	for _, r := range rows {
		a, err := attemptOf(practicedb.StudentAttemptsRow{
			ID: r.ID, StudentClientID: r.StudentClientID, Code: r.Code, Title: r.Title, TestVersion: r.TestVersion,
			Sections: r.Sections, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, Scores: r.Scores,
		})
		if err != nil {
			return nil, err
		}
		own := OwnAttempt{Attempt: a, Answers: []Answer{}}
		if err := json.Unmarshal(r.Answers, &own.Answers); err != nil {
			return nil, fmt.Errorf("attempt %s answers: %w", a.ID, err)
		}
		out = append(out, own)
	}
	return out, nil
}

func (s *Service) attemptsOf(ctx context.Context, students []int32) (map[int32][]Attempt, error) {
	rows, err := practicedb.New(s.pool).StudentAttempts(ctx, students)
	if err != nil {
		return nil, err
	}
	out := map[int32][]Attempt{}
	for _, r := range rows {
		a, err := attemptOf(r)
		if err != nil {
			return nil, err
		}
		out[r.StudentClientID] = append(out[r.StudentClientID], a)
	}
	return out, nil
}

// GroupStudent is a student of a group with their attempts, for the teacher.
type GroupStudent struct {
	ClientID  int32     `json:"client_id"`
	FirstName string    `json:"first_name"`
	LastName  string    `json:"last_name"`
	Attempts  []Attempt `json:"attempts"`
}

// GroupAttempts is the practice of a group's students, for one of its
// teachers (today: the school's date). ErrNotTeacher otherwise.
func (s *Service) GroupAttempts(ctx context.Context, teacher, group int32, today time.Time) ([]GroupStudent, error) {
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
	for _, st := range students {
		ids = append(ids, st.ClientID)
	}
	attempts, err := s.attemptsOf(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]GroupStudent, 0, len(students))
	for _, st := range students {
		a := attempts[st.ClientID]
		if a == nil {
			a = []Attempt{}
		}
		out = append(out, GroupStudent{ClientID: st.ClientID, FirstName: st.FirstName, LastName: st.LastName, Attempts: a})
	}
	return out, nil
}
