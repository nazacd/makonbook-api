package library

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // decoders, for DecodeConfig
	_ "image/png"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "golang.org/x/image/webp" // the WebP decoder

	"github.com/nazacd/makonbook-api/internal/library/librarydb"
)

// The import (spec 25.2) reads a checkout of makonbook-library: every book
// is three files with one name, books/<name>.json (title, author, subject,
// description, pages), books/<name>.pdf and covers/<name>.png (or .jpg,
// .webp; optional). The name, without a leading "NN-" that only orders the
// shelf, is the book's id. Everything is checked first and nothing is stored
// unless all of it passes.

const (
	// MaxPDFBytes bounds a book's PDF (the API serves it from memory).
	MaxPDFBytes = 40 << 20
	// MaxCoverBytes bounds a cover image.
	MaxCoverBytes = 2 << 20
)

var orderPrefix = regexp.MustCompile(`^\d+-`)

// Importer loads the books; Store is needed when there are files to upload.
type Importer struct {
	Pool  *pgxpool.Pool
	Store Store
	// Progress, when set, hears each upload (done of total).
	Progress func(done, total int)
}

// ImportSummary says what an import did.
type ImportSummary struct {
	Listed, New, Changed, Unlisted int
	Uploaded                       int
}

// ImportError lists every problem found; nothing was stored.
type ImportError struct{ Problems []string }

func (e *ImportError) Error() string {
	return fmt.Sprintf("%d problems, nothing stored:\n  %s", len(e.Problems), strings.Join(e.Problems, "\n  "))
}

type meta struct {
	Title       string `json:"title"`
	Author      string `json:"author"`
	Subject     string `json:"subject"`
	Description string `json:"description"`
	Pages       int    `json:"pages"`
}

type upload struct {
	key, contentType string
	body             []byte
}

var coverTypes = map[string]string{"png": "image/png", "jpeg": "image/jpeg", "webp": "image/webp"}

