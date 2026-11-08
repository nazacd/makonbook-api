package roadmap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nazacd/makonbook-api/internal/practice"
	"github.com/nazacd/makonbook-api/internal/roadmap/roadmapdb"
)

// Assessment kinds.
const (
	kindLevelCheck = "level_check"
	kindHomework   = "homework"
)

// maxAnswer bounds one answer's length (a letter or a grid-in entry).
const maxAnswer = 32

// AnswersError is a set of answers that can't be saved.
type AnswersError struct{ Reason string }

func (e *AnswersError) Error() string { return e.Reason }

// Assessment is a level check or a homework set. While it is open its
// questions carry no key or explanation; once submitted they do, and Result
// is filled.
type Assessment struct {
	ID            string                        `json:"id"`
	Kind          string                        `json:"kind"`
	RoadmapID     string                        `json:"roadmap_id"`
	LessonID      string                        `json:"lesson_id"`
	HomeworkIndex *int                          `json:"homework_index"`
	Title         string                        `json:"title"`
	Subject       string                        `json:"subject"`
	CreatedAt     time.Time                     `json:"created_at"`
	FinishedAt    *time.Time                    `json:"finished_at"`
	Questions     []AssessmentQuestion          `json:"questions"`
	Images        map[string]practice.ImageLink `json:"images"` // a figure's key → its link
	Answers       map[string]*string            `json:"answers"`
	Result        *Result                       `json:"result"`
}

// AssessmentQuestion is a question as the student sees it.
type AssessmentQuestion struct {
	ID         string            `json:"id"`
	Position   int               `json:"position"`
	Type       string            `json:"type"`
	Passage    *string           `json:"passage"`
	Stem       string            `json:"stem"`
	Choices    map[string]string `json:"choices"`
	Figures    []practice.Figure `json:"figures"`
	Domain     string            `json:"domain"`
	Skill      string            `json:"skill"`
	Difficulty *string           `json:"difficulty"`
	// Only once submitted: the letter (mcq), the accepted forms (grid-in)
	// and the explanation.
	Correct     *string  `json:"correct,omitempty"`
	Accepted    []string `json:"accepted,omitempty"`
	Explanation *string  `json:"explanation,omitempty"`
}

// Result is how a submitted assessment went.
type Result struct {
	Percent    int          `json:"percent"`
	Correct    int          `json:"correct"`
	Total      int          `json:"total"`
	Passed     bool         `json:"passed"` // level check: ≥ PassPercent; homework: always
	LessonDone bool         `json:"lesson_done"`
	Items      []ResultItem `json:"items"`
}

// ResultItem is one question's answer.
type ResultItem struct {
	QuestionID    string  `json:"question_id"`
	Given         *string `json:"given"`
	Correct       bool    `json:"correct"`
	CorrectAnswer string  `json:"correct_answer"` // the letter, or the first accepted form
	Explanation   string  `json:"explanation"`
}

