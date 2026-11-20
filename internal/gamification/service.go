// Package gamification is the progress a student can feel between tests
// (spec 27): XP the server awards once per thing done, a level, a streak of
// active days, daily and weekly quests, and achievements. Nothing here
// changes a score or a grade. Other features call Award after they have
// recorded the work; the key makes a second call for the same thing a no-op.
package gamification

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nazacd/makonbook-api/internal/gamification/gamificationdb"
)

// Amounts (spec 27).
const (
	AttemptXP      = 30 // a mock test attempt, plus 1 per right answer
	AssessmentXP   = 15 // a level check, homework set, assignment or practice set, plus 1 per right answer
	CompetitionXP  = 50 // a competition attempt, plus 1 per right answer
	LevelCheckPass = 20 // a level check passed (70 %)
	OnTimeXP       = 10 // an assignment finished before its deadline
	ReviewXP       = 1  // per word review
	ReviewsDayCap  = 40
	GameXP         = 2 // per right answer in a game
	GamesDayCap    = 100
	ReadingXP      = 5 // a page read in the Library, once a day
	AttendanceXP   = 10
	RecentCount    = 12
)

// Event is a thing done, for Award. Key is unique per student (the same
// key again awards nothing); for a day's aggregates (Cap > 0) the amounts
// add up to the cap. HappenedAt is when the work was done.
type Event struct {
	Student    int32
	Key        string // "<kind>:<id>" (attempt:…, assessment:…, vocab:<day>, games:<day>, read:<day>, attendance:<session>)
	Title      string
	Amount     int32
	Cap        int32 // > 0: a day's aggregate; the amount accumulates up to it
	HappenedAt time.Time
	// For the quests and achievements.
	Right    int32 // right answers
	Reviews  int32 // word reviews
	Finished bool  // a test or assessment finished
}

// Quest definitions: three a day, three a week.
type QuestDef struct {
	Code, Title, Metric string
	Target, Reward      int32
	Weekly              bool
}

const (
	metricRight    = "right_answers"
	metricReviews  = "reviews"
	metricFinished = "finished"
	metricDays     = "active_days"
)

var Quests = []QuestDef{
	{Code: "daily_right_10", Title: "10 right answers", Metric: metricRight, Target: 10, Reward: 30},
	{Code: "daily_reviews_10", Title: "10 words reviewed", Metric: metricReviews, Target: 10, Reward: 30},
	{Code: "daily_finish_1", Title: "One test or assessment finished", Metric: metricFinished, Target: 1, Reward: 40},
	{Code: "weekly_right_60", Title: "60 right answers", Metric: metricRight, Target: 60, Reward: 90, Weekly: true},
	{Code: "weekly_reviews_60", Title: "60 words reviewed", Metric: metricReviews, Target: 60, Reward: 90, Weekly: true},
	{Code: "weekly_days_5", Title: "5 active days", Metric: metricDays, Target: 5, Reward: 120, Weekly: true},
}

// AchievementDef: unlocked once a figure reaches the target.
type AchievementDef struct {
	Code, Title, Description, Key string
	Target                        int32
}

const (
	keyXP     = "xp_total"
	keyLevel  = "level"
	keyStreak = "best_streak"
	keyRight  = "right_answers"
	keyQuests = "quests"
)

var Achievements = []AchievementDef{
	{"first_xp", "First Step", "Earn your first XP.", keyXP, 1},
	{"xp_500", "Momentum", "Earn 500 XP.", keyXP, 500},
	{"xp_1000", "Four Digits", "Earn 1,000 XP.", keyXP, 1000},
	{"level_5", "Scholar", "Reach level 5.", keyLevel, 5},
	{"level_10", "Double Digits", "Reach level 10.", keyLevel, 10},
	{"streak_3", "Three-Day Spark", "Three days in a row.", keyStreak, 3},
	{"streak_7", "Week Warrior", "Seven days in a row.", keyStreak, 7},
	{"streak_30", "Month of Momentum", "Thirty days in a row.", keyStreak, 30},
	{"answers_100", "Century", "100 right answers.", keyRight, 100},
	{"answers_500", "Five Hundred", "500 right answers.", keyRight, 500},
	{"answers_2000", "Two Thousand", "2,000 right answers.", keyRight, 2000},
	{"quests_5", "Quest Habit", "Five quests finished.", keyQuests, 5},
	{"quests_25", "Quest Regular", "Twenty-five quests finished.", keyQuests, 25},
}

var titles = []struct {
	level int32
	title string
}{{25, "Master"}, {18, "Expert"}, {12, "Specialist"}, {8, "Strategist"}, {5, "Scholar"}, {3, "Learner"}, {1, "Starter"}}

