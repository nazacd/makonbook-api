package roadmap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/practice"
	"github.com/nazacd/makonbook-api/internal/roadmap/roadmapdb"
)

// Onboarding (spec 22.3): a registered student says when they graduate and
// sit the SAT, where they are and where they want to be, what to work on and
// how much time they have. It sets their goals and gives them roadmaps.

const dateLayout = "2006-01-02"

// Graduations a student can choose: these years, or "graduated".
var Graduations = []string{"2027", "2028", "2029", "2030", "2031", "2032", "2033", "2034", "graduated"}

// Weekdays, in order.
var Weekdays = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// Onboarding is a student's answers.
type Onboarding struct {
	Graduation string   `json:"graduation"`
	SATDates   []string `json:"sat_dates"` // YYYY-MM-DD; empty: not decided
	Current    *Scores  `json:"current"`   // null: no score yet
	Target     Scores   `json:"target"`
	Focus      string   `json:"focus"` // "math", "english" or "both"
	Schedule   Schedule `json:"schedule"`
	// UniversityID is the dream university (spec 24.3); left out or null
	// keeps the one already chosen.
	UniversityID *string `json:"university_id,omitempty"`
	// UpdatedAt is when they were last sent (only in answers read back).
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// Scores are SAT scores: a total, or the two sections.
type Scores struct {
	Total *int `json:"total,omitempty"`
	Math  *int `json:"math,omitempty"`
	RW    *int `json:"rw,omitempty"`
}

// Schedule is when the student can study.
type Schedule struct {
	Days    []string `json:"days"`
	Minutes int      `json:"minutes"`
}

// OnboardingError is answers that can't be stored.
type OnboardingError struct{ Reason string }

func (e *OnboardingError) Error() string { return e.Reason }

func section(name string, v *int) error {
	if v == nil || *v < 200 || *v > 800 || *v%10 != 0 {
		return &OnboardingError{name + " must be 200–800, a multiple of 10"}
	}
	return nil
}

func total(name string, v *int) error {
	if v == nil || *v < 400 || *v > 1600 || *v%10 != 0 {
		return &OnboardingError{name + " must be 400–1600, a multiple of 10"}
	}
	return nil
}

// check validates the answers and puts them in order (dates sorted, days
// in week order).
func (o *Onboarding) check() error {
	if !slices.Contains(Graduations, o.Graduation) {
		return &OnboardingError{"graduation must be a year from 2027 to 2034, or graduated"}
	}
	dates := []string{}
	for _, d := range o.SATDates {
		if _, err := time.Parse(dateLayout, d); err != nil {
			return &OnboardingError{"sat_dates must be dates (YYYY-MM-DD)"}
		}
		if !slices.Contains(dates, d) {
			dates = append(dates, d)
		}
	}
	if len(dates) > 12 {
		return &OnboardingError{"sat_dates can have at most 12 dates"}
	}
	slices.Sort(dates)
	o.SATDates = dates
	if c := o.Current; c != nil {
		switch {
		case c.Total != nil && c.Math == nil && c.RW == nil:
			if err := total("current.total", c.Total); err != nil {
				return err
			}
		case c.Total == nil && c.Math != nil && c.RW != nil:
			if err := section("current.math", c.Math); err != nil {
				return err
			}
			if err := section("current.rw", c.RW); err != nil {
				return err
			}
		default:
			return &OnboardingError{"current must be null, {total} or {math, rw}"}
		}
	}
	t := o.Target
	if err := section("target.math", t.Math); err != nil {
		return err
	}
	if err := section("target.rw", t.RW); err != nil {
		return err
	}
	if err := total("target.total", t.Total); err != nil {
		return err
	}
	if *t.Total != *t.Math+*t.RW {
		return &OnboardingError{"target.total must be target.math + target.rw"}
	}
	if !slices.Contains([]string{"math", "english", "both"}, o.Focus) {
		return &OnboardingError{"focus must be math, english or both"}
	}
	days := []string{}
	for _, d := range Weekdays {
		if slices.Contains(o.Schedule.Days, d) {
			days = append(days, d)
		}
	}
	for _, d := range o.Schedule.Days {
		if !slices.Contains(Weekdays, d) {
			return &OnboardingError{"schedule.days must be weekdays: mon, tue, wed, thu, fri, sat, sun"}
		}
	}
	if len(days) == 0 {
		return &OnboardingError{"schedule.days needs at least one day"}
	}
	o.Schedule.Days = days
	if !slices.Contains([]int{15, 30, 45, 60}, o.Schedule.Minutes) {
		return &OnboardingError{"schedule.minutes must be 15, 30, 45 or 60"}
	}
	o.UpdatedAt = nil
	return nil
}

// LevelFor is the course level for a section score: below 450 Foundation,
// 450–599 Pre-SAT, 600 and up Advanced; no score, Foundation.
func LevelFor(score *int) string {
	switch {
	case score == nil || *score < 450:
		return "foundation"
	case *score < 600:
		return "pre-sat"
	default:
		return "advanced"
	}
}

// courses are the roadmaps the answers give: one per subject in focus, at
// the level of that section's current score (a total counts half for each).
func (o *Onboarding) courses() []string {
	var math, rw *int
	if c := o.Current; c != nil {
		if c.Total != nil {
			half := *c.Total / 2
			math, rw = &half, &half
		} else {
			math, rw = c.Math, c.RW
		}
	}
	var out []string
	if o.Focus == "math" || o.Focus == "both" {
		out = append(out, "math-"+LevelFor(math))
	}
	if o.Focus == "english" || o.Focus == "both" {
		out = append(out, "english-"+LevelFor(rw))
	}
	return out
}

// examDate is the first SAT date from today on, if any.
func (o *Onboarding) examDate(today time.Time) *string {
	for _, d := range o.SATDates { // sorted
		if t, _ := time.Parse(dateLayout, d); !t.Before(today) {
			return &d
		}
	}
	return nil
}

// SaveOnboarding stores a registered student's answers, sets their goals
// (targets, and the exam date: the first SAT date to come, or none) and
// replaces the roadmaps onboarding gave them; their progress stays.
// ErrNotNeeded for anyone else.
func (s *Service) SaveOnboarding(ctx context.Context, who Who, o Onboarding, today time.Time) error {
	if !who.Registered {
		return ErrNotNeeded
	}
	if err := o.check(); err != nil {
		return err
	}
	_, err := s.bank.SetGoals(ctx, who.ID, practice.GoalsInput{
		TargetMath: o.Target.Math, TargetRW: o.Target.RW, ExamDate: o.examDate(today),
		UniversityID: practice.Optional{Set: o.UniversityID != nil, Value: o.UniversityID},
	}, today)
	var bad *practice.GoalsError
	if errors.As(err, &bad) {
		return &OnboardingError{Reason: bad.Reason}
	}
	if err != nil {
		return fmt.Errorf("goals: %w", err)
	}
	body, err := json.Marshal(o)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(q *roadmapdb.Queries) error {
		if err := q.SaveOnboarding(ctx, roadmapdb.SaveOnboardingParams{Student: who.ID, Answers: body}); err != nil {
			return err
		}
		if err := q.DeleteChoices(ctx, who.ID); err != nil {
			return err
		}
		for _, id := range o.courses() {
			if err := q.InsertChoice(ctx, roadmapdb.InsertChoiceParams{Student: who.ID, RoadmapID: id}); err != nil {
				return err
			}
		}
		return nil
	})
}

// Onboarding returns the student's stored answers; ErrNotFound if they
// never sent any.
func (s *Service) Onboarding(ctx context.Context, who Who) (Onboarding, error) {
	if who.Kind != people.KindStudent {
		return Onboarding{}, ErrNotFound
	}
	row, err := roadmapdb.New(s.pool).GetOnboarding(ctx, who.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Onboarding{}, ErrNotFound
	}
	if err != nil {
		return Onboarding{}, err
	}
	var o Onboarding
	if err := json.Unmarshal(row.Answers, &o); err != nil {
		return Onboarding{}, fmt.Errorf("stored onboarding: %w", err)
	}
	t := row.UpdatedAt.Time.UTC()
	o.UpdatedAt = &t
	return o, nil
}

// Onboarded says whether a student has sent onboarding (Profile.onboarded
// for registered students).
func (s *Service) Onboarded(ctx context.Context, student int32) (bool, error) {
	return roadmapdb.New(s.pool).HasOnboarding(ctx, student)
}
