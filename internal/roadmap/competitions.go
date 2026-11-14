package roadmap

import (
	"context"
	"errors"

	"math/rand/v2"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nazacd/makonbook-api/internal/practice"
	"github.com/nazacd/makonbook-api/internal/roadmap/roadmapdb"
)

// Competitions (spec 26): the same questions for everyone, once, in a
// window and against a time limit; a board of right answers, then time.
// Strict: one attempt, the server keeps the clock (answers after the time
// are refused), and keys, explanations and per-question results open only
// when the competition ends.

var (
	// ErrNotLive means the competition hasn't started or has ended.
	ErrNotLive = errors.New("this competition isn't open now")
	// ErrAttempted means the student has taken the competition already.
	ErrAttempted = errors.New("you have taken this competition already")
	// ErrTimeUp means the attempt's time is over.
	ErrTimeUp = errors.New("time is up for this attempt")
	// ErrCompetitionLocked means it can't change any more (started or taken).
	ErrCompetitionLocked = errors.New("this competition has started or been taken: it can't change now")
)

// CompetitionError is a competition that can't be saved or published as sent.
type CompetitionError struct{ Reason string }

func (e *CompetitionError) Error() string { return e.Reason }

// Grace is how long after the time an answer still counts (a slow network).
const Grace = 30 * time.Second

// Competition is a competition as students and editors see it.
type Competition struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	Description  string     `json:"description"`
	Prize        string     `json:"prize"`
	Subject      string     `json:"subject"` // math, english or mixed
	Source       string     `json:"source"`  // bank or reserved
	StartsAt     time.Time  `json:"starts_at"`
	EndsAt       time.Time  `json:"ends_at"`
	Minutes      int        `json:"minutes"`
	Questions    int        `json:"questions"`
	Published    bool       `json:"published"`
	Status       string     `json:"status"` // draft, upcoming, live, ended
	Participants int        `json:"participants"`
	Mine         *MyAttempt `json:"mine"` // students: their attempt, null before they start
	QuestionIDs  []string   `json:"question_ids,omitempty"`
}

// MyAttempt is a student's attempt at a competition.
type MyAttempt struct {
	AssessmentID string     `json:"assessment_id"`
	Finished     bool       `json:"finished"`
	Correct      *int       `json:"correct"`
	Total        *int       `json:"total"`
	Deadline     time.Time  `json:"deadline"`
	FinishedAt   *time.Time `json:"finished_at"`
}

// CompetitionBrief is the competition an attempt belongs to (in the
// assessment): when the attempt's time and the competition end, the server's
// clock now, and whether answers are open yet.
type CompetitionBrief struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	EndsAt     time.Time `json:"ends_at"`
	Deadline   time.Time `json:"deadline"`
	ServerTime time.Time `json:"server_time"`
	Ended      bool      `json:"ended"`
}

// CompetitionInput is a competition as an editor saves it.
type CompetitionInput struct {
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Prize       string    `json:"prize"`
	Subject     string    `json:"subject"`
	Source      string    `json:"source"`
	QuestionIDs []string  `json:"question_ids"`
	StartsAt    time.Time `json:"starts_at"`
	EndsAt      time.Time `json:"ends_at"`
	Minutes     int       `json:"minutes"`
}

func statusOf(c roadmapdb.Competition, now time.Time) string {
	switch {
	case !c.PublishedAt.Valid:
		return "draft"
	case now.Before(c.StartsAt.Time):
		return "upcoming"
	case now.Before(c.EndsAt.Time):
		return "live"
	}
	return "ended"
}

func deadlineOf(c roadmapdb.Competition, started time.Time) time.Time {
	d := started.Add(time.Duration(c.Minutes) * time.Minute)
	if c.EndsAt.Time.Before(d) {
		return c.EndsAt.Time
	}
	return d
}

func competitionOut(c roadmapdb.Competition, participants int, now time.Time, editor bool) Competition {
	out := Competition{
		ID: uuidString(c.ID), Title: c.Title, Description: c.Description, Prize: c.Prize, Subject: c.Subject, Source: c.Source,
		StartsAt: c.StartsAt.Time.UTC(), EndsAt: c.EndsAt.Time.UTC(), Minutes: int(c.Minutes), Questions: len(c.QuestionIds),
		Published: c.PublishedAt.Valid, Status: statusOf(c, now), Participants: participants,
	}
	if editor {
		out.QuestionIDs = idStrings(c.QuestionIds)
	}
	return out
}

