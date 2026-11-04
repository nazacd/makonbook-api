package practice

import (
	"errors"
	"strings"
	"testing"
)

func TestReports(t *testing.T) {
	svc, pkg := published(t)
	ctx := t.Context()
	key := "answer_key_wrong"
	in := ReportInput{ID: "3f2b8a6e-1c4d-4e5f-9a0b-1c2d3e4f5a6b", QuestionRef: ref(pkg, "math", 1, 4), Kind: "question", Reason: &key,
		Text: "  B is right, not A. "}

	id, created, err := svc.SendReport(ctx, 11, in)
	if err != nil || !created || id != in.ID {
		t.Fatalf("send: %q %v %v", id, created, err)
	}
	if id, created, err := svc.SendReport(ctx, 11, in); err != nil || created || id != in.ID {
		t.Errorf("sent again: %q %v %v", id, created, err)
	}
	if _, _, err := svc.SendReport(ctx, 12, in); !errors.Is(err, ErrReportTaken) {
		t.Errorf("another student's id: %v", err)
	}

	bad, invalid := "boring", (*ReportError)(nil)
	for name, mod := range map[string]func(*ReportInput){
		"id":          func(in *ReportInput) { in.ID = "x" },
		"kind":        func(in *ReportInput) { in.Kind = "bug" },
		"reason":      func(in *ReportInput) { in.Reason = &bad },
		"too long":    func(in *ReportInput) { in.Text = strings.Repeat("a", MaxReportText+1) },
		"says naught": func(in *ReportInput) { in.Reason, in.Text = nil, "  " },
		"place":       func(in *ReportInput) { in.Position = 30 },
		"question":    func(in *ReportInput) { in.QuestionID = ref(pkg, "math", 1, 5).QuestionID },
	} {
		c := in
		c.ID = "7d1e2f3a-4b5c-4d6e-8f70-8192a3b4c5d6"
		mod(&c)
		if _, _, err := svc.SendReport(ctx, 12, c); !errors.As(err, &invalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A technical problem, in the student's words only.
	tech := ReportInput{ID: "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d", QuestionRef: ref(pkg, "english", 2, 1), Kind: "technical",
		Text: "The calculator didn't open."}
	if _, created, err := svc.SendReport(ctx, 12, tech); err != nil || !created {
		t.Fatalf("technical: %v %v", created, err)
	}

	open, err := svc.Reports(ctx, "open")
	if err != nil || len(open) != 2 || open[0].ID != tech.ID || open[1].Text != "B is right, not A." || open[1].TestTitle == "" ||
		open[1].Student.LastName != "Valiyev" || *open[1].Reason != key || open[1].ResolvedAt != nil || open[0].Reason != nil {
		t.Fatalf("open, newest first: %+v %v", open, err)
	}
	r, err := svc.SetReportStatus(ctx, in.ID, "resolved", "Boss B")
	if err != nil || r.Status != "resolved" || r.ResolvedAt == nil || *r.ResolvedBy != "Boss B" {
		t.Fatalf("resolve: %+v %v", r, err)
	}
	if again, err := svc.SetReportStatus(ctx, in.ID, "resolved", "Someone Else"); err != nil || *again.ResolvedBy != "Boss B" || !again.ResolvedAt.Equal(*r.ResolvedAt) {
		t.Errorf("resolved twice keeps the first: %+v %v", again, err)
	}
	if done, _ := svc.Reports(ctx, "resolved"); len(done) != 1 || done[0].ID != in.ID {
		t.Errorf("resolved: %+v", done)
	}
	if r, err := svc.SetReportStatus(ctx, in.ID, "open", "Boss B"); err != nil || r.Status != "open" || r.ResolvedAt != nil || r.ResolvedBy != nil {
		t.Errorf("reopen: %+v %v", r, err)
	}
	if _, err := svc.SetReportStatus(ctx, "9a8b7c6d-5e4f-4a3b-9c2d-1e0f9a8b7c6d", "resolved", "Boss B"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown report: %v", err)
	}
	if _, err := svc.SetReportStatus(ctx, in.ID, "closed", "Boss B"); !errors.As(err, &invalid) {
		t.Errorf("unknown status: %v", err)
	}
}
