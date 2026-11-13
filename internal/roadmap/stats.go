package roadmap

import (
	"context"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nazacd/makonbook-api/internal/roadmap/roadmapdb"
)

// The question bank's analytics (spec 22.8), from question_answers, in
// school time. The site-wide averages are cached and left out below a
// minimum of answers.

// MinSiteAnswers is how many answers a site-wide average needs to be shown.
const MinSiteAnswers = 100

// siteTTL is how long the site-wide averages are kept.
const siteTTL = 10 * time.Minute

// PracticeStats is everything the question bank's page shows.
type PracticeStats struct {
	Subjects map[string]SubjectStats `json:"subjects"`
	// Attempted: questions answered at least once; Answers and Correct count every answer.
	Attempted int `json:"attempted"`
	Answers   int `json:"answers"`
	Correct   int `json:"correct"`
	Saved     int `json:"saved"`
	// Streak: school days in a row with an answer, to today (or yesterday).
	Streak int         `json:"streak"`
	Weeks  []WeekStats `json:"weeks"`
}

// SubjectStats is a subject's progress and its breakdowns.
type SubjectStats struct {
	Total      int              `json:"total"` // approved questions
	Attempted  int              `json:"attempted"`
	Solved     int              `json:"solved"`
	Skills     map[string]int   `json:"skills"` // skill → questions solved
	Domains    []DomainStats    `json:"domains"`
	Difficulty []DifficultyStat `json:"difficulty"`
}

// DomainStats is a domain's answers and right ones.
type DomainStats struct {
	Domain  string `json:"domain"`
	Answers int    `json:"answers"`
	Correct int    `json:"correct"`
}

// DifficultyStat is a difficulty's answers, right ones and average time,
// with the site-wide figures (null below MinSiteAnswers).
type DifficultyStat struct {
	Difficulty     string   `json:"difficulty"`
	Answers        int      `json:"answers"`
	Correct        int      `json:"correct"`
	AvgSeconds     *float64 `json:"avg_seconds"` // null when no answer was timed
	SiteAccuracy   *float64 `json:"site_accuracy"`
	SiteAvgSeconds *float64 `json:"site_avg_seconds"`
}

// WeekStats is a week's answers (from its Monday), right and wrong, by difficulty.
type WeekStats struct {
	Week    string         `json:"week"` // the Monday, YYYY-MM-DD
	Correct map[string]int `json:"correct"`
	Wrong   map[string]int `json:"wrong"` // keys easy, medium, hard, unrated
}

// WeeksShown is how many weeks the trend shows.
const WeeksShown = 8

type siteCache struct {
	at   time.Time
	rows []roadmapdb.DifficultyStatsRow
}

