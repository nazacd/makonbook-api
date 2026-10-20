// Package practice holds the practice tests and their question bank (spec 9).
//
// Questions arrive from the AI review of SAT MAKON's bank (the private
// makonbook-questionbank repository, one folder per test with a
// questions.jsonl). Import loads a test: each question is stored once, even
// when several tests use it, together with every AI review of it. Everything
// starts in review; a test opens to students only when all its questions
// are approved.
package practice

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/text/unicode/norm"

	"github.com/nazacd/makonbook-api/internal/practice/practicedb"
)

// Questions per module, as on the digital SAT.
var moduleSize = map[string]int{"math": 22, "english": 27}

var (
	// ErrImported means the test is already loaded; reloading it needs Replace.
	ErrImported = errors.New("already imported")
	// ErrTouched means people have approved or edited questions of the test,
	// so reloading it would throw their work away.
	ErrTouched = errors.New("questions of this test were already approved or edited")
)

// Record is one line of a review's questions.jsonl (the fields we keep; the
// whole line is stored too).
type Record struct {
	QID    string `json:"qid"`
	Source struct {
		Subject string `json:"subject"`
		CSVID   int32  `json:"csv_id"`
		TestID  string `json:"test_id"`
		Module  string `json:"module"`
		Number  int    `json:"number"`
	} `json:"source"`
	Type     string            `json:"type"`
	Passage  *string           `json:"passage"`
	Stem     string            `json:"stem"`
	Choices  map[string]string `json:"choices"`
	Figures  []Figure          `json:"figures"`
	Key      string            `json:"key"`
	Accepted []string          `json:"accepted"`
	Verdict  string            `json:"verdict"`
	// Fix is the fix step's corrected version (see tools/prompts/fix.md);
	// the importer only needs the figures it drew.
	Fix *struct {
		Figures []Figure `json:"figures"`
	} `json:"fix"`
	Risk        string `json:"risk"`
	Explanation struct {
		Text string `json:"text"`
	} `json:"explanation"`
	Tags struct {
		Domain     string `json:"domain"`
		Skill      string `json:"skill"`
		Difficulty string `json:"difficulty"`
	} `json:"tags"`

	raw json.RawMessage
}

// Figure is an image kept as an image (graph, chart, geometry), by its R2 key.
type Figure struct {
	Key  string `json:"key"`
	Kind string `json:"kind"`
	Alt  string `json:"alt"`
}

// Test is one reviewed test read from disk.
type Test struct {
	Code    string // the bank's test id without spaces, e.g. DAY12
	Path    string
	Records []Record
}

// Summary says what an import did.
type Summary struct {
	Code      string
	Questions int
	New       int // stored for the first time
	Repeats   int // already stored from another test (or earlier in this one)
	Risk      map[string]int
	Replaced  bool
	Refreshed int // a loaded test: reviews the fix step added to
}

var spaces = regexp.MustCompile(`\s+`)

const oldFormat = "the pilot's old format: re-run this test's review with the current tools"

// Find returns the questions.jsonl files under path: path itself, a test
// folder, or a folder of test folders (the whole questionbank checkout).
func Find(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{path}, nil
	}
	if _, err := os.Stat(filepath.Join(path, "questions.jsonl")); err == nil {
		return []string{filepath.Join(path, "questions.jsonl")}, nil
	}
	found, err := filepath.Glob(filepath.Join(path, "*", "questions.jsonl"))
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("%s: no questions.jsonl here or in its folders", path)
	}
	sort.Slice(found, func(i, j int) bool { return testOrder(found[i]) < testOrder(found[j]) })
	return found, nil
}

// testOrder sorts DAY2 before DAY10.
func testOrder(path string) string {
	name := filepath.Base(filepath.Dir(path))
	return fmt.Sprintf("%04d%s", len(name), name)
}

// Read parses and checks one test's questions.jsonl.
func Read(path string) (Test, error) {
	f, err := os.Open(path) //nolint:gosec // an operator-given path, read only
	if err != nil {
		return Test{}, err
	}
	defer func() { _ = f.Close() }()
	t, err := parse(f, path)
	if err != nil {
		return t, err
	}
	return t, t.addOriginals(filepath.Join(filepath.Dir(path), "original.jsonl"))
}

