package practice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nazacd/makonbook-api/internal/practice/practicedb"
)

var (
	// ErrNotFound means no such test or question.
	ErrNotFound = errors.New("not found")
	// ErrConflict means someone saved the question since the editor opened it.
	ErrConflict = errors.New("the question changed since you opened it")
)

// InvalidError is an edit that doesn't pass the checks, field by field.
type InvalidError struct{ Fields map[string]string }

func (e *InvalidError) Error() string {
	return fmt.Sprintf("the question has %d problems", len(e.Fields))
}

// Service is what the admin panel reads and changes in the question bank.
type Service struct {
	pool    *pgxpool.Pool
	uploads Store // nil: uploads not configured
}

// NewService: uploads is MakonBook's image bucket, or nil.
func NewService(pool *pgxpool.Pool, uploads Store) *Service {
	return &Service{pool: pool, uploads: uploads}
}

// TestSummary is one test and how far its review has come.
type TestSummary struct {
	Code      string `json:"code"`
	Title     string `json:"title"`
	Questions int32  `json:"questions"`
	Approved  int32  `json:"approved"`
	Red       int32  `json:"red"`    // not yet approved
	Yellow    int32  `json:"yellow"` // not yet approved
	Shared    int32  `json:"shared"` // also used by other tests
	Open      bool   `json:"open"`   // every question approved: students can take it
}

// Tests lists every test with its review progress.
func (s *Service) Tests(ctx context.Context) ([]TestSummary, error) {
	rows, err := practicedb.New(s.pool).TestProgress(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]TestSummary, 0, len(rows))
	for _, r := range rows {
		out = append(out, TestSummary{Code: r.Code, Title: r.Title, Questions: r.Questions, Approved: r.Approved,
			Red: r.Red, Yellow: r.Yellow, Shared: r.Shared, Open: r.Approved == r.Questions})
	}
	return out, nil
}

// QuestionSummary is a question in a list.
type QuestionSummary struct {
	ID       string  `json:"id"`
	Subject  string  `json:"subject"`
	Type     string  `json:"type"`
	Status   string  `json:"status"`
	AIRisk   *string `json:"ai_risk"`
	Preview  string  `json:"preview"` // the start of the question, as plain text
	Version  int32   `json:"version"`
	Places   []Place `json:"places"`
	Position *Place  `json:"position,omitempty"` // in a test's list: this slot
}

// Place is where a question is used.
type Place struct {
	Test     string `json:"test"`
	Subject  string `json:"subject"`
	Module   int16  `json:"module"`
	Position int16  `json:"position"`
}

// TestDetail is a test with its questions in order (its slots).
type TestDetail struct {
	TestSummary
	Slots []QuestionSummary `json:"slots"`
}

// Test returns a test with its questions, by code (e.g. DAY12).
func (s *Service) Test(ctx context.Context, code string) (TestDetail, error) {
	q := practicedb.New(s.pool)
	t, err := q.GetTestByCode(ctx, code)
	if errors.Is(err, pgx.ErrNoRows) {
		return TestDetail{}, ErrNotFound
	}
	if err != nil {
		return TestDetail{}, err
	}
	all, err := s.Tests(ctx)
	if err != nil {
		return TestDetail{}, err
	}
	d := TestDetail{Slots: []QuestionSummary{}}
	for _, sum := range all {
		if sum.Code == t.Code {
			d.TestSummary = sum
		}
	}
	slots, err := q.TestSlots(ctx, t.ID)
	if err != nil {
		return TestDetail{}, err
	}
	ids := make([]pgtype.UUID, 0, len(slots))
	for _, sl := range slots {
		ids = append(ids, sl.ID)
	}
	places, err := s.places(ctx, q, ids)
	if err != nil {
		return TestDetail{}, err
	}
	for _, sl := range slots {
		d.Slots = append(d.Slots, QuestionSummary{
			ID: uuidString(sl.ID), Subject: sl.Subject, Type: sl.Type, Status: sl.Status, AIRisk: textPtr(sl.AiRisk),
			Preview: preview(sl.Passage, sl.Stem), Version: sl.Version, Places: places[uuidString(sl.ID)],
			Position: &Place{Test: t.Code, Subject: sl.Subject, Module: sl.Module, Position: sl.Position},
		})
	}
	return d, nil
}

