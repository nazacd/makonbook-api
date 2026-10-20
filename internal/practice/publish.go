package practice

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nazacd/makonbook-api/internal/practice/practicedb"
)

// PackageFormat changes when a device must read packages differently.
const PackageFormat = 1

// ImagePrefix is where published images sit in MakonBook's bucket.
const ImagePrefix = "p/img/"

// Package is a published version of a test: everything a device needs to
// take it offline (spec 9.5). A version never changes once published.
type Package struct {
	Format      int                     `json:"format"`
	Code        string                  `json:"code"`
	Title       string                  `json:"title"`
	Version     int32                   `json:"version"`
	PublishedAt time.Time               `json:"published_at"`
	Sections    []Section               `json:"sections"`
	Images      map[string]PackageImage `json:"images"` // a figure's key → its file
}

// Section is one subject: its two modules and its score scale.
type Section struct {
	Subject string   `json:"subject"`
	Modules []Module `json:"modules"`
	// Scale is the estimated 200–800 section score for each raw score
	// (the number right in both modules).
	Scale []int `json:"scale"`
}

// Module is one timed module.
type Module struct {
	Module    int16             `json:"module"`
	Minutes   int               `json:"minutes"`
	Questions []PackageQuestion `json:"questions"`
}

// PackageQuestion is a question as a student gets it, with its key and
// explanation for the review afterwards. Text is in the stored format (9.2).
type PackageQuestion struct {
	ID          string            `json:"id"`
	Version     int32             `json:"version"` // the question's own version
	Position    int16             `json:"position"`
	Type        string            `json:"type"`
	Passage     *string           `json:"passage"`
	Stem        string            `json:"stem"`
	Choices     map[string]string `json:"choices"`
	Figures     []Figure          `json:"figures"`
	Key         *string           `json:"correct"`
	Accepted    []string          `json:"accepted"`
	Explanation string            `json:"explanation"`
	Domain      string            `json:"domain"`
	Skill       string            `json:"skill"`
	Difficulty  *string           `json:"difficulty"`
}

// PackageImage is an image file the device downloads beside the package.
type PackageImage struct {
	File   string `json:"file"` // <sha-256>.<ext>
	Bytes  int32  `json:"bytes"`
	Width  int32  `json:"width"`
	Height int32  `json:"height"`
}

// Published is a version that Publish made.
type Published struct {
	Code    string
	Version int32
	Bytes   int32
}

// Publish builds a new version of every test whose questions are all
// approved and whose content changed since its last version (or that has
// none), copying its images into MakonBook's bucket first. A test that
// fails (an image it can't read) is skipped and reported; the others go on.
func (s *Service) Publish(ctx context.Context) ([]Published, error) {
	if s.uploads == nil {
		return nil, ErrUploadsOff
	}
	q := practicedb.New(s.pool)
	tests, err := q.CompleteTests(ctx)
	if err != nil {
		return nil, err
	}
	var out []Published
	var errs []error
	for _, t := range tests {
		p, err := s.publishTest(ctx, q, t)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", t.Code, err))
			continue
		}
		if p != nil {
			out = append(out, *p)
		}
	}
	return out, errors.Join(errs...)
}

func (s *Service) publishTest(ctx context.Context, q *practicedb.Queries, t practicedb.CompleteTestsRow) (*Published, error) {
	pkg, err := buildPackage(ctx, q, t.ID, t.Code, t.Title)
	if err != nil {
		return nil, err
	}
	hash := packageHash(pkg)
	latest, err := q.LatestTestVersion(ctx, t.ID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return nil, err
	case latest.ContentHash == hash:
		return nil, nil // nothing changed
	}
	size, err := s.publishImages(ctx, q, &pkg)
	if err != nil {
		return nil, err
	}
	pkg.Version, pkg.PublishedAt = latest.Version+1, time.Now().UTC().Truncate(time.Second)
	body, err := json.Marshal(pkg)
	if err != nil {
		return nil, err
	}
	size += int64(len(body))
	err = q.InsertTestVersion(ctx, practicedb.InsertTestVersionParams{
		TestID: t.ID, Version: pkg.Version, ContentHash: hash, Package: body, Bytes: int32(min(size, 1<<31-1)), //nolint:gosec // bounded
	})
	if err != nil {
		return nil, err
	}
	return &Published{Code: t.Code, Version: pkg.Version, Bytes: int32(min(size, 1<<31-1))}, nil //nolint:gosec // bounded
}

