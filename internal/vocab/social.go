package vocab

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nazacd/makonbook-api/internal/vocab/vocabdb"
)

// The word games (spec 11.4) ------------------------------------------------

// GameResult is a finished round of a word game, as the app sends it.
type GameResult struct {
	ID         string    `json:"id"`
	Game       string    `json:"game"`
	Score      int32     `json:"score"`
	Total      int32     `json:"total"`
	Mistakes   int32     `json:"mistakes"`
	DurationMs int32     `json:"duration_ms"`
	Missed     []string  `json:"missed"`
	PlayedAt   time.Time `json:"played_at"`
}

// GameRound is the most a round of each game can hold.
var GameRound = map[string]int32{"pairs": 10, "unscramble": 8, "gap": 10}

const (
	// DailyGamePoints is the most a day's games add to the leaderboard.
	DailyGamePoints = 100
	// PointsPerItem is what each right answer in a game is worth.
	PointsPerItem = 2
	// MaxGames is the most results one upload may carry.
	MaxGames = 50
)

// GamesError is a batch of results that can't be stored.
type GamesError struct{ Reason string }

func (e *GamesError) Error() string { return e.Reason }

// SaveGames stores a student's game results (a repeat is skipped) and gives
// each its points, keeping a day's games (school time) within DailyGamePoints.
func (s *Service) SaveGames(ctx context.Context, student int32, results []GameResult, loc *time.Location) (int, error) {
	if len(results) > MaxGames {
		return 0, &GamesError{fmt.Sprintf("at most %d results at a time", MaxGames)}
	}
	now := time.Now()
	type row struct {
		id     pgtype.UUID
		missed []pgtype.UUID
		g      GameResult
	}
	rows := make([]row, 0, len(results))
	for i, g := range results {
		id, ok := parseUUID(g.ID)
		limit, known := GameRound[g.Game]
		switch {
		case !ok:
			return 0, &GamesError{fmt.Sprintf("result %d: id must be a UUID", i+1)}
		case !known:
			return 0, &GamesError{fmt.Sprintf("result %d: game must be pairs, unscramble or gap", i+1)}
		case g.Total < 1 || g.Total > limit || g.Score < 0 || g.Score > g.Total || g.Mistakes < 0 || g.DurationMs < 0:
			return 0, &GamesError{fmt.Sprintf("result %d: score, total, mistakes and duration_ms must fit the game", i+1)}
		case g.PlayedAt.IsZero() || g.PlayedAt.After(now.Add(time.Hour)) || g.PlayedAt.Before(now.AddDate(0, 0, -30)):
			return 0, &GamesError{fmt.Sprintf("result %d: played_at must be within the last 30 days", i+1)}
		case len(g.Missed) > int(limit):
			return 0, &GamesError{fmt.Sprintf("result %d: too many missed words", i+1)}
		}
		missed := make([]pgtype.UUID, 0, len(g.Missed))
		for _, m := range g.Missed {
			u, ok := parseUUID(m)
			if !ok {
				return 0, &GamesError{fmt.Sprintf("result %d: missed must be word ids", i+1)}
			}
			missed = append(missed, u)
		}
		rows = append(rows, row{id, missed, g})
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := vocabdb.New(tx)
	added := 0
	for _, r := range rows {
		day := r.g.PlayedAt.In(loc)
		start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
		had, err := q.GamePointsBetween(ctx, vocabdb.GamePointsBetweenParams{StudentClientID: student, Since: ts(start), Until: ts(start.AddDate(0, 0, 1))})
		if err != nil {
			return 0, err
		}
		points := max(0, min(r.g.Score*PointsPerItem, DailyGamePoints-had))
		n, err := q.InsertGameResult(ctx, vocabdb.InsertGameResultParams{
			ID: r.id, StudentClientID: student, Game: r.g.Game, Score: r.g.Score, Total: r.g.Total,
			Mistakes: r.g.Mistakes, DurationMs: min(r.g.DurationMs, 3_600_000), Missed: r.missed,
			PlayedAt: ts(r.g.PlayedAt), Points: points,
		})
		if err != nil {
			return 0, err
		}
		added += int(n)
	}
	return added, tx.Commit(ctx)
}

// GameStat is how a student has done at one game: rounds played, and the
// best (Pairs: the quickest time in ms, mistakes included; the others: the
// highest score). Best is null before the first round.
type GameStat struct {
	Plays int32  `json:"plays"`
	Best  *int32 `json:"best"`
}

// GameStats are a student's results at each game.
func (s *Service) GameStats(ctx context.Context, student int32) (map[string]GameStat, error) {
	rows, err := vocabdb.New(s.pool).GameBests(ctx, student)
	if err != nil {
		return nil, err
	}
	out := map[string]GameStat{}
	for g := range GameRound {
		out[g] = GameStat{}
	}
	for _, r := range rows {
		best := r.BestScore
		if r.Game == "pairs" {
			best = r.BestMs
		}
		out[r.Game] = GameStat{Plays: r.Plays, Best: &best}
	}
	return out, nil
}

// The leaderboard (spec 11.5) ------------------------------------------------

// Board is a month's leaderboard: the first entries and where the viewer stands.
type Board struct {
	Scope    string       `json:"scope"`
	Month    string       `json:"month"` // 2026-11
	Entries  []BoardEntry `json:"entries"`
	Me       *BoardEntry  `json:"me"`
	Learners int          `json:"learners"`
}

// BoardEntry is one person on a board. On the board of everyone, names are
// shortened to the first name and an initial.
type BoardEntry struct {
	Rank     int     `json:"rank"`
	Name     string  `json:"name"`
	PhotoURL *string `json:"photo_url"`
	Points   int32   `json:"points"`
	Me       bool    `json:"me"`
}

// BoardSize is how many entries a board lists.
const BoardSize = 50

// Leaderboard is the month's board (school time) of everyone ("all") or of
// the students sharing a group with the viewer ("groups"). viewer is 0 for
// a teacher.
func (s *Service) Leaderboard(ctx context.Context, viewer int32, scope string, now time.Time, loc *time.Location) (Board, error) {
	local := now.In(loc)
	since := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc)
	q := vocabdb.New(s.pool)
	rows, err := q.Points(ctx, vocabdb.PointsParams{Tz: loc.String(), Since: ts(since), Until: ts(since.AddDate(0, 1, 0))})
	if err != nil {
		return Board{}, err
	}
	if scope == "groups" {
		y, m, d := local.Date()
		mates, err := q.Classmates(ctx, vocabdb.ClassmatesParams{StudentClientID: viewer, Today: pgtype.Date{Time: time.Date(y, m, d, 0, 0, 0, 0, time.UTC), Valid: true}})
		if err != nil {
			return Board{}, err
		}
		in := map[int32]bool{viewer: true}
		for _, c := range mates {
			in[c] = true
		}
		kept := rows[:0]
		for _, r := range rows {
			if in[r.ClientID] {
				kept = append(kept, r)
			}
		}
		rows = kept
	}
	b := Board{Scope: scope, Month: since.Format("2006-01"), Entries: []BoardEntry{}, Learners: len(rows)}
	rank := 0
	for i, r := range rows {
		if i == 0 || r.Points != rows[i-1].Points {
			rank = i + 1
		}
		e := BoardEntry{Rank: rank, Points: r.Points, Me: r.ClientID == viewer}
		if scope == "groups" {
			e.Name = strings.TrimSpace(r.FirstName + " " + r.LastName)
		} else {
			e.Name = shortName(r.FirstName, r.LastName)
		}
		if r.PhotoID.Valid {
			u := "/v1/photos/" + r.PhotoID.String
			e.PhotoURL = &u
		}
		if i < BoardSize {
			b.Entries = append(b.Entries, e)
		}
		if e.Me {
			me := e
			b.Me = &me
		}
	}
	return b, nil
}

