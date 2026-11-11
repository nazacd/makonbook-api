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
	"sort"
	"sync"
	"time"

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
	// Facts are the record's few facts the university section filters and
	// sorts by (in the list only; null while only listed).
	Facts *Facts `json:"facts,omitempty"`
}

// Facts are a university's facts for browsing (spec 24.4), read from its
// record: money in US dollars a year, null where the data has none.
type Facts struct {
	Type             *string   `json:"type"`
	Website          *string   `json:"website"`
	Tuition          *USDRange `json:"tuition"`
	Living           *USDRange `json:"living"`
	AcceptanceRate   *float64  `json:"acceptance_rate"`
	EnglishBachelors *string   `json:"english_bachelors"`
	Aid              AidFacts  `json:"aid"`
	Deadlines        []string  `json:"deadlines"` // the next intake's dates, YYYY-MM-DD, soonest first
	Region           *string   `json:"region"`
	// Full: researched in full (admissions page, costs, aid), not only listed.
	Full bool `json:"full"`
}

// USDRange is an amount a year in whole US dollars (min = max for one amount).
type USDRange struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// AidFacts is what the record says about aid for international undergraduates.
type AidFacts struct {
	NeedBased    *bool `json:"need_based"`
	FullNeed     *bool `json:"full_need"`
	Merit        *bool `json:"merit"`
	Scholarships int   `json:"scholarships"`
}

// record is the part of a university's record Facts are read from.
type record struct {
	Type             *string `json:"type"`
	Website          *string `json:"website"`
	AdmissionsURL    *string `json:"admissions_url"`
	EnglishBachelors *string `json:"english_bachelors"`
	Tuition          *money  `json:"tuition"`
	LivingCost       *money  `json:"living_cost"`
	Region           *struct {
		Name string `json:"name"`
	} `json:"region"`
	Aid *struct {
		NeedBased    *bool             `json:"need_based_international"`
		FullNeed     *bool             `json:"meets_full_need_international"`
		Merit        *bool             `json:"merit_scholarships_international"`
		Scholarships []json.RawMessage `json:"scholarships"`
	} `json:"aid"`
	Admissions *struct {
		AcceptanceRate *struct {
			Value *float64 `json:"value"`
		} `json:"acceptance_rate"`
		Deadlines []struct {
			Date string `json:"date"`
		} `json:"deadlines"`
	} `json:"admissions"`
}

// money is the data's money object (README 4.2), in its dollar figures.
type money struct {
	USD    *float64 `json:"usd"`
	USDMin *float64 `json:"usd_min"`
	USDMax *float64 `json:"usd_max"`
}

func (m *money) usd() *USDRange {
	switch {
	case m == nil:
		return nil
	case m.USD != nil:
		return &USDRange{int(*m.USD), int(*m.USD)}
	case m.USDMin != nil && m.USDMax != nil:
		return &USDRange{int(*m.USDMin), int(*m.USDMax)}
	}
	return nil
}

// factsOf reads a record's Facts (nil for no record or one that can't be read).
func factsOf(details []byte) *Facts {
	if details == nil {
		return nil
	}
	var r record
	if json.Unmarshal(details, &r) != nil {
		return nil
	}
	f := &Facts{Type: r.Type, Website: r.Website, Tuition: r.Tuition.usd(), Living: r.LivingCost.usd(), EnglishBachelors: r.EnglishBachelors, Deadlines: []string{}}
	f.Full = r.AdmissionsURL != nil && f.Tuition != nil
	if r.Region != nil && r.Region.Name != "" {
		f.Region = &r.Region.Name
	}
	if r.Aid != nil {
		f.Aid = AidFacts{NeedBased: r.Aid.NeedBased, FullNeed: r.Aid.FullNeed, Merit: r.Aid.Merit, Scholarships: len(r.Aid.Scholarships)}
	}
	if a := r.Admissions; a != nil {
		if a.AcceptanceRate != nil {
			f.AcceptanceRate = a.AcceptanceRate.Value
		}
		for _, d := range a.Deadlines {
			if len(d.Date) == len("2006-01-02") {
				f.Deadlines = append(f.Deadlines, d.Date)
			}
		}
		sort.Strings(f.Deadlines)
	}
	return f
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

	mu     sync.Mutex
	cached List // the last list worked out, by version
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

// List gives every listed university. It is worked out again only when
// the data's version changes.
func (s *Service) List(ctx context.Context) (List, error) {
	q := universitiesdb.New(s.pool)
	v, err := q.ListVersion(ctx)
	if err != nil {
		return List{}, err
	}
	version := fmt.Sprintf("%d-%d", v.N, v.Latest.Time.UnixMilli())
	s.mu.Lock()
	cached := s.cached
	s.mu.Unlock()
	if cached.Version == version {
		return cached, nil
	}
	rows, err := q.ListedUniversities(ctx)
	if err != nil {
		return List{}, err
	}
	out := List{Version: version, Universities: make([]Summary, 0, len(rows))}
	for _, r := range rows {
		out.Universities = append(out.Universities, Summary{
			ID: r.ID, Name: r.Name, ShortName: text(r.ShortName), Country: r.Country, City: text(r.City),
			Location: location(r.Lat, r.Lng), QS: qs(r.QsRank, r.QsDisplay), LogoURL: LogoURL(r.ID, r.LogoSha256),
			SAT: raw(r.Sat), HasDetails: r.Details != nil, Facts: factsOf(r.Details),
		})
	}
	s.mu.Lock()
	s.cached = out
	s.mu.Unlock()
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

// Saved is a university a student saved to their list.
type Saved struct {
	ID      string    `json:"id"`
	SavedAt time.Time `json:"saved_at"`
}

// SavedOf is a student's list, newest first.
func (s *Service) SavedOf(ctx context.Context, student int32) ([]Saved, error) {
	rows, err := universitiesdb.New(s.pool).SavedUniversities(ctx, student)
	if err != nil {
		return nil, err
	}
	out := make([]Saved, 0, len(rows))
	for _, r := range rows {
		out = append(out, Saved{ID: r.UniversityID, SavedAt: r.SavedAt.Time.UTC()})
	}
	return out, nil
}

// Save puts a university on a student's list (or takes it off). ErrNotFound
// for an unknown university.
func (s *Service) Save(ctx context.Context, student int32, id string, on bool) error {
	q := universitiesdb.New(s.pool)
	if !on {
		return q.UnsaveUniversity(ctx, universitiesdb.UnsaveUniversityParams{StudentClientID: student, UniversityID: id})
	}
	if !IDPattern.MatchString(id) {
		return ErrNotFound
	}
	ok, err := q.UniversityExists(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	_, err = q.SaveUniversity(ctx, universitiesdb.SaveUniversityParams{StudentClientID: student, UniversityID: id})
	return err
}