// LevelFloor is the XP a level starts at: 25 × (L − 1) × (L + 2).
func LevelFloor(level int32) int64 { return 25 * int64(level-1) * int64(level+2) }

// Level for a total.
func Level(xp int64) int32 {
	l := int32(1)
	for xp >= LevelFloor(l+1) {
		l++
	}
	return l
}

func LevelTitle(level int32) string {
	for _, t := range titles {
		if level >= t.level {
			return t.title
		}
	}
	return titles[len(titles)-1].title
}

// Service awards and reads. It opens its own transactions on the pool.
type Service struct {
	pool *pgxpool.Pool
	loc  *time.Location
	log  *slog.Logger
}

func NewService(pool *pgxpool.Pool, loc *time.Location, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{pool: pool, loc: loc, log: log}
}

// Hook is Award for the other features: it never fails them, only logs.
// The backfill repairs anything missed.
func (s *Service) Hook() func(context.Context, Event) {
	return func(ctx context.Context, e Event) {
		if err := s.Award(ctx, e); err != nil {
			s.log.ErrorContext(ctx, "gamification: award", "key", e.Key, "student", e.Student, "error", err)
		}
	}
}

// day is the school date of an instant, as midnight UTC (how dates are kept).
func (s *Service) day(t time.Time) time.Time {
	y, m, d := t.In(s.loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// dayStart is the instant the school day began.
func (s *Service) dayStart(t time.Time) time.Time {
	y, m, d := t.In(s.loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, s.loc)
}

func monday(day time.Time) time.Time { return day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7)) }

func date(t time.Time) pgtype.Date      { return pgtype.Date{Time: t, Valid: true} }
func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
func kindOf(key string) string          { k, _, _ := strings.Cut(key, ":"); return k }
func period(q QuestDef, day time.Time) time.Time {
	if q.Weekly {
		return monday(day)
	}
	return day
}

// Award records a thing done: the event (once per key), the day for the
// streak, the quests of that day and week, and any achievement reached.
func (s *Service) Award(ctx context.Context, e Event) error {
	if e.Student == 0 || e.Key == "" || e.HappenedAt.IsZero() {
		return errors.New("gamification: incomplete event")
	}
	if e.Amount < 0 {
		e.Amount = 0
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := gamificationdb.New(tx)
		if e.Cap > 0 {
			if _, err := q.AccumulateEvent(ctx, gamificationdb.AccumulateEventParams{
				StudentClientID: e.Student, Key: e.Key, Kind: kindOf(e.Key), Title: e.Title, Amount: e.Amount, Cap: e.Cap, RightAnswers: e.Right, HappenedAt: ts(e.HappenedAt),
			}); err != nil {
				return err
			}
		} else {
			n, err := q.InsertEvent(ctx, gamificationdb.InsertEventParams{
				StudentClientID: e.Student, Key: e.Key, Kind: kindOf(e.Key), Title: e.Title, Amount: e.Amount, RightAnswers: e.Right, HappenedAt: ts(e.HappenedAt),
			})
			if err != nil {
				return err
			}
			if n == 0 {
				return nil // heard of it before
			}
		}
		day := s.day(e.HappenedAt)
		if err := q.MarkDay(ctx, gamificationdb.MarkDayParams{StudentClientID: e.Student, Day: date(day)}); err != nil {
			return err
		}
		if err := s.advanceQuests(ctx, q, e, day); err != nil {
			return err
		}
		return s.unlock(ctx, q, e.Student, e.HappenedAt)
	})
}

func (s *Service) advanceQuests(ctx context.Context, q *gamificationdb.Queries, e Event, day time.Time) error {
	for _, def := range Quests {
		start := period(def, day)
		if err := q.EnsureQuest(ctx, gamificationdb.EnsureQuestParams{StudentClientID: e.Student, Code: def.Code, PeriodStart: date(start), Target: def.Target, RewardXp: def.Reward}); err != nil {
			return err
		}
		var n int32
		absolute := false
		switch def.Metric {
		case metricRight:
			n = e.Right
		case metricReviews:
			n = e.Reviews
		case metricFinished:
			if e.Finished {
				n = 1
			}
		case metricDays:
			count, err := q.DaysBetween(ctx, gamificationdb.DaysBetweenParams{StudentClientID: e.Student, Since: date(start), Until: date(start.AddDate(0, 0, 7))})
			if err != nil {
				return err
			}
			n, absolute = int32(count), true //nolint:gosec // at most 7
		}
		if n == 0 {
			continue
		}
		row, err := q.AdvanceQuest(ctx, gamificationdb.AdvanceQuestParams{Absolute: absolute, N: n, StudentClientID: e.Student, Code: def.Code, PeriodStart: date(start)})
		if errors.Is(err, pgx.ErrNoRows) {
			continue // done already
		}
		if err != nil {
			return err
		}
		if row.Progress < row.Target {
			continue
		}
		done, err := q.FinishQuest(ctx, gamificationdb.FinishQuestParams{DoneAt: ts(e.HappenedAt), StudentClientID: e.Student, Code: def.Code, PeriodStart: date(start)})
		if err != nil || done == 0 {
			return err
		}
		if _, err := q.InsertEvent(ctx, gamificationdb.InsertEventParams{
			StudentClientID: e.Student, Key: fmt.Sprintf("quest:%s:%s", def.Code, start.Format("2006-01-02")), Kind: "quest",
			Title: "Quest: " + def.Title, Amount: row.RewardXp, HappenedAt: ts(e.HappenedAt),
		}); err != nil {
			return err
		}
	}
	return nil
}

