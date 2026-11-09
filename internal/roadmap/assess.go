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

// maxSeconds bounds the time a student says they spent on one question.
const maxSeconds = 6 * 60 * 60

// AnswersError is a set of answers that can't be saved.
type AnswersError struct{ Reason string }

func (e *AnswersError) Error() string { return e.Reason }

// Assessment is a level check or a homework set. While it is open its
// questions carry no key or explanation; once submitted they do, and Result
// is filled.
type Assessment struct {
	ID string `json:"id"`
	// Kind is level_check or homework.
	Kind string `json:"kind"`
	// RoadmapID (the course) and LessonID are null for custom homework
	// given as an assignment (spec 23).
	RoadmapID     *string `json:"roadmap_id"`
	LessonID      *string `json:"lesson_id"`
	HomeworkIndex *int    `json:"homework_index"`
	// Assignment is the assignment it was started from; null for the
	// roadmap's own.
	Assignment *AssignmentBrief              `json:"assignment"`
	Title      string                        `json:"title"`
	Subject    string                        `json:"subject"`
	CreatedAt  time.Time                     `json:"created_at"`
	FinishedAt *time.Time                    `json:"finished_at"`
	Questions  []AssessmentQuestion          `json:"questions"`
	Images     map[string]practice.ImageLink `json:"images"` // a figure's key → its link
	Answers    map[string]*string            `json:"answers"`
	// Times is question id → seconds spent, as the client sent them.
	Times  map[string]int `json:"times"`
	Result *Result        `json:"result"`
}

