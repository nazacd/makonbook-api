package roadmap

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	_ "image/png" // decoders, for DecodeConfig
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
	_ "golang.org/x/image/webp" //
	"golang.org/x/text/unicode/norm"

	"github.com/nazacd/makonbook-api/internal/roadmap/roadmapdb"
)

// Logos of the university stops (spec 22.7), used with the universities'
// permission and never committed: they are loaded from logos/ next to the
// curriculum files, named by the university's Slug.

// MaxLogoBytes bounds a logo file.
const MaxLogoBytes = 1 << 20

// logoPath is where the API serves a logo.
const logoPath = "/v1/logos/"

// logoTypes are the extensions a logo file may have.
var logoTypes = []string{".svg", ".png", ".webp"}

// folded are letters that don't decompose into a base letter and a mark.
var folded = map[rune]string{'ı': "i", 'ø': "o", 'ł': "l", 'đ': "d", 'ß': "ss", 'æ': "ae", 'œ': "oe", 'þ': "th", 'ð': "d"}

// Slug is the file name (without extension) of a university's logo: its
// name in lower case, accents folded to ASCII, each run of anything but
// letters and digits a "-", trimmed ("Boğaziçi University" →
// "bogazici-university").
func Slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range norm.NFD.String(strings.ToLower(name)) {
		if unicode.Is(unicode.Mn, r) {
			continue // an accent
		}
		s := string(r)
		if f, ok := folded[r]; ok {
			s = f
		}
		for _, c := range s {
			if c < 128 && (unicode.IsLetter(c) || unicode.IsDigit(c)) {
				if dash && b.Len() > 0 {
					b.WriteByte('-')
				}
				dash = false
				b.WriteRune(c)
			} else {
				dash = true
			}
		}
	}
	return b.String()
}

// CheckLogo checks a logo's bytes and returns its content type: an SVG
// (well-formed XML with an <svg> root) for a .svg file, a PNG or WebP that
// decodes for the others; at most MaxLogoBytes.
func CheckLogo(data []byte, ext string) (string, error) {
	if len(data) == 0 || len(data) > MaxLogoBytes {
		return "", errors.New("empty or over 1 MB")
	}
	if strings.EqualFold(ext, ".svg") {
		if err := checkSVG(data); err != nil {
			return "", err
		}
		return "image/svg+xml", nil
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return "", errors.New("not a PNG or WebP image")
	}
	switch format {
	case "png":
		return "image/png", nil
	case "webp":
		return "image/webp", nil
	}
	return "", fmt.Errorf("a %s image, not PNG or WebP", format)
}

func checkSVG(data []byte) error {
	dec := xml.NewDecoder(bytes.NewReader(data))
	root := ""
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("not well-formed XML: %w", err)
		}
		if el, ok := tok.(xml.StartElement); ok && root == "" {
			root = el.Name.Local
		}
	}
	if root != "svg" {
		return errors.New("not an SVG: the root element isn't <svg>")
	}
	return nil
}

// LogoReport is what loading logos did.
type LogoReport struct {
	Loaded    []string // slugs stored (new or changed)
	Unchanged []string
	Skipped   []SkippedLogo
	Missing   []string // universities of the courses without a logo
}

// SkippedLogo is a file that wasn't loaded, and why.
type SkippedLogo struct {
	File   string
	Reason string
}

