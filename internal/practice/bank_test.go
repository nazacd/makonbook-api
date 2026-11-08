package practice

import (
	"bytes"
	"image"
	"image/png"
	"testing"

	"github.com/nazacd/makonbook-api/internal/platform/testdb"
)

// Roadmap assessments link a figure to its published file, copying it once.
func TestFigureLinks(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	var pic bytes.Buffer
	if err := png.Encode(&pic, image.NewGray(image.Rect(0, 0, 20, 10))); err != nil {
		t.Fatal(err)
	}
	bank := memStore{"media/sat/f.png": pic.Bytes()}
	uploads := signingStore{memStore{}}
	svc := NewService(pool, uploads).WithBank(bank)

	links, err := svc.FigureLinks(ctx, []string{"sat/f.png", "sat/missing.png"})
	if err != nil || len(links) != 1 {
		t.Fatalf("links: %v %v", links, err)
	}
	l := links["sat/f.png"]
	if l.Width != 20 || l.Height != 10 || len(uploads.memStore) != 1 {
		t.Errorf("link: %+v, %d copied", l, len(uploads.memStore))
	}
	delete(bank, "media/sat/f.png") // known now: the bank isn't asked again
	if again, err := svc.FigureLinks(ctx, []string{"sat/f.png"}); err != nil || again["sat/f.png"] != l {
		t.Errorf("again: %v %v", again, err)
	}
	if none, err := NewService(pool, nil).FigureLinks(ctx, []string{"sat/f.png"}); err != nil || len(none) != 0 {
		t.Errorf("without a bucket: %v %v", none, err)
	}
}