// AssignmentBrief is the assignment an assessment comes from.
type AssignmentBrief struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`
	Title   string    `json:"title"`
	DueAt   time.Time `json:"due_at"`
	subject string
}

// Answers is what a student sends as they go, and on submitting: answers
// (null or "" clears one) and seconds spent per question, each replacing
// that question's.
type Answers struct {
	Answers map[string]*string `json:"answers"`
	Times   map[string]float64 `json:"times"`
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
		skills, want = l.LevelCheck.Skills, l.LevelCheck.Mix.Counts()
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
	// An open one, assigned (spec 23.3) or the roadmap's own, is continued.
	open, err := q.OpenAssessment(ctx, roadmapdb.OpenAssessmentParams{Student: who.ID, LessonID: l.ID, Kind: kind, HomeworkIndex: int32(index)})
	switch {
	case err == nil:
		return s.viewRow(ctx, open, p)
	case !errors.Is(err, pgx.ErrNoRows):
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
	ids, err := s.Draw(ctx, a.course.Subject, skills, want, seen)
	if err != nil {
		return Assessment{}, err
	}
	row, err := q.InsertAssessment(ctx, roadmapdb.InsertAssessmentParams{
		Student: who.ID, RoadmapID: text(roadmapID), LessonID: text(l.ID), Kind: kind, HomeworkIndex: hwIndex, QuestionIds: uuids(ids),
	})
	if errors.Is(err, pgx.ErrNoRows) { // started at the same moment elsewhere
		row, err = q.OpenOwnAssessment(ctx, roadmapdb.OpenOwnAssessmentParams{Student: who.ID, LessonID: l.ID, Kind: kind, HomeworkIndex: int32(index)})
	}
	if err != nil {
		return Assessment{}, err
	}
	return s.view(ctx, row, a.course, nil, p)
}

// Draw picks questions for a mix of difficulties from the approved ones of
// a subject with one of skills (as resolveSkills reads them), preferring
// those not in seen, easy to hard (see choose). ErrNoQuestions when there
// are none.
func (s *Service) Draw(ctx context.Context, subject string, skills []string, want map[string]int, seen map[string]bool) ([]string, error) {
	cands, err := s.bank.Candidates(ctx, subject, resolveSkills(subject, skills))
	if err != nil {
		return nil, err
	}
	ids := choose(cands, seen, want, rand.Shuffle) //nolint:gosec // not a secret: which questions come
	if len(ids) == 0 {
		return nil, ErrNoQuestions
	}
	return ids, nil
}

// Assigned is an assignment's set as a student takes it (spec 23.3): the
// same questions for everyone, and the course, lesson and set when it comes
// from the curriculum (nil for custom homework).
type Assigned struct {
	ID            string
	Kind          string // level_check or homework
	Course        *string
	LessonID      *string
	HomeworkIndex *int
	QuestionIDs   []string
}

// StartAssigned returns the student's open assessment for an assignment,
// or makes one with its questions. The caller has checked that the student
// may start it.
func (s *Service) StartAssigned(ctx context.Context, student int32, a Assigned) (Assessment, error) {
	id, ok := parseUUID(a.ID)
	if !ok {
		return Assessment{}, ErrNotFound
	}
	q := roadmapdb.New(s.pool)
	row, err := q.OpenAssigned(ctx, roadmapdb.OpenAssignedParams{Student: student, AssignmentID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		hw := pgtype.Int2{}
		if a.HomeworkIndex != nil && a.LessonID != nil {
			hw = int2(*a.HomeworkIndex)
		}
		row, err = q.InsertAssessment(ctx, roadmapdb.InsertAssessmentParams{
			Student: student, RoadmapID: textPtr(a.Course), LessonID: textPtr(a.LessonID), Kind: a.Kind, HomeworkIndex: hw,
			QuestionIds: uuids(a.QuestionIDs), AssignmentID: id,
		})
		if errors.Is(err, pgx.ErrNoRows) { // started at the same moment elsewhere
			row, err = q.OpenAssigned(ctx, roadmapdb.OpenAssignedParams{Student: student, AssignmentID: id})
		}
	}
	if err != nil {
		return Assessment{}, err
	}
	p, err := s.progress(ctx, student)
	if err != nil {
		return Assessment{}, err
	}
	return s.viewRow(ctx, row, p)
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
	row, err := s.own(ctx, who, id)
	if err != nil {
		return Assessment{}, err
	}
	p, err := s.progress(ctx, who.ID)
	if err != nil {
		return Assessment{}, err
	}
	return s.viewRow(ctx, row, p)
}

// AssessmentOf returns anyone's assessment, as its student sees it: for a
// teacher looking at an assignment's results (the caller checks they may).
func (s *Service) AssessmentOf(ctx context.Context, id string) (Assessment, error) {
	u, ok := parseUUID(id)
	if !ok {
		return Assessment{}, ErrNotFound
	}
	row, err := roadmapdb.New(s.pool).GetAssessment(ctx, u)
	if errors.Is(err, pgx.ErrNoRows) {
		return Assessment{}, ErrNotFound
	}
	if err != nil {
		return Assessment{}, err
	}
	p, err := s.progress(ctx, row.StudentClientID)
	if err != nil {
		return Assessment{}, err
	}
	return s.viewRow(ctx, row, p)
}

// own reads an assessment of the person's.
func (s *Service) own(ctx context.Context, who Who, id string) (roadmapdb.RoadmapAssessment, error) {
	u, ok := parseUUID(id)
	if !ok {
		return roadmapdb.RoadmapAssessment{}, ErrNotFound
	}
	row, err := roadmapdb.New(s.pool).GetAssessment(ctx, u)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && row.StudentClientID != who.ID) {
		return row, ErrNotFound
	}
	return row, err
}

// contextOf reads an assessment's course (nil for custom homework, or a course
// no longer imported that an assignment still names) and its assignment.
func (s *Service) contextOf(ctx context.Context, row roadmapdb.RoadmapAssessment) (*Course, *AssignmentBrief, error) {
	var brief *AssignmentBrief
	if row.AssignmentID.Valid {
		a, err := roadmapdb.New(s.pool).AssessmentAssignment(ctx, row.AssignmentID)
		if err != nil {
			return nil, nil, fmt.Errorf("assignment of %s: %w", uuidString(row.ID), err)
		}
		brief = &AssignmentBrief{ID: uuidString(a.ID), Kind: a.Kind, Title: a.Title, DueAt: a.DueAt.Time.UTC(), subject: a.Subject}
	}
	if !row.RoadmapID.Valid {
		return nil, brief, nil
	}
	c, err := s.course(ctx, row.RoadmapID.String)
	if errors.Is(err, ErrNotFound) && brief != nil {
		return nil, brief, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return c, brief, nil
}

// viewRow is view with the assessment's course and assignment read first.
func (s *Service) viewRow(ctx context.Context, row roadmapdb.RoadmapAssessment, p progress) (Assessment, error) {
	c, brief, err := s.contextOf(ctx, row)
	if err != nil {
		return Assessment{}, err
	}
	return s.view(ctx, row, c, brief, p)
}

// SaveAnswers stores answers given so far: each replaces that question's
// answer (null clears it); the others stay. ErrFinished once submitted.
func (s *Service) SaveAnswers(ctx context.Context, who Who, id string, in Answers) error {
	_, err := s.own(ctx, who, id)
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
		merged, err := mergeAnswers(row, in.Answers)
		if err != nil {
			return err
		}
		times, err := mergeTimes(row, in.Times)
		if err != nil {
			return err
		}
		body, err := json.Marshal(merged)
		if err != nil {
			return err
		}
		return q.SaveAnswers(ctx, roadmapdb.SaveAnswersParams{ID: row.ID, Answers: body, Times: times})
	})
}

// Submit stores the last answers and grades the assessment, as practice
// tests are graded. ErrFinished if it was submitted before.
func (s *Service) Submit(ctx context.Context, who Who, id string, in Answers) (Assessment, error) {
	if _, err := s.own(ctx, who, id); err != nil {
		return Assessment{}, err
	}
	var finished roadmapdb.RoadmapAssessment
	err := s.inTx(ctx, func(q *roadmapdb.Queries) error {
		row, err := q.LockAssessment(ctx, mustUUID(id))
		if err != nil {
			return err
		}
		if row.FinishedAt.Valid {
			return ErrFinished
		}
		merged, err := mergeAnswers(row, in.Answers)
		if err != nil {
			return err
		}
		times, err := mergeTimes(row, in.Times)
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
			ID: row.ID, Answers: body, Times: times, Correct: int2(correct), Total: int2(total), Percent: int2(percent),
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
	return s.viewRow(ctx, finished, p)
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

// mergeTimes checks seconds per question against the assessment's questions
// and lays them over those saved, as JSON.
func mergeTimes(row roadmapdb.RoadmapAssessment, times map[string]float64) ([]byte, error) {
	merged := map[string]int{}
	if err := json.Unmarshal(row.Times, &merged); err != nil {
		return nil, fmt.Errorf("stored times: %w", err)
	}
	ids := idStrings(row.QuestionIds)
	for qid, sec := range times {
		if !slices.Contains(ids, qid) {
			return nil, &AnswersError{fmt.Sprintf("%s is not a question of this assessment", qid)}
		}
		if sec < 0 || sec > maxSeconds || sec != float64(int(sec)) {
			return nil, &AnswersError{fmt.Sprintf("the time for %s must be whole seconds from 0 to %d", qid, maxSeconds)}
		}
		merged[qid] = int(sec)
	}
	return json.Marshal(merged)
}

// view is an assessment as the student gets it. c is its course (nil for
// custom homework), brief its assignment (nil for the roadmap's own).
func (s *Service) view(ctx context.Context, row roadmapdb.RoadmapAssessment, c *Course, brief *AssignmentBrief, p progress) (Assessment, error) {
	ids := idStrings(row.QuestionIds)
	questions, err := s.bank.BankQuestions(ctx, ids)
	if err != nil {
		return Assessment{}, err
	}
	saved := map[string]*string{}
	if err := json.Unmarshal(row.Answers, &saved); err != nil {
		return Assessment{}, fmt.Errorf("stored answers: %w", err)
	}
	times := map[string]int{}
	if err := json.Unmarshal(row.Times, &times); err != nil {
		return Assessment{}, fmt.Errorf("stored times: %w", err)
	}
	finished := row.FinishedAt.Valid
	out := Assessment{
		ID: uuidString(row.ID), Kind: row.Kind, RoadmapID: textValue(row.RoadmapID), LessonID: textValue(row.LessonID),
		Assignment: brief, CreatedAt: row.CreatedAt.Time.UTC(), Questions: []AssessmentQuestion{},
		Answers: map[string]*string{}, Times: times,
	}
	switch {
	case c != nil:
		out.Subject = c.Subject
	case brief != nil:
		out.Subject = brief.subject
	}
	if finished {
		t := row.FinishedAt.Time.UTC()
		out.FinishedAt = &t
	}
	var lesson *Lesson
	if c != nil && row.LessonID.Valid {
		_, lesson, _ = c.Lesson(row.LessonID.String)
	}
	switch {
	case row.Kind == kindHomework && row.HomeworkIndex.Valid:
		i := int(row.HomeworkIndex.Int16)
		out.HomeworkIndex = &i
		out.Title = fmt.Sprintf("Homework %d", i+1)
		if lesson != nil && i < len(lesson.Homework) {
			out.Title = lesson.Homework[i].Title
		}
	case row.Kind == kindHomework:
		out.Title = "Homework"
	case lesson != nil:
		out.Title = lesson.Title + " · Level check"
	default:
		out.Title = "Level check"
	}
	if brief != nil {
		out.Title = brief.Title
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

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }

func textPtr(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{}
	}
	return text(*s)
}

func textValue(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

func uuids(ids []string) []pgtype.UUID {
	out := make([]pgtype.UUID, len(ids))
	for i, id := range ids {
		out[i], _ = parseUUID(id)
	}
	return out
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
