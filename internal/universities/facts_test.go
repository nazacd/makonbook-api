package universities

import (
	"errors"
	"testing"

	"github.com/nazacd/makonbook-api/internal/platform/testdb"
)

func TestFactsOf(t *testing.T) {
	if factsOf(nil) != nil {
		t.Error("no record: facts")
	}
	f := factsOf([]byte(`{"type":"private","website":"https://x.edu","admissions_url":"https://x.edu/apply",
		"english_bachelors":"all","tuition":{"min":40000,"max":50000,"currency":"USD","usd_min":40000,"usd_max":50000},
		"living_cost":{"amount":0,"currency":"EUR","usd":0},
		"region":{"name":"Northern Cyprus","note":"n"},
		"aid":{"need_based_international":true,"meets_full_need_international":null,"merit_scholarships_international":false,"scholarships":[{"name":"A"},{"name":"B"}]},
		"admissions":{"acceptance_rate":{"value":0.07,"year":"2024"},"deadlines":[{"round":"regular","date":"2027-01-05"},{"round":"early","date":"2026-11-01"}]}}`))
	switch {
	case f == nil || *f.Type != "private" || f.Tuition.Min != 40000 || f.Tuition.Max != 50000 || f.Living.Max != 0:
		t.Fatalf("money: %+v", f)
	case !f.Full || *f.Region != "Northern Cyprus" || *f.AcceptanceRate != 0.07 || *f.EnglishBachelors != "all":
		t.Errorf("facts: %+v", f)
	case !*f.Aid.NeedBased || f.Aid.FullNeed != nil || *f.Aid.Merit || f.Aid.Scholarships != 2:
		t.Errorf("aid: %+v", f.Aid)
	case len(f.Deadlines) != 2 || f.Deadlines[0] != "2026-11-01":
		t.Errorf("deadlines: %v", f.Deadlines)
	}
	light := factsOf([]byte(`{"type":"public","website":null,"tuition":null,"aid":null,"admissions":null}`))
	if light == nil || light.Full || light.Tuition != nil || light.Deadlines == nil {
		t.Errorf("light: %+v", light)
	}
}

func TestSaved(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `
		INSERT INTO offices (id, name) VALUES (1, 'Main');
		INSERT INTO students (client_id, hh_student_id, first_name, last_name, phone_raw, office_ids) VALUES (11, 1, 'A', 'B', '', '{1}');
		INSERT INTO universities (id, name, country, list_reason, details) VALUES
			('a-uni', 'A University', 'GB', 'qs', '{"type":"public"}'), ('b-uni', 'B University', 'US', 'qs', NULL);`); err != nil {
		t.Fatal(err)
	}
	svc := NewService(pool, nil)
	l, err := svc.List(ctx)
	if err != nil || len(l.Universities) != 2 || l.Universities[0].Facts == nil || l.Universities[1].Facts != nil {
		t.Fatalf("list: %+v %v", l, err)
	}
	if again, _ := svc.List(ctx); again.Version != l.Version {
		t.Error("version moved")
	}
	if err := svc.Save(ctx, 11, "no-such", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	for _, id := range []string{"a-uni", "b-uni", "a-uni"} {
		if err := svc.Save(ctx, 11, id, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.Save(ctx, 11, "a-uni", false); err != nil {
		t.Fatal(err)
	}
	saved, err := svc.SavedOf(ctx, 11)
	if err != nil || len(saved) != 1 || saved[0].ID != "b-uni" {
		t.Errorf("saved: %+v %v", saved, err)
	}
}
