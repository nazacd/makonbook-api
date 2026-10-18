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

	"github.com/jackc/pgx/v5"
	_ "golang.org/x/image/webp" //

	"github.com/nazacd/makonbook-api/internal/practice/practicedb"
)

// UploadPrefix marks images editors uploaded (kept in our database), as
// opposed to the bank's images in SAT MAKON's R2 bucket.
const UploadPrefix = "mb/"

// MaxImageBytes bounds an upload.
const MaxImageBytes = 5 << 20

const maxImageSide = 6000

// ErrBadImage means the upload isn't a PNG, JPEG, GIF or WebP image we accept.
var ErrBadImage = errors.New("not an image we accept: PNG, JPEG, GIF or WebP, at most 5 MB and 6000 px a side")

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

// UploadImage stores an image and returns its key. The key comes from the
// bytes, so uploading the same file twice stores it once.
func (s *Service) UploadImage(ctx context.Context, data []byte, by string) (Image, error) {
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
	err = practicedb.New(s.pool).InsertImage(ctx, practicedb.InsertImageParams{
		Key: key, ContentType: t.contentType, Data: data, Width: int32(cfg.Width), Height: int32(cfg.Height), UploadedBy: by, //nolint:gosec // ≤ 6000
	})
	if err != nil {
		return Image{}, fmt.Errorf("store image: %w", err)
	}
	return Image{Key: key, Width: cfg.Width, Height: cfg.Height}, nil
}

// GetImage returns an uploaded image: its content type and bytes.
func (s *Service) GetImage(ctx context.Context, key string) (string, []byte, error) {
	img, err := practicedb.New(s.pool).GetImage(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	return img.ContentType, img.Data, err
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