// Filter narrows a question list; empty fields don't filter.
type Filter struct {
	Risk, Status, Subject string
	Limit, Offset         int32
}

// Questions lists questions, red first, then yellow, then green.
func (s *Service) Questions(ctx context.Context, f Filter) ([]QuestionSummary, int32, error) {
	q := practicedb.New(s.pool)
	risk, status, subject := optText(f.Risk), optText(f.Status), optText(f.Subject)
	total, err := q.CountQuestions(ctx, practicedb.CountQuestionsParams{Risk: risk, Status: status, Subject: subject})
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.ListQuestions(ctx, practicedb.ListQuestionsParams{
		Risk: risk, Status: status, Subject: subject, Lim: f.Limit, Off: f.Offset,
	})
	if err != nil {
		return nil, 0, err
	}
	ids := make([]pgtype.UUID, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	places, err := s.places(ctx, q, ids)
	if err != nil {
		return nil, 0, err
	}
	out := make([]QuestionSummary, 0, len(rows))
	for _, r := range rows {
		out = append(out, QuestionSummary{
			ID: uuidString(r.ID), Subject: r.Subject, Type: r.Type, Status: r.Status, AIRisk: textPtr(r.AiRisk),
			Preview: preview(r.Passage, r.Stem), Version: r.Version, Places: places[uuidString(r.ID)],
		})
	}
	return out, total, nil
}

func (s *Service) places(ctx context.Context, q *practicedb.Queries, ids []pgtype.UUID) (map[string][]Place, error) {
	rows, err := q.QuestionPlaces(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := map[string][]Place{}
	for _, r := range rows {
		id := uuidString(r.QuestionID)
		out[id] = append(out[id], Place{Test: r.Code, Subject: r.Subject, Module: r.Module, Position: r.Position})
	}
	return out, nil
}

// Content is what an editor sees and changes.
type Content struct {
	Passage     *string           `json:"passage"`
	Stem        string            `json:"stem"`
	Choices     map[string]string `json:"choices"` // null for grid-in
	Figures     []Figure          `json:"figures"`
	Correct     *string           `json:"correct"`  // mcq
	Accepted    []string          `json:"accepted"` // grid-in
	Explanation string            `json:"explanation"`
	Domain      string            `json:"domain"`
	Skill       string            `json:"skill"`
	Difficulty  *string           `json:"difficulty"`
}

// Question is everything about one question: content, where it is used,
// its AI reviews and its versions.
type Question struct {
	ID         string     `json:"id"`
	Subject    string     `json:"subject"`
	Type       string     `json:"type"`
	Status     string     `json:"status"`
	AIRisk     *string    `json:"ai_risk"`
	Version    int32      `json:"version"`
	UpdatedAt  time.Time  `json:"updated_at"`
	ApprovedAt *time.Time `json:"approved_at"`
	// AutoApproved: approved on import because its AI review was green.
	AutoApproved bool `json:"auto_approved"`
	// ApprovedBy: the person who approved it in the editor.
	ApprovedBy *string `json:"approved_by"`
	Content
	Places   []Place    `json:"places"`
	Reviews  []AIReview `json:"reviews"`
	Versions []Version  `json:"versions"`
}

// AIReview is the AI review of the question in one test.
type AIReview struct {
	Test       string          `json:"test"`
	QID        string          `json:"qid"`
	Risk       string          `json:"risk"`
	Verdict    string          `json:"verdict"`
	Record     json.RawMessage `json:"record"` // the review's whole record
	ImportedAt time.Time       `json:"imported_at"`
}

// Version is one saved version of the question.
type Version struct {
	Version   int32           `json:"version"`
	Source    string          `json:"source"` // import | editor
	CreatedBy *string         `json:"created_by"`
	CreatedAt time.Time       `json:"created_at"`
	Content   json.RawMessage `json:"content"`
}

// Question returns one question with its reviews and versions.
func (s *Service) Question(ctx context.Context, id string) (Question, error) {
	uid, ok := parseUUID(id)
	if !ok {
		return Question{}, ErrNotFound
	}
	q := practicedb.New(s.pool)
	row, err := q.GetQuestion(ctx, uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return Question{}, ErrNotFound
	}
	if err != nil {
		return Question{}, err
	}
	out := Question{
		ID: id, Subject: row.Subject, Type: row.Type, Status: row.Status, AIRisk: textPtr(row.AiRisk),
		Version: row.Version, UpdatedAt: row.UpdatedAt.Time, AutoApproved: row.AutoApproved, ApprovedBy: textPtr(row.ApprovedBy),
		Content: Content{
			Passage: textPtr(row.Passage), Stem: row.Stem, Correct: textPtr(row.Correct), Accepted: row.Accepted,
			Explanation: row.Explanation, Domain: row.Domain, Skill: row.Skill, Difficulty: textPtr(row.Difficulty),
		},
		Reviews: []AIReview{}, Versions: []Version{},
	}
	if row.ApprovedAt.Valid {
		out.ApprovedAt = &row.ApprovedAt.Time
	}
	if row.Choices != nil {
		if err := json.Unmarshal(row.Choices, &out.Choices); err != nil {
			return Question{}, fmt.Errorf("choices: %w", err)
		}
	}
	if err := json.Unmarshal(row.Figures, &out.Figures); err != nil {
		return Question{}, fmt.Errorf("figures: %w", err)
	}
	places, err := s.places(ctx, q, []pgtype.UUID{uid})
	if err != nil {
		return Question{}, err
	}
	out.Places = places[id]
	if out.Places == nil {
		out.Places = []Place{}
	}
	reviews, err := q.QuestionReviews(ctx, uid)
	if err != nil {
		return Question{}, err
	}
	for _, r := range reviews {
		out.Reviews = append(out.Reviews, AIReview{Test: r.Code, QID: r.Qid, Risk: r.Risk, Verdict: r.Verdict,
			Record: r.Record, ImportedAt: r.ImportedAt.Time})
	}
	versions, err := q.QuestionVersions(ctx, uid)
	if err != nil {
		return Question{}, err
	}
	for _, v := range versions {
		out.Versions = append(out.Versions, Version{Version: v.Version, Source: v.Source, CreatedBy: textPtr(v.CreatedBy),
			CreatedAt: v.CreatedAt.Time, Content: v.Content})
	}
	return out, nil
}

// Edit is an editor's change: the full content, and the version it was made on.
type Edit struct {
	BaseVersion int32             `json:"version"`
	Passage     *string           `json:"passage"`
	Stem        string            `json:"stem"`
	Choices     map[string]string `json:"choices"`
	Figures     []Figure          `json:"figures"`
	Correct     string            `json:"correct"`
	Accepted    []string          `json:"accepted"`
	Explanation string            `json:"explanation"`
	Domain      string            `json:"domain"`
	Skill       string            `json:"skill"`
	Difficulty  string            `json:"difficulty"`
}

// Update saves an edit as a new version. An approved question goes back to
// review. ErrConflict if someone saved it since BaseVersion; *InvalidError if
// the content doesn't pass the checks.
func (s *Service) Update(ctx context.Context, id, editor string, e Edit) (Question, error) {
	uid, ok := parseUUID(id)
	if !ok {
		return Question{}, ErrNotFound
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := practicedb.New(tx)
		cur, err := q.LockQuestion(ctx, uid)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if cur.Version != e.BaseVersion {
			return ErrConflict
		}
		e.normalize(cur.Type)
		bad := e.Validate(cur.Subject, cur.Type)
		missing, err := missingUploads(ctx, q, e.Figures)
		if err != nil {
			return err
		}
		if len(missing) > 0 {
			bad["figures"] = "this image was never uploaded: " + missing[0]
		}
		if len(bad) > 0 {
			return &InvalidError{Fields: bad}
		}
		figures, _ := json.Marshal(e.Figures)
		p := practicedb.UpdateQuestionParams{
			ID: uid, Passage: optText(deref(e.Passage)), Stem: e.Stem, Figures: figures, Explanation: e.Explanation,
			Domain: e.Domain, Skill: e.Skill, Difficulty: optText(e.Difficulty),
		}
		content := map[string]any{
			"passage": e.Passage, "stem": e.Stem, "figures": e.Figures, "explanation": e.Explanation,
			"domain": e.Domain, "skill": e.Skill, "difficulty": e.Difficulty,
		}
		if cur.Type == "mcq" {
			p.Choices, _ = json.Marshal(e.Choices)
			p.Correct = optText(e.Correct)
			content["choices"], content["correct"] = e.Choices, e.Correct
		} else {
			p.Accepted = e.Accepted
			content["accepted"] = e.Accepted
		}
		version, err := q.UpdateQuestion(ctx, p)
		if err != nil {
			return err
		}
		snapshot, _ := json.Marshal(content)
		return q.InsertVersion(ctx, practicedb.InsertVersionParams{
			QuestionID: uid, Version: version, Content: snapshot, Source: "editor", CreatedBy: optText(editor),
		})
	})
	if err != nil {
		return Question{}, err
	}
	return s.Question(ctx, id)
}

// SetApproval approves a question as the version the person saw, or takes an
// approval back. ErrConflict if it changed since; approving runs the checks
// a save does (*InvalidError), so a question without a key can't pass.
func (s *Service) SetApproval(ctx context.Context, id, by string, version int32, approved bool) (Question, error) {
	uid, ok := parseUUID(id)
	if !ok {
		return Question{}, ErrNotFound
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := practicedb.New(tx)
		cur, err := q.LockQuestion(ctx, uid)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if cur.Version != version {
			return ErrConflict
		}
		if !approved {
			return q.UnapproveQuestion(ctx, uid)
		}
		e, err := editOf(cur)
		if err != nil {
			return err
		}
		e.normalize(cur.Type)
		if bad := e.Validate(cur.Subject, cur.Type); len(bad) > 0 {
			return &InvalidError{Fields: bad}
		}
		return q.ApproveQuestion(ctx, practicedb.ApproveQuestionParams{ID: uid, ApprovedBy: optText(by)})
	})
	if err != nil {
		return Question{}, err
	}
	return s.Question(ctx, id)
}