// shortName is "Madina K.": a first name and the surname's initial.
func shortName(first, last string) string {
	first = strings.TrimSpace(first)
	if r, _ := utf8.DecodeRuneInString(strings.TrimSpace(last)); r != utf8.RuneError {
		return first + " " + strings.ToUpper(string(r)) + "."
	}
	return first
}

// Students' decks (spec 11.6) ------------------------------------------------

var (
	// ErrDeckNotFound: no such deck, or not one this person may see.
	ErrDeckNotFound = errors.New("no such deck")
	// ErrNotOwner: only the deck's owner may change it.
	ErrNotOwner = errors.New("only the deck's owner can change it")
	// ErrTooManyDecks: a student has MaxDecks already.
	ErrTooManyDecks = fmt.Errorf("at most %d decks each", MaxDecks)
	// ErrDeckTooSmall: a deck needs MinPublished words to be shared.
	ErrDeckTooSmall = fmt.Errorf("a deck needs at least %d words to be shared", MinPublished)
	// ErrOwnDeck: a student can't add or report their own deck.
	ErrOwnDeck = errors.New("that is your own deck")
)

const (
	MaxDecks     = 30
	MaxDeckWords = 200
	MinPublished = 5
	// ReportsToHide is how many students' reports take a deck down until a teacher looks.
	ReportsToHide = 3
)