// addOriginals puts the bank's own text of each question (original.jsonl
// next to questions.jsonl, from the questionbank) into its record, as
// "original", for the teachers' comparison. No file: nothing to add.
func (t *Test) addOriginals(path string) error {
	data, err := os.ReadFile(path) //nolint:gosec // next to the operator-given test
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	byQID := map[string]json.RawMessage{}
	for n, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var o struct {
			QID string `json:"qid"`
		}
		if err := json.Unmarshal([]byte(line), &o); err != nil || o.QID == "" {
			return fmt.Errorf("%s line %d: not a question's original", path, n+1)
		}
		byQID[o.QID] = json.RawMessage(line)
	}
	for i, r := range t.Records {
		o, ok := byQID[r.QID]
		if !ok {
			continue
		}
		var rec map[string]json.RawMessage
		if err := json.Unmarshal(r.raw, &rec); err != nil {
			return err
		}
		rec["original"] = o
		if t.Records[i].raw, err = json.Marshal(rec); err != nil {
			return err
		}
	}
	return nil
}

func parse(r io.Reader, path string) (Test, error) {
	t := Test{Path: path}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	seen := map[string]bool{}
	slots := map[string]bool{}
	count := map[string]int{}
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			if !strings.Contains(line, `"qid"`) {
				return t, errors.New(oldFormat)
			}
			return t, fmt.Errorf("line %d: %w", n, err)
		}
		rec.raw = json.RawMessage(line)
		if err := rec.check(); err != nil {
			return t, fmt.Errorf("line %d (%s): %w", n, rec.QID, err)
		}
		code := spaces.ReplaceAllString(rec.Source.TestID, "")
		if t.Code == "" {
			t.Code = code
		} else if code != t.Code {
			return t, fmt.Errorf("line %d: test %q in a file of test %q", n, code, t.Code)
		}
		slot := fmt.Sprintf("%s/%s/%d", rec.Source.Subject, rec.Source.Module, rec.Source.Number)
		if seen[rec.QID] || slots[slot] {
			return t, fmt.Errorf("line %d: %s appears twice", n, rec.QID)
		}
		seen[rec.QID], slots[slot] = true, true
		count[rec.Source.Subject+"/"+rec.Source.Module]++
		t.Records = append(t.Records, rec)
	}
	if err := sc.Err(); err != nil {
		return t, err
	}
	if len(t.Records) == 0 {
		return t, errors.New("no questions")
	}
	for key, n := range count {
		if want := moduleSize[strings.Split(key, "/")[0]]; n != want {
			return t, fmt.Errorf("%s has %d questions, want %d", key, n, want)
		}
	}
	return t, nil
}

func (r Record) check() error {
	switch {
	case r.QID == "":
		return errors.New(oldFormat)
	case r.Source.Subject != "math" && r.Source.Subject != "english":
		return fmt.Errorf("subject %q", r.Source.Subject)
	case r.Source.Module != "module_1" && r.Source.Module != "module_2":
		return fmt.Errorf("module %q", r.Source.Module)
	case r.Source.Number < 1 || r.Source.Number > moduleSize[r.Source.Subject]:
		return fmt.Errorf("number %d", r.Source.Number)
	case r.Source.TestID == "":
		return errors.New("no test id")
	case r.Risk != "green" && r.Risk != "yellow" && r.Risk != "red":
		return fmt.Errorf("risk %q", r.Risk)
	case r.Explanation.Text == "" || r.Tags.Domain == "" || r.Tags.Skill == "":
		return errors.New("explanation or tags missing")
	}
	switch r.Type {
	case "mcq":
		for _, l := range []string{"A", "B", "C", "D"} {
			if _, ok := r.Choices[l]; !ok {
				return fmt.Errorf("choice %s missing", l)
			}
		}
	case "grid_in":
		if len(r.Accepted) == 0 {
			return errors.New("grid-in without accepted answers")
		}
	default:
		return fmt.Errorf("type %q", r.Type)
	}
	return nil
}

var (
	figureRef = regexp.MustCompile(`!\[[^\]]*\]\(r2:([^)]*)\)`)
	quotes    = strings.NewReplacer("’", "'", "‘", "'", "“", `"`, "”", `"`, "—", "-", "–", "-")
)