// universities lists the stops' universities of courses, once each, in order.
func universities(courses []*Course) []string {
	var out []string
	for _, c := range courses {
		for _, u := range c.Units {
			if name := u.Stop.University; name != "" && !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
	}
	return out
}

// ImportLogos loads the logos in dirs (curriculum folders' logos/) whose
// name is the Slug of a university in courses: new and changed ones are
// stored, the same ones left alone, anything else skipped with its reason.
// A dir that doesn't exist loads nothing.
func (s *Service) ImportLogos(ctx context.Context, dirs []string, courses []Course) (LogoReport, error) {
	var rep LogoReport
	ptrs := make([]*Course, len(courses))
	for i := range courses {
		ptrs[i] = &courses[i]
	}
	names := universities(ptrs)
	wanted := map[string]bool{}
	for _, n := range names {
		wanted[Slug(n)] = true
	}
	q := roadmapdb.New(s.pool)
	seen := map[string]string{}
	for _, dir := range dirs {
		if err := s.importLogoDir(ctx, q, dir, wanted, seen, &rep); err != nil {
			return rep, err
		}
	}
	have, err := s.logoHashes(ctx)
	if err != nil {
		return rep, err
	}
	for _, n := range names {
		if _, ok := have[Slug(n)]; !ok {
			rep.Missing = append(rep.Missing, n)
		}
	}
	return rep, nil
}

func (s *Service) importLogoDir(ctx context.Context, q *roadmapdb.Queries, dir string, wanted map[string]bool, seen map[string]string, rep *LogoReport) error {
	entries, err := os.ReadDir(dir) //nolint:gosec // the operator's own folder
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("%s: can't read the folder (the import runs as uid 65532; chown -R 65532:65532 it): %w", dir, err)
	case err != nil:
		return err
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		slug := strings.TrimSuffix(name, filepath.Ext(name))
		skip := func(reason string) { rep.Skipped = append(rep.Skipped, SkippedLogo{File: name, Reason: reason}) }
		switch {
		case !slices.Contains(logoTypes, ext):
			skip("not .svg, .png or .webp")
			continue
		case !wanted[slug]:
			skip("no university of the courses has this name (see `curriculum logos`)")
			continue
		case seen[slug] != "":
			skip("another file is this university's logo: " + seen[slug])
			continue
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if info.Size() > MaxLogoBytes {
			skip("over 1 MB")
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // the operator's own files
		if errors.Is(err, fs.ErrPermission) {
			skip("can't be read (the import runs as uid 65532)")
			continue
		}
		if err != nil {
			return err
		}
		contentType, err := CheckLogo(data, ext)
		if err != nil {
			skip(err.Error())
			continue
		}
		seen[slug] = name
		sum := sha256.Sum256(data)
		hash := hex.EncodeToString(sum[:])
		cur, err := q.GetLogo(ctx, slug)
		switch {
		case err == nil && cur.Sha256 == hash && cur.ContentType == contentType:
			rep.Unchanged = append(rep.Unchanged, slug)
			continue
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		err = q.SaveLogo(ctx, roadmapdb.SaveLogoParams{Slug: slug, ContentType: contentType, Body: data, Sha256: hash})
		if err != nil {
			return err
		}
		rep.Loaded = append(rep.Loaded, slug)
	}
	return nil
}

// University is a stop's university and whether it has a logo.
type University struct {
	Name    string
	Slug    string
	HasLogo bool
}

// Universities lists the universities of every live course.
func (s *Service) Universities(ctx context.Context) ([]University, error) {
	versions, err := s.versions(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(versions))
	for id := range versions {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b string) int { return courseOrder(a) - courseOrder(b) })
	var courses []*Course
	for _, id := range ids {
		c, err := s.course(ctx, id)
		if err != nil {
			return nil, err
		}
		courses = append(courses, c)
	}
	have, err := s.logoHashes(ctx)
	if err != nil {
		return nil, err
	}
	var out []University
	for _, n := range universities(courses) {
		_, ok := have[Slug(n)]
		out = append(out, University{Name: n, Slug: Slug(n), HasLogo: ok})
	}
	return out, nil
}

// logoHashes is every stored logo's sha-256, by slug.
func (s *Service) logoHashes(ctx context.Context) (map[string]string, error) {
	rows, err := roadmapdb.New(s.pool).LogoHashes(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.Slug] = r.Sha256
	}
	return out, nil
}

// logoURL is where a university's logo is served (versioned by its bytes,
// so a new logo isn't cached as the old), or nil if it has none.
func logoURL(hashes map[string]string, university string) *string {
	slug := Slug(university)
	sha, ok := hashes[slug]
	if !ok {
		return nil
	}
	u := logoPath + slug + "?v=" + sha[:8]
	return &u
}

// Logo is a stored logo.
type Logo struct {
	ContentType string
	Body        []byte
	SHA256      string
}

// Logo returns a university's logo by slug; ErrNotFound if it has none.
func (s *Service) Logo(ctx context.Context, slug string) (Logo, error) {
	row, err := roadmapdb.New(s.pool).GetLogo(ctx, slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return Logo{}, ErrNotFound
	}
	if err != nil {
		return Logo{}, err
	}
	return Logo{ContentType: row.ContentType, Body: row.Body, SHA256: row.Sha256}, nil
}
