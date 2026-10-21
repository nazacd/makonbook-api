package vocab

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nazacd/makonbook-api/internal/vocab/vocabdb"
)

var (
	// ErrNotTeacher means the teacher doesn't teach the group.
	ErrNotTeacher = errors.New("you don't teach this group")
	// ErrImagesOff means the server has no bucket to link images from.
	ErrImagesOff = errors.New("word images aren't set up on the server")
)

// ReviewsError is a batch of reviews that can't be stored.
type ReviewsError struct{ Reason string }

func (e *ReviewsError) Error() string { return e.Reason }

// LinkTTL is how long image links work.
const LinkTTL = 6 * time.Hour

// MaxBatch is the most reviews one upload may carry.
const MaxBatch = 200

// Signer makes download links to MakonBook's bucket.
type Signer interface {
	PresignGet(key string, expires time.Duration) (string, error)
}

// Service is what the apps read and send.
type Service struct {
	pool   *pgxpool.Pool
	signer Signer // nil: images not configured
}

func NewService(pool *pgxpool.Pool, signer Signer) *Service {
	return &Service{pool: pool, signer: signer}
}

// Deck is every list students can study.
type Deck struct {
	Version string `json:"version"`
	Lists   []List `json:"lists"`
}

type List struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
	Words []Word `json:"words"`
}

type Word struct {
	ID           string  `json:"id"`
	Word         string  `json:"word"`
	PartOfSpeech *string `json:"part_of_speech"`
	Definition   string  `json:"definition"`
	Example      string  `json:"example"`
	Image        *Image  `json:"image"`
}

// Image is a word's picture, downloaded beside the deck.
type Image struct {
	File   string `json:"file"`
	Bytes  int32  `json:"bytes"`
	Width  int32  `json:"width"`
	Height int32  `json:"height"`
}

func buildDeck(ctx context.Context, q *vocabdb.Queries) (Deck, error) {
	rows, err := q.DeckWords(ctx)
	if err != nil {
		return Deck{}, err
	}
	d := Deck{Lists: []List{}}
	for _, r := range rows {
		if n := len(d.Lists); n == 0 || d.Lists[n-1].Slug != r.Slug {
			d.Lists = append(d.Lists, List{Slug: r.Slug, Title: r.Title, Words: []Word{}})
		}
		w := Word{ID: uuidString(r.ID), Word: r.Word, Definition: r.Definition, Example: r.Example}
		if r.PartOfSpeech.Valid {
			w.PartOfSpeech = &r.PartOfSpeech.String
		}
		if r.Image.Valid {
			w.Image = &Image{File: r.Image.String, Bytes: r.ImageBytes.Int32, Width: r.ImageWidth.Int32, Height: r.ImageHeight.Int32}
		}
		l := &d.Lists[len(d.Lists)-1]
		l.Words = append(l.Words, w)
	}
	return d, nil
}

