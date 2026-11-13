package roadmap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nazacd/makonbook-api/internal/practice"
	"github.com/nazacd/makonbook-api/internal/roadmap/roadmapdb"
)

// Answers to bank questions (spec 22.8): one row per question an assessment
// asked and the student answered, graded as the assessment is, written on
// submit and on each check of a practice set taken at one's own pace.

var (
	// ErrAlreadyChecked means the question was checked already in this set.
	ErrAlreadyChecked = errors.New("this question was checked already")
	// ErrNotCheckable means the assessment isn't a practice set taken at one's own pace.
	ErrNotCheckable = errors.New("only a practice set taken at your own pace checks one question at a time")
)

// recordAnswers writes the answered questions of an assessment (a checked
// one keeps its row).
func recordAnswers(ctx context.Context, q *roadmapdb.Queries, student int32, assessment pgtype.UUID, answers map[string]*string, timesJSON []byte, questions map[string]practice.PackageQuestion, at time.Time) error {
	times := map[string]int{}
	if len(timesJSON) > 0 {
		if err := json.Unmarshal(timesJSON, &times); err != nil {
			return fmt.Errorf("stored times: %w", err)
		}
	}
	for qid, a := range answers {
		pq, ok := questions[qid]
		if !ok || a == nil || strings.TrimSpace(*a) == "" {
			continue
		}
		u, ok := parseUUID(qid)
		if !ok {
			continue
		}
		secs := pgtype.Int4{}
		if t, ok := times[qid]; ok && t > 0 {
			secs = pgtype.Int4{Int32: int32(min(t, maxSeconds)), Valid: true} //nolint:gosec // bounded above
		}
		if _, err := q.InsertAnswer(ctx, roadmapdb.InsertAnswerParams{
			Student: student, QuestionID: u, AssessmentID: assessment, Answer: strings.TrimSpace(*a),
			Correct: pq.Correct(*a), Seconds: secs, AnsweredAt: pgtype.Timestamptz{Time: at, Valid: true},
		}); err != nil {
			return err
		}
	}
	return nil
}

// dropChecked takes out of answers the questions already checked in a
// practice set: a checked answer stays as it was.
func dropChecked(ctx context.Context, q *roadmapdb.Queries, row roadmapdb.RoadmapAssessment, answers map[string]*string) error {
	if row.Kind != kindPractice || len(answers) == 0 {
		return nil
	}
	rows, err := q.CheckedAnswers(ctx, row.ID)
	if err != nil {
		return err
	}
	for _, r := range rows {
		delete(answers, uuidString(r.QuestionID))
	}
	return nil
}

// Checked is how a question checked on its own went.
type Checked struct {
	Correct     bool     `json:"correct"`
	Answer      string   `json:"answer"`
	Key         *string  `json:"correct_answer"`
	Accepted    []string `json:"accepted"`
	Explanation string   `json:"explanation"`
}