// figures are what achievements compare with.
type figures struct {
	xp, right, quests int64
	level, best       int32
}

func (s *Service) figuresOf(ctx context.Context, q *gamificationdb.Queries, student int32, now time.Time) (figures, []pgtype.Date, error) {
	t, err := q.Totals(ctx, gamificationdb.TotalsParams{TodayStart: ts(s.dayStart(now)), StudentClientID: student})
	if err != nil {
		return figures{}, nil, err
	}
	days, err := q.Days(ctx, student)
	if err != nil {
		return figures{}, nil, err
	}
	quests, err := q.QuestsDone(ctx, student)
	if err != nil {
		return figures{}, nil, err
	}
	_, best := streaks(days, s.day(now))
	return figures{xp: t.XpTotal, right: t.RightAnswers, quests: quests, level: Level(t.XpTotal), best: best}, days, nil
}

func (f figures) of(key string) int64 {
	switch key {
	case keyXP:
		return f.xp
	case keyLevel:
		return int64(f.level)
	case keyStreak:
		return int64(f.best)
	case keyRight:
		return f.right
	case keyQuests:
		return f.quests
	}
	return 0
}

func (s *Service) unlock(ctx context.Context, q *gamificationdb.Queries, student int32, at time.Time) error {
	f, _, err := s.figuresOf(ctx, q, student, at)
	if err != nil {
		return err
	}
	for _, a := range Achievements {
		if f.of(a.Key) < int64(a.Target) {
			continue
		}
		if _, err := q.UnlockAchievement(ctx, gamificationdb.UnlockAchievementParams{StudentClientID: student, Code: a.Code, UnlockedAt: ts(at)}); err != nil {
			return err
		}
	}
	return nil
}

// streaks: the days in a row ending today or yesterday, and the longest
// ever. days are newest first.
func streaks(days []pgtype.Date, today time.Time) (current, best int32) {
	set := make(map[time.Time]bool, len(days))
	for _, d := range days {
		set[d.Time] = true
	}
	start := today
	if !set[start] {
		start = start.AddDate(0, 0, -1)
	}
	for d := start; set[d]; d = d.AddDate(0, 0, -1) {
		current++
	}
	for _, d := range days {
		if set[d.Time.AddDate(0, 0, 1)] {
			continue // not the end of a run
		}
		var n int32
		for x := d.Time; set[x]; x = x.AddDate(0, 0, -1) {
			n++
		}
		best = max(best, n)
	}
	return current, best
}

// State is GET /v1/me/gamification.
type State struct {
	XPTotal      int64         `json:"xp_total"`
	XPToday      int64         `json:"xp_today"`
	Level        int32         `json:"level"`
	LevelTitle   string        `json:"level_title"`
	LevelFloor   int64         `json:"level_floor"`
	NextLevelXP  int64         `json:"next_level_xp"`
	LeveledToday bool          `json:"leveled_up_today"`
	Streak       Streak        `json:"streak"`
	Quests       QuestGroups   `json:"quests"`
	Achievements []Achievement `json:"achievements"`
	Recent       []Recent      `json:"recent"`
}

type Streak struct {
	Current     int32 `json:"current"`
	Best        int32 `json:"best"`
	ActiveToday bool  `json:"active_today"`
	AtRisk      bool  `json:"at_risk"`
	Days        []Day `json:"days"`
}

type Day struct {
	Day    string `json:"day"`
	Active bool   `json:"active"`
}

type QuestGroups struct {
	Daily    []Quest   `json:"daily"`
	Weekly   []Quest   `json:"weekly"`
	ResetsAt time.Time `json:"resets_at"`
}