func (in *CompetitionInput) check() error {
	in.Title, in.Description, in.Prize = strings.TrimSpace(in.Title), strings.TrimSpace(in.Description), strings.TrimSpace(in.Prize)
	switch {
	case in.Title == "" || utf8.RuneCountInString(in.Title) > 120:
		return &CompetitionError{"the title is needed, at most 120 characters"}
	case utf8.RuneCountInString(in.Description) > 2000:
		return &CompetitionError{"the description is at most 2000 characters"}
	case utf8.RuneCountInString(in.Prize) > 300:
		return &CompetitionError{"the prize is at most 300 characters"}
	case in.Subject != "math" && in.Subject != "english" && in.Subject != "mixed":
		return &CompetitionError{"subject is math, english or mixed"}
	case in.Source != "bank" && in.Source != "reserved":
		return &CompetitionError{"source is bank or reserved"}
	case in.StartsAt.IsZero() || in.EndsAt.IsZero() || !in.EndsAt.After(in.StartsAt):
		return &CompetitionError{"starts_at and ends_at are needed, the end after the start"}
	case in.Minutes < 1 || in.Minutes > 300:
		return &CompetitionError{"minutes is 1 to 300"}
	case len(in.QuestionIDs) > 100:
		return &CompetitionError{"at most 100 questions"}
	}
	seen := map[string]bool{}
	for _, id := range in.QuestionIDs {
		if _, ok := parseUUID(id); !ok || seen[id] {
			return &CompetitionError{"question_ids must be distinct question ids"}
		}
		seen[id] = true
	}
	return nil
}

// Competitions lists them, newest first: published ones for students and
// teachers (with the student's attempt), every one for editors.
func (s *Service) Competitions(ctx context.Context, student int32, editor bool, now time.Time) ([]Competition, error) {
	q := roadmapdb.New(s.pool)
	rows, err := q.ListCompetitions(ctx, editor)
	if err != nil {
		return nil, err
	}
	out := make([]Competition, 0, len(rows))
	ids := make([]pgtype.UUID, 0, len(rows))
	for _, r := range rows {
		c := roadmapdb.Competition{ID: r.ID, Title: r.Title, Description: r.Description, Prize: r.Prize, Subject: r.Subject, Source: r.Source,
			QuestionIds: r.QuestionIds, StartsAt: r.StartsAt, EndsAt: r.EndsAt, Minutes: r.Minutes, PublishedAt: r.PublishedAt}
		out = append(out, competitionOut(c, int(r.Participants), now, editor))
		ids = append(ids, r.ID)
	}
	if student != 0 && len(ids) > 0 {
		attempts, err := q.AttemptsIn(ctx, roadmapdb.AttemptsInParams{Student: student, Competitions: ids})
		if err != nil {
			return nil, err
		}
		for i, r := range rows {
			for _, a := range attempts {
				if a.CompetitionID == r.ID {
					c := roadmapdb.Competition{Minutes: r.Minutes, EndsAt: r.EndsAt}
					out[i].Mine = attemptOut(a, c)
				}
			}
		}
	}
	return out, nil
}

func attemptOut(a roadmapdb.RoadmapAssessment, c roadmapdb.Competition) *MyAttempt {
	m := &MyAttempt{AssessmentID: uuidString(a.ID), Finished: a.FinishedAt.Valid, Deadline: deadlineOf(c, a.CreatedAt.Time).UTC()}
	if a.FinishedAt.Valid {
		c, t := int(a.Correct.Int16), int(a.Total.Int16)
		f := a.FinishedAt.Time.UTC()
		m.Correct, m.Total, m.FinishedAt = &c, &t, &f
	}
	return m
}

// CompetitionByID is one competition; a student sees published ones only.
func (s *Service) CompetitionByID(ctx context.Context, id string, student int32, editor bool, now time.Time) (Competition, error) {
	c, err := s.competition(ctx, id)
	if err != nil {
		return Competition{}, err
	}
	if !editor && !c.PublishedAt.Valid {
		return Competition{}, ErrNotFound
	}
	q := roadmapdb.New(s.pool)
	n, err := q.CompetitionAttempts(ctx, c.ID)
	if err != nil {
		return Competition{}, err
	}
	out := competitionOut(c, int(n), now, editor)
	if student != 0 {
		a, err := q.CompetitionAttempt(ctx, roadmapdb.CompetitionAttemptParams{Student: student, CompetitionID: c.ID})
		switch {
		case err == nil:
			out.Mine = attemptOut(a, c)
		case !errors.Is(err, pgx.ErrNoRows):
			return Competition{}, err
		}
	}
	return out, nil
}