// Start returns the person's open level check or homework set of a lesson,
// or makes a new one with questions they haven't had where possible. index
// is the homework set (ignored for a level check).
func (s *Service) Start(ctx context.Context, who Who, roadmapID, lessonID, kind string, index int, today time.Time) (Assessment, error) {
	a, p, ls, err := s.lessonIn(ctx, who, roadmapID, lessonID, today)
	if err != nil {
		return Assessment{}, err
	}
	l := ls.lesson
	var skills []string
	var want map[string]int
	hwIndex := pgtype.Int2{}
	switch kind {
	case kindLevelCheck:
		skills, want = l.LevelCheck.Skills, l.LevelCheck.Mix.counts()
		index = -1
	case kindHomework:
		if index < 0 || index >= len(l.Homework) {
			return Assessment{}, ErrNotFound
		}
		h := l.Homework[index]
		skills, want = h.Skills, map[string]int{h.Difficulty: h.Questions}
		hwIndex = pgtype.Int2{Int16: int16(index), Valid: true} //nolint:gosec // a handful of sets
	default:
		return Assessment{}, ErrNotFound
	}
	q := roadmapdb.New(s.pool)
	open, err := q.OpenAssessment(ctx, roadmapdb.OpenAssessmentParams{Student: who.ID, LessonID: l.ID, Kind: kind, HomeworkIndex: int32(index)})
	switch {
	case err == nil:
		return s.view(ctx, open, a.course, p)
	case !errors.Is(err, pgx.ErrNoRows):
		return Assessment{}, err
	}
	cands, err := s.bank.Candidates(ctx, a.course.Subject, resolveSkills(a.course.Subject, skills))
	if err != nil {
		return Assessment{}, err
	}
	seenIDs, err := q.SeenQuestions(ctx, who.ID)
	if err != nil {
		return Assessment{}, err
	}
	seen := make(map[string]bool, len(seenIDs))
	for _, id := range seenIDs {
		seen[uuidString(id)] = true
	}
	ids := choose(cands, seen, want, rand.Shuffle) //nolint:gosec // not a secret: which questions come
	if len(ids) == 0 {
		return Assessment{}, ErrNoQuestions
	}
	uuids := make([]pgtype.UUID, len(ids))
	for i, id := range ids {
		uuids[i], _ = parseUUID(id)
	}
	row, err := q.InsertAssessment(ctx, roadmapdb.InsertAssessmentParams{
		Student: who.ID, RoadmapID: roadmapID, LessonID: l.ID, Kind: kind, HomeworkIndex: hwIndex, QuestionIds: uuids,
	})
	if errors.Is(err, pgx.ErrNoRows) { // started at the same moment elsewhere
		row, err = q.OpenAssessment(ctx, roadmapdb.OpenAssessmentParams{Student: who.ID, LessonID: l.ID, Kind: kind, HomeworkIndex: int32(index)})
	}
	if err != nil {
		return Assessment{}, err
	}
	return s.view(ctx, row, a.course, p)
}

// neighbours are the difficulties next to each, nearest first.
var neighbours = map[string][]string{"easy": {"medium"}, "medium": {"easy", "hard"}, "hard": {"medium"}}

// choose picks questions for a mix of difficulties: first questions the
// person hasn't had, at the difficulty asked, then at the neighbouring
// difficulty, then at any; then the same among those they had. Short of
// all that, it gives fewer. The result runs from easy to hard.
func choose(cands []practice.Candidate, seen map[string]bool, want map[string]int, shuffle func(n int, swap func(i, j int))) []string {
	pools := map[bool]map[string][]string{true: {}, false: {}}
	for _, c := range cands {
		pools[!seen[c.ID]][c.Difficulty] = append(pools[!seen[c.ID]][c.Difficulty], c.ID)
	}
	for _, byDiff := range pools {
		for _, ids := range byDiff {
			shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
		}
	}
	remaining := map[string]int{}
	for d, n := range want {
		remaining[d] = n
	}
	picked := map[string][]string{} // by the difficulty asked
	take := func(fresh bool, from, forDiff string) {
		pool := pools[fresh][from]
		n := min(remaining[forDiff], len(pool))
		picked[forDiff] = append(picked[forDiff], pool[:n]...)
		pools[fresh][from] = pool[n:]
		remaining[forDiff] -= n
	}
	for _, fresh := range []bool{true, false} {
		for _, d := range Difficulties {
			take(fresh, d, d)
		}
		for _, d := range Difficulties {
			for _, nb := range neighbours[d] {
				take(fresh, nb, d)
			}
		}
		for _, d := range Difficulties {
			for _, other := range append(slices.Clone(Difficulties), "") {
				take(fresh, other, d)
			}
		}
	}
	var out []string
	for _, d := range Difficulties {
		out = append(out, picked[d]...)
	}
	return out
}

// Assessment returns one of the person's assessments.
func (s *Service) Assessment(ctx context.Context, who Who, id string) (Assessment, error) {
	row, c, err := s.own(ctx, who, id)
	if err != nil {
		return Assessment{}, err
	}
	p, err := s.progress(ctx, who.ID)
	if err != nil {
		return Assessment{}, err
	}
	return s.view(ctx, row, c, p)
}

