package universities

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	_ "image/png" // the PNG decoder, for DecodeConfig
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/nazacd/makonbook-api/internal/universities/universitiesdb"
)

// The import (spec 24.2) reads a checkout of makonbook-universities:
// data/list.jsonl (every university), data/universities.jsonl (the whole
// records collected so far), data/countries.jsonl and logos/. It checks
// everything first and stores nothing unless all of it passes.

// MaxLogoBytes bounds a logo file.
const MaxLogoBytes = 1 << 20

var countryPattern = regexp.MustCompile(`^[A-Z]{2}$`)

// maxRank bounds a QS rank (QS ranks about 1500).
const maxRank = 100000

// Importer loads the data; Store is needed when there are logos to upload.
type Importer struct {
	Pool  *pgxpool.Pool
	Store Store
	// Progress, when set, hears how the logo uploads go (done of total),
	// every few uploads and at the end; the first upload takes minutes.
	Progress func(done, total int)
}

// uploaders is how many logos go up at once.
const uploaders = 8

// ImportSummary says what an import did.
type ImportSummary struct {
	Listed, New, Changed, Unlisted int
	WithDetails, WithLogo          int
	LogosUploaded                  int
	Countries                      int
}

// ImportError lists every problem found; nothing was stored.
type ImportError struct{ Problems []string }

func (e *ImportError) Error() string {
	return fmt.Sprintf("%d problems, nothing stored:\n  %s", len(e.Problems), strings.Join(e.Problems, "\n  "))
}

// entry is a line of list.jsonl, or the same fields of a whole record.
type entry struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	ShortName  *string   `json:"short_name"`
	Country    string    `json:"country"`
	City       *string   `json:"city"`
	Location   *Location `json:"location"`
	ListReason string    `json:"list_reason"`
	QS         *struct {
		Rank    int    `json:"rank"`
		Display string `json:"rank_display"`
	} `json:"qs"`
	Logo *struct {
		File string `json:"file"`
	} `json:"logo"`
	Admissions *struct {
		SAT json.RawMessage `json:"sat"`
	} `json:"admissions"`
}

type logoFile struct {
	key, contentType, sha string
	body                  []byte
}