// DeckError is a deck that can't be saved as sent.
type DeckError struct{ Reason string }

func (e *DeckError) Error() string { return e.Reason }

// DeckInput is a deck as its owner saves it: the whole of it each time.
type DeckInput struct {
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Words       []DeckWordInput `json:"words"`
}

// DeckWordInput is one word; id (from an earlier answer) keeps a word's
// reviews when it is edited.
type DeckWordInput struct {
	ID           string  `json:"id"`
	Word         string  `json:"word"`
	PartOfSpeech *string `json:"part_of_speech"`
	Definition   string  `json:"definition"`
	Example      string  `json:"example"`
}

// UserDeck is a student's deck as someone sees it.
type UserDeck struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	Description  string     `json:"description"`
	Owner        string     `json:"owner"`
	Mine         bool       `json:"mine"`
	Added        bool       `json:"added"`
	Published    bool       `json:"published"`
	PublishedAt  *time.Time `json:"published_at"`
	Hidden       bool       `json:"hidden"`
	HiddenReason *string    `json:"hidden_reason"` // the owner and teachers only
	Adds         int32      `json:"adds"`
	WordCount    int        `json:"word_count"`
	Reports      *int32     `json:"reports"` // teachers only
	Words        []Word     `json:"words"`
}

func clean(s string) string { return strings.Join(strings.Fields(s), " ") }

func checkDeck(in *DeckInput) error {
	in.Title, in.Description = clean(in.Title), strings.TrimSpace(in.Description)
	switch {
	case in.Title == "" || utf8.RuneCountInString(in.Title) > 80:
		return &DeckError{"the title is needed, at most 80 characters"}
	case utf8.RuneCountInString(in.Description) > 300:
		return &DeckError{"the description is at most 300 characters"}
	case len(in.Words) > MaxDeckWords:
		return &DeckError{fmt.Sprintf("at most %d words in a deck", MaxDeckWords)}
	}
	seen := map[string]bool{}
	for i := range in.Words {
		w := &in.Words[i]
		w.Word, w.Definition, w.Example = clean(w.Word), strings.TrimSpace(w.Definition), strings.TrimSpace(w.Example)
		if w.PartOfSpeech != nil {
			p := clean(*w.PartOfSpeech)
			w.PartOfSpeech = &p
			if p == "" {
				w.PartOfSpeech = nil
			}
		}
		key := strings.ToLower(w.Word)
		switch {
		case w.Word == "" || utf8.RuneCountInString(w.Word) > 100:
			return &DeckError{fmt.Sprintf("word %d: the word is needed, at most 100 characters", i+1)}
		case w.Definition == "" || utf8.RuneCountInString(w.Definition) > 1000:
			return &DeckError{fmt.Sprintf("word %d (%s): the meaning is needed, at most 1000 characters", i+1, w.Word)}
		case utf8.RuneCountInString(w.Example) > 1000:
			return &DeckError{fmt.Sprintf("word %d (%s): the example is at most 1000 characters", i+1, w.Word)}
		case w.PartOfSpeech != nil && utf8.RuneCountInString(*w.PartOfSpeech) > 30:
			return &DeckError{fmt.Sprintf("word %d (%s): the part of speech is at most 30 characters", i+1, w.Word)}
		case seen[key]:
			return &DeckError{fmt.Sprintf("“%s” is in the deck twice", w.Word)}
		}
		seen[key] = true
	}
	return nil
}

