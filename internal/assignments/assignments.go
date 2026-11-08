// Package assignments is the work teachers give their groups (spec 23):
// homework, level checks and exams with deadlines, the students' standing in
// them, results per student and per question, and in-app notifications.
// Homework and level checks are taken as roadmap assessments
// (internal/roadmap), exams as practice tests (internal/practice).
package assignments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nazacd/makonbook-api/internal/assignments/assignmentsdb"
	"github.com/nazacd/makonbook-api/internal/practice"
	"github.com/nazacd/makonbook-api/internal/roadmap"
)

// Kinds of assignment.
const (
	KindHomework   = "homework"
	KindLevelCheck = "level_check"
	KindExam       = "exam"
)

// Statuses of an assignment, worked out from its dates.
const (
	StatusScheduled = "scheduled"
	StatusOpen      = "open"
	StatusOverdue   = "overdue"
	StatusClosed    = "closed"
)

// A student's standing.
const (
	StandingNew        = "new"
	StandingInProgress = "in_progress"
	StandingDone       = "done"
)

// Limits on what a teacher sends.
const (
	maxTitle        = 120
	maxInstructions = 2000
	maxQuestions    = 60
)

var (
	// ErrNotFound means no such assignment (for this person), or student.
	ErrNotFound = errors.New("not found")
	// ErrForbidden means the teacher doesn't teach the group, or the person
	// isn't a teacher.
	ErrForbidden = errors.New("you don't teach this group")
	// ErrNotAnAssessment means an exam was started as an assessment.
	ErrNotAnAssessment = errors.New("an exam is taken as the mock test itself")
	// ErrNotOpen means the assignment hasn't opened yet.
	ErrNotOpen = errors.New("this assignment hasn't opened yet")
	// ErrClosed means the teacher closed the assignment.
	ErrClosed = errors.New("the teacher closed this assignment")
	// ErrHasWork means a student has started the assignment.
	ErrHasWork = errors.New("a student has started this assignment: close it instead")
)

// InvalidError is an assignment that can't be saved, field by field.
type InvalidError struct{ Fields map[string]string }

func (e *InvalidError) Error() string {
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + ": " + e.Fields[k]
	}
	return strings.Join(parts, "; ")
}

func invalid(field, message string) error {
	return &InvalidError{Fields: map[string]string{field: message}}
}

// Service is assignments and notifications.
type Service struct {
	pool     *pgxpool.Pool
	roadmap  *roadmap.Service
	practice *practice.Service
	location *time.Location // the school's time, for deadlines in notifications
	now      func() time.Time
}

// NewService builds the service.
func NewService(pool *pgxpool.Pool, rm *roadmap.Service, pr *practice.Service, location *time.Location) *Service {
	return &Service{pool: pool, roadmap: rm, practice: pr, location: location, now: time.Now}
}

// Lesson is the curriculum lesson an assignment comes from.
type Lesson struct {
	Course    string `json:"course"`
	LessonID  string `json:"lesson_id"`
	Title     string `json:"title"`
	UnitID    string `json:"unit_id"`
	UnitTitle string `json:"unit_title"`
}

// Test is an exam's mock test: the sections to take and how many questions
// each section of its latest version has.
type Test struct {
	Code     string   `json:"code"`
	Title    string   `json:"title"`
	Sections []string `json:"sections"`
	English  int      `json:"english"`
	Math     int      `json:"math"`
}

// Summary is how the group stands on an assignment.
type Summary struct {
	Students       int      `json:"students"`
	Done           int      `json:"done"`
	Late           int      `json:"late"`
	InProgress     int      `json:"in_progress"`
	AveragePercent *float64 `json:"average_percent"`
	AverageScore   *float64 `json:"average_score"`
}