func (s *Service) competition(ctx context.Context, id string) (roadmapdb.Competition, error) {
	u, ok := parseUUID(id)
	if !ok {
		return roadmapdb.Competition{}, ErrNotFound
	}
	c, err := roadmapdb.New(s.pool).GetCompetition(ctx, u)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// StartCompetition gives a student their attempt: the open one, or a new
// one while the competition is live. A finished attempt is ErrAttempted.
func (s *Service) StartCompetition(ctx context.Context, student int32, id string, now time.Time) (Assessment, error) {
	c, err := s.competition(ctx, id)
	if err != nil {
		return Assessment{}, err
	}
	if !c.PublishedAt.Valid {
		return Assessment{}, ErrNotFound
	}
	q := roadmapdb.New(s.pool)
	row, err := q.CompetitionAttempt(ctx, roadmapdb.CompetitionAttemptParams{Student: student, CompetitionID: c.ID})
	switch {
	case err == nil:
		if row.FinishedAt.Valid {
			return Assessment{}, ErrAttempted
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return Assessment{}, err
	default:
		if statusOf(c, now) != "live" {
			return Assessment{}, ErrNotLive
		}
		row, err = q.InsertCompetitionAttempt(ctx, roadmapdb.InsertCompetitionAttemptParams{Student: student, QuestionIds: c.QuestionIds, CompetitionID: c.ID})
		if errors.Is(err, pgx.ErrNoRows) { // started at the same moment elsewhere
			row, err = q.CompetitionAttempt(ctx, roadmapdb.CompetitionAttemptParams{Student: student, CompetitionID: c.ID})
		}
		if err != nil {
			return Assessment{}, err
		}
	}
	p, err := s.progress(ctx, student)
	if err != nil {
		return Assessment{}, err
	}
	return s.viewRow(ctx, row, p)
}

// timeIsUp says whether a competition attempt's time (with Grace) is over now.
func timeIsUp(ctx context.Context, q *roadmapdb.Queries, row roadmapdb.RoadmapAssessment, now time.Time) (bool, error) {
	c, err := q.GetCompetition(ctx, row.CompetitionID)
	if err != nil {
		return false, err
	}
	return now.After(deadlineOf(c, row.CreatedAt.Time).Add(Grace)), nil
}

// FinishExpired grades the open attempts of a competition whose time is up
// (a closed tab still lands on the board, with what it had saved).
func (s *Service) FinishExpired(ctx context.Context, competition pgtype.UUID) error {
	rows, err := roadmapdb.New(s.pool).ExpiredAttempts(ctx, competition)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if _, err := s.Submit(ctx, Who{ID: r.StudentClientID}, uuidString(r.ID), Answers{}); err != nil && !errors.Is(err, ErrFinished) {
			return err
		}
	}
	return nil
}

// BoardEntry is one place on a competition's board.
type BoardEntry struct {
	Rank     int     `json:"rank"`
	Name     string  `json:"name"`
	PhotoURL *string `json:"photo_url"`
	Correct  int     `json:"correct"`
	Total    int     `json:"total"`
	Seconds  int     `json:"seconds"`
	Me       bool    `json:"me"`
}

// Board is a competition's standings: the first 100 and the viewer's place.
type Board struct {
	Entries      []BoardEntry `json:"entries"`
	Me           *BoardEntry  `json:"me"`
	Participants int          `json:"participants"`
}

// CompetitionBoard ranks the finished attempts (the expired ones are graded
// first). Equal right answers and seconds share a rank.
func (s *Service) CompetitionBoard(ctx context.Context, id string, viewer int32, editor bool) (Board, error) {
	c, err := s.competition(ctx, id)
	if err != nil {
		return Board{}, err
	}
	if !editor && !c.PublishedAt.Valid {
		return Board{}, ErrNotFound
	}
	if err := s.FinishExpired(ctx, c.ID); err != nil {
		return Board{}, err
	}
	rows, err := roadmapdb.New(s.pool).CompetitionBoard(ctx, c.ID)
	if err != nil {
		return Board{}, err
	}
	b := Board{Entries: []BoardEntry{}, Participants: len(rows)}
	rank := 0
	for i, r := range rows {
		if i == 0 || r.Correct != rows[i-1].Correct || r.Seconds != rows[i-1].Seconds {
			rank = i + 1
		}
		e := BoardEntry{Rank: rank, Name: shortName(r.FirstName, r.LastName), Correct: int(r.Correct), Total: int(r.Total), Seconds: max(0, int(r.Seconds)), Me: viewer != 0 && r.StudentClientID == viewer}
		if r.PhotoID.Valid {
			u := "/v1/photos/" + r.PhotoID.String
			e.PhotoURL = &u
		}
		if i < 100 {
			b.Entries = append(b.Entries, e)
		}
		if e.Me {
			me := e
			b.Me = &me
		}
	}
	return b, nil
}

// shortName is "Madina K.".
func shortName(first, last string) string {
	first, last = strings.TrimSpace(first), strings.TrimSpace(last)
	if r := []rune(last); len(r) > 0 {
		return first + " " + strings.ToUpper(string(r[0])) + "."
	}
	return first
}

// Editors --------------------------------------------------------------------

// SaveCompetition makes a competition (id "") or changes one. Once it has
// started or anyone has taken it, only its title, description and prize change.
func (s *Service) SaveCompetition(ctx context.Context, id string, in CompetitionInput, editor string, now time.Time) (Competition, error) {
	if err := in.check(); err != nil {
		return Competition{}, err
	}
	q := roadmapdb.New(s.pool)
	ids := uuids(in.QuestionIDs)
	if id == "" {
		c, err := q.InsertCompetition(ctx, roadmapdb.InsertCompetitionParams{
			Title: in.Title, Description: in.Description, Prize: in.Prize, Subject: in.Subject, Source: in.Source, QuestionIds: ids,
			StartsAt: ts(in.StartsAt), EndsAt: ts(in.EndsAt), Minutes: int32(in.Minutes), CreatedBy: editor, //nolint:gosec // checked: 1–300
		})
		if err != nil {
			return Competition{}, err
		}
		return competitionOut(c, 0, now, true), nil
	}
	c, err := s.competition(ctx, id)
	if err != nil {
		return Competition{}, err
	}
	n, err := q.CompetitionAttempts(ctx, c.ID)
	if err != nil {
		return Competition{}, err
	}
	locked := n > 0 || (c.PublishedAt.Valid && !now.Before(c.StartsAt.Time))
	if locked {
		// Only the words may change.
		in.Subject, in.Source, in.Minutes = c.Subject, c.Source, int(c.Minutes)
		in.StartsAt, in.EndsAt = c.StartsAt.Time, c.EndsAt.Time
		ids = c.QuestionIds
	}
	saved, err := q.UpdateCompetition(ctx, roadmapdb.UpdateCompetitionParams{
		ID: c.ID, Title: in.Title, Description: in.Description, Prize: in.Prize, Subject: in.Subject, Source: in.Source, QuestionIds: ids,
		StartsAt: ts(in.StartsAt), EndsAt: ts(in.EndsAt), Minutes: int32(in.Minutes), //nolint:gosec // checked: 1–300
	})
	if err != nil {
		return Competition{}, err
	}
	return competitionOut(saved, int(n), now, true), nil
}

// PublishCompetition shows a competition to students (or takes it back
// before it starts). Publishing checks its questions: at least one, all
// approved, and for a reserved one in no mock test and held by no other.
func (s *Service) PublishCompetition(ctx context.Context, id string, on bool, now time.Time) error {
	c, err := s.competition(ctx, id)
	if err != nil {
		return err
	}
	q := roadmapdb.New(s.pool)
	if !on {
		if c.PublishedAt.Valid && !now.Before(c.StartsAt.Time) {
			return ErrCompetitionLocked
		}
		return q.SetCompetitionPublished(ctx, roadmapdb.SetCompetitionPublishedParams{ID: c.ID, Publish: false})
	}
	if !now.Before(c.EndsAt.Time) {
		return &CompetitionError{"it has already ended: move the end later first"}
	}
	if len(c.QuestionIds) == 0 {
		return &CompetitionError{"choose its questions first"}
	}
	standing, err := q.QuestionStanding(ctx, roadmapdb.QuestionStandingParams{CompetitionID: c.ID, Ids: c.QuestionIds})
	if err != nil {
		return err
	}
	if len(standing) != len(c.QuestionIds) {
		return &CompetitionError{"a question it names no longer exists"}
	}
	for _, st := range standing {
		switch {
		case st.Status != "approved":
			return &CompetitionError{"every question must be approved first (" + uuidString(st.ID) + " isn't)"}
		case c.Subject != "mixed" && st.Subject != c.Subject:
			return &CompetitionError{"a question is of the other subject (" + uuidString(st.ID) + ")"}
		case st.Held:
			return &CompetitionError{"another competition holds a question (" + uuidString(st.ID) + ")"}
		case c.Source == "reserved" && st.InTest:
			return &CompetitionError{"a reserved question is in a mock test (" + uuidString(st.ID) + "): students may know it"}
		}
	}
	return q.SetCompetitionPublished(ctx, roadmapdb.SetCompetitionPublishedParams{ID: c.ID, Publish: true})
}

// DeleteCompetition removes one nobody has taken.
func (s *Service) DeleteCompetition(ctx context.Context, id string) error {
	c, err := s.competition(ctx, id)
	if err != nil {
		return err
	}
	q := roadmapdb.New(s.pool)
	n, err := q.CompetitionAttempts(ctx, c.ID)
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrCompetitionLocked
	}
	return q.DeleteCompetition(ctx, c.ID)
}