// Import loads dir (a checkout of makonbook-universities) in one transaction.
func (im Importer) Import(ctx context.Context, dir string) (ImportSummary, error) {
	var sum ImportSummary
	var problems []string
	bad := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }

	list, err := readJSONL(filepath.Join(dir, "data", "list.jsonl"))
	if err != nil {
		return sum, fmt.Errorf("data/list.jsonl: %w", err)
	}
	records := map[string]json.RawMessage{}
	recs, err := readJSONL(filepath.Join(dir, "data", "universities.jsonl"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return sum, fmt.Errorf("data/universities.jsonl: %w", err)
	}
	for n, r := range recs {
		var e entry
		if err := json.Unmarshal(r, &e); err != nil || e.ID == "" {
			bad("data/universities.jsonl line %d: not a record with an id", n+1)
			continue
		}
		if _, dup := records[e.ID]; dup {
			bad("data/universities.jsonl: %s appears twice", e.ID)
		}
		records[e.ID] = r
	}
	countries, err := readJSONL(filepath.Join(dir, "data", "countries.jsonl"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return sum, fmt.Errorf("data/countries.jsonl: %w", err)
	}

	type row struct {
		p    universitiesdb.UpsertUniversityParams
		logo *logoFile
	}
	var rows []row
	seen := map[string]bool{}
	for n, line := range list {
		var e entry
		if err := json.Unmarshal(line, &e); err != nil {
			bad("data/list.jsonl line %d: %v", n+1, err)
			continue
		}
		where := fmt.Sprintf("data/list.jsonl line %d (%s)", n+1, e.ID)
		var details []byte
		if rec, ok := records[e.ID]; ok {
			// The whole record is newer than the list: its fields win.
			if err := json.Unmarshal(rec, &e); err != nil {
				bad("%s: its record: %v", where, err)
				continue
			}
			details = rec
		}
		switch {
		case !IDPattern.MatchString(e.ID):
			bad("%s: id isn't a slug", where)
		case seen[e.ID]:
			bad("%s: appears twice", where)
		case strings.TrimSpace(e.Name) == "":
			bad("%s: no name", where)
		case !countryPattern.MatchString(e.Country):
			bad("%s: country %q isn't an ISO code", where, e.Country)
		case e.ListReason != "qs" && e.ListReason != "uzbekistan" && e.ListReason != "roadmap":
			bad("%s: list_reason %q", where, e.ListReason)
		case e.Location != nil && (math.Abs(e.Location.Lat) > 90 || math.Abs(e.Location.Lng) > 180):
			bad("%s: location out of range", where)
		case e.QS != nil && (e.QS.Rank < 1 || e.QS.Rank > maxRank):
			bad("%s: qs rank %d", where, e.QS.Rank)
		}
		seen[e.ID] = true
		p := universitiesdb.UpsertUniversityParams{
			ID: e.ID, Name: strings.TrimSpace(e.Name), ShortName: optText(e.ShortName), Country: e.Country,
			City: optText(e.City), ListReason: e.ListReason, Details: details, Position: int32(n),
		}
		if e.Location != nil {
			p.Lat = pgtype.Float8{Float64: e.Location.Lat, Valid: true}
			p.Lng = pgtype.Float8{Float64: e.Location.Lng, Valid: true}
		}
		if e.QS != nil {
			p.QsRank = pgtype.Int4{Int32: int32(e.QS.Rank), Valid: true} //nolint:gosec // 1..maxRank, checked above
			p.QsDisplay = pgtype.Text{String: e.QS.Display, Valid: true}
		}
		if e.Admissions != nil && len(e.Admissions.SAT) > 0 && string(e.Admissions.SAT) != "null" {
			p.Sat = e.Admissions.SAT
		}
		r := row{p: p}
		if e.Logo != nil && e.Logo.File != "" {
			l, err := readLogo(dir, e.ID, e.Logo.File)
			if err != nil {
				bad("%s: logo: %v", where, err)
			} else {
				r.logo = &l
				r.p.LogoKey = pgtype.Text{String: l.key, Valid: true}
				r.p.LogoType = pgtype.Text{String: l.contentType, Valid: true}
				r.p.LogoSha256 = pgtype.Text{String: l.sha, Valid: true}
			}
		}
		rows = append(rows, r)
	}
	for id := range records {
		if !seen[id] {
			bad("data/universities.jsonl: %s isn't in data/list.jsonl", id)
		}
	}
	type country struct {
		code string
		body []byte
	}
	var cs []country
	for n, line := range countries {
		var c struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(line, &c); err != nil || !countryPattern.MatchString(c.Code) {
			bad("data/countries.jsonl line %d: no ISO code", n+1)
			continue
		}
		cs = append(cs, country{c.Code, line})
	}
	if len(rows) == 0 && len(problems) == 0 {
		bad("data/list.jsonl has no universities")
	}
	if len(problems) > 0 {
		return sum, &ImportError{Problems: problems}
	}

	// Logos first (their keys are their content, so a repeat is harmless),
	// then the rows in one transaction.
	q := universitiesdb.New(im.Pool)
	stored, err := q.StoredLogos(ctx)
	if err != nil {
		return sum, err
	}
	have := map[string]string{}
	for _, s := range stored {
		have[s.ID] = s.LogoSha256.String
	}
	var todo []*logoFile
	queued := map[string]bool{}
	for _, r := range rows {
		if r.logo == nil || have[r.p.ID] == r.logo.sha || queued[r.logo.key] {
			continue
		}
		queued[r.logo.key] = true
		todo = append(todo, r.logo)
	}
	if len(todo) > 0 && im.Store == nil {
		return sum, errors.New("there are logos to upload: set the R2_UPLOADS_* settings")
	}
	if err := im.upload(ctx, todo); err != nil {
		return sum, err
	}
	sum.LogosUploaded = len(todo)
	err = pgx.BeginFunc(ctx, im.Pool, func(tx pgx.Tx) error {
		q := universitiesdb.New(tx)
		var now pgtype.Timestamptz // the transaction's now(): a changed row's updated_at
		if err := tx.QueryRow(ctx, "SELECT now()").Scan(&now); err != nil {
			return err
		}
		ids := make([]string, 0, len(rows))
		for _, r := range rows {
			res, err := q.UpsertUniversity(ctx, r.p)
			if err != nil {
				return fmt.Errorf("%s: %w", r.p.ID, err)
			}
			ids = append(ids, r.p.ID)
			sum.Listed++
			if res.Inserted {
				sum.New++
			} else if res.UpdatedAt.Time.Equal(now.Time) {
				sum.Changed++
			}
			if r.p.Details != nil {
				sum.WithDetails++
			}
			if r.logo != nil {
				sum.WithLogo++
			}
		}
		unlisted, err := q.UnlistOthers(ctx, ids)
		if err != nil {
			return err
		}
		sum.Unlisted = int(unlisted)
		for _, c := range cs {
			if err := q.UpsertCountry(ctx, universitiesdb.UpsertCountryParams{Code: c.code, Details: c.body}); err != nil {
				return fmt.Errorf("country %s: %w", c.code, err)
			}
			sum.Countries++
		}
		return nil
	})
	return sum, err
}

// upload puts the logos in the bucket, a few at a time, telling Progress.
func (im Importer) upload(ctx context.Context, todo []*logoFile) error {
	if len(todo) == 0 {
		return nil
	}
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(uploaders)
	var mu sync.Mutex
	done := 0
	for _, l := range todo {
		g.Go(func() error {
			if err := im.Store.Put(ctx, l.key, l.body, l.contentType); err != nil {
				return fmt.Errorf("upload logo %s: %w", l.key, err)
			}
			mu.Lock()
			done++
			n := done
			mu.Unlock()
			if im.Progress != nil && (n%50 == 0 || n == len(todo)) {
				im.Progress(n, len(todo))
			}
			return nil
		})
	}
	return g.Wait()
}

func optText(s *string) pgtype.Text {
	if s == nil || strings.TrimSpace(*s) == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: strings.TrimSpace(*s), Valid: true}
}

