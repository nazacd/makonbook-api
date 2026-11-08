package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // DecodeConfig
	_ "image/png"
	"io"
	"log/slog"
	"net/http"
	neturl "net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	_ "golang.org/x/image/webp" // DecodeConfig

	"github.com/nazacd/makonbook-api/internal/auth/authdb"
	"github.com/nazacd/makonbook-api/internal/people"
)

// Profile photos (spec 21.5): one the person uploads (PUT /v1/me/photo), or a
// copy of the Google or Telegram picture, taken when someone signs in with
// one and has no photo, unless they chose their own (or none) themselves.
// Kept in the database (at most 512 KB) and served from /v1/photos/{id}: the
// web's pages load images from their own origin only, and the providers'
// CDNs are never hotlinked. A new photo gets a new id (old URLs are cached
// as immutable).

const (
	maxPhotoBytes = 512 << 10
	photoPath     = "/v1/photos/"
)

// photoFetcher downloads pictures.
type photoFetcher struct {
	client    *http.Client
	allowHTTP bool // tests' servers
}

var (
	ErrPhotoTooLarge = errors.New("the photo must be at most 512 KB")
	ErrInvalidPhoto  = errors.New("the photo must be a JPEG, PNG or WebP image")
)

// Own photos: uploads per person.
const (
	photoUploads      = 20
	photoUploadWindow = time.Hour
)

// ensurePhoto copies the picture at url as p's photo if they have none yet
// and never chose one themselves. Best effort: a failure is only logged. It
// runs before the caller answers, so the answer's profile has the photo.
func (s *Service) ensurePhoto(ctx context.Context, p people.Person, url string) {
	if url == "" {
		return
	}
	if err := s.copyPhoto(ctx, p, url); err != nil {
		s.log.Warn("profile photo not copied", slog.String("person", fmt.Sprintf("%s %d", p.Kind, p.ID)), slog.String("from", photoSource(url)), slog.Any("err", err))
	}
}

func (s *Service) copyPhoto(ctx context.Context, p people.Person, url string) error {
	c, err := s.q.GetCredential(ctx, authdb.GetCredentialParams{PersonKind: p.Kind, PersonID: p.ID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if c.PhotoID.Valid || c.PhotoChosen {
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
	req.Header.Set("Accept", "image/jpeg, image/png, image/webp, image/*;q=0.8")
	req.Header.Set("User-Agent", "MakonBook/1.0 (+https://sat.makonbook.uz)")
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
	// Still none, and not chosen meanwhile (another sign-in, an upload).
	c, err = q.GetCredential(ctx, authdb.GetCredentialParams{PersonKind: p.Kind, PersonID: p.ID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if c.PhotoID.Valid || c.PhotoChosen {
		return nil
	}
	if err := q.SetPhoto(ctx, authdb.SetPhotoParams{PersonKind: p.Kind, PersonID: p.ID, PhotoID: pgtype.Text{String: id, Valid: true}}); err != nil {
		return fmt.Errorf("set photo: %w", err)
	}
	return tx.Commit(ctx)
}

// SetMyPhoto makes data (the request's bytes) the person's photo: JPEG, PNG
// or WebP by its bytes, an image that decodes, at most 512 KB. It gets a new
// id; the previous photo is deleted. From then on the automatic copy stops.
func (s *Service) SetMyPhoto(ctx context.Context, pr Principal, data []byte) error {
	if len(data) > maxPhotoBytes {
		return ErrPhotoTooLarge
	}
	typ := http.DetectContentType(data)
	if typ != "image/jpeg" && typ != "image/png" && typ != "image/webp" {
		return ErrInvalidPhoto
	}
	if cfg, format, err := image.DecodeConfig(bytes.NewReader(data)); err != nil || cfg.Width <= 0 || cfg.Height <= 0 ||
		cfg.Width > 4096 || cfg.Height > 4096 || "image/"+format != typ {
		return ErrInvalidPhoto
	}
	if !s.photoLimit.Allow(fmt.Sprintf("%s:%d", pr.Kind, pr.ID), s.now()) {
		return &RateLimitedError{RetryAfter: photoUploadWindow}
	}
	id, err := randomString()
	if err != nil {
		return err
	}
	return s.choosePhoto(ctx, pr, func(q *authdb.Queries) (pgtype.Text, error) {
		if err := q.InsertPhoto(ctx, authdb.InsertPhotoParams{ID: id, ContentType: typ, Data: data}); err != nil {
			return pgtype.Text{}, fmt.Errorf("store photo: %w", err)
		}
		return pgtype.Text{String: id, Valid: true}, nil
	})
}

// RemoveMyPhoto deletes the person's photo; the automatic copy won't add one
// again.
func (s *Service) RemoveMyPhoto(ctx context.Context, pr Principal) error {
	return s.choosePhoto(ctx, pr, func(*authdb.Queries) (pgtype.Text, error) { return pgtype.Text{}, nil })
}

// choosePhoto replaces the person's photo with the one next makes (none:
// invalid), marks it their choice and deletes the old one.
func (s *Service) choosePhoto(ctx context.Context, pr Principal, next func(*authdb.Queries) (pgtype.Text, error)) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := authdb.New(tx)
	c, err := q.GetCredential(ctx, authdb.GetCredentialParams{PersonKind: pr.Kind, PersonID: pr.ID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	id, err := next(q)
	if err != nil {
		return err
	}
	if err := q.ChoosePhoto(ctx, authdb.ChoosePhotoParams{PersonKind: pr.Kind, PersonID: pr.ID, PhotoID: id}); err != nil {
		return fmt.Errorf("choose photo: %w", err)
	}
	if c.PhotoID.Valid {
		if err := q.DeletePhoto(ctx, c.PhotoID.String); err != nil {
			return fmt.Errorf("delete old photo: %w", err)
		}
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

// photoSource says where a picture link points without the rest of it (which
// can carry a username or a signed token): the host and the first path
// segment, e.g. "t.me/i" (a userpic link) or "cdn4.telesco.pe/file".
func photoSource(raw string) string {
	u, err := neturl.Parse(raw)
	if err != nil || u.Host == "" {
		return "unparsable"
	}
	first, _, _ := strings.Cut(strings.TrimPrefix(u.Path, "/"), "/")
	return u.Host + "/" + first
}
