package roadmap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nazacd/makonbook-api/internal/practice"
	"github.com/nazacd/makonbook-api/internal/roadmap/roadmapdb"
)

// Practice sets (spec 22.8): a student builds a set from the question bank
// (topics, difficulty, how many, time a question) and takes it like a level
// check, graded on submit. One unfinished set at a time.

// ErrPracticeOpen means the student has an unfinished set; it is returned
// with the error, to continue or discard.
var ErrPracticeOpen = errors.New("finish or discard the practice set you started first")

// Practice modes: a rush is timed and graded when handed in; at one's own
// pace, each question is checked as it is answered, with its explanation.
const (
	ModeRush    = "rush"
	ModeOwnPace = "own_pace"
)

const (
	// MaxPracticeQuestions is the most a rush may have.
	MaxPracticeQuestions = 50
	// MaxOwnPaceQuestions is the most a set taken at one's own pace may have.
	MaxOwnPaceQuestions = 200
)

// PracticeError is a practice request that can't be met as sent.
type PracticeError struct{ Reason string }

func (e *PracticeError) Error() string { return e.Reason }

// PracticeRequest is what a student asks for. QuestionIDs, when given (a
// question from the bank, or a few), is the set as it is; otherwise the
// questions are drawn from Skills (none: every skill of the subject) at
// Difficulty (easy, medium, hard, or mixed).
type PracticeRequest struct {
	Subject            string   `json:"subject"`
	Skills             []string `json:"skills"`
	Difficulty         string   `json:"difficulty"`
	Count              int      `json:"count"`
	SecondsPerQuestion int      `json:"seconds_per_question"` // 0: untimed
	QuestionIDs        []string `json:"question_ids"`
	// Mode is rush (the default) or own_pace.
	Mode string `json:"mode"`
	// Unsolved leaves out the questions the student has answered right before.
	Unsolved bool `json:"unsolved"`
	// Shuffle mixes the order; otherwise easy to hard.
	Shuffle bool `json:"shuffle"`
}

// PracticeInfo is what a set was built from, kept with it.
type PracticeInfo struct {
	Title              string   `json:"title"`
	Subject            string   `json:"subject"`
	Skills             []string `json:"skills"`
	Difficulty         string   `json:"difficulty"`
	SecondsPerQuestion int      `json:"seconds_per_question"`
	Mode               string   `json:"mode"`
}

// PracticeSummary is one of a student's sets, for their history.
type PracticeSummary struct {
	ID         string       `json:"id"`
	Practice   PracticeInfo `json:"practice"`
	Questions  int          `json:"questions"`
	CreatedAt  time.Time    `json:"created_at"`
	FinishedAt *time.Time   `json:"finished_at"`
	Correct    *int         `json:"correct"`
	Total      *int         `json:"total"`
	Percent    *int         `json:"percent"`
}

func (r *PracticeRequest) check() error {
	r.Difficulty = strings.ToLower(strings.TrimSpace(r.Difficulty))
	switch {
	case !slices.Contains(Subjects, r.Subject):
		return &PracticeError{"subject is math or english"}
	case r.Difficulty == "":
		r.Difficulty = "mixed"
	case r.Difficulty != "mixed" && !slices.Contains(Difficulties, r.Difficulty):
		return &PracticeError{"difficulty is easy, medium, hard or mixed"}
	}
	switch r.Mode {
	case "":
		r.Mode = ModeRush
	case ModeRush, ModeOwnPace:
	default:
		return &PracticeError{"mode is rush or own_pace"}
	}
	most := MaxPracticeQuestions
	if r.Mode == ModeOwnPace {
		most = MaxOwnPaceQuestions
		r.SecondsPerQuestion = 0 // at one's own pace: no clock
	}
	switch {
	case len(r.QuestionIDs) > most:
		return &PracticeError{fmt.Sprintf("at most %d questions", most)}
	case len(r.QuestionIDs) == 0 && (r.Count < 1 || r.Count > most):
		return &PracticeError{fmt.Sprintf("count is 1 to %d", most)}
	case r.SecondsPerQuestion != 0 && (r.SecondsPerQuestion < 15 || r.SecondsPerQuestion > 600):
		return &PracticeError{"seconds_per_question is 0 (untimed) or 15 to 600"}
	case len(r.Skills) > 30:
		return &PracticeError{"at most 30 skills"}
	}
	return nil
}

// practiceTitle names a set: its skill, or how many topics, or the subject.
func practiceTitle(r PracticeRequest, n int) string {
	subject := map[string]string{"math": "Math", "english": "Reading and Writing"}[r.Subject]
	switch {
	case len(r.QuestionIDs) == 1:
		return subject + " · one question"
	case len(r.Skills) == 1:
		return r.Skills[0]
	case len(r.Skills) > 1:
		return fmt.Sprintf("%s · %d topics", subject, len(r.Skills))
	}
	return fmt.Sprintf("%s · %d questions", subject, n)
}