// DrawCompetition picks bank questions for a competition: per difficulty
// the count asked, from skills (none: the whole subject), the same for
// everyone (no one's history counts), easy to hard.
func (s *Service) DrawCompetition(ctx context.Context, subject string, skills []string, want map[string]int) ([]string, error) {
	if !slices.Contains(Subjects, subject) {
		return nil, &CompetitionError{"subject is math or english"}
	}
	total := 0
	for d, n := range want {
		if !slices.Contains(Difficulties, d) || n < 0 {
			return nil, &CompetitionError{"counts are per easy, medium and hard"}
		}
		total += n
	}
	if total < 1 || total > 100 {
		return nil, &CompetitionError{"1 to 100 questions"}
	}
	if len(skills) == 0 {
		for _, names := range practice.Taxonomy[subject] {
			skills = append(skills, names...)
		}
	}
	cands, err := s.bank.Candidates(ctx, subject, resolveSkills(subject, skills))
	if err != nil {
		return nil, err
	}
	ids := choose(cands, map[string]bool{}, want, rand.Shuffle) //nolint:gosec // not a secret: which questions come
	if len(ids) == 0 {
		return nil, ErrNoQuestions
	}
	return ids, nil
}

// Reservable is a question a reserved competition may use.
type Reservable struct {
	practice.QuestionPreview
	Subject string `json:"subject"`
	Status  string `json:"status"`
}