func deckVersion(d Deck) string {
	d.Version = ""
	b, _ := json.Marshal(d)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// Deck returns the deck, or only its version when the phone already has it
// (have == the current version).
func (s *Service) Deck(ctx context.Context, have string) (Deck, bool, error) {
	q := vocabdb.New(s.pool)
	version, err := q.DeckVersion(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return Deck{Lists: []List{}}, false, nil // nothing imported yet
	}
	if err != nil {
		return Deck{}, false, err
	}
	if have != "" && have == version {
		return Deck{Version: version}, true, nil
	}
	d, err := buildDeck(ctx, q)
	if err != nil {
		return Deck{}, false, err
	}
	d.Version = version
	return d, false, nil
}

// Images returns a link to each image of a list, valid until the time given.
func (s *Service) Images(ctx context.Context, slug string) (map[string]string, time.Time, error) {
	files, err := vocabdb.New(s.pool).ListImages(ctx, slug)
	if err != nil {
		return nil, time.Time{}, err
	}
	out := map[string]string{}
	expires := time.Now().Add(LinkTTL).UTC().Truncate(time.Second)
	if len(files) == 0 {
		return out, expires, nil
	}
	if s.signer == nil {
		return nil, time.Time{}, ErrImagesOff
	}
	for _, f := range files {
		if out[f], err = s.signer.PresignGet(ImagePrefix+f, LinkTTL); err != nil {
			return nil, time.Time{}, err
		}
	}
	return out, expires, nil
}

// Review is one review as the phone made it: the rating, and the card's
// FSRS state after it.
type Review struct {
	Seq           int64     `json:"seq,omitempty"` // the server's order; set in answers only
	ID            string    `json:"id"`
	WordID        string    `json:"word_id"`
	Rating        int16     `json:"rating"` // 1 again, 2 hard, 3 good, 4 easy
	ReviewedAt    time.Time `json:"reviewed_at"`
	DurationMs    int32     `json:"duration_ms"`
	State         int16     `json:"state"` // 0 new, 1 learning, 2 review, 3 relearning
	Due           time.Time `json:"due"`
	Stability     float32   `json:"stability"`
	Difficulty    float32   `json:"difficulty"`
	ScheduledDays int32     `json:"scheduled_days"`
}

// SaveReviews stores a student's reviews. A review already stored (sent
// again after a lost answer) is skipped. It returns how many were new.
func (s *Service) SaveReviews(ctx context.Context, student int32, reviews []Review) (int, error) {
	if len(reviews) > MaxBatch {
		return 0, &ReviewsError{fmt.Sprintf("at most %d reviews at a time", MaxBatch)}
	}
	ids := make([]pgtype.UUID, 0, len(reviews))
	type row struct {
		id, word pgtype.UUID
		r        Review
	}
	rows := make([]row, 0, len(reviews))
	soon := time.Now().Add(24 * time.Hour)
	for i, r := range reviews {
		id, ok1 := parseUUID(r.ID)
		word, ok2 := parseUUID(r.WordID)
		switch {
		case !ok1 || !ok2:
			return 0, &ReviewsError{fmt.Sprintf("review %d: id and word_id must be UUIDs", i+1)}
		case r.Rating < 1 || r.Rating > 4:
			return 0, &ReviewsError{fmt.Sprintf("review %d: rating must be 1–4", i+1)}
		case r.State < 0 || r.State > 3:
			return 0, &ReviewsError{fmt.Sprintf("review %d: state must be 0–3", i+1)}
		case r.ReviewedAt.IsZero() || r.ReviewedAt.After(soon) || r.Due.IsZero():
			return 0, &ReviewsError{fmt.Sprintf("review %d: reviewed_at and due are required, reviewed_at not in the future", i+1)}
		case r.DurationMs < 0 || r.ScheduledDays < 0 || !finite(r.Stability) || !finite(r.Difficulty):
			return 0, &ReviewsError{fmt.Sprintf("review %d: duration_ms, scheduled_days, stability and difficulty must be valid numbers", i+1)}
		}
		ids = append(ids, word)
		rows = append(rows, row{id, word, r})
	}
	q := vocabdb.New(s.pool)
	known, err := q.KnownWords(ctx, ids)
	if err != nil {
		return 0, err
	}
	have := map[[16]byte]bool{}
	for _, k := range known {
		have[k.Bytes] = true
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qt := q.WithTx(tx)
	added := 0
	for i, r := range rows {
		if !have[r.word.Bytes] {
			return 0, &ReviewsError{fmt.Sprintf("review %d: no such word", i+1)}
		}
		n, err := qt.InsertReview(ctx, vocabdb.InsertReviewParams{
			ID: r.id, StudentClientID: student, WordID: r.word, Rating: r.r.Rating,
			ReviewedAt: ts(r.r.ReviewedAt), DurationMs: min(r.r.DurationMs, 3_600_000), State: r.r.State, Due: ts(r.r.Due),
			Stability: r.r.Stability, Difficulty: r.r.Difficulty, ScheduledDays: r.r.ScheduledDays,
		})
		if err != nil {
			return 0, err
		}
		added += int(n)
	}
	return added, tx.Commit(ctx)
}

// Reviews returns a student's reviews the server received after seq, oldest
// first, at most limit of them.
func (s *Service) Reviews(ctx context.Context, student int32, after int64, limit int32) ([]Review, error) {
	rows, err := vocabdb.New(s.pool).StudentReviews(ctx, vocabdb.StudentReviewsParams{StudentClientID: student, After: after, Lim: limit})
	if err != nil {
		return nil, err
	}
	out := make([]Review, 0, len(rows))
	for _, r := range rows {
		out = append(out, Review{
			Seq: r.Seq, ID: uuidString(r.ID), WordID: uuidString(r.WordID), Rating: r.Rating,
			ReviewedAt: r.ReviewedAt.Time.UTC(), DurationMs: r.DurationMs, State: r.State, Due: r.Due.Time.UTC(),
			Stability: r.Stability, Difficulty: r.Difficulty, ScheduledDays: r.ScheduledDays,
		})
	}
	return out, nil
}

// StudentProgress is one student's vocabulary, for their teacher.
type StudentProgress struct {
	ClientID  int32  `json:"client_id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	// Started: words reviewed at least once. Learned: words whose next
	// review is 21 days or more away (Anki's "mature").
	Started     int32      `json:"started"`
	Learned     int32      `json:"learned"`
	ReviewsWeek int32      `json:"reviews_week"`
	LastReview  *time.Time `json:"last_review"`
}

// GroupProgress is the vocabulary of a group's students, for one of its
// teachers (today: the school's date). ErrNotTeacher otherwise.
func (s *Service) GroupProgress(ctx context.Context, teacher, group int32, today time.Time) ([]StudentProgress, int32, error) {
	q := vocabdb.New(s.pool)
	day := pgtype.Date{Time: today, Valid: true}
	ok, err := q.TeachesGroup(ctx, vocabdb.TeachesGroupParams{GroupID: group, TeacherID: teacher, Today: day})
	if err != nil {
		return nil, 0, err
	}
	if !ok {
		return nil, 0, ErrNotTeacher
	}
	rows, err := q.GroupProgress(ctx, vocabdb.GroupProgressParams{GroupID: group, Today: day})
	if err != nil {
		return nil, 0, err
	}
	total, err := q.CountWords(ctx)
	if err != nil {
		return nil, 0, err
	}
	out := make([]StudentProgress, 0, len(rows))
	for _, r := range rows {
		p := StudentProgress{ClientID: r.ClientID, FirstName: r.FirstName, LastName: r.LastName,
			Started: r.Started, Learned: r.Learned, ReviewsWeek: r.ReviewsWeek}
		if r.LastReview.Valid {
			t := r.LastReview.Time.UTC()
			p.LastReview = &t
		}
		out = append(out, p)
	}
	return out, total, nil
}

func finite(f float32) bool {
	return !math.IsNaN(float64(f)) && !math.IsInf(float64(f), 0) && f >= 0
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func optText(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

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
