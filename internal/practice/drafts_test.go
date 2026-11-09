package practice

import (
	"strings"
	"testing"

	"github.com/nazacd/makonbook-api/internal/platform/testdb"
)

const evalSkill = "Evaluating statistical claims: observational studies and experiments"

func draftLine(stem string) string {
	return `{"subject":"math","type":"mcq","stem":"` + stem + `","choices":{"A":"a","B":"b","C":"c","D":"d"},` +
		`"correct":"C","explanation":"Because.","domain":"Problem-Solving and Data Analysis",` +
		`"skill":"` + evalSkill + `","difficulty":"medium"}`
}

func TestReadDraftsRefuses(t *testing.T) {
	for name, line := range map[string]string{
		"unknown field": strings.Replace(draftLine("Q?"), `"subject"`, `"subjekt":"x","subject"`, 1),
		"no difficulty": strings.Replace(draftLine("Q?"), `,"difficulty":"medium"`, ``, 1),
		"skill":         strings.Replace(draftLine("Q?"), evalSkill, "Statistics", 1),
		"correct":       strings.Replace(draftLine("Q?"), `"correct":"C"`, `"correct":"E"`, 1),
		"subject":       strings.Replace(draftLine("Q?"), `"math"`, `"physics"`, 1),
		"figures":       strings.Replace(draftLine("Q?"), `"correct"`, `"figures":[{"key":"sat/g.png","alt":"g"}],"correct"`, 1),
	} {
		if _, err := ReadDrafts(strings.NewReader(draftLine("Fine?") + "\n" + line + "\n")); err == nil || !strings.HasPrefix(err.Error(), "line 2:") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := ReadDrafts(strings.NewReader("\n\n")); err == nil {
		t.Error("an empty file was read")
	}
}

func TestAddDrafts(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()

	grid := `{"subject":"math","type":"grid_in","stem":"How many?","accepted":[" 17/2 ","8.5"],"explanation":"Count.",` +
		`"domain":"Algebra","skill":"Linear functions","difficulty":"easy"}`
	// The third line repeats the first, written with other spacing and case.
	file := draftLine("First question?") + "\n\n" + grid + "\n" + draftLine("first  QUESTION?") + "\n"
	drafts, err := ReadDrafts(strings.NewReader(file))
	if err != nil {
		t.Fatal(err)
	}
	added, err := AddDrafts(ctx, pool, drafts)
	if err != nil {
		t.Fatal(err)
	}
	if len(added.IDs) != 2 || len(added.Repeats) != 1 || added.Repeats[0] != 3 {
		t.Fatalf("added %+v", added)
	}

	// They wait for an editor: in review, never auto-approved, not in any
	// roadmap draw, with a first version marked as a draft.
	var n int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM questions q JOIN question_versions v ON v.question_id = q.id
		WHERE q.status = 'review' AND NOT q.auto_approved AND q.ai_risk IS NULL AND q.version = 1
		  AND v.version = 1 AND v.source = 'draft' AND v.created_by IS NULL`).Scan(&n)
	if err != nil || n != 2 {
		t.Fatalf("drafts in review: %d %v", n, err)
	}
	var accepted []string
	if err := pool.QueryRow(ctx, `SELECT accepted FROM questions WHERE type = 'grid_in'`).Scan(&accepted); err != nil ||
		strings.Join(accepted, ",") != "17/2,8.5" {
		t.Errorf("accepted %v %v", accepted, err)
	}

	// Approving one in the panel runs the save's checks, and it passes.
	svc := NewService(pool, nil)
	q, err := svc.SetApproval(ctx, added.IDs[0], "editor@satmakon", 1, true)
	if err != nil || q.Status != "approved" {
		t.Fatalf("approve: %+v %v", q, err)
	}

	// Adding the same file again stores nothing new.
	again, err := AddDrafts(ctx, pool, drafts)
	if err != nil || len(again.IDs) != 0 || len(again.Repeats) != 3 {
		t.Errorf("again: %+v %v", again, err)
	}
}