// Assignment is an assignment as its group's teachers see it.
type Assignment struct {
	ID            string       `json:"id"`
	GroupID       int32        `json:"group_id"`
	GroupName     string       `json:"group_name"`
	TeacherID     int32        `json:"teacher_id"`
	Kind          string       `json:"kind"`
	Status        string       `json:"status"`
	Title         string       `json:"title"`
	Instructions  string       `json:"instructions"`
	Subject       string       `json:"subject"`
	Lesson        *Lesson      `json:"lesson"`
	HomeworkIndex *int         `json:"homework_index"`
	Test          *Test        `json:"test"`
	Skills        []string     `json:"skills"`
	Mix           *roadmap.Mix `json:"mix"`
	Questions     int          `json:"questions"`
	OpensAt       time.Time    `json:"opens_at"`
	DueAt         time.Time    `json:"due_at"`
	ClosedAt      *time.Time   `json:"closed_at"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
	Summary       Summary      `json:"summary"`
	row           assignmentsdb.Assignment
}

// Detail is an assignment with its questions whole.
type Detail struct {
	Assignment
	Items  []Question                    `json:"items"` // empty for an exam
	Images map[string]practice.ImageLink `json:"images"`
}

// Question is a question as a teacher sees it: with its key, accepted forms
// and explanation.
type Question struct {
	ID          string            `json:"id"`
	Position    int               `json:"position"`
	Type        string            `json:"type"`
	Passage     *string           `json:"passage"`
	Stem        string            `json:"stem"`
	Choices     map[string]string `json:"choices"`
	Figures     []practice.Figure `json:"figures"`
	Correct     *string           `json:"correct"`
	Accepted    []string          `json:"accepted"`
	Explanation string            `json:"explanation"`
	Domain      string            `json:"domain"`
	Skill       string            `json:"skill"`
	Difficulty  *string           `json:"difficulty"`
}

// Standing is how a student stands on an assignment: the first finished
// work is the result; later tries count in Tries and Best.
type Standing struct {
	Status       string                    `json:"status"`
	AssessmentID *string                   `json:"assessment_id"`
	AttemptID    *string                   `json:"attempt_id"`
	StartedAt    *time.Time                `json:"started_at"`
	FinishedAt   *time.Time                `json:"finished_at"`
	Late         bool                      `json:"late"`
	Percent      *int                      `json:"percent"`
	Correct      *int                      `json:"correct"`
	Total        *int                      `json:"total"`
	Passed       *bool                     `json:"passed"`
	Scores       map[string]practice.Score `json:"scores"`
	TotalScore   *int                      `json:"total_score"`
	Tries        int                       `json:"tries"`
	Best         *int                      `json:"best"`

	counted *work        // homework and level checks: the result
	attempt *examAttempt // exams: the attempt that counted
	hasWork bool         // anything started
}

// work is a roadmap assessment started from an assignment.
type work struct {
	id                      string
	assignment              string
	student                 int32
	created                 time.Time
	finished                *time.Time
	correct, total, percent int
	answers                 map[string]*string
	times                   map[string]int
}

// examAttempt is a practice attempt that may count for an exam.
type examAttempt struct {
	practice.Attempt
	student int32
	answers []practice.Answer
}

// rosterStudent is a student of a group now.
type rosterStudent struct {
	ClientID  int32
	FirstName string
	LastName  string
}

// facts is what standings are worked out from: the groups' rosters, the
// work on the assignments, the attempts on the exams' tests, and the tests'
// latest versions.
type facts struct {
	groupNames map[int32]string
	rosters    map[int32][]rosterStudent
	work       map[string]map[int32][]work // assignment → student → work, oldest first
	attempts   map[string]map[int32][]examAttempt
	packages   map[string]*practice.Package // nil: never published
}

// gather reads the facts about rows; students limits the rosters' work and
// attempts to those people (nil: every student of the groups).
func (s *Service) gather(ctx context.Context, rows []assignmentsdb.Assignment, day time.Time, only []int32) (facts, error) {
	q := assignmentsdb.New(s.pool)
	f := facts{
		groupNames: map[int32]string{}, rosters: map[int32][]rosterStudent{},
		work: map[string]map[int32][]work{}, attempts: map[string]map[int32][]examAttempt{}, packages: map[string]*practice.Package{},
	}
	if len(rows) == 0 {
		return f, nil
	}
	var groupIDs []int32
	var ids []pgtype.UUID
	var codes []string
	for _, r := range rows {
		if !slices.Contains(groupIDs, r.GroupID) {
			groupIDs = append(groupIDs, r.GroupID)
		}
		ids = append(ids, r.ID)
		if r.TestCode.Valid && !slices.Contains(codes, r.TestCode.String) {
			codes = append(codes, r.TestCode.String)
		}
	}
	names, err := q.GroupNames(ctx, groupIDs)
	if err != nil {
		return f, err
	}
	for _, n := range names {
		f.groupNames[n.ID] = n.Name
	}
	students := only
	if only == nil {
		roster, err := q.Rosters(ctx, assignmentsdb.RostersParams{GroupIds: groupIDs, Today: date(day)})
		if err != nil {
			return f, err
		}
		seen := map[int32]bool{}
		for _, r := range roster {
			f.rosters[r.GroupID] = append(f.rosters[r.GroupID], rosterStudent{ClientID: r.ClientID, FirstName: r.FirstName, LastName: r.LastName})
			if !seen[r.ClientID] {
				seen[r.ClientID] = true
				students = append(students, r.ClientID)
			}
		}
	}
	if len(students) == 0 {
		return f, s.loadPackages(ctx, codes, f)
	}
	ws, err := q.Work(ctx, assignmentsdb.WorkParams{Ids: ids, Students: students})
	if err != nil {
		return f, err
	}
	for _, w := range ws {
		item, err := workOf(w)
		if err != nil {
			return f, err
		}
		if f.work[item.assignment] == nil {
			f.work[item.assignment] = map[int32][]work{}
		}
		f.work[item.assignment][item.student] = append(f.work[item.assignment][item.student], item)
	}
	if len(codes) > 0 {
		as, err := q.ExamAttempts(ctx, assignmentsdb.ExamAttemptsParams{Codes: codes, Students: students})
		if err != nil {
			return f, err
		}
		for _, a := range as {
			ea, err := attemptOf(a)
			if err != nil {
				return f, err
			}
			if f.attempts[a.Code] == nil {
				f.attempts[a.Code] = map[int32][]examAttempt{}
			}
			f.attempts[a.Code][a.StudentClientID] = append(f.attempts[a.Code][a.StudentClientID], ea)
		}
	}
	return f, s.loadPackages(ctx, codes, f)
}

func (s *Service) loadPackages(ctx context.Context, codes []string, f facts) error {
	for _, code := range codes {
		pkg, err := s.practice.LatestPackage(ctx, code)
		switch {
		case errors.Is(err, practice.ErrNotFound):
			f.packages[code] = nil
		case err != nil:
			return err
		default:
			f.packages[code] = &pkg
		}
	}
	return nil
}

func workOf(w assignmentsdb.WorkRow) (work, error) {
	out := work{
		id: uuidString(w.ID), assignment: uuidString(w.AssignmentID), student: w.StudentClientID, created: w.CreatedAt.Time.UTC(),
		correct: int(w.Correct.Int16), total: int(w.Total.Int16), percent: int(w.Percent.Int16),
		answers: map[string]*string{}, times: map[string]int{},
	}
	if w.FinishedAt.Valid {
		t := w.FinishedAt.Time.UTC()
		out.finished = &t
	}
	if err := json.Unmarshal(w.Answers, &out.answers); err != nil {
		return out, fmt.Errorf("assessment %s answers: %w", out.id, err)
	}
	if err := json.Unmarshal(w.Times, &out.times); err != nil {
		return out, fmt.Errorf("assessment %s times: %w", out.id, err)
	}
	return out, nil
}

func attemptOf(r assignmentsdb.ExamAttemptsRow) (examAttempt, error) {
	a := examAttempt{student: r.StudentClientID, Attempt: practice.Attempt{
		ID: uuidString(r.ID), Test: r.Code, Title: r.Title, Version: r.TestVersion, Sections: r.Sections,
		StartedAt: r.StartedAt.Time.UTC(), FinishedAt: r.FinishedAt.Time.UTC(),
	}}
	if err := json.Unmarshal(r.Scores, &a.Scores); err != nil {
		return a, fmt.Errorf("attempt %s scores: %w", a.ID, err)
	}
	if err := json.Unmarshal(r.Answers, &a.answers); err != nil {
		return a, fmt.Errorf("attempt %s answers: %w", a.ID, err)
	}
	if e, ok := a.Scores["english"]; ok {
		if m, ok := a.Scores["math"]; ok {
			total := e.Score + m.Score
			a.Total = &total
		}
	}
	return a, nil
}

// standing works out a student's standing on an assignment from the facts.
func standing(r assignmentsdb.Assignment, f facts, student int32) Standing {
	st := Standing{Status: StandingNew}
	due := r.DueAt.Time
	if r.Kind == KindExam {
		var counted []examAttempt
		for _, a := range f.attempts[r.TestCode.String][student] {
			if !a.FinishedAt.Before(r.OpensAt.Time) && covers(a.Sections, r.Sections) {
				counted = append(counted, a)
			}
		}
		st.Tries = len(counted)
		if len(counted) == 0 {
			return st
		}
		st.hasWork = true
		first := counted[0]
		st.attempt = &first
		st.Status = StandingDone
		st.AttemptID = &first.ID
		st.StartedAt, st.FinishedAt = &first.StartedAt, &first.FinishedAt
		st.Late = first.FinishedAt.After(due)
		st.Scores = map[string]practice.Score{}
		for _, sec := range r.Sections {
			st.Scores[sec] = first.Scores[sec]
		}
		total := examScore(first, r.Sections)
		st.TotalScore = &total
		best := total
		for _, a := range counted[1:] {
			best = max(best, examScore(a, r.Sections))
		}
		st.Best = &best
		return st
	}
	ws := f.work[uuidString(r.ID)][student]
	if len(ws) == 0 {
		return st
	}
	st.hasWork = true
	started := ws[0].created
	st.StartedAt = &started
	var first *work
	var open *work
	best := -1
	for i := range ws {
		w := &ws[i]
		if w.finished == nil {
			open = w
			continue
		}
		st.Tries++
		best = max(best, w.percent)
		if first == nil || w.finished.Before(*first.finished) {
			first = w
		}
	}
	switch {
	case first != nil:
		st.Status = StandingDone
		st.counted = first
		st.AssessmentID = &first.id
		st.FinishedAt = first.finished
		st.Late = first.finished.After(due)
		st.Percent, st.Correct, st.Total = &first.percent, &first.correct, &first.total
		passed := r.Kind != KindLevelCheck || first.percent >= roadmap.PassPercent
		st.Passed = &passed
		st.Best = &best
	case open != nil:
		st.Status = StandingInProgress
		st.AssessmentID = &open.id
	}
	return st
}

// covers: the attempt took every section asked.
func covers(taken, asked []string) bool {
	for _, s := range asked {
		if !slices.Contains(taken, s) {
			return false
		}
	}
	return true
}

// examScore is an attempt's score on the sections asked: their sum.
func examScore(a examAttempt, sections []string) int {
	total := 0
	for _, s := range sections {
		total += a.Scores[s].Score
	}
	return total
}

// summarize works out how a group stands.
func summarize(r assignmentsdb.Assignment, f facts) Summary {
	roster := f.rosters[r.GroupID]
	sum := Summary{Students: len(roster)}
	var percents, scores []int
	for _, st := range roster {
		s := standing(r, f, st.ClientID)
		switch s.Status {
		case StandingDone:
			sum.Done++
			if s.Late {
				sum.Late++
			}
			if s.Percent != nil {
				percents = append(percents, *s.Percent)
			}
			if s.TotalScore != nil {
				scores = append(scores, *s.TotalScore)
			}
		case StandingInProgress:
			sum.InProgress++
		}
	}
	sum.AveragePercent, sum.AverageScore = mean(percents), mean(scores)
	return sum
}

func mean(xs []int) *float64 {
	if len(xs) == 0 {
		return nil
	}
	total := 0
	for _, x := range xs {
		total += x
	}
	m := float64(total) / float64(len(xs))
	return &m
}

// status is an assignment's status at now.
func status(r assignmentsdb.Assignment, now time.Time) string {
	switch {
	case r.ClosedAt.Valid:
		return StatusClosed
	case now.Before(r.OpensAt.Time):
		return StatusScheduled
	case now.After(r.DueAt.Time):
		return StatusOverdue
	default:
		return StatusOpen
	}
}

// view is an assignment as teachers see it, with its group's summary.
func (s *Service) view(ctx context.Context, r assignmentsdb.Assignment, f facts) (Assignment, error) {
	a := Assignment{
		ID: uuidString(r.ID), GroupID: r.GroupID, GroupName: f.groupNames[r.GroupID], TeacherID: r.TeacherID, Kind: r.Kind,
		Status: status(r, s.now()), Title: r.Title, Instructions: r.Instructions, Subject: r.Subject,
		Skills: r.Skills, Questions: len(r.QuestionIds), OpensAt: r.OpensAt.Time.UTC(), DueAt: r.DueAt.Time.UTC(),
		CreatedAt: r.CreatedAt.Time.UTC(), UpdatedAt: r.UpdatedAt.Time.UTC(), Summary: summarize(r, f), row: r,
	}
	if a.Skills == nil {
		a.Skills = []string{}
	}
	if r.ClosedAt.Valid {
		t := r.ClosedAt.Time.UTC()
		a.ClosedAt = &t
	}
	if r.HomeworkIndex.Valid {
		i := int(r.HomeworkIndex.Int16)
		a.HomeworkIndex = &i
	}
	if len(r.Mix) > 0 {
		a.Mix = new(roadmap.Mix)
		if err := json.Unmarshal(r.Mix, a.Mix); err != nil {
			return a, fmt.Errorf("assignment %s mix: %w", a.ID, err)
		}
	}
	lesson, err := s.lessonOf(ctx, r)
	if err != nil {
		return a, err
	}
	a.Lesson = lesson
	if r.TestCode.Valid {
		a.Test = testOf(r, f.packages[r.TestCode.String])
		a.Questions = 0
		for _, sec := range r.Sections {
			if sec == "english" {
				a.Questions += a.Test.English
			} else {
				a.Questions += a.Test.Math
			}
		}
	}
	return a, nil
}

// lessonOf is the curriculum lesson an assignment comes from (null for
// custom homework and exams). A lesson gone from the curriculum keeps its
// id as its title.
func (s *Service) lessonOf(ctx context.Context, r assignmentsdb.Assignment) (*Lesson, error) {
	if !r.Course.Valid || !r.LessonID.Valid {
		return nil, nil
	}
	l := &Lesson{Course: r.Course.String, LessonID: r.LessonID.String, Title: r.LessonID.String}
	c, err := s.roadmap.Course(ctx, r.Course.String)
	switch {
	case errors.Is(err, roadmap.ErrNotFound):
		return l, nil
	case err != nil:
		return nil, err
	}
	if u, lesson, ok := c.Lesson(r.LessonID.String); ok {
		l.Title, l.UnitID, l.UnitTitle = lesson.Title, u.ID, u.Title
	}
	return l, nil
}

// testOf is an exam's test as of its latest version (nil: not published).
func testOf(r assignmentsdb.Assignment, pkg *practice.Package) *Test {
	t := &Test{Code: r.TestCode.String, Title: r.TestCode.String, Sections: r.Sections}
	if pkg == nil {
		return t
	}
	t.Title = pkg.Title
	for _, sec := range pkg.Sections {
		n := 0
		for _, m := range sec.Modules {
			n += len(m.Questions)
		}
		switch sec.Subject {
		case "english":
			t.English = n
		case "math":
			t.Math = n
		}
	}
	return t
}

// access reads an assignment for one of its group's teachers.
func (s *Service) access(ctx context.Context, teacher int32, id string, day time.Time) (assignmentsdb.Assignment, error) {
	u, ok := parseUUID(id)
	if !ok {
		return assignmentsdb.Assignment{}, ErrNotFound
	}
	q := assignmentsdb.New(s.pool)
	r, err := q.GetAssignment(ctx, u)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if err := s.teaches(ctx, teacher, r.GroupID, day); err != nil {
		return r, err
	}
	return r, nil
}

// teaches is nil when the teacher teaches the group now or later.
func (s *Service) teaches(ctx context.Context, teacher, group int32, day time.Time) error {
	ok, err := assignmentsdb.New(s.pool).TeachesGroup(ctx, assignmentsdb.TeachesGroupParams{GroupID: group, TeacherID: teacher, Today: date(day)})
	if err != nil {
		return err
	}
	if !ok {
		return ErrForbidden
	}
	return nil
}

// List lists the assignments of the teacher's groups (or of one of them),
// newest first.
func (s *Service) List(ctx context.Context, teacher int32, group *int32, day time.Time) ([]Assignment, error) {
	q := assignmentsdb.New(s.pool)
	var groups []int32
	if group != nil {
		if err := s.teaches(ctx, teacher, *group, day); err != nil {
			return nil, err
		}
		groups = []int32{*group}
	} else {
		var err error
		if groups, err = q.TeacherGroupIDs(ctx, assignmentsdb.TeacherGroupIDsParams{TeacherID: teacher, Today: date(day)}); err != nil {
			return nil, err
		}
	}
	out := []Assignment{}
	if len(groups) == 0 {
		return out, nil
	}
	rows, err := q.GroupAssignments(ctx, groups)
	if err != nil {
		return nil, err
	}
	f, err := s.gather(ctx, rows, day, nil)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		a, err := s.view(ctx, r, f)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// Get returns an assignment with its questions, for its group's teachers.
func (s *Service) Get(ctx context.Context, teacher int32, id string, day time.Time) (Detail, error) {
	r, err := s.access(ctx, teacher, id, day)
	if err != nil {
		return Detail{}, err
	}
	return s.detail(ctx, r, day)
}

func (s *Service) detail(ctx context.Context, r assignmentsdb.Assignment, day time.Time) (Detail, error) {
	f, err := s.gather(ctx, []assignmentsdb.Assignment{r}, day, nil)
	if err != nil {
		return Detail{}, err
	}
	a, err := s.view(ctx, r, f)
	if err != nil {
		return Detail{}, err
	}
	items, images, err := s.questions(ctx, idStrings(r.QuestionIds))
	if err != nil {
		return Detail{}, err
	}
	return Detail{Assignment: a, Items: items, Images: images}, nil
}

// questions are questions whole, in order, with links to their figures.
// Questions gone from the bank are left out.
func (s *Service) questions(ctx context.Context, ids []string) ([]Question, map[string]practice.ImageLink, error) {
	items := []Question{}
	if len(ids) == 0 {
		return items, map[string]practice.ImageLink{}, nil
	}
	bank, err := s.practice.BankQuestions(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	var keys []string
	for _, id := range ids {
		pq, ok := bank[id]
		if !ok {
			continue
		}
		figures := pq.Figures
		if figures == nil {
			figures = []practice.Figure{}
		}
		items = append(items, Question{
			ID: id, Position: len(items) + 1, Type: pq.Type, Passage: pq.Passage, Stem: pq.Stem, Choices: pq.Choices,
			Figures: figures, Correct: pq.Key, Accepted: pq.Accepted, Explanation: pq.Explanation, Domain: pq.Domain,
			Skill: pq.Skill, Difficulty: pq.Difficulty,
		})
		for _, fig := range pq.Figures {
			keys = append(keys, fig.Key)
		}
	}
	slices.Sort(keys)
	images, err := s.practice.FigureLinks(ctx, slices.Compact(keys))
	if err != nil {
		return nil, nil, err
	}
	return items, images, nil
}

func date(t time.Time) pgtype.Date { return pgtype.Date{Time: t, Valid: true} }

func timestamptz(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }

func textPtr(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

func idStrings(ids []pgtype.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = uuidString(id)
	}
	return out
}

func uuidString(u pgtype.UUID) string {
	b := u.Bytes
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func parseUUID(s string) (pgtype.UUID, bool) {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		return u, false
	}
	return u, u.Valid
}
