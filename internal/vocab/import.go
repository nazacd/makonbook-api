// Package vocab is the vocabulary bank (spec 11): SAT MAKON's word lists,
// imported from a file, studied on the phone with spaced repetition (FSRS).
// The phone schedules; the server keeps the words and every review, so a
// student's progress survives a new phone and teachers can follow it.
package vocab

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // decoders, for DecodeConfig
	_ "image/png"  //
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "golang.org/x/image/webp" //

	"github.com/nazacd/makonbook-api/internal/vocab/vocabdb"
)

// ImagePrefix is where word images sit in MakonBook's bucket.
const ImagePrefix = "v/img/"

// MaxImageBytes bounds one word's image.
const MaxImageBytes = 2 << 20

// Store is MakonBook's R2 bucket.
type Store interface {
	Put(ctx context.Context, key string, body []byte, contentType string) error
}

// Entry is one word as the import file has it (words.jsonl, or words.csv
// with these names as its header).
type Entry struct {
	List         string `json:"list"`       // the list's id, e.g. sat-1
	ListTitle    string `json:"list_title"` // e.g. "SAT Words 1"
	Word         string `json:"word"`
	PartOfSpeech string `json:"part_of_speech"` // optional
	Definition   string `json:"definition"`
	Example      string `json:"example"`
	Image        string `json:"image"` // optional: a file in images/ beside the word file
}