// ReservableQuestions lists questions in no mock test and held by no other
// competition (in review or approved), newest first.
func (s *Service) ReservableQuestions(ctx context.Context, competition, subject, text string) ([]Reservable, error) {
	subjects := Subjects
	if subject == "math" || subject == "english" {
		subjects = []string{subject}
	}
	var cid pgtype.UUID
	if u, ok := parseUUID(competition); ok {
		cid = u
	}
	rows, err := roadmapdb.New(s.pool).ReservableQuestions(ctx, roadmapdb.ReservableQuestionsParams{Subjects: subjects, CompetitionID: cid, Q: strings.TrimSpace(text)})
	if err != nil {
		return nil, err
	}
	out := make([]Reservable, 0, len(rows))
	for _, r := range rows {
		var diff *string
		if r.Difficulty.Valid {
			d := r.Difficulty.String
			diff = &d
		}
		stem := r.Stem
		if r.Passage.Valid && r.Passage.String != "" {
			stem = r.Passage.String + " " + stem
		}
		if len([]rune(stem)) > 160 {
			stem = string([]rune(stem)[:160]) + "…"
		}
		out = append(out, Reservable{QuestionPreview: practice.QuestionPreview{ID: uuidString(r.ID), Type: r.Type, Domain: r.Domain, Skill: r.Skill, Difficulty: diff, Preview: stem}, Subject: r.Subject, Status: r.Status})
	}
	return out, nil
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

// QuestionCards are previews of the questions by id, in the order given,
// with their subject and status (an editor's view of a competition).
func (s *Service) QuestionCards(ctx context.Context, ids []string) ([]Reservable, error) {
	rows, err := roadmapdb.New(s.pool).QuestionCards(ctx, uuids(ids))
	if err != nil {
		return nil, err
	}
	byID := map[string]Reservable{}
	for _, r := range rows {
		var diff *string
		if r.Difficulty.Valid {
			d := r.Difficulty.String
			diff = &d
		}
		stem := r.Stem
		if r.Passage.Valid && r.Passage.String != "" {
			stem = r.Passage.String + " " + stem
		}
		if len([]rune(stem)) > 160 {
			stem = string([]rune(stem)[:160]) + "…"
		}
		byID[uuidString(r.ID)] = Reservable{QuestionPreview: practice.QuestionPreview{ID: uuidString(r.ID), Type: r.Type, Domain: r.Domain, Skill: r.Skill, Difficulty: diff, Preview: stem}, Subject: r.Subject, Status: r.Status}
	}
	out := make([]Reservable, 0, len(ids))
	for _, id := range ids {
		if c, ok := byID[id]; ok {
			out = append(out, c)
		}
	}
	return out, nil
}