// readJSONL reads one JSON object per line, skipping blank lines.
func readJSONL(path string) ([]json.RawMessage, error) {
	f, err := os.Open(path) //nolint:gosec // the operator's own checkout
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // read only
	var out []json.RawMessage
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for n := 1; sc.Scan(); n++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if !json.Valid(line) || line[0] != '{' {
			return nil, fmt.Errorf("line %d isn't a JSON object", n)
		}
		out = append(out, append(json.RawMessage(nil), line...))
	}
	return out, sc.Err()
}

// readLogo reads and checks a logo: logos/<id>.svg (well-formed XML with an
// <svg> root) or .png (decodes), padded for a circle (square), at most 1 MB.
func readLogo(dir, id, file string) (logoFile, error) {
	ext := filepath.Ext(file)
	if file != "logos/"+id+ext || (ext != ".svg" && ext != ".png") {
		return logoFile{}, fmt.Errorf("%q must be logos/%s.svg or .png", file, id)
	}
	body, err := os.ReadFile(filepath.Join(dir, file)) //nolint:gosec // a checked name in the operator's checkout
	if err != nil {
		return logoFile{}, err
	}
	if len(body) == 0 || len(body) > MaxLogoBytes {
		return logoFile{}, errors.New("empty or over 1 MB")
	}
	contentType := "image/png"
	if ext == ".svg" {
		contentType = "image/svg+xml"
		if err := checkSVG(body); err != nil {
			return logoFile{}, err
		}
	} else {
		cfg, format, err := image.DecodeConfig(bytes.NewReader(body))
		if err != nil || format != "png" {
			return logoFile{}, errors.New("not a PNG")
		}
		if cfg.Width != cfg.Height {
			return logoFile{}, fmt.Errorf("%dx%d, not square: pad it (tools/fetch_logos.py --pad)", cfg.Width, cfg.Height)
		}
	}
	sum := sha256.Sum256(body)
	sha := hex.EncodeToString(sum[:])
	return logoFile{key: "universities/logos/" + sha + ext, contentType: contentType, sha: sha, body: body}, nil
}

// checkSVG: well-formed XML whose root element is <svg>.
func checkSVG(body []byte) error {
	dec := xml.NewDecoder(bytes.NewReader(body))
	root := ""
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("not well-formed XML: %w", err)
		}
		if se, ok := tok.(xml.StartElement); ok && root == "" {
			root = se.Name.Local
		}
	}
	if root != "svg" {
		return errors.New("the root element isn't <svg>")
	}
	return nil
}