var (
	slugRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	imageRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._ -]{0,127}$`)
)

var imageTypes = map[string]struct{ ext, contentType string }{
	"png":  {"png", "image/png"},
	"jpeg": {"jpg", "image/jpeg"},
	"webp": {"webp", "image/webp"},
}

// Read reads a word file: words.jsonl or words.csv, or a folder holding one.
// It returns the entries and the folder images are read from.
func Read(path string) ([]Entry, string, error) {
	if st, err := os.Stat(path); err == nil && st.IsDir() {
		for _, name := range []string{"words.jsonl", "words.csv"} {
			if _, err := os.Stat(filepath.Join(path, name)); err == nil {
				return Read(filepath.Join(path, name))
			}
		}
		return nil, "", fmt.Errorf("%s has no words.jsonl or words.csv", path)
	}
	f, err := os.Open(path) //nolint:gosec // the operator names the file
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = f.Close() }()
	var entries []Entry
	if strings.HasSuffix(strings.ToLower(path), ".csv") {
		entries, err = parseCSV(f)
	} else {
		entries, err = parseJSONL(f)
	}
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", path, err)
	}
	return entries, filepath.Join(filepath.Dir(path), "images"), nil
}

func parseJSONL(r io.Reader) ([]Entry, error) {
	var out []Entry
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	for n := 1; ; n++ {
		var e Entry
		if err := dec.Decode(&e); errors.Is(err, io.EOF) {
			return out, nil
		} else if err != nil {
			return nil, fmt.Errorf("entry %d: %w", n, err)
		}
		out = append(out, e)
	}
}

func parseCSV(r io.Reader) ([]Entry, error) {
	rd := csv.NewReader(r)
	rd.FieldsPerRecord = -1
	rows, err := rd.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.New("empty file")
	}
	col := map[string]int{}
	for i, h := range rows[0] {
		col[strings.TrimSpace(strings.TrimPrefix(h, "\uFEFF"))] = i
	}
	for _, need := range []string{"list", "list_title", "word", "definition", "example"} {
		if _, ok := col[need]; !ok {
			return nil, fmt.Errorf("the header has no %q column", need)
		}
	}
	get := func(row []string, name string) string {
		if i, ok := col[name]; ok && i < len(row) {
			return row[i]
		}
		return ""
	}
	var out []Entry
	for _, row := range rows[1:] {
		if strings.TrimSpace(strings.Join(row, "")) == "" {
			continue
		}
		out = append(out, Entry{
			List: get(row, "list"), ListTitle: get(row, "list_title"), Word: get(row, "word"),
			PartOfSpeech: get(row, "part_of_speech"), Definition: get(row, "definition"),
			Example: get(row, "example"), Image: get(row, "image"),
		})
	}
	return out, nil
}

// check trims an entry and says what is wrong with it, if anything.
func (e *Entry) check() string {
	for _, s := range []*string{&e.List, &e.ListTitle, &e.Word, &e.PartOfSpeech, &e.Definition, &e.Example, &e.Image} {
		*s = strings.TrimSpace(*s)
	}
	switch {
	case !slugRE.MatchString(e.List):
		return fmt.Sprintf("list %q must be lower-case letters, digits and dashes", e.List)
	case e.ListTitle == "":
		return "list_title is empty"
	case e.Word == "" || utf8.RuneCountInString(e.Word) > 100:
		return "word is empty or longer than 100 characters"
	case e.Definition == "" || utf8.RuneCountInString(e.Definition) > 1000:
		return "definition is empty or longer than 1000 characters"
	case e.Example == "" || utf8.RuneCountInString(e.Example) > 1000:
		return "example is empty or longer than 1000 characters"
	case utf8.RuneCountInString(e.PartOfSpeech) > 40:
		return "part_of_speech is longer than 40 characters"
	case e.Image != "" && !imageRE.MatchString(e.Image):
		return fmt.Sprintf("image %q isn't a plain file name", e.Image)
	}
	return ""
}

// Summary is what an import did.
type Summary struct {
	Lists         int
	Words         int
	RemovedWords  int64
	RemovedLists  int64
	ImagesStored  int
	Version       string
	VersionBefore string
}

// Import makes the bank what the file says: its lists in its order, its
// words in each list. Words and lists the file no longer has are marked
// removed (their reviews stay). Images are copied into MakonBook's bucket
// first; nothing is changed if any entry or image is wrong.
func Import(ctx context.Context, pool *pgxpool.Pool, uploads Store, entries []Entry, imagesDir string) (Summary, error) {
	type list struct {
		slug, title string
		words       []Entry
	}
	var lists []*list
	bySlug := map[string]*list{}
	seen := map[string]bool{}
	var bad []string
	for i := range entries {
		e := &entries[i]
		if msg := e.check(); msg != "" {
			bad = append(bad, fmt.Sprintf("entry %d (%s): %s", i+1, e.Word, msg))
			continue
		}
		l := bySlug[e.List]
		if l == nil {
			l = &list{slug: e.List, title: e.ListTitle}
			bySlug[e.List] = l
			lists = append(lists, l)
		} else if l.title != e.ListTitle {
			bad = append(bad, fmt.Sprintf("entry %d (%s): list %s is titled both %q and %q", i+1, e.Word, e.List, l.title, e.ListTitle))
		}
		k := e.List + "\x00" + strings.ToLower(e.Word)
		if seen[k] {
			bad = append(bad, fmt.Sprintf("entry %d: %q is in list %s twice", i+1, e.Word, e.List))
		}
		seen[k] = true
		l.words = append(l.words, *e)
	}
	if len(bad) > 0 {
		if len(bad) > 20 {
			bad = append(bad[:20], fmt.Sprintf("… and %d more", len(bad)-20))
		}
		return Summary{}, fmt.Errorf("the file has problems:\n  %s", strings.Join(bad, "\n  "))
	}
	if len(lists) == 0 {
		return Summary{}, errors.New("the file has no words")
	}

	q := vocabdb.New(pool)
	var sum Summary
	sum.VersionBefore, _ = q.DeckVersion(ctx)
	files := map[string]string{} // the file's image name → our file
	for _, e := range entries {
		if e.Image == "" || files[e.Image] != "" {
			continue
		}
		file, stored, err := storeImage(ctx, q, uploads, filepath.Join(imagesDir, e.Image))
		if err != nil {
			return Summary{}, fmt.Errorf("image %s (%s): %w", e.Image, e.Word, err)
		}
		files[e.Image] = file
		if stored {
			sum.ImagesStored++
		}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return Summary{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qt := q.WithTx(tx)
	var slugs []string
	for i, l := range lists {
		slugs = append(slugs, l.slug)
		id, err := qt.UpsertList(ctx, vocabdb.UpsertListParams{Slug: l.slug, Title: l.title, Position: int32(i + 1)}) //nolint:gosec // a few lists
		if err != nil {
			return Summary{}, err
		}
		var words []string
		for j, w := range l.words {
			words = append(words, strings.ToLower(w.Word))
			err := qt.UpsertWord(ctx, vocabdb.UpsertWordParams{
				ListID: id, Position: int32(j + 1), Word: w.Word, PartOfSpeech: optText(w.PartOfSpeech), //nolint:gosec // a few thousand words
				Definition: w.Definition, Example: w.Example, Image: optText(files[w.Image]),
			})
			if err != nil {
				return Summary{}, fmt.Errorf("%s / %s: %w", l.slug, w.Word, err)
			}
		}
		n, err := qt.RemoveWordsExcept(ctx, vocabdb.RemoveWordsExceptParams{ListID: id, Words: words})
		if err != nil {
			return Summary{}, err
		}
		sum.RemovedWords += n
		sum.Words += len(l.words)
	}
	if sum.RemovedLists, err = qt.RemoveListsExcept(ctx, slugs); err != nil {
		return Summary{}, err
	}
	deck, err := buildDeck(ctx, qt)
	if err != nil {
		return Summary{}, err
	}
	sum.Version = deckVersion(deck)
	if err := qt.SetDeckVersion(ctx, sum.Version); err != nil {
		return Summary{}, err
	}
	sum.Lists = len(lists)
	return sum, tx.Commit(ctx)
}

// storeImage checks an image file and puts it in the bucket once (it is
// named by its bytes). It returns our file name and whether it was new.
func storeImage(ctx context.Context, q *vocabdb.Queries, uploads Store, path string) (string, bool, error) {
	data, err := os.ReadFile(path) //nolint:gosec // a checked name in the operator's folder
	if err != nil {
		return "", false, err
	}
	if len(data) > MaxImageBytes {
		return "", false, fmt.Errorf("larger than %d MB", MaxImageBytes>>20)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	t, ok := imageTypes[format]
	if err != nil || !ok || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 4000 || cfg.Height > 4000 {
		return "", false, errors.New("not a PNG, JPEG or WebP image up to 4000 px a side")
	}
	sum := sha256.Sum256(data)
	file := hex.EncodeToString(sum[:20]) + "." + t.ext
	have, err := q.HaveImages(ctx, []string{file})
	if err != nil {
		return "", false, err
	}
	if len(have) > 0 {
		return file, false, nil
	}
	if uploads == nil {
		return "", false, errors.New("the words have images to upload: set R2_UPLOADS_*")
	}
	if err := uploads.Put(ctx, ImagePrefix+file, data, t.contentType); err != nil {
		return "", false, err
	}
	err = q.InsertImage(ctx, vocabdb.InsertImageParams{File: file, ContentType: t.contentType, Bytes: int32(len(data)), //nolint:gosec // ≤ 2 MB
		Width: int32(cfg.Width), Height: int32(cfg.Height)}) //nolint:gosec // ≤ 4000
	return file, true, err
}
