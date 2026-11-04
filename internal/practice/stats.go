package practice

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nazacd/makonbook-api/internal/practice/practicedb"
)

// Question statistics (spec 9.6): how many students got each question right,
// counted over every uploaded attempt that had the question, in any test or
// version (a question several tests use counts in all of them).

// StatsTTL is how long the counts are kept before they are counted again.
const StatsTTL = 5 * time.Minute

// QuestionStat is one question's counts: attempts that took its section
// (a blank answer counts as answered and wrong) and those graded correct.
type QuestionStat struct {
	ID       string `json:"id"`
	Answered int32  `json:"answered"`
	Correct  int32  `json:"correct"`
}

// QuestionStats lists the questions of a test's latest published version, in
// the package's order, with their counts. ErrNotFound if the test isn't
// published.
func (s *Service) QuestionStats(ctx context.Context, code string) ([]QuestionStat, error) {
	pkg, err := s.LatestPackage(ctx, code)
	if err != nil {
		return nil, err
	}
	counts, err := s.statsCounts(ctx)
	if err != nil {
		return nil, err
	}
	out := []QuestionStat{}
	for _, sec := range pkg.Sections {
		for _, m := range sec.Modules {
			for _, q := range m.Questions {
				c := counts[q.ID]
				out = append(out, QuestionStat{ID: q.ID, Answered: c.Answered, Correct: c.Correct})
			}
		}
	}
	return out, nil
}

// statsCounts is every question's counts, kept for StatsTTL.
func (s *Service) statsCounts(ctx context.Context) (map[string]QuestionStat, error) {
	s.stats.mu.Lock()
	defer s.stats.mu.Unlock()
	if s.stats.counts != nil && time.Since(s.stats.at) < StatsTTL {
		return s.stats.counts, nil
	}
	counts, err := countStats(ctx, practicedb.New(s.pool))
	if err != nil {
		return nil, err
	}
	s.stats.counts, s.stats.at = counts, time.Now()
	return counts, nil
}

// statsCache holds the latest counts; one count runs at a time.
type statsCache struct {
	mu     sync.Mutex
	at     time.Time
	counts map[string]QuestionStat
}

// countStats counts every question: answered from the sections attempts
// took, correct by grading each distinct answer with the key of the version
// the attempt used, as SaveAttempt graded it.
func countStats(ctx context.Context, q *practicedb.Queries) (map[string]QuestionStat, error) {
	type place struct {
		test             pgtype.UUID
		version          int32
		subject          string
		module, position int16
	}
	slots, err := q.StatsSlots(ctx)
	if err != nil {
		return nil, err
	}
	keys := make(map[place]PackageQuestion, len(slots))
	counts := map[string]QuestionStat{}
	for _, r := range slots {
		var pq PackageQuestion
		if err := json.Unmarshal(r.Question, &pq); err != nil {
			return nil, fmt.Errorf("stats: a question of version %d: %w", r.TestVersion, err)
		}
		keys[place{r.TestID, r.TestVersion, r.Subject, r.Module, r.Position}] = pq
		c := counts[pq.ID]
		c.ID = pq.ID
		c.Answered += r.Attempts
		counts[pq.ID] = c
	}
	answers, err := q.StatsAnswers(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range answers {
		pq, ok := keys[place{r.TestID, r.TestVersion, r.Subject, r.Module, r.Position}]
		if !ok || !pq.Correct(r.Answer) {
			continue
		}
		c := counts[pq.ID]
		c.Correct += r.Answers
		counts[pq.ID] = c
	}
	return counts, nil
}