// Import loads dir (a checkout of makonbook-library) in one transaction.
func (im Importer) Import(ctx context.Context, dir string) (ImportSummary, error) {
	var sum ImportSummary
	var problems []string
	bad := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }

	files, err := filepath.Glob(filepath.Join(dir, "books", "*.json"))
	if err != nil {
		return sum, err
	}
	slices.Sort(files)
	if len(files) == 0 {
		return sum, fmt.Errorf("no books in %s (books/<name>.json with <name>.pdf beside it)", filepath.Join(dir, "books"))
	}

	q := librarydb.New(im.Pool)
	stored, err := q.StoredFiles(ctx)
	if err != nil {
		return sum, err
	}
	have := map[string]bool{} // sha256 of every file already in the bucket
	for _, s := range stored {
		have[s.PdfSha256] = true
		if s.CoverSha256.Valid {
			have[s.CoverSha256.String] = true
		}
	}

	var rows []librarydb.UpsertBookParams
	var uploads []upload
	seen := map[string]string{}
	for n, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		id := orderPrefix.ReplaceAllString(name, "")
		where := "books/" + name
		if !IDPattern.MatchString(id) {
			bad("%s: the name must be lower-case letters, digits and dashes (after an optional NN- for the order)", where)
			continue
		}
		if other, dup := seen[id]; dup {
			bad("%s: the same book as books/%s (names differ only in their number)", where, other)
			continue
		}
		seen[id] = name

		var m meta
		raw, err := os.ReadFile(f) //nolint:gosec // the operator's own checkout
		if err != nil {
			return sum, err
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&m); err != nil {
			bad("%s.json: %v", where, err)
			continue
		}
		m.Title, m.Author, m.Description = strings.TrimSpace(m.Title), strings.TrimSpace(m.Author), strings.TrimSpace(m.Description)
		switch {
		case m.Title == "" || m.Author == "" || m.Description == "":
			bad("%s.json: title, author and description are needed", where)
		case m.Subject != "math" && m.Subject != "english":
			bad("%s.json: subject %q (math or english)", where, m.Subject)
		case m.Pages < 1 || m.Pages > 5000:
			bad("%s.json: pages %d", where, m.Pages)
		}

		pdf, err := os.ReadFile(strings.TrimSuffix(f, ".json") + ".pdf") //nolint:gosec // as above
		switch {
		case errors.Is(err, os.ErrNotExist):
			bad("%s.pdf is missing", where)
			continue
		case err != nil:
			return sum, err
		case len(pdf) > MaxPDFBytes:
			bad("%s.pdf is %d MB, over %d MB", where, len(pdf)>>20, MaxPDFBytes>>20)
			continue
		case !bytes.HasPrefix(pdf, []byte("%PDF-")) || !bytes.Contains(pdf[max(0, len(pdf)-2048):], []byte("%%EOF")):
			bad("%s.pdf isn't a whole PDF file", where)
			continue
		}
		sha := hashOf(pdf)
		p := librarydb.UpsertBookParams{
			ID: id, Title: m.Title, Author: m.Author, Subject: m.Subject, Description: m.Description, Pages: int32(m.Pages), //nolint:gosec // 1..5000, checked above
			PdfKey: "library/books/" + sha + ".pdf", PdfSha256: sha, PdfBytes: int32(len(pdf)), Position: int32(n), //nolint:gosec // at most MaxPDFBytes; a few files
		}
		if !have[sha] {
			uploads = append(uploads, upload{p.PdfKey, "application/pdf", pdf})
			have[sha] = true
		}

		for _, ext := range []string{".png", ".jpg", ".jpeg", ".webp"} {
			cover, err := os.ReadFile(filepath.Join(dir, "covers", name+ext)) //nolint:gosec // as above
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return sum, err
			}
			_, format, err := image.DecodeConfig(bytes.NewReader(cover))
			if err != nil || coverTypes[format] == "" || len(cover) > MaxCoverBytes {
				bad("covers/%s%s must be a PNG, JPEG or WebP of at most %d MB", name, ext, MaxCoverBytes>>20)
				break
			}
			csha := hashOf(cover)
			p.CoverKey = pgtype.Text{String: "library/covers/" + csha + ext, Valid: true}
			p.CoverType = pgtype.Text{String: coverTypes[format], Valid: true}
			p.CoverSha256 = pgtype.Text{String: csha, Valid: true}
			if !have[csha] {
				uploads = append(uploads, upload{p.CoverKey.String, coverTypes[format], cover})
				have[csha] = true
			}
			break
		}
		rows = append(rows, p)
	}
	if len(problems) > 0 {
		return sum, &ImportError{Problems: problems}
	}

	// Files first (their keys are their content, so a repeat is harmless),
	// then the rows in one transaction.
	if len(uploads) > 0 && im.Store == nil {
		return sum, errors.New("there are files to upload: set the R2_UPLOADS_* settings")
	}
	for i, u := range uploads {
		if err := im.Store.Put(ctx, u.key, u.body, u.contentType); err != nil {
			return sum, fmt.Errorf("upload %s: %w", u.key, err)
		}
		if im.Progress != nil {
			im.Progress(i+1, len(uploads))
		}
	}
	sum.Uploaded = len(uploads)
	err = pgx.BeginFunc(ctx, im.Pool, func(tx pgx.Tx) error {
		q := librarydb.New(tx)
		var now pgtype.Timestamptz // the transaction's now(): a changed row's updated_at
		if err := tx.QueryRow(ctx, "SELECT now()").Scan(&now); err != nil {
			return err
		}
		ids := make([]string, 0, len(rows))
		for _, p := range rows {
			res, err := q.UpsertBook(ctx, p)
			if err != nil {
				return fmt.Errorf("%s: %w", p.ID, err)
			}
			ids = append(ids, p.ID)
			sum.Listed++
			if res.Inserted {
				sum.New++
			} else if res.UpdatedAt.Time.Equal(now.Time) {
				sum.Changed++
			}
		}
		unlisted, err := q.UnlistOtherBooks(ctx, ids)
		sum.Unlisted = int(unlisted)
		return err
	})
	return sum, err
}

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