// Stats works out a student's question bank analytics. now and loc: the
// school's clock.
func (s *Service) Stats(ctx context.Context, student int32, now time.Time, loc *time.Location) (PracticeStats, error) {
	q := roadmapdb.New(s.pool)
	out := PracticeStats{Subjects: map[string]SubjectStats{}}
	for _, subject := range Subjects {
		out.Subjects[subject] = SubjectStats{Skills: map[string]int{}, Domains: []DomainStats{}, Difficulty: []DifficultyStat{}}
	}
	prog, err := q.SubjectProgress(ctx, student)
	if err != nil {
		return out, err
	}
	for _, p := range prog {
		st := out.Subjects[p.Subject]
		st.Total, st.Attempted, st.Solved = int(p.Total), int(p.Attempted), int(p.Solved)
		out.Subjects[p.Subject] = st
	}
	skills, err := q.SkillSolved(ctx, student)
	if err != nil {
		return out, err
	}
	for _, k := range skills {
		out.Subjects[k.Subject].Skills[k.Skill] = int(k.Solved)
	}
	totals, err := q.AnswerTotals(ctx, student)
	if err != nil {
		return out, err
	}
	out.Answers, out.Correct, out.Attempted = int(totals.Answers), int(totals.Correct), int(totals.Questions)
	saved, err := q.SavedQuestionIDs(ctx, student)
	if err != nil {
		return out, err
	}
	out.Saved = len(saved)

	days, err := q.AnswerDays(ctx, roadmapdb.AnswerDaysParams{Student: student, Tz: loc.String()})
	if err != nil {
		return out, err
	}
	out.Streak = streak(days, now.In(loc))

	// The trend: the last WeeksShown weeks, Monday to Sunday, school time.
	local := now.In(loc)
	monday := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -((int(local.Weekday()) + 6) % 7))
	first := monday.AddDate(0, 0, -7*(WeeksShown-1))
	weekly, err := q.WeeklyAnswers(ctx, roadmapdb.WeeklyAnswersParams{Student: student, Tz: loc.String(), Since: pgtype.Timestamptz{Time: first, Valid: true}})
	if err != nil {
		return out, err
	}
	byWeek := map[string]*WeekStats{}
	for i := range WeeksShown {
		d := first.AddDate(0, 0, 7*i).Format(time.DateOnly)
		w := &WeekStats{Week: d, Correct: map[string]int{}, Wrong: map[string]int{}}
		byWeek[d] = w
		out.Weeks = append(out.Weeks, *w)
	}
	for _, r := range weekly {
		w := byWeek[r.Week.Time.Format(time.DateOnly)]
		if w == nil {
			continue
		}
		d := r.Difficulty
		if d == "" {
			d = "unrated"
		}
		if r.Correct {
			w.Correct[d] += int(r.N)
		} else {
			w.Wrong[d] += int(r.N)
		}
	}
	for i := range out.Weeks {
		out.Weeks[i] = *byWeek[out.Weeks[i].Week]
	}

	domains, err := q.DomainAccuracy(ctx, student)
	if err != nil {
		return out, err
	}
	for _, d := range domains {
		st := out.Subjects[d.Subject]
		st.Domains = append(st.Domains, DomainStats{Domain: d.Domain, Answers: int(d.Answers), Correct: int(d.Correct)})
		out.Subjects[d.Subject] = st
	}
	for subject, st := range out.Subjects {
		order := domainRank(subject)
		sort.SliceStable(st.Domains, func(i, j int) bool { return order(st.Domains[i].Domain) < order(st.Domains[j].Domain) })
		out.Subjects[subject] = st
	}

	mine, err := q.DifficultyStats(ctx, student)
	if err != nil {
		return out, err
	}
	site, err := s.siteDifficulty(ctx, q)
	if err != nil {
		return out, err
	}
	for _, subject := range Subjects {
		st := out.Subjects[subject]
		for _, d := range Difficulties {
			ds := DifficultyStat{Difficulty: d}
			for _, r := range mine {
				if r.Subject == subject && r.Difficulty == d {
					ds.Answers, ds.Correct = int(r.Answers), int(r.Correct)
					if r.Timed > 0 {
						v := r.AvgSeconds
						ds.AvgSeconds = &v
					}
				}
			}
			for _, r := range site {
				if r.Subject == subject && r.Difficulty == d && r.Answers >= MinSiteAnswers {
					acc := float64(r.Correct) / float64(r.Answers)
					ds.SiteAccuracy = &acc
					if r.Timed >= MinSiteAnswers {
						v := r.AvgSeconds
						ds.SiteAvgSeconds = &v
					}
				}
			}
			st.Difficulty = append(st.Difficulty, ds)
		}
		out.Subjects[subject] = st
	}
	return out, nil
}

func (s *Service) siteDifficulty(ctx context.Context, q *roadmapdb.Queries) ([]roadmapdb.DifficultyStatsRow, error) {
	s.mu.Lock()
	c := s.site
	s.mu.Unlock()
	if c != nil && time.Since(c.at) < siteTTL {
		return c.rows, nil
	}
	rows, err := q.DifficultyStats(ctx, 0)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.site = &siteCache{at: time.Now(), rows: rows}
	s.mu.Unlock()
	return rows, nil
}

// streak counts the days in a row, newest first, ending today or yesterday.
func streak(days []pgtype.Date, today time.Time) int {
	if len(days) == 0 {
		return 0
	}
	day := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	first := days[0].Time
	if !first.Equal(day) {
		day = day.AddDate(0, 0, -1)
		if !first.Equal(day) {
			return 0
		}
	}
	n := 0
	for _, d := range days {
		if !d.Time.Equal(day) {
			break
		}
		n++
		day = day.AddDate(0, 0, -1)
	}
	return n
}

// domainRank orders a subject's domains as the digital SAT does.
func domainRank(subject string) func(string) int {
	order := map[string][]string{
		"english": {"Information and Ideas", "Craft and Structure", "Expression of Ideas", "Standard English Conventions"},
		"math":    {"Algebra", "Advanced Math", "Problem-Solving and Data Analysis", "Geometry and Trigonometry"},
	}[subject]
	return func(d string) int {
		for i, x := range order {
			if x == d {
				return i
			}
		}
		return len(order)
	}
}