// CreateDeck makes a private deck for a student.
func (s *Service) CreateDeck(ctx context.Context, student int32, in DeckInput) (string, error) {
	if err := checkDeck(&in); err != nil {
		return "", err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := vocabdb.New(tx)
	n, err := q.CountOwnDecks(ctx, student)
	if err != nil {
		return "", err
	}
	if n >= MaxDecks {
		return "", ErrTooManyDecks
	}
	id, err := q.InsertDeck(ctx, vocabdb.InsertDeckParams{Owner: student, Title: in.Title, Description: in.Description})
	if err != nil {
		return "", err
	}
	if err := saveWords(ctx, q, id, in.Words); err != nil {
		return "", err
	}
	return uuidString(id), tx.Commit(ctx)
}

// saveWords makes the deck's words what words says: a word is matched to
// one already there by its id, else by its spelling (so its reviews follow
// it), and updated; the rest are added, and words left out marked removed.
func saveWords(ctx context.Context, q *vocabdb.Queries, deck pgtype.UUID, words []DeckWordInput) error {
	existing, err := q.AllDeckWords(ctx, deck)
	if err != nil {
		return err
	}
	byID := map[[16]byte]bool{}
	byKey := map[string]pgtype.UUID{}
	for _, w := range existing {
		byID[w.ID.Bytes] = true
		byKey[w.Key] = w.ID
	}
	used := map[[16]byte]bool{}
	keep := []pgtype.UUID{}
	for i, w := range words {
		id, ok := parseUUID(w.ID)
		if !ok || !byID[id.Bytes] || used[id.Bytes] {
			id, ok = byKey[strings.ToLower(w.Word)]
			if ok && used[id.Bytes] {
				ok = false
			}
		}
		if ok {
			used[id.Bytes] = true
			if _, err := q.UpdateDeckWord(ctx, vocabdb.UpdateDeckWordParams{ID: id, DeckID: deck, Position: int32(i), Word: w.Word, PartOfSpeech: optPtr(w.PartOfSpeech), Definition: w.Definition, Example: w.Example}); err != nil {
				return err
			}
			keep = append(keep, id)
			continue
		}
		id, err := q.InsertDeckWord(ctx, vocabdb.InsertDeckWordParams{DeckID: deck, Position: int32(i), Word: w.Word, PartOfSpeech: optPtr(w.PartOfSpeech), Definition: w.Definition, Example: w.Example})
		if err != nil {
			return err
		}
		keep = append(keep, id)
	}
	return q.RemoveDeckWords(ctx, vocabdb.RemoveDeckWordsParams{DeckID: deck, Keep: keep})
}

func optPtr(p *string) pgtype.Text {
	if p == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *p, Valid: true}
}

// owned loads a deck its owner is changing.
func owned(ctx context.Context, q *vocabdb.Queries, student int32, id string) (vocabdb.GetDeckRow, error) {
	u, ok := parseUUID(id)
	if !ok {
		return vocabdb.GetDeckRow{}, ErrDeckNotFound
	}
	d, err := q.GetDeck(ctx, u)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrDeckNotFound
	}
	if err == nil && d.OwnerClientID != student {
		return d, ErrNotOwner
	}
	return d, err
}