// own reads an assessment of the person's, and its course.
func (s *Service) own(ctx context.Context, who Who, id string) (roadmapdb.RoadmapAssessment, *Course, error) {
	u, ok := parseUUID(id)
	if !ok {
		return roadmapdb.RoadmapAssessment{}, nil, ErrNotFound
	}
	row, err := roadmapdb.New(s.pool).GetAssessment(ctx, u)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && row.StudentClientID != who.ID) {
		return row, nil, ErrNotFound
	}
	if err != nil {
		return row, nil, err
	}
	c, err := s.course(ctx, row.RoadmapID)
	if err != nil {
		return row, nil, err
	}
	return row, c, nil
}

// SaveAnswers stores answers given so far: each replaces that question's
// answer (null clears it); the others stay. ErrFinished once submitted.
func (s *Service) SaveAnswers(ctx context.Context, who Who, id string, answers map[string]*string) error {
	_, _, err := s.own(ctx, who, id)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(q *roadmapdb.Queries) error {
		row, err := q.LockAssessment(ctx, mustUUID(id))
		if err != nil {
			return err
		}
		if row.FinishedAt.Valid {
			return ErrFinished
		}
		merged, err := mergeAnswers(row, answers)
		if err != nil {
			return err
		}
		body, err := json.Marshal(merged)
		if err != nil {
			return err
		}
		return q.SaveAnswers(ctx, roadmapdb.SaveAnswersParams{ID: row.ID, Answers: body})
	})
}

// Submit stores the last answers and grades the assessment, as practice
// tests are graded. ErrFinished if it was submitted before.
func (s *Service) Submit(ctx context.Context, who Who, id string, answers map[string]*string) (Assessment, error) {
	_, c, err := s.own(ctx, who, id)
	if err != nil {
		return Assessment{}, err
	}
	var finished roadmapdb.RoadmapAssessment
	err = s.inTx(ctx, func(q *roadmapdb.Queries) error {
		row, err := q.LockAssessment(ctx, mustUUID(id))
		if err != nil {
			return err
		}
		if row.FinishedAt.Valid {
			return ErrFinished
		}
		merged, err := mergeAnswers(row, answers)
		if err != nil {
			return err
		}
		questions, err := s.bank.BankQuestions(ctx, idStrings(row.QuestionIds))
		if err != nil {
			return err
		}
		correct, total := 0, 0
		for _, qid := range idStrings(row.QuestionIds) {
			pq, ok := questions[qid]
			if !ok {
				continue // gone from the bank: not counted
			}
			total++
			if a := merged[qid]; a != nil && pq.Correct(*a) {
				correct++
			}
		}
		percent := 0
		if total > 0 {
			percent = correct * 100 / total
		}
		body, err := json.Marshal(merged)
		if err != nil {
			return err
		}
		err = q.FinishAssessment(ctx, roadmapdb.FinishAssessmentParams{
			ID: row.ID, Answers: body, Correct: int2(correct), Total: int2(total), Percent: int2(percent),
		})
		if err != nil {
			return err
		}
		finished, err = q.GetAssessment(ctx, row.ID)
		return err
	})
	if err != nil {
		return Assessment{}, err
	}
	p, err := s.progress(ctx, who.ID)
	if err != nil {
		return Assessment{}, err
	}
	return s.view(ctx, finished, c, p)
}

// mergeAnswers checks answers against the assessment's questions and lays
// them over those saved.
func mergeAnswers(row roadmapdb.RoadmapAssessment, answers map[string]*string) (map[string]*string, error) {
	merged := map[string]*string{}
	if err := json.Unmarshal(row.Answers, &merged); err != nil {
		return nil, fmt.Errorf("stored answers: %w", err)
	}
	ids := idStrings(row.QuestionIds)
	for qid, a := range answers {
		if !slices.Contains(ids, qid) {
			return nil, &AnswersError{fmt.Sprintf("%s is not a question of this assessment", qid)}
		}
		if a != nil && utf8.RuneCountInString(*a) > maxAnswer {
			return nil, &AnswersError{fmt.Sprintf("the answer to %s is too long", qid)}
		}
		if a == nil || *a == "" {
			delete(merged, qid)
			continue
		}
		merged[qid] = a
	}
	return merged, nil
}

