// Package universities is the universities the apps show (spec 24): the
// dream-university globe in onboarding and the university section. The data
// is imported from SATMAKON's private makonbook-universities repository; the
// logos live in MakonBook's R2 bucket and are served from the API's origin.
package universities

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nazacd/makonbook-api/internal/platform/r2"
	"github.com/nazacd/makonbook-api/internal/universities/universitiesdb"
)

// ErrNotFound means no such university (or no logo for it).
var ErrNotFound = errors.New("no such university")

// Store is MakonBook's R2 bucket (the uploads bucket).
type Store interface {
	Get(ctx context.Context, key string) (r2.Object, error)
	Put(ctx context.Context, key string, body []byte, contentType string) error
}

// IDPattern is a university's id: the slug of its English name.
var IDPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Location is a point on the globe, in decimal degrees.
type Location struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// QS is the university's QS World University Rankings place, as QS gives it.
type QS struct {
	Rank    int    `json:"rank"`    // the lower end of a band: "601-610" is 601
	Display string `json:"display"` // as QS shows it: "2", "=45", "601-610"
}

// Summary is a university on the globe and in the search: the list endpoint
// gives every listed one.
type Summary struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	ShortName *string   `json:"short_name"`
	Country   string    `json:"country"` // ISO 3166-1 alpha-2
	City      *string   `json:"city"`
	Location  *Location `json:"location"`
	QS        *QS       `json:"qs"`
	LogoURL   *string   `json:"logo_url"`
	// SAT is the record's admissions.sat as the data gives it (policy, the
	// admitted students' 25th–75th percentiles, a published minimum), or null.
	SAT json.RawMessage `json:"sat"`
	// HasDetails says whether the university's whole record has been
	// collected (GET /v1/universities/{id} gives it).
	HasDetails bool `json:"has_details"`
}

// Detail is a university's card: its summary, the whole record and its
// country's record (spec 24.1), without the collectors' doubts.
type Detail struct {
	Summary
	Details        json.RawMessage `json:"details"`
	CountryDetails json.RawMessage `json:"country_details"`
}

// List is every listed university, in the data's order, with a version
// that changes whenever any of them does.
type List struct {
	Version      string    `json:"version"`
	Universities []Summary `json:"universities"`
}

// Logo is a university's logo file.
type Logo struct {
	Body        []byte
	ContentType string
	SHA256      string
}

// Service reads the universities.
type Service struct {
	pool  *pgxpool.Pool
	store Store
}

// NewService: store may be nil (no logos served).
func NewService(pool *pgxpool.Pool, store Store) *Service {
	return &Service{pool: pool, store: store}
}

// LogoURL is where the API serves a logo, versioned by its content so a
// browser can keep it for good.
func LogoURL(id string, sha pgtype.Text) *string {
	if !sha.Valid || len(sha.String) < 12 {
		return nil
	}
	u := fmt.Sprintf("/v1/universities/%s/logo?v=%s", id, sha.String[:12])
	return &u
}

func text(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

func location(lat, lng pgtype.Float8) *Location {
	if !lat.Valid || !lng.Valid {
		return nil
	}
	return &Location{Lat: lat.Float64, Lng: lng.Float64}
}

func qs(rank pgtype.Int4, display pgtype.Text) *QS {
	if !rank.Valid {
		return nil
	}
	return &QS{Rank: int(rank.Int32), Display: display.String}
}

func raw(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("null")
	}
	return b
}

// List gives every listed university.
func (s *Service) List(ctx context.Context) (List, error) {
	q := universitiesdb.New(s.pool)
	v, err := q.ListVersion(ctx)
	if err != nil {
		return List{}, err
	}
	rows, err := q.ListedUniversities(ctx)
	if err != nil {
		return List{}, err
	}
	out := List{Version: fmt.Sprintf("%d-%d", v.N, v.Latest.Time.UnixMilli()), Universities: make([]Summary, 0, len(rows))}
	for _, r := range rows {
		out.Universities = append(out.Universities, Summary{
			ID: r.ID, Name: r.Name, ShortName: text(r.ShortName), Country: r.Country, City: text(r.City),
			Location: location(r.Lat, r.Lng), QS: qs(r.QsRank, r.QsDisplay), LogoURL: LogoURL(r.ID, r.LogoSha256),
			SAT: raw(r.Sat), HasDetails: r.HasDetails,
		})
	}
	return out, nil
}

// SummaryOf is one university's summary (listed or not: a student may have
// chosen one that has since left the data).
func (s *Service) SummaryOf(ctx context.Context, id string) (Summary, error) {
	d, err := s.Get(ctx, id)
	return d.Summary, err
}

// Get gives a university's card.
func (s *Service) Get(ctx context.Context, id string) (Detail, error) {
	if !IDPattern.MatchString(id) {
		return Detail{}, ErrNotFound
	}
	q := universitiesdb.New(s.pool)
	r, err := q.GetUniversity(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Detail{}, ErrNotFound
	}
	if err != nil {
		return Detail{}, err
	}
	d := Detail{
		Summary: Summary{
			ID: r.ID, Name: r.Name, ShortName: text(r.ShortName), Country: r.Country, City: text(r.City),
			Location: location(r.Lat, r.Lng), QS: qs(r.QsRank, r.QsDisplay), LogoURL: LogoURL(r.ID, r.LogoSha256),
			SAT: raw(r.Sat), HasDetails: r.Details != nil,
		},
		Details: json.RawMessage("null"), CountryDetails: json.RawMessage("null"),
	}
	if r.Details != nil {
		if d.Details, err = withoutDoubts(r.Details); err != nil {
			return Detail{}, err
		}
	}
	c, err := q.GetCountry(ctx, r.Country)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return Detail{}, err
	default:
		if d.CountryDetails, err = withoutDoubts(c); err != nil {
			return Detail{}, err
		}
	}
	return d, nil
}

// withoutDoubts drops the collectors' notes to themselves from a record.
func withoutDoubts(b []byte) (json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	delete(m, "doubts")
	return json.Marshal(m)
}

// Logo reads a university's logo from the bucket.
func (s *Service) Logo(ctx context.Context, id string) (Logo, error) {
	if s.store == nil || !IDPattern.MatchString(id) {
		return Logo{}, ErrNotFound
	}
	r, err := universitiesdb.New(s.pool).UniversityLogo(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Logo{}, ErrNotFound
	}
	if err != nil {
		return Logo{}, err
	}
	obj, err := s.store.Get(ctx, r.LogoKey.String)
	if err != nil {
		return Logo{}, err
	}
	return Logo{Body: obj.Body, ContentType: r.LogoType.String, SHA256: r.LogoSha256.String}, nil
}