// editOf is a stored question as an edit of itself, to check it.
func editOf(row practicedb.Question) (Edit, error) {
	e := Edit{
		BaseVersion: row.Version, Passage: textPtr(row.Passage), Stem: row.Stem, Correct: row.Correct.String,
		Accepted: row.Accepted, Explanation: row.Explanation, Domain: row.Domain, Skill: row.Skill,
		Difficulty: row.Difficulty.String,
	}
	if row.Choices != nil {
		if err := json.Unmarshal(row.Choices, &e.Choices); err != nil {
			return e, fmt.Errorf("choices: %w", err)
		}
	}
	if err := json.Unmarshal(row.Figures, &e.Figures); err != nil {
		return e, fmt.Errorf("figures: %w", err)
	}
	return e, nil
}

// normalize trims what doesn't matter, so a blank passage is no passage.
func (e *Edit) normalize(qtype string) {
	if e.Passage != nil && strings.TrimSpace(*e.Passage) == "" {
		e.Passage = nil
	}
	if e.Figures == nil {
		e.Figures = []Figure{}
	}
	var accepted []string
	for _, a := range e.Accepted {
		if a = strings.TrimSpace(a); a != "" {
			accepted = append(accepted, a)
		}
	}
	e.Accepted = accepted
	if qtype != "mcq" {
		e.Choices, e.Correct = nil, ""
	} else {
		e.Accepted = nil
	}
}

var fracRE = regexp.MustCompile(`\\[dt]?frac\{([^{}]*)\}\{([^{}]*)\}`)

func preview(passage pgtype.Text, stem string) string {
	text := stem
	if passage.Valid && passage.String != "" {
		text = passage.String
	}
	text = imageRE.ReplaceAllString(text, "[figure]")
	text = fracRE.ReplaceAllString(text, "$1/$2")
	text = strings.NewReplacer(`\(`, "", `\)`, "", `\[`, "", `\]`, "", "*", "", "<u>", "", "</u>", "", `\`, "").Replace(text)
	text = spaces.ReplaceAllString(text, " ")
	if utf8.RuneCountInString(text) > 140 {
		text = string([]rune(text)[:140]) + "…"
	}
	return strings.TrimSpace(text)
}

func optText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}

func textPtr(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
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