// Check grades one question of a practice set taken at one's own pace,
// records the answer and gives back the key and explanation. A question is
// checked once.
func (s *Service) Check(ctx context.Context, who Who, id, questionID, answer string, seconds int) (Checked, error) {
	row, err := s.own(ctx, who, id)
	if err != nil {
		return Checked{}, err
	}
	var info PracticeInfo
	if row.Kind != kindPractice || row.Practice == nil || json.Unmarshal(row.Practice, &info) != nil || info.Mode != ModeOwnPace {
		return Checked{}, ErrNotCheckable
	}
	answer = strings.TrimSpace(answer)
	if answer == "" || len(answer) > maxAnswer {
		return Checked{}, &AnswersError{"an answer is needed, at most 32 characters"}
	}
	qid, ok := parseUUID(questionID)
	if !ok || !containsUUID(row.QuestionIds, qid) {
		return Checked{}, &AnswersError{"that question isn't in this set"}
	}
	questions, err := s.bank.BankQuestions(ctx, []string{questionID})
	if err != nil {
		return Checked{}, err
	}
	pq, ok := questions[questionID]
	if !ok {
		return Checked{}, ErrNotFound
	}
	out := Checked{Correct: pq.Correct(answer), Answer: answer, Key: pq.Key, Accepted: pq.Accepted, Explanation: pq.Explanation}
	if out.Accepted == nil {
		out.Accepted = []string{}
	}
	err = s.inTx(ctx, func(q *roadmapdb.Queries) error {
		locked, err := q.LockAssessment(ctx, row.ID)
		if err != nil {
			return err
		}
		if locked.FinishedAt.Valid {
			return ErrFinished
		}
		times := map[string]float64{}
		if seconds > 0 {
			times[questionID] = float64(seconds)
		}
		merged, err := mergeAnswers(locked, map[string]*string{questionID: &answer})
		if err != nil {
			return err
		}
		mergedTimes, err := mergeTimes(locked, times)
		if err != nil {
			return err
		}
		body, err := json.Marshal(merged)
		if err != nil {
			return err
		}
		secs := pgtype.Int4{}
		if seconds > 0 {
			secs = pgtype.Int4{Int32: int32(min(seconds, maxSeconds)), Valid: true} //nolint:gosec // bounded above
		}
		n, err := q.InsertAnswer(ctx, roadmapdb.InsertAnswerParams{
			Student: who.ID, QuestionID: qid, AssessmentID: row.ID, Answer: answer, Correct: out.Correct, Seconds: secs,
			AnsweredAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrAlreadyChecked
		}
		return q.SaveAnswers(ctx, roadmapdb.SaveAnswersParams{ID: row.ID, Answers: body, Times: mergedTimes})
	})
	if err != nil {
		return Checked{}, err
	}
	return out, nil
}

func containsUUID(list []pgtype.UUID, u pgtype.UUID) bool {
	for _, x := range list {
		if x == u {
			return true
		}
	}
	return false
}

// BackfillAnswers fills question_answers from the assessments finished
// before it existed (a row already there stays). It returns how many it added.
func (s *Service) BackfillAnswers(ctx context.Context) (int, error) {
	q := roadmapdb.New(s.pool)
	rows, err := q.FinishedAssessments(ctx)
	if err != nil {
		return 0, err
	}
	added := 0
	for _, r := range rows {
		answers := map[string]*string{}
		if err := json.Unmarshal(r.Answers, &answers); err != nil {
			return added, fmt.Errorf("assessment %s: %w", uuidString(r.ID), err)
		}
		questions, err := s.bank.BankQuestions(ctx, idStrings(r.QuestionIds))
		if err != nil {
			return added, err
		}
		before, err := q.CheckedAnswers(ctx, r.ID)
		if err != nil {
			return added, err
		}
		if err := recordAnswers(ctx, q, r.StudentClientID, r.ID, answers, r.Times, questions, r.FinishedAt.Time); err != nil {
			return added, err
		}
		after, err := q.CheckedAnswers(ctx, r.ID)
		if err != nil {
			return added, err
		}
		added += len(after) - len(before)
	}
	return added, nil
}

// SavedQuestions are a student's saved questions, newest first.
func (s *Service) SavedQuestions(ctx context.Context, student int32) ([]string, error) {
	ids, err := roadmapdb.New(s.pool).SavedQuestionIDs(ctx, student)
	if err != nil {
		return nil, err
	}
	return idStrings(ids), nil
}

// SaveQuestion saves a question (an approved one) or unsaves it.
func (s *Service) SaveQuestion(ctx context.Context, student int32, id string, on bool) error {
	u, ok := parseUUID(id)
	if !ok {
		return ErrNotFound
	}
	q := roadmapdb.New(s.pool)
	if !on {
		return q.UnsaveQuestion(ctx, roadmapdb.UnsaveQuestionParams{Student: student, QuestionID: u})
	}
	found, err := s.bank.BankQuestions(ctx, []string{id})
	if err != nil {
		return err
	}
	if _, ok := found[id]; !ok {
		return ErrNotFound
	}
	return q.SaveQuestion(ctx, roadmapdb.SaveQuestionParams{Student: student, QuestionID: u})
}
