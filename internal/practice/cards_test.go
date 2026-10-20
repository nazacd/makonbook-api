package practice

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/nazacd/makonbook-api/internal/platform/testdb"
)

// fakePictures records what it was asked to draw.
type fakePictures struct{ card Card }

func (f *fakePictures) Picture(_ context.Context, html, fn string, data any) ([]byte, error) {
	if html != "<page>" || fn != "renderCard" {
		return nil, errors.New("wrong page or function")
	}
	f.card = data.(Card)
	return []byte("PNG"), nil
}

func TestPictures(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	var shot bytes.Buffer
	if err := png.Encode(&shot, image.NewGray(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	bank := memStore{"media/sat/q.png": shot.Bytes()} // the bank's keys sit under media/

	day := review(t, "DAY7", func(subject string, module, n int, rec map[string]any) {
		switch {
		case subject == "math" && module == 1 && n == 3:
			rec["risk"] = "yellow"
			rec["stem"] = "Look:\n\n![a line](r2:sat/q.png)\n\nWhich?"
			rec["figures"] = []map[string]string{{"key": "sat/q.png", "kind": "graph", "alt": "a line"}}
			rec["source"].(map[string]any)["images"] = map[string]string{"question": "sat/q.png"}
			rec["original"] = map[string]any{"passage": nil, "question": "Which $x$?", "choices": map[string]string{"A": "a", "B": "b", "C": "b", "D": "d"}, "answer": "B"}
			rec["fix"] = map[string]any{"kinds": []string{"invented"}, "changes": map[string]string{"choices.C": "c2"}, "note": "C was the same as B."}
		case subject == "math" && module == 1 && n == 4:
			rec["risk"] = "yellow"
		}
	})
	if _, err := (Importer{Pool: pool}).Import(ctx, load(t, day), false); err != nil {
		t.Fatal(err)
	}
	pics := &fakePictures{}
	svc := NewService(pool, nil).WithBank(bank)
	if _, _, err := svc.Picture(ctx, "x", "fix", "", nil); !errors.Is(err, ErrPicturesOff) {
		t.Errorf("no renderer: %v", err)
	}
	svc.WithPictures(pics, "<page>")
	id, err := svc.QuestionID(ctx, "DAY7", "math-m1-03")
	if err != nil {
		t.Fatal(err)
	}

	// The bank's own question, its screenshot inline.
	_, name, err := svc.Picture(ctx, id, "original", "DAY7", nil)
	if err != nil || name != "DAY7-Math-M1-Q03-original.png" {
		t.Fatalf("original: %q %v", name, err)
	}
	c := pics.card
	if c.Kind != "original" || c.Place != "DAY7 · Math · Module 1 · Question 3" || c.Bank == nil || c.Bank.Question != "Which $x$?" ||
		c.Bank.Answer != "B" || !strings.HasPrefix(c.Bank.Images["question"], "data:image/png;base64,") {
		t.Errorf("original card: %+v", c)
	}

	// The AI's fix over the question as it is now, with its figure.
	if _, _, err = svc.Picture(ctx, id, "fix", "DAY7", nil); err != nil {
		t.Fatal(err)
	}
	c = pics.card
	if c.Content.Choices["C"] != "c2" || c.Base.Choices["C"] != "c" || *c.Content.Correct != "B" ||
		!strings.HasPrefix(c.Figures["sat/q.png"], "data:image/png;base64,") {
		t.Errorf("fix card: %+v %+v", c.Content, c.Base)
	}

	// Export: what the editor has, unsaved.
	edit := Edit{Stem: "Edited?", Choices: map[string]string{"A": "1", "B": "2", "C": "3", "D": "4"}, Correct: "D"}
	if _, name, err = svc.Picture(ctx, id, "question", "", &edit); err != nil || name != "DAY7-Math-M1-Q03.png" {
		t.Fatalf("export: %q %v", name, err)
	}
	if c = pics.card; c.Kind != "question" || c.Content.Stem != "Edited?" || *c.Content.Correct != "D" || c.Base != nil {
		t.Errorf("export card: %+v", c.Content)
	}

	// A question with no fix or bank text can't be drawn that way.
	other, _ := svc.QuestionID(ctx, "DAY7", "math-m1-04")
	if _, _, err := svc.Picture(ctx, other, "fix", "", nil); !errors.Is(err, ErrNoFix) {
		t.Errorf("no fix: %v", err)
	}
	if _, _, err := svc.Picture(ctx, other, "original", "", nil); !errors.Is(err, ErrNoOriginal) {
		t.Errorf("no bank text: %v", err)
	}
	if _, err := svc.QuestionID(ctx, "DAY7", "math-m9-99"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown qid: %v", err)
	}
}
