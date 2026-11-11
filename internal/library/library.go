// Package library is SAT MAKON's own books, read in the apps (spec 25). The
// books are imported from the private makonbook-library repository; their
// PDFs and covers live in MakonBook's R2 bucket and are served from the
// API's origin. Everyone signed in reads and downloads every book; each
// reader's page is remembered, and a book's views are the people who have
// opened it.
package library

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nazacd/makonbook-api/internal/library/librarydb"
	"github.com/nazacd/makonbook-api/internal/platform/r2"
)

var (
	// ErrNotFound means no such book (or no cover for it).
	ErrNotFound = errors.New("no such book")
	// ErrBadPage means a page outside the book.
	ErrBadPage = errors.New("no such page")
)

// Store is MakonBook's R2 bucket (the uploads bucket).
type Store interface {
	Get(ctx context.Context, key string) (r2.Object, error)
	Put(ctx context.Context, key string, body []byte, contentType string) error
}

// IDPattern is a book's id: the slug its files are named by.
var IDPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Who is the reader.
type Who struct {
	Kind string // people.KindStudent or people.KindTeacher
	ID   int32
}

// Progress is where a reader is in a book.
type Progress struct {
	Page      int       `json:"page"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Book is a book on the shelf, as a reader sees it.
type Book struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Author      string `json:"author"`
	Subject     string `json:"subject"` // "math" or "english"
	Description string `json:"description"`
	Pages       int    `json:"pages"`
	Bytes       int    `json:"bytes"` // the PDF's size
	// CoverURL is /v1/library/{id}/cover?v=… (same origin), or null.
	CoverURL *string `json:"cover_url"`
	// Views is how many people have opened the book.
	Views int `json:"views"`
	// Progress is where this reader stopped; null if they never opened it.
	Progress *Progress `json:"progress"`
}

// File is a book's PDF, or a cover.
type File struct {
	Body        []byte
	ContentType string
	SHA256      string
	Title       string
	ModTime     time.Time
}

// Service reads the library.
type Service struct {
	pool  *pgxpool.Pool
	store Store
}

// NewService: store may be nil (the list works; files answer ErrNotFound).
func NewService(pool *pgxpool.Pool, store Store) *Service {
	return &Service{pool: pool, store: store}
}

func coverURL(id string, sha pgtype.Text) *string {
	if !sha.Valid || len(sha.String) < 12 {
		return nil
	}
	u := fmt.Sprintf("/v1/library/%s/cover?v=%s", id, sha.String[:12])
	return &u
}

// Books lists the shelf, in the data's order.
func (s *Service) Books(ctx context.Context, who Who) ([]Book, error) {
	rows, err := librarydb.New(s.pool).ListBooks(ctx, librarydb.ListBooksParams{PersonKind: who.Kind, PersonID: who.ID})
	if err != nil {
		return nil, err
	}
	out := make([]Book, 0, len(rows))
	for _, r := range rows {
		b := Book{
			ID: r.ID, Title: r.Title, Author: r.Author, Subject: r.Subject, Description: r.Description,
			Pages: int(r.Pages), Bytes: int(r.PdfBytes), CoverURL: coverURL(r.ID, r.CoverSha256), Views: int(r.Views),
		}
		if r.MyPage.Valid {
			b.Progress = &Progress{Page: int(r.MyPage.Int32), UpdatedAt: r.MyUpdatedAt.Time.UTC()}
		}
		out = append(out, b)
	}
	return out, nil
}

// PDF reads a book's file from the bucket.
func (s *Service) PDF(ctx context.Context, id string) (File, error) {
	if s.store == nil || !IDPattern.MatchString(id) {
		return File{}, ErrNotFound
	}
	b, err := librarydb.New(s.pool).GetBook(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return File{}, ErrNotFound
	}
	if err != nil {
		return File{}, err
	}
	obj, err := s.store.Get(ctx, b.PdfKey)
	if err != nil {
		return File{}, err
	}
	return File{Body: obj.Body, ContentType: "application/pdf", SHA256: b.PdfSha256, Title: b.Title, ModTime: b.UpdatedAt.Time}, nil
}

// Cover reads a book's cover from the bucket.
func (s *Service) Cover(ctx context.Context, id string) (File, error) {
	if s.store == nil || !IDPattern.MatchString(id) {
		return File{}, ErrNotFound
	}
	c, err := librarydb.New(s.pool).BookCover(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return File{}, ErrNotFound
	}
	if err != nil {
		return File{}, err
	}
	obj, err := s.store.Get(ctx, c.CoverKey.String)
	if err != nil {
		return File{}, err
	}
	return File{Body: obj.Body, ContentType: c.CoverType.String, SHA256: c.CoverSha256.String}, nil
}

// SetProgress remembers the reader's page (the first time, it also counts
// them among the book's views). ErrBadPage outside 1..pages.
func (s *Service) SetProgress(ctx context.Context, who Who, id string, page int) (Progress, error) {
	if !IDPattern.MatchString(id) {
		return Progress{}, ErrNotFound
	}
	q := librarydb.New(s.pool)
	b, err := q.GetBook(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Progress{}, ErrNotFound
	}
	if err != nil {
		return Progress{}, err
	}
	if page < 1 || page > int(b.Pages) {
		return Progress{}, ErrBadPage
	}
	r, err := q.SetProgress(ctx, librarydb.SetProgressParams{PersonKind: who.Kind, PersonID: who.ID, BookID: id, Page: int32(page)}) //nolint:gosec // 1..pages, checked above
	if err != nil {
		return Progress{}, err
	}
	return Progress{Page: int(r.Page), UpdatedAt: r.UpdatedAt.Time.UTC()}, nil
}