// ContentHash identifies a question by what a student sees, so the same
// question in two tests is stored once. Case, spacing, curly quotes and a
// figure's alt text don't count; punctuation does (grammar questions differ
// by a comma).
func (r Record) ContentHash() string {
	var b strings.Builder
	if r.Passage != nil {
		b.WriteString(*r.Passage)
	}
	b.WriteString("\x1f" + r.Stem)
	for _, l := range []string{"A", "B", "C", "D"} {
		b.WriteString("\x1f" + r.Choices[l])
	}
	text := figureRef.ReplaceAllString(b.String(), "[$1]")
	text = quotes.Replace(norm.NFKC.String(text))
	text = spaces.ReplaceAllString(strings.ToLower(text), "")
	sum := sha256.Sum256([]byte(r.Source.Subject + "\x1f" + r.Type + "\x1f" + text))
	return hex.EncodeToString(sum[:])
}

// Importer loads reviewed tests into the database.
type Importer struct {
	Pool *pgxpool.Pool
	// Uploads is MakonBook's bucket, for the figures the fix step drew
	// (the questionbank's figures/); a test with any needs it.
	Uploads Store
}

// Import loads one test in a single transaction. A test already loaded gets
// what the fix step added to its reviews (Refreshed), or is refused with
// ErrImported when there is nothing new, unless replace is set and nobody has
// approved or edited any of its questions yet.
func (im Importer) Import(ctx context.Context, t Test, replace bool) (Summary, error) {
	sum := Summary{Code: t.Code, Questions: len(t.Records), Risk: map[string]int{}}
	if err := im.storeFigures(ctx, t); err != nil {
		return sum, err
	}
	err := pgx.BeginFunc(ctx, im.Pool, func(tx pgx.Tx) error {
		q := practicedb.New(tx)
		old, err := q.GetTestByCode(ctx, t.Code)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return err
		case !replace:
			n, err := refreshReviews(ctx, q, old.ID, t)
			if err != nil {
				return err
			}
			if n == 0 {
				return ErrImported
			}
			sum.Refreshed = n
			return nil
		default:
			touched, err := q.TouchedQuestions(ctx, old.ID)
			if err != nil {
				return err
			}
			if touched > 0 {
				return fmt.Errorf("%w (%d)", ErrTouched, touched)
			}
			if err := q.DeleteTest(ctx, old.ID); err != nil {
				return err
			}
			if _, err := q.DeleteOrphanQuestions(ctx); err != nil {
				return err
			}
			sum.Replaced = true
		}

		test, err := q.CreateTest(ctx, practicedb.CreateTestParams{Code: t.Code, Title: t.Code})
		if err != nil {
			return err
		}
		for _, r := range t.Records {
			id, isNew, err := storeQuestion(ctx, q, r)
			if err != nil {
				return fmt.Errorf("%s: %w", r.QID, err)
			}
			if isNew {
				sum.New++
			} else {
				sum.Repeats++
			}
			sum.Risk[r.Risk]++
			module := int16(1)
			if r.Source.Module == "module_2" {
				module = 2
			}
			if err := q.LinkQuestion(ctx, practicedb.LinkQuestionParams{
				TestID: test.ID, Subject: r.Source.Subject, Module: module,
				Position: int16(r.Source.Number), QuestionID: id, //nolint:gosec // checked: 1..27
			}); err != nil {
				return fmt.Errorf("%s: %w", r.QID, err)
			}
			if err := q.InsertAIReview(ctx, practicedb.InsertAIReviewParams{
				QuestionID: id, TestID: test.ID, Qid: r.QID, CsvID: r.Source.CSVID,
				Risk: r.Risk, Verdict: r.Verdict, Record: r.raw,
			}); err != nil {
				return fmt.Errorf("%s: %w", r.QID, err)
			}
		}
		return nil
	})
	return sum, err
}