type Quest struct {
	Code     string     `json:"code"`
	Title    string     `json:"title"`
	Progress int32      `json:"progress"`
	Target   int32      `json:"target"`
	RewardXP int32      `json:"reward_xp"`
	DoneAt   *time.Time `json:"done_at"`
}

type Achievement struct {
	Code        string     `json:"code"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	UnlockedAt  *time.Time `json:"unlocked_at"`
	Progress    int64      `json:"progress"`
	Target      int32      `json:"target"`
}

type Recent struct {
	Title      string    `json:"title"`
	Amount     int32     `json:"amount"`
	HappenedAt time.Time `json:"happened_at"`
}

// State reads a student's progress as of now.
func (s *Service) State(ctx context.Context, student int32, now time.Time) (State, error) {
	q := gamificationdb.New(s.pool)
	today := s.day(now)
	// Today's quests exist from the first look.
	for _, def := range Quests {
		if err := q.EnsureQuest(ctx, gamificationdb.EnsureQuestParams{StudentClientID: student, Code: def.Code, PeriodStart: date(period(def, today)), Target: def.Target, RewardXp: def.Reward}); err != nil {
			return State{}, err
		}
	}
	f, days, err := s.figuresOf(ctx, q, student, now)
	if err != nil {
		return State{}, err
	}
	current, best := streaks(days, today)
	active := map[time.Time]bool{}
	for _, d := range days {
		active[d.Time] = true
	}
	strip := make([]Day, 14)
	for i := range strip {
		d := today.AddDate(0, 0, i-13)
		strip[i] = Day{Day: d.Format("2006-01-02"), Active: active[d]}
	}
	rows, err := q.QuestsAt(ctx, gamificationdb.QuestsAtParams{StudentClientID: student, Periods: []pgtype.Date{date(today), date(monday(today))}})
	if err != nil {
		return State{}, err
	}
	byKey := map[string]gamificationdb.QuestsAtRow{}
	for _, r := range rows {
		byKey[r.Code+"@"+r.PeriodStart.Time.Format("2006-01-02")] = r
	}
	groups := QuestGroups{Daily: []Quest{}, Weekly: []Quest{}, ResetsAt: s.dayStart(now).AddDate(0, 0, 1)}
	for _, def := range Quests {
		r := byKey[def.Code+"@"+period(def, today).Format("2006-01-02")]
		qv := Quest{Code: def.Code, Title: def.Title, Progress: r.Progress, Target: def.Target, RewardXP: def.Reward}
		if r.DoneAt.Valid {
			t := r.DoneAt.Time.UTC()
			qv.DoneAt = &t
		}
		if def.Weekly {
			groups.Weekly = append(groups.Weekly, qv)
		} else {
			groups.Daily = append(groups.Daily, qv)
		}
	}
	unlocked, err := q.Achievements(ctx, student)
	if err != nil {
		return State{}, err
	}
	at := map[string]time.Time{}
	for _, u := range unlocked {
		at[u.Code] = u.UnlockedAt.Time.UTC()
	}
	achievements := make([]Achievement, 0, len(Achievements))
	for _, a := range Achievements {
		av := Achievement{Code: a.Code, Title: a.Title, Description: a.Description, Progress: min(f.of(a.Key), int64(a.Target)), Target: a.Target}
		if t, ok := at[a.Code]; ok {
			av.UnlockedAt = &t
			av.Progress = int64(a.Target)
		}
		achievements = append(achievements, av)
	}
	recentRows, err := q.RecentEvents(ctx, gamificationdb.RecentEventsParams{StudentClientID: student, Lim: RecentCount})
	if err != nil {
		return State{}, err
	}
	recent := make([]Recent, 0, len(recentRows))
	for _, r := range recentRows {
		recent = append(recent, Recent{Title: r.Title, Amount: r.Amount, HappenedAt: r.HappenedAt.Time.UTC()})
	}
	t, err := q.Totals(ctx, gamificationdb.TotalsParams{TodayStart: ts(s.dayStart(now)), StudentClientID: student})
	if err != nil {
		return State{}, err
	}
	level := Level(t.XpTotal)
	return State{
		XPTotal: t.XpTotal, XPToday: t.XpToday, Level: level, LevelTitle: LevelTitle(level), LevelFloor: LevelFloor(level), NextLevelXP: LevelFloor(level + 1),
		LeveledToday: t.XpToday > 0 && Level(t.XpTotal-t.XpToday) < level,
		Streak:       Streak{Current: current, Best: best, ActiveToday: active[today], AtRisk: current > 0 && !active[today], Days: strip},
		Quests:       groups, Achievements: achievements, Recent: recent,
	}, nil
}