// buildPackage reads a test's questions into a package without its version
// and images' files (Images keyed by figure, files filled by publishImages).
func buildPackage(ctx context.Context, q *practicedb.Queries, testID pgtype.UUID, code, title string) (Package, error) {
	rows, err := q.TestContent(ctx, testID)
	if err != nil {
		return Package{}, err
	}
	pkg := Package{Format: PackageFormat, Code: code, Title: title, Sections: []Section{}, Images: map[string]PackageImage{}}
	for _, r := range rows {
		row := r.Question
		e, err := editOf(row)
		if err != nil {
			return Package{}, fmt.Errorf("question %s: %w", uuidString(row.ID), err)
		}
		e.normalize(row.Type)
		pq := PackageQuestion{
			ID: uuidString(row.ID), Version: row.Version, Position: r.Position, Type: row.Type,
			Passage: e.Passage, Stem: e.Stem, Choices: e.Choices, Figures: e.Figures, Key: textPtr(row.Correct),
			Accepted: e.Accepted, Explanation: e.Explanation, Domain: e.Domain, Skill: e.Skill,
			Difficulty: textPtr(row.Difficulty),
		}
		for _, f := range pq.Figures {
			pkg.Images[f.Key] = PackageImage{}
		}
		if n := len(pkg.Sections); n == 0 || pkg.Sections[n-1].Subject != row.Subject {
			pkg.Sections = append(pkg.Sections, Section{Subject: row.Subject})
		}
		sec := &pkg.Sections[len(pkg.Sections)-1]
		if n := len(sec.Modules); n == 0 || sec.Modules[n-1].Module != r.Module {
			sec.Modules = append(sec.Modules, Module{Module: r.Module, Minutes: moduleMinutes[row.Subject]})
		}
		mod := &sec.Modules[len(sec.Modules)-1]
		mod.Questions = append(mod.Questions, pq)
	}
	for i := range pkg.Sections {
		n := 0
		for _, m := range pkg.Sections[i].Modules {
			n += len(m.Questions)
		}
		pkg.Sections[i].Scale = Scale(pkg.Sections[i].Subject, n)
	}
	return pkg, nil
}

// packageHash identifies a package's content apart from its version and
// time, and apart from where its images come from (their keys).
func packageHash(p Package) string {
	p.Version, p.PublishedAt = 0, time.Time{}
	b, _ := json.Marshal(p)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// publishImages copies each image the package uses into MakonBook's bucket
// (once per file: they are named by their bytes) and fills in its file. It
// returns the images' total size.
func (s *Service) publishImages(ctx context.Context, q *practicedb.Queries, pkg *Package) (int64, error) {
	var total int64
	for key := range pkg.Images {
		obj, err := s.Figure(ctx, key)
		if err != nil {
			return 0, fmt.Errorf("image %s: %w", key, err)
		}
		cfg, format, err := image.DecodeConfig(bytes.NewReader(obj.Body))
		t, ok := imageTypes[format]
		if err != nil || !ok {
			return 0, fmt.Errorf("image %s: not a PNG, JPEG, GIF or WebP image", key)
		}
		sum := sha256.Sum256(obj.Body)
		file := hex.EncodeToString(sum[:20]) + "." + t.ext
		img := PackageImage{File: file, Bytes: int32(len(obj.Body)), Width: int32(cfg.Width), Height: int32(cfg.Height)} //nolint:gosec // ≤ 15 MB, small sides
		have, err := q.PublishedImages(ctx, []string{file})
		if err != nil {
			return 0, err
		}
		if len(have) == 0 {
			if err := s.uploads.Put(ctx, ImagePrefix+file, obj.Body, t.contentType); err != nil {
				return 0, fmt.Errorf("image %s: %w", key, err)
			}
			err = q.InsertPublishedImage(ctx, practicedb.InsertPublishedImageParams{
				File: file, ContentType: t.contentType, Bytes: img.Bytes, Width: img.Width, Height: img.Height,
			})
			if err != nil {
				return 0, err
			}
		}
		pkg.Images[key] = img
		total += int64(img.Bytes)
	}
	return total, nil
}

// PublishLoop publishes what is ready now and then every interval, until ctx
// ends (the worker runs it).
func (s *Service) PublishLoop(ctx context.Context, interval time.Duration, log *slog.Logger) {
	for {
		published, err := s.Publish(ctx)
		for _, p := range published {
			log.Info("practice: published a test", "test", p.Code, "version", p.Version, "bytes", p.Bytes)
		}
		if err != nil && ctx.Err() == nil {
			log.Error("practice: publishing failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
