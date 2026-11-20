package gamification

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nazacd/makonbook-api/internal/gamification/gamificationdb"
)

// Backfill writes the ledger from what already happened: attempts,
// assessments, reviews, games, reading and attendance, with the same keys
// the live hooks use, oldest first, so quests and achievements fall where
// they would have. It starts by wiping the four tables, so running it again
// gives the same result; run it when the API is quiet.
// `extra` add events another feature works out (graduations).
func (s *Service) Backfill(ctx context.Context, progress func(done, total int), extra ...func(context.Context) ([]Event, error)) (int, error) {
	q := gamificationdb.New(s.pool)
	var events []Event
	attempts, err := q.PastAttempts(ctx)
	if err != nil {
		return 0, err
	}
	for _, a := range attempts {
		events = append(events, AttemptEvent(a.StudentClientID, uuidString(a.ID), a.Title, int(a.RightAnswers), int(a.Questions), int(a.Score), 0, a.FinishedAt.Time))
	}
	assessments, err := q.PastAssessments(ctx)
	if err != nil {
		return 0, err
	}
	for _, a := range assessments {
		events = append(events, AssessmentEvent(a.StudentClientID, uuidString(a.ID), a.Kind, a.AssignmentTitle.String, int(a.Correct), int(a.Total), int(a.Percent), 0, a.Assigned, a.OnTime, a.FinishedAt.Time))
	}
	tz := s.loc.String()
	reviews, err := q.PastReviewDays(ctx, tz)
	if err != nil {
		return 0, err
	}
	for _, r := range reviews {
		events = append(events, ReviewsEvent(r.StudentClientID, r.Day.Time, r.Reviews, r.LastAt.Time))
	}
	games, err := q.PastGameDays(ctx, tz)
	if err != nil {
		return 0, err
	}
	for _, g := range games {
		events = append(events, GamesEvent(g.StudentClientID, g.Day.Time, g.RightAnswers, g.LastAt.Time))
	}
	reading, err := q.PastReading(ctx, tz)
	if err != nil {
		return 0, err
	}
	for _, r := range reading {
		events = append(events, ReadingEvent(r.StudentClientID, r.Day.Time, r.LastAt.Time))
	}
	marks, err := q.PastAttendance(ctx)
	if err != nil {
		return 0, err
	}
	for _, m := range marks {
		events = append(events, AttendanceEvent(m.StudentClientID, uuidString(m.SessionID), m.At.Time))
	}
	for _, more := range extra {
		list, err := more(ctx)
		if err != nil {
			return 0, err
		}
		events = append(events, list...)
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].HappenedAt.Before(events[j].HappenedAt) })
	if err := q.Wipe(ctx); err != nil {
		return 0, err
	}
	for i, e := range events {
		if err := s.Award(ctx, e); err != nil {
			return i, fmt.Errorf("%s for %d: %w", e.Key, e.Student, err)
		}
		if progress != nil && (i%500 == 499 || i == len(events)-1) {
			progress(i+1, len(events))
		}
	}
	return len(events), nil
}

func uuidString(u pgtype.UUID) string {
	s, _ := u.Value()
	str, _ := s.(string)
	return str
}

// The events the features send, built in one place so the backfill and the
// live hooks agree.

// AssessmentEvent: kind is the assessment's (level_check, homework, practice,
// competition); assigned says it was a teacher's assignment, onTime that it
// was finished before the deadline.
// run is the longest run of right answers in it (0 when unknown, as in the backfill).
func AssessmentEvent(student int32, id, kind, assignmentTitle string, correct, total, percent, run int, assigned, onTime bool, finishedAt time.Time) Event {
	title := map[string]string{"level_check": "Level check", "homework": "Homework set", "practice": "Practice set", "competition": "Competition"}[kind]
	if title == "" {
		title = "Assessment"
	}
	if assigned && assignmentTitle != "" {
		title = assignmentTitle
	}
	amount := AssessmentXP + correct
	if kind == "competition" {
		amount = CompetitionXP + correct
	}
	if kind == "level_check" && percent >= 70 {
		amount += LevelCheckPass
	}
	if assigned && onTime {
		amount += OnTimeXP
	}
	ev := Event{Student: student, Key: "assessment:" + id, Kind: "assessment", Title: title, Amount: int32(amount), HappenedAt: finishedAt, Right: int32(correct), Finished: true, Percent: &percent, Questions: total, Run: run} //nolint:gosec // small counts
	if kind == "competition" {
		ev.Kind = "competition"
	}
	return ev
}

// ReviewsEvent: n reviews on a day (1 XP each, capped per day).
func ReviewsEvent(student int32, day time.Time, n int32, at time.Time) Event {
	return Event{Student: student, Key: "vocab:" + day.Format("2006-01-02"), Title: "Word reviews", Amount: n * ReviewXP, Cap: ReviewsDayCap, HappenedAt: at, Reviews: n, Units: int(n)}
}

// GamesEvent: right answers in games on a day (2 XP each, capped per day).
func GamesEvent(student int32, day time.Time, right int32, at time.Time) Event {
	return Event{Student: student, Key: "games:" + day.Format("2006-01-02"), Title: "Word games", Amount: right * GameXP, Cap: GamesDayCap, HappenedAt: at, Right: right}
}

// ReadingEvent: a page read on a day.
func ReadingEvent(student int32, day time.Time, at time.Time) Event {
	return Event{Student: student, Key: "read:" + day.Format("2006-01-02"), Title: "Reading in the Library", Amount: ReadingXP, HappenedAt: at, Units: 1}
}

// AttendanceEvent: marked present or late at a lesson.
func AttendanceEvent(student int32, sessionID string, at time.Time) Event {
	return Event{Student: student, Key: "attendance:" + sessionID, Title: "At the lesson", Amount: AttendanceXP, HappenedAt: at}
}

// AttemptEvent: a mock test attempt; score is the total when both sections were taken, else 0.
func AttemptEvent(student int32, id, title string, right, questions, score, run int, finishedAt time.Time) Event {
	ev := Event{Student: student, Key: "attempt:" + id, Title: title, Amount: AttemptXP + int32(right), HappenedAt: finishedAt, Right: int32(right), Finished: true, Questions: questions, Score: score, Run: run} //nolint:gosec // small counts
	if questions > 0 {
		p := right * 100 / questions
		ev.Percent = &p
	}
	return ev
}

// GraduationEvent: a roadmap course finished, once per course.
func GraduationEvent(student int32, roadmapID, title string, at time.Time) Event {
	return Event{Student: student, Key: "graduation:" + roadmapID, Kind: "graduation", Title: "Graduated: " + title, Amount: GraduationXP, HappenedAt: at}
}

// PodiumEvent: a place (1–3) in a competition's final board.
func PodiumEvent(student int32, competitionID, title string, place int, at time.Time) Event {
	place = min(max(place, 1), len(PodiumXP))
	return Event{Student: student, Key: "podium:" + competitionID, Kind: "podium", Title: fmt.Sprintf("%s: place %d", title, place), Amount: PodiumXP[place-1], HappenedAt: at, Units: place}
}

// DayOf is the school date of an instant, for the day-keyed events.
func (s *Service) DayOf(t time.Time) time.Time { return s.day(t) }