// UpdateDeck replaces a deck's title, description and words. A shared deck
// that falls under MinPublished words stops being shared.
func (s *Service) UpdateDeck(ctx context.Context, student int32, id string, in DeckInput) error {
	if err := checkDeck(&in); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := vocabdb.New(tx)
	d, err := owned(ctx, q, student, id)
	if err != nil {
		return err
	}
	if err := q.UpdateDeck(ctx, vocabdb.UpdateDeckParams{ID: d.ID, Title: in.Title, Description: in.Description}); err != nil {
		return err
	}
	if err := saveWords(ctx, q, d.ID, in.Words); err != nil {
		return err
	}
	if d.PublishedAt.Valid && len(in.Words) < MinPublished {
		if err := q.SetPublished(ctx, vocabdb.SetPublishedParams{ID: d.ID, Publish: false}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// DeleteDeck removes a deck (its words stay for the reviews that point to them).
func (s *Service) DeleteDeck(ctx context.Context, student int32, id string) error {
	q := vocabdb.New(s.pool)
	d, err := owned(ctx, q, student, id)
	if err != nil {
		return err
	}
	return q.DeleteDeck(ctx, d.ID)
}

// Publish shares a deck with everyone (or stops sharing it).
func (s *Service) Publish(ctx context.Context, student int32, id string, on bool) error {
	q := vocabdb.New(s.pool)
	d, err := owned(ctx, q, student, id)
	if err != nil {
		return err
	}
	if on {
		words, err := q.AllDeckWords(ctx, d.ID)
		if err != nil {
			return err
		}
		if len(words) < MinPublished {
			return ErrDeckTooSmall
		}
	}
	return q.SetPublished(ctx, vocabdb.SetPublishedParams{ID: d.ID, Publish: on})
}

// visible loads a deck someone else is looking at: published and not hidden
// (a teacher sees hidden ones too).
func visible(ctx context.Context, q *vocabdb.Queries, viewer int32, teacher bool, id string) (vocabdb.GetDeckRow, error) {
	u, ok := parseUUID(id)
	if !ok {
		return vocabdb.GetDeckRow{}, ErrDeckNotFound
	}
	d, err := q.GetDeck(ctx, u)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return d, ErrDeckNotFound
	case err != nil:
		return d, err
	case !teacher && d.OwnerClientID == viewer:
		return d, nil
	case !d.PublishedAt.Valid || (d.HiddenAt.Valid && !teacher):
		return d, ErrDeckNotFound
	}
	return d, nil
}

// AddDeck puts a community deck among a student's decks (or takes it out).
func (s *Service) AddDeck(ctx context.Context, student int32, id string, on bool) error {
	q := vocabdb.New(s.pool)
	if !on {
		u, ok := parseUUID(id)
		if !ok {
			return ErrDeckNotFound
		}
		return q.RemoveDeckAdd(ctx, vocabdb.RemoveDeckAddParams{StudentClientID: student, DeckID: u})
	}
	d, err := visible(ctx, q, student, false, id)
	if err != nil {
		return err
	}
	if d.OwnerClientID == student {
		return ErrOwnDeck
	}
	return q.AddDeck(ctx, vocabdb.AddDeckParams{StudentClientID: student, DeckID: d.ID})
}

// ReportDeck records a student's report; ReportsToHide open reports take the
// deck out of the community until a teacher looks at it.
func (s *Service) ReportDeck(ctx context.Context, student int32, id, reason string) error {
	reason = clean(reason)
	if reason == "" || utf8.RuneCountInString(reason) > 300 {
		return &DeckError{"say what is wrong, in at most 300 characters"}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := vocabdb.New(tx)
	d, err := visible(ctx, q, student, false, id)
	if err != nil {
		return err
	}
	if d.OwnerClientID == student {
		return ErrOwnDeck
	}
	if err := q.ReportDeck(ctx, vocabdb.ReportDeckParams{DeckID: d.ID, StudentClientID: student, Reason: reason}); err != nil {
		return err
	}
	n, err := q.OpenReports(ctx, d.ID)
	if err != nil {
		return err
	}
	if n >= ReportsToHide && !d.HiddenAt.Valid {
		if err := q.HideDeck(ctx, vocabdb.HideDeckParams{ID: d.ID, Reason: pgtype.Text{String: "Reported by students; a teacher will look at it.", Valid: true}}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Moderate is a teacher hiding a community deck (with the reason its owner
// sees), or putting it back and closing its reports.
func (s *Service) Moderate(ctx context.Context, teacher int32, id string, hide bool, reason string) error {
	reason = clean(reason)
	if hide && (reason == "" || utf8.RuneCountInString(reason) > 300) {
		return &DeckError{"give the owner a reason, in at most 300 characters"}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := vocabdb.New(tx)
	d, err := visible(ctx, q, 0, true, id)
	if err != nil {
		return err
	}
	if hide {
		err = q.HideDeck(ctx, vocabdb.HideDeckParams{ID: d.ID, Teacher: pgtype.Int4{Int32: teacher, Valid: true}, Reason: pgtype.Text{String: reason, Valid: true}})
	} else {
		err = q.UnhideDeck(ctx, d.ID)
	}
	if err != nil {
		return err
	}
	if err := q.ResolveReports(ctx, d.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func deckWords(ctx context.Context, q *vocabdb.Queries, ids []pgtype.UUID) (map[[16]byte][]Word, error) {
	rows, err := q.DeckWordsOf(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := map[[16]byte][]Word{}
	for _, r := range rows {
		w := Word{ID: uuidString(r.ID), Word: r.Word, Definition: r.Definition, Example: r.Example}
		if r.PartOfSpeech.Valid {
			p := r.PartOfSpeech.String
			w.PartOfSpeech = &p
		}
		out[r.DeckID.Bytes] = append(out[r.DeckID.Bytes], w)
	}
	return out, nil
}

func tsPtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC()
	return &v
}

func textPtr(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

// MyDecks are a student's own decks and the community decks they added, with their words.
func (s *Service) MyDecks(ctx context.Context, student int32) ([]UserDeck, error) {
	q := vocabdb.New(s.pool)
	rows, err := q.MyDecks(ctx, student)
	if err != nil {
		return nil, err
	}
	ids := make([]pgtype.UUID, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	words, err := deckWords(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	out := make([]UserDeck, 0, len(rows))
	for _, r := range rows {
		mine := r.OwnerClientID == student
		d := UserDeck{
			ID: uuidString(r.ID), Title: r.Title, Description: r.Description, Owner: shortName(r.OwnerFirst, r.OwnerLast),
			Mine: mine, Added: !mine, Published: r.PublishedAt.Valid, PublishedAt: tsPtr(r.PublishedAt),
			Hidden: r.HiddenAt.Valid, Adds: r.Adds, Words: words[r.ID.Bytes],
		}
		if d.Words == nil {
			d.Words = []Word{}
		}
		d.WordCount = len(d.Words)
		if mine {
			d.HiddenReason = textPtr(r.HiddenReason)
		}
		out = append(out, d)
	}
	return out, nil
}

// Community lists the shared decks (search: q). moderation, for teachers:
// the hidden ones and those with open reports instead.
func (s *Service) Community(ctx context.Context, viewer int32, teacher, moderation bool, search string) ([]UserDeck, error) {
	rows, err := vocabdb.New(s.pool).CommunityDecks(ctx, vocabdb.CommunityDecksParams{Student: viewer, Moderation: teacher && moderation, Q: clean(search)})
	if err != nil {
		return nil, err
	}
	out := make([]UserDeck, 0, len(rows))
	for _, r := range rows {
		d := UserDeck{
			ID: uuidString(r.ID), Title: r.Title, Description: r.Description, Owner: shortName(r.OwnerFirst, r.OwnerLast),
			Mine: !teacher && r.OwnerClientID == viewer, Added: r.Added, Published: true, PublishedAt: tsPtr(r.PublishedAt),
			Hidden: r.HiddenAt.Valid, Adds: r.Adds, WordCount: int(r.Words), Words: []Word{},
		}
		if teacher {
			n := r.Reports
			d.Reports = &n
			d.HiddenReason = textPtr(r.HiddenReason)
		}
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Mine && !out[j].Mine })
	return out, nil
}

// DeckDetail is one deck with its words, for its owner, anyone when shared,
// and teachers; teachers also get the open reports' reasons.
func (s *Service) DeckDetail(ctx context.Context, viewer int32, teacher bool, id string) (UserDeck, []string, error) {
	q := vocabdb.New(s.pool)
	d, err := visible(ctx, q, viewer, teacher, id)
	if err != nil {
		return UserDeck{}, nil, err
	}
	words, err := deckWords(ctx, q, []pgtype.UUID{d.ID})
	if err != nil {
		return UserDeck{}, nil, err
	}
	mine := !teacher && d.OwnerClientID == viewer
	out := UserDeck{
		ID: uuidString(d.ID), Title: d.Title, Description: d.Description, Owner: shortName(d.OwnerFirst, d.OwnerLast),
		Mine: mine, Published: d.PublishedAt.Valid, PublishedAt: tsPtr(d.PublishedAt), Hidden: d.HiddenAt.Valid,
		Adds: d.Adds, Words: words[d.ID.Bytes],
	}
	if out.Words == nil {
		out.Words = []Word{}
	}
	out.WordCount = len(out.Words)
	if !mine && !teacher {
		my, err := q.MyDecks(ctx, viewer)
		if err != nil {
			return UserDeck{}, nil, err
		}
		for _, m := range my {
			if m.ID == d.ID {
				out.Added = true
			}
		}
	}
	var reasons []string
	if mine || teacher {
		out.HiddenReason = textPtr(d.HiddenReason)
	}
	if teacher {
		n := d.Reports
		out.Reports = &n
		if reasons, err = q.ReportReasons(ctx, d.ID); err != nil {
			return UserDeck{}, nil, err
		}
	}
	return out, reasons, nil
}

// Teachers: games ------------------------------------------------------------

// StudentGame is one round a student of the group played.
type StudentGame struct {
	ID         string       `json:"id"`
	ClientID   int32        `json:"client_id"`
	FirstName  string       `json:"first_name"`
	LastName   string       `json:"last_name"`
	Game       string       `json:"game"`
	Score      int32        `json:"score"`
	Total      int32        `json:"total"`
	Mistakes   int32        `json:"mistakes"`
	DurationMs int32        `json:"duration_ms"`
	PlayedAt   time.Time    `json:"played_at"`
	Missed     []MissedWord `json:"missed"`
}

// MissedWord is a word a student got wrong in a game.
type MissedWord struct {
	ID         string `json:"id"`
	Word       string `json:"word"`
	Definition string `json:"definition"`
}

// GroupGames are the latest rounds the group's students played (for one of its teachers).
func (s *Service) GroupGames(ctx context.Context, teacher, group int32, today time.Time, limit int32) ([]StudentGame, error) {
	q := vocabdb.New(s.pool)
	day := pgtype.Date{Time: today, Valid: true}
	ok, err := q.TeachesGroup(ctx, vocabdb.TeachesGroupParams{GroupID: group, TeacherID: teacher, Today: day})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotTeacher
	}
	students, err := q.GroupProgress(ctx, vocabdb.GroupProgressParams{GroupID: group, Today: day})
	if err != nil {
		return nil, err
	}
	ids := make([]int32, 0, len(students))
	for _, st := range students {
		ids = append(ids, st.ClientID)
	}
	rows, err := q.GroupRecentGames(ctx, vocabdb.GroupRecentGamesParams{Students: ids, Lim: limit})
	if err != nil {
		return nil, err
	}
	var wordIDs []pgtype.UUID
	for _, r := range rows {
		wordIDs = append(wordIDs, r.Missed...)
	}
	words := map[[16]byte]MissedWord{}
	if len(wordIDs) > 0 {
		found, err := q.WordsByID(ctx, wordIDs)
		if err != nil {
			return nil, err
		}
		for _, w := range found {
			words[w.ID.Bytes] = MissedWord{ID: uuidString(w.ID), Word: w.Word, Definition: w.Definition}
		}
	}
	out := make([]StudentGame, 0, len(rows))
	for _, r := range rows {
		g := StudentGame{ID: uuidString(r.ID), ClientID: r.StudentClientID, FirstName: r.FirstName, LastName: r.LastName,
			Game: r.Game, Score: r.Score, Total: r.Total, Mistakes: r.Mistakes, DurationMs: r.DurationMs,
			PlayedAt: r.PlayedAt.Time.UTC(), Missed: []MissedWord{}}
		for _, m := range r.Missed {
			if w, ok := words[m.Bytes]; ok {
				g.Missed = append(g.Missed, w)
			}
		}
		out = append(out, g)
	}
	return out, nil
}