// refreshReviews puts a loaded test's records in again, where the fix step
// changed them.
func refreshReviews(ctx context.Context, q *practicedb.Queries, testID pgtype.UUID, t Test) (int, error) {
	n := 0
	for _, r := range t.Records {
		rows, err := q.RefreshAIReview(ctx, practicedb.RefreshAIReviewParams{
			Record: r.raw, TestID: testID, Qid: r.QID, Risk: r.Risk, Verdict: r.Verdict,
		})
		if err != nil {
			return n, fmt.Errorf("%s: %w", r.QID, err)
		}
		n += int(rows)
	}
	return n, nil
}

var drawnKey = regexp.MustCompile(`^mb/[0-9a-f]{40}\.png$`)

// storeFigures uploads the figures the fix step drew that aren't stored yet,
// from figures/ next to the test folders. The name is the image's hash, as
// for an editor's upload, so it is checked on the way.
func (im Importer) storeFigures(ctx context.Context, t Test) error {
	var keys []string
	for _, r := range t.Records {
		if r.Fix == nil {
			continue
		}
		for _, f := range r.Fix.Figures {
			if !drawnKey.MatchString(f.Key) {
				return fmt.Errorf("%s: fix figure %q isn't a drawn figure's name", r.QID, f.Key)
			}
			keys = append(keys, f.Key)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	have, err := practicedb.New(im.Pool).ExistingImages(ctx, keys)
	if err != nil {
		return err
	}
	stored := map[string]bool{}
	for _, k := range have {
		stored[k] = true
	}
	dir := filepath.Join(filepath.Dir(filepath.Dir(t.Path)), "figures")
	for _, key := range keys {
		if stored[key] {
			continue
		}
		if im.Uploads == nil {
			return fmt.Errorf("the fixes have figures to upload: set R2_UPLOADS_*")
		}
		data, err := os.ReadFile(filepath.Join(dir, path.Base(key))) //nolint:gosec // a checked name in the operator's checkout
		if err != nil {
			return fmt.Errorf("fix figure: %w", err)
		}
		img, err := storeImage(ctx, im.Pool, im.Uploads, data, AIFixer)
		if err != nil {
			return fmt.Errorf("fix figure %s: %w", key, err)
		}
		if img.Key != key {
			return fmt.Errorf("fix figure %s: its content doesn't match its name", key)
		}
		stored[key] = true
	}
	return nil
}

// storeQuestion inserts the question, or finds the stored one with the same
// content (and raises its risk if this review is worse).
func storeQuestion(ctx context.Context, q *practicedb.Queries, r Record) (pgtype.UUID, bool, error) {
	hash := r.ContentHash()
	figures := r.Figures
	if figures == nil {
		figures = []Figure{}
	}
	figJSON, _ := json.Marshal(figures)
	p := practicedb.InsertQuestionParams{
		Subject: r.Source.Subject, Type: r.Type, ContentHash: hash, Stem: r.Stem, Figures: figJSON,
		Explanation: r.Explanation.Text, Domain: r.Tags.Domain, Skill: r.Tags.Skill,
		Passage: text(r.Passage), Difficulty: text(&r.Tags.Difficulty), AiRisk: text(&r.Risk),
	}
	content := map[string]any{
		"passage": r.Passage, "stem": r.Stem, "figures": figures, "explanation": r.Explanation.Text,
		"domain": r.Tags.Domain, "skill": r.Tags.Skill, "difficulty": r.Tags.Difficulty,
	}
	if r.Type == "mcq" {
		p.Choices, _ = json.Marshal(r.Choices)
		p.Correct = pgtype.Text{String: r.Key, Valid: true} // may be "" when the bank has no key (the question is red)
		content["choices"], content["correct"] = r.Choices, r.Key
	} else {
		p.Accepted = r.Accepted
		content["accepted"] = r.Accepted
	}

	id, err := q.InsertQuestion(ctx, p)
	if errors.Is(err, pgx.ErrNoRows) {
		id, err = q.RaiseRisk(ctx, practicedb.RaiseRiskParams{AiRisk: r.Risk, ContentHash: hash})
		return id, false, err
	}
	if err != nil {
		return id, false, err
	}
	snapshot, _ := json.Marshal(content)
	err = q.InsertVersion(ctx, practicedb.InsertVersionParams{QuestionID: id, Version: 1, Content: snapshot, Source: "import"})
	return id, true, err
}

func text(s *string) pgtype.Text {
	if s == nil || *s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *s, Valid: true}
}
