package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nazacd/makonbook-api/internal/auth/authdb"
	"github.com/nazacd/makonbook-api/internal/people"
)

// Profile photos (spec 21.5): a copy of the Google or Telegram picture, taken
// the first time someone signs in with one, kept in the database (at most
// 512 KB) and served from /v1/photos/{id}: the web's pages load images from
// their own origin only, and the providers' CDNs are never hotlinked.

const (
	maxPhotoBytes = 512 << 10
	photoPath     = "/v1/photos/"
)

// photoFetcher downloads pictures.
type photoFetcher struct {
	client    *http.Client
	allowHTTP bool // tests' servers
}

// ensurePhoto copies the picture at url as p's photo if they have none yet.
// Best effort: a failure is only logged.
func (s *Service) ensurePhoto(ctx context.Context, p people.Person, url string) {
	if url == "" {
		return
	}
	if err := s.copyPhoto(ctx, p, url); err != nil {
		s.log.Warn("profile photo not copied", slog.Any("err", err))
	}
}

func (s *Service) copyPhoto(ctx context.Context, p people.Person, url string) error {
	c, err := s.q.GetCredential(ctx, authdb.GetCredentialParams{PersonKind: p.Kind, PersonID: p.ID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if c.PhotoID.Valid {
		return nil
	}
	f := s.photos
	if !strings.HasPrefix(url, "https://") && (!f.allowHTTP || !strings.HasPrefix(url, "http://")) {
		return fmt.Errorf("photo %q: not https", url)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return fmt.Errorf("photo: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("photo: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPhotoBytes+1))
	if err != nil {
		return fmt.Errorf("photo: %w", err)
	}
	if len(data) > maxPhotoBytes {
		return errors.New("photo: larger than 512 KB")
	}
	// What the bytes are, not what the server says.
	typ := http.DetectContentType(data)
	if typ != "image/jpeg" && typ != "image/png" && typ != "image/webp" {
		return fmt.Errorf("photo: %s isn't an image we keep", typ)
	}
	id, err := randomString()
	if err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := authdb.New(tx)
	if err := q.InsertPhoto(ctx, authdb.InsertPhotoParams{ID: id, ContentType: typ, Data: data}); err != nil {
		return fmt.Errorf("store photo: %w", err)
	}
	if err := q.SetPhoto(ctx, authdb.SetPhotoParams{PersonKind: p.Kind, PersonID: p.ID, PhotoID: pgtype.Text{String: id, Valid: true}}); err != nil {
		return fmt.Errorf("set photo: %w", err)
	}
	return tx.Commit(ctx)
}

// servePhoto answers GET /v1/photos/{id}: no token needed (an <img> can't
// send one; the id is a random 256-bit name), cached for a year (a photo
// never changes; a new one gets a new id).
func (h *Handler) servePhoto(w http.ResponseWriter, r *http.Request) {
	ph, err := h.svc.q.GetPhoto(r.Context(), r.PathValue("id"))
	if errors.Is(err, pgx.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.internal(w, r, "photo", err)
		return
	}
	w.Header().Set("Content-Type", ph.ContentType)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	_, _ = w.Write(ph.Data)
}