// view is an assessment as the student gets it.
func (s *Service) view(ctx context.Context, row roadmapdb.RoadmapAssessment, c *Course, p progress) (Assessment, error) {
	ids := idStrings(row.QuestionIds)
	questions, err := s.bank.BankQuestions(ctx, ids)
	if err != nil {
		return Assessment{}, err
	}
	saved := map[string]*string{}
	if err := json.Unmarshal(row.Answers, &saved); err != nil {
		return Assessment{}, fmt.Errorf("stored answers: %w", err)
	}
	finished := row.FinishedAt.Valid
	out := Assessment{
		ID: uuidString(row.ID), Kind: row.Kind, RoadmapID: row.RoadmapID, LessonID: row.LessonID, Subject: c.Subject,
		CreatedAt: row.CreatedAt.Time.UTC(), Questions: []AssessmentQuestion{}, Answers: map[string]*string{},
	}
	if finished {
		t := row.FinishedAt.Time.UTC()
		out.FinishedAt = &t
	}
	var lesson *Lesson
	for _, pl := range c.lessons() {
		if pl.lesson.ID == row.LessonID {
			lesson = pl.lesson
		}
	}
	switch {
	case row.Kind == kindHomework:
		i := int(row.HomeworkIndex.Int16)
		out.HomeworkIndex = &i
		out.Title = fmt.Sprintf("Homework %d", i+1)
		if lesson != nil && i < len(lesson.Homework) {
			out.Title = lesson.Homework[i].Title
		}
	case lesson != nil:
		out.Title = lesson.Title + " · Level check"
	default:
		out.Title = "Level check"
	}
	var keys []string
	var result *Result
	if finished {
		result = &Result{
			Percent: int(row.Percent.Int16), Correct: int(row.Correct.Int16), Total: int(row.Total.Int16), Passed: true,
			Items: []ResultItem{},
		}
		if row.Kind == kindLevelCheck {
			result.Passed = result.Percent >= PassPercent
		}
		if lesson != nil {
			result.LessonDone = p.done(lesson)
		}
	}
	for _, qid := range ids {
		pq, ok := questions[qid]
		if !ok {
			continue
		}
		aq := AssessmentQuestion{
			ID: qid, Position: len(out.Questions) + 1, Type: pq.Type, Passage: pq.Passage, Stem: pq.Stem, Choices: pq.Choices,
			Figures: pq.Figures, Domain: pq.Domain, Skill: pq.Skill, Difficulty: pq.Difficulty,
		}
		for _, f := range pq.Figures {
			keys = append(keys, f.Key)
		}
		out.Answers[qid] = saved[qid]
		if finished {
			aq.Correct, aq.Accepted, aq.Explanation = pq.Key, pq.Accepted, &pq.Explanation
			item := ResultItem{QuestionID: qid, Given: saved[qid], Explanation: pq.Explanation}
			item.Correct = saved[qid] != nil && pq.Correct(*saved[qid])
			switch {
			case pq.Key != nil:
				item.CorrectAnswer = *pq.Key
			case len(pq.Accepted) > 0:
				item.CorrectAnswer = pq.Accepted[0]
			}
			result.Items = append(result.Items, item)
		}
		out.Questions = append(out.Questions, aq)
	}
	out.Result = result
	slices.Sort(keys)
	out.Images, err = s.bank.FigureLinks(ctx, slices.Compact(keys))
	if err != nil {
		return Assessment{}, err
	}
	return out, nil
}

func (s *Service) inTx(ctx context.Context, fn func(q *roadmapdb.Queries) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(roadmapdb.New(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func int2(n int) pgtype.Int2 {
	return pgtype.Int2{Int16: int16(min(n, 1<<15-1)), Valid: true} //nolint:gosec // bounded
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

func mustUUID(s string) pgtype.UUID {
	u, _ := parseUUID(s)
	return u
}
