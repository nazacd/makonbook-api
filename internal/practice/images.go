package practice

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // decoders, for DecodeConfig
	_ "image/jpeg" //
	_ "image/png"  //
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "golang.org/x/image/webp" //

	"github.com/nazacd/makonbook-api/internal/platform/r2"
	"github.com/nazacd/makonbook-api/internal/practice/practicedb"
)

// UploadPrefix marks images editors uploaded (in MakonBook's own R2
// bucket), as opposed to the bank's images in SAT MAKON's.
const UploadPrefix = "mb/"

// MaxImageBytes bounds an upload.
const MaxImageBytes = 5 << 20

const maxImageSide = 6000

var (
	// ErrBadImage means the upload isn't a PNG, JPEG, GIF or WebP image we accept.
	ErrBadImage = errors.New("not an image we accept: PNG, JPEG, GIF or WebP, at most 5 MB and 6000 px a side")
	// ErrUploadsOff means the server has no bucket for uploads (R2_UPLOADS_*).
	ErrUploadsOff = errors.New("image uploads aren't set up on the server")
)

// Store is where uploaded images go: MakonBook's R2 bucket.
type Store interface {
	Get(ctx context.Context, key string) (r2.Object, error)
	Put(ctx context.Context, key string, body []byte, contentType string) error
}

var imageTypes = map[string]struct{ ext, contentType string }{
	"png":  {"png", "image/png"},
	"jpeg": {"jpg", "image/jpeg"},
	"gif":  {"gif", "image/gif"},
	"webp": {"webp", "image/webp"},
}

// Image is a stored upload.
type Image struct {
	Key    string `json:"key"` // use as ![description](r2:<key>)
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// UploadImage checks an image, puts it in the bucket and returns its key.
// The key comes from the bytes, so the same file uploaded twice is one object.
func (s *Service) UploadImage(ctx context.Context, data []byte, by string) (Image, error) {
	return storeImage(ctx, s.pool, s.uploads, data, by)
}

// storeImage checks an image, puts it in the bucket and records it.
func storeImage(ctx context.Context, pool *pgxpool.Pool, uploads Store, data []byte, by string) (Image, error) {
	if uploads == nil {
		return Image{}, ErrUploadsOff
	}
	if len(data) == 0 || len(data) > MaxImageBytes {
		return Image{}, ErrBadImage
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	t, ok := imageTypes[format]
	if err != nil || !ok || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxImageSide || cfg.Height > maxImageSide {
		return Image{}, ErrBadImage
	}
	sum := sha256.Sum256(data)
	key := UploadPrefix + hex.EncodeToString(sum[:20]) + "." + t.ext
	if err := uploads.Put(ctx, key, data, t.contentType); err != nil {
		return Image{}, fmt.Errorf("upload image: %w", err)
	}
	err = practicedb.New(pool).InsertImage(ctx, practicedb.InsertImageParams{
		Key: key, ContentType: t.contentType, Bytes: int32(len(data)), //nolint:gosec // ≤ 5 MB
		Width: int32(cfg.Width), Height: int32(cfg.Height), UploadedBy: by, //nolint:gosec // ≤ 6000
	})
	if err != nil {
		return Image{}, fmt.Errorf("record image: %w", err)
	}
	return Image{Key: key, Width: cfg.Width, Height: cfg.Height}, nil
}

// GetImage returns an uploaded image from the bucket.
func (s *Service) GetImage(ctx context.Context, key string) (r2.Object, error) {
	if s.uploads == nil {
		return r2.Object{}, ErrUploadsOff
	}
	obj, err := s.uploads.Get(ctx, key)
	if errors.Is(err, r2.ErrNotFound) {
		return r2.Object{}, ErrNotFound
	}
	return obj, err
}

// missingUploads lists the uploaded-image keys among figures that aren't stored.
func missingUploads(ctx context.Context, q *practicedb.Queries, figures []Figure) ([]string, error) {
	var keys []string
	for _, f := range figures {
		if strings.HasPrefix(f.Key, UploadPrefix) {
			keys = append(keys, f.Key)
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	found, err := q.ExistingImages(ctx, keys)
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, k := range found {
		have[k] = true
	}
	var missing []string
	for _, k := range keys {
		if !have[k] {
			missing = append(missing, k)
		}
	}
	return missing, nil
}