// StartPractice makes a practice set. An unfinished set comes back with
// ErrPracticeOpen.
func (s *Service) StartPractice(ctx context.Context, student int32, r PracticeRequest) (Assessment, error) {
	if err := r.check(); err != nil {
		return Assessment{}, err
	}
	q := roadmapdb.New(s.pool)
	p, err := s.progress(ctx, student)
	if err != nil {
		return Assessment{}, err
	}
	open, err := q.OpenPractice(ctx, student)
	switch {
	case err == nil:
		a, err := s.viewRow(ctx, open, p)
		if err != nil {
			return Assessment{}, err
		}
		return a, ErrPracticeOpen
	case !errors.Is(err, pgx.ErrNoRows):
		return Assessment{}, err
	}

	var ids []string
	if len(r.QuestionIDs) > 0 {
		found, err := s.bank.BankQuestions(ctx, r.QuestionIDs)
		if err != nil {
			return Assessment{}, err
		}
		for _, id := range r.QuestionIDs {
			if _, ok := found[id]; ok && !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			return Assessment{}, ErrNoQuestions
		}
	} else {
		seenIDs, err := q.SeenQuestions(ctx, student)
		if err != nil {
			return Assessment{}, err
		}
		seen := make(map[string]bool, len(seenIDs))
		for _, id := range seenIDs {
			seen[uuidString(id)] = true
		}
		var leaveOut map[string]bool
		if r.Unsolved {
			solved, err := q.SolvedQuestions(ctx, student)
			if err != nil {
				return Assessment{}, err
			}
			leaveOut = make(map[string]bool, len(solved))
			for _, id := range solved {
				leaveOut[uuidString(id)] = true
			}
		}
		want := map[string]int{r.Difficulty: r.Count}
		if r.Difficulty == "mixed" {
			// About a third each, the rest medium.
			third := r.Count / 3
			want = map[string]int{"easy": third, "hard": third, "medium": r.Count - 2*third}
		}
		skills := r.Skills
		if len(skills) == 0 { // every skill of the subject
			for _, names := range practice.Taxonomy[r.Subject] {
				skills = append(skills, names...)
			}
		}
		only := ""
		if r.Difficulty != "mixed" {
			only = r.Difficulty // a chosen difficulty is kept to, never padded with others
		}
		if ids, err = s.drawLeavingOut(ctx, r.Subject, skills, want, seen, leaveOut, only); err != nil {
			return Assessment{}, err
		}
		if r.Shuffle {
			rand.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] }) //nolint:gosec // not a secret: the order
		}
	}
	info := PracticeInfo{Title: practiceTitle(r, len(ids)), Subject: r.Subject, Skills: r.Skills, Difficulty: r.Difficulty, SecondsPerQuestion: r.SecondsPerQuestion, Mode: r.Mode}
	if info.Skills == nil {
		info.Skills = []string{}
	}
	body, err := json.Marshal(info)
	if err != nil {
		return Assessment{}, err
	}
	row, err := q.InsertPractice(ctx, roadmapdb.InsertPracticeParams{Student: student, QuestionIds: uuids(ids), Practice: body})
	if errors.Is(err, pgx.ErrNoRows) { // another started at the same moment
		if open, err := q.OpenPractice(ctx, student); err == nil {
			a, err := s.viewRow(ctx, open, p)
			if err != nil {
				return Assessment{}, err
			}
			return a, ErrPracticeOpen
		}
	}
	if err != nil {
		return Assessment{}, err
	}
	return s.viewRow(ctx, row, p)
}

// drawLeavingOut is Draw without the questions in leaveOut (nil: none) and,
// when only is set, with that difficulty alone.
func (s *Service) drawLeavingOut(ctx context.Context, subject string, skills []string, want map[string]int, seen, leaveOut map[string]bool, only string) ([]string, error) {
	cands, err := s.bank.Candidates(ctx, subject, resolveSkills(subject, skills))
	if err != nil {
		return nil, err
	}
	kept := cands[:0]
	for _, c := range cands {
		if !leaveOut[c.ID] && (only == "" || c.Difficulty == only) {
			kept = append(kept, c)
		}
	}
	cands = kept
	ids := choose(cands, seen, want, rand.Shuffle) //nolint:gosec // not a secret: which questions come
	if len(ids) == 0 {
		return nil, ErrNoQuestions
	}
	return ids, nil
}

// DiscardPractice deletes a student's unfinished set. ErrNotFound otherwise.
func (s *Service) DiscardPractice(ctx context.Context, student int32, id string) error {
	u, ok := parseUUID(id)
	if !ok {
		return ErrNotFound
	}
	n, err := roadmapdb.New(s.pool).DiscardPractice(ctx, roadmapdb.DiscardPracticeParams{ID: u, Student: student})
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

// PracticeSets are a student's latest sets, newest first.
func (s *Service) PracticeSets(ctx context.Context, student int32, limit int32) ([]PracticeSummary, error) {
	rows, err := roadmapdb.New(s.pool).PracticeSets(ctx, roadmapdb.PracticeSetsParams{Student: student, Lim: limit})
	if err != nil {
		return nil, err
	}
	out := make([]PracticeSummary, 0, len(rows))
	for _, r := range rows {
		ps := PracticeSummary{ID: uuidString(r.ID), Questions: int(r.Questions), CreatedAt: r.CreatedAt.Time.UTC()}
		if err := json.Unmarshal(r.Practice, &ps.Practice); err != nil {
			return nil, fmt.Errorf("stored practice: %w", err)
		}
		if r.FinishedAt.Valid {
			t := r.FinishedAt.Time.UTC()
			c, tot, pc := int(r.Correct.Int16), int(r.Total.Int16), int(r.Percent.Int16)
			ps.FinishedAt, ps.Correct, ps.Total, ps.Percent = &t, &c, &tot, &pc
		}
		out = append(out, ps)
	}
	return out, nil
}

// FinishedQuestions are the questions a student has submitted in any
// assessment (the bank marks them done).
func (s *Service) FinishedQuestions(ctx context.Context, student int32) (map[string]bool, error) {
	ids, err := roadmapdb.New(s.pool).FinishedQuestions(ctx, student)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[uuidString(id)] = true
	}
	return out, nil
}
