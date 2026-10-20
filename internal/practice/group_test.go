package practice

import (
	"errors"
	"testing"

	"github.com/nazacd/makonbook-api/internal/platform/testdb"
)

func TestApproveByGroup(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	const drawn = "mb/0123456789abcdef0123456789abcdef01234567.png"
	day := review(t, "DAY9", func(subject string, module, n int, rec map[string]any) {
		if subject == "math" && module == 2 && n == 1 {
			rec["risk"] = "yellow"
			rec["stem"] = "The graph:\n\n![a blurry line](r2:sat/old.png)\n\nWhat is the slope?"
			rec["figures"] = []map[string]string{{"key": "sat/old.png", "kind": "graph", "alt": "a blurry line"}}
			rec["fix"] = map[string]any{
				"kinds":   []string{"redrawn"},
				"changes": map[string]string{"stem": "The graph:\n\n![a line through (0, 2) and (1, 5)](r2:" + drawn + ")\n\nWhat is the slope?"},
				"figures": []map[string]string{{"key": drawn, "kind": "graph", "alt": "a line through (0, 2) and (1, 5)"}},
			}
		}
	})
	// The drawn figure is already uploaded (TestFixStep covers uploading).
	if _, err := pool.Exec(ctx, `INSERT INTO practice_images (key, content_type, bytes, width, height, uploaded_by)
		VALUES ($1, 'image/png', 10, 40, 30, 'AI review')`, drawn); err != nil {
		t.Fatal(err)
	}
	if _, err := (Importer{Pool: pool}).Import(ctx, load(t, day), false); err != nil {
		t.Fatal(err)
	}
	svc := NewService(pool, nil)
	id, err := svc.QuestionID(ctx, "DAY9", "math-m2-01")
	if err != nil {
		t.Fatal(err)
	}
	info, err := svc.GroupReady(ctx, id, "DAY9")
	if err != nil || !info.Changes || info.Risk != "yellow" || info.Place != "DAY9 · Math · Module 2 · Question 1" {
		t.Fatalf("ready: %+v %v", info, err)
	}
	if err := svc.ApproveByGroup(ctx, id, 2, "DAY9", "Teachers' group"); !errors.Is(err, ErrConflict) {
		t.Errorf("a version that isn't the question's: %v", err)
	}
	if err := svc.ApproveByGroup(ctx, id, 1, "DAY9", "Teachers' group (5 for, 0 against)"); err != nil {
		t.Fatal(err)
	}
	q, err := svc.Question(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	// The old figure is gone, the drawn one placed, as a version of the group's.
	if q.Status != "approved" || *q.ApprovedBy != "Teachers' group (5 for, 0 against)" || len(q.Figures) != 1 || q.Figures[0].Key != drawn ||
		q.Versions[0].Source != "group" || *q.Versions[0].CreatedBy != GroupReviewer {
		t.Errorf("approved by the group: %+v %+v", q.Figures, q.Versions[0])
	}
	if err := svc.ApproveByGroup(ctx, id, 2, "DAY9", "Teachers' group"); !errors.Is(err, ErrConflict) {
		t.Errorf("approved twice: %v", err)
	}
}
