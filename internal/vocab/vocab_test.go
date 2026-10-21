package vocab

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nazacd/makonbook-api/internal/platform/testdb"
)

// memStore is a bucket in memory that also makes links.
type memStore map[string][]byte

func (m memStore) Put(_ context.Context, key string, body []byte, _ string) error {
	m[key] = body
	return nil
}

func (m memStore) PresignGet(key string, _ time.Duration) (string, error) {
	return "https://r2.test/" + key, nil
}

func writeFolder(t *testing.T, words string, images ...string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "words.jsonl"), []byte(words), 0o600); err != nil {
		t.Fatal(err)
	}
	if len(images) > 0 {
		if err := os.Mkdir(filepath.Join(dir, "images"), 0o750); err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 30, 20))); err != nil {
			t.Fatal(err)
		}
		for _, name := range images {
			if err := os.WriteFile(filepath.Join(dir, "images", name), buf.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return dir
}

const twoLists = `{"list":"sat-1","list_title":"SAT Words 1","word":"abate","part_of_speech":"verb","definition":"to become less intense","example":"The storm abated by morning.","image":"abate.png"}
{"list":"sat-1","list_title":"SAT Words 1","word":"benevolent","definition":"kind and generous","example":"A benevolent donor paid for the library."}
{"list":"sat-2","list_title":"SAT Words 2","word":"candid","part_of_speech":"adjective","definition":"honest and direct","example":"She gave a candid answer."}
`

func TestImportAndDeck(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	bucket := memStore{}
	svc := NewService(pool, bucket)

	if d, _, err := svc.Deck(ctx, ""); err != nil || len(d.Lists) != 0 {
		t.Fatalf("empty bank: %+v %v", d, err)
	}
	entries, dir, err := Read(writeFolder(t, twoLists, "abate.png"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Import(ctx, pool, nil, entries, dir); err == nil || !strings.Contains(err.Error(), "R2_UPLOADS") {
		t.Errorf("images without a bucket: %v", err)
	}
	s, err := Import(ctx, pool, bucket, entries, dir)
	if err != nil || s.Lists != 2 || s.Words != 3 || s.ImagesStored != 1 || s.Version == "" {
		t.Fatalf("import: %+v %v", s, err)
	}
	d, unchanged, err := svc.Deck(ctx, "")
	if err != nil || unchanged || d.Version != s.Version || len(d.Lists) != 2 || len(d.Lists[0].Words) != 2 ||
		d.Lists[0].Words[0].Image == nil || d.Lists[0].Words[0].Image.Width != 30 || d.Lists[0].Words[1].PartOfSpeech != nil {
		t.Fatalf("deck: %+v %v", d, err)
	}
	if _, ok := bucket[ImagePrefix+d.Lists[0].Words[0].Image.File]; !ok {
		t.Error("image not in the bucket")
	}
	if d2, unchanged, _ := svc.Deck(ctx, s.Version); !unchanged || d2.Lists != nil {
		t.Errorf("a phone that has it: %+v", d2)
	}
	links, _, err := svc.Images(ctx, "sat-1")
	if err != nil || len(links) != 1 {
		t.Errorf("image links: %v %v", links, err)
	}
	abate := d.Lists[0].Words[0].ID

	// The same file again: nothing changes, ids stay.
	again, err := Import(ctx, pool, bucket, entries, dir)
	if err != nil || again.Version != s.Version || again.ImagesStored != 0 {
		t.Errorf("reimport: %+v %v", again, err)
	}
	// A word dropped, one edited, list 2 gone: removed, not deleted.
	edited := strings.Replace(strings.Split(twoLists, "\n")[0], "to become less intense", "to lessen", 1) + "\n"
	entries, dir, _ = Read(writeFolder(t, edited, "abate.png"))
	s2, err := Import(ctx, pool, bucket, entries, dir)
	if err != nil || s2.RemovedWords != 1 || s2.RemovedLists != 1 || s2.Version == s.Version {
		t.Fatalf("second import: %+v %v", s2, err)
	}
	d, _, _ = svc.Deck(ctx, s.Version)
	if len(d.Lists) != 1 || len(d.Lists[0].Words) != 1 || d.Lists[0].Words[0].ID != abate || d.Lists[0].Words[0].Definition != "to lessen" {
		t.Errorf("deck after: %+v", d)
	}
}

func TestReadAndCheck(t *testing.T) {
	dir := t.TempDir()
	csvFile := filepath.Join(dir, "words.csv")
	csv := "\uFEFFlist,list_title,word,definition,example\nsat-1,SAT 1,abate,to lessen,\"The storm abated, slowly.\"\n\n"
	if err := os.WriteFile(csvFile, []byte(csv), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, _, err := Read(dir)
	if err != nil || len(entries) != 1 || entries[0].Example != "The storm abated, slowly." {
		t.Errorf("csv: %+v %v", entries, err)
	}
	pool := testdb.New(t)
	bad := `{"list":"SAT 1","list_title":"x","word":"a","definition":"d","example":"e"}
{"list":"sat-1","list_title":"A","word":"abate","definition":"d","example":"e"}
{"list":"sat-1","list_title":"B","word":"Abate","definition":"d","example":""}
{"list":"sat-1","list_title":"A","word":"x","definition":"d","example":"e","image":"../etc/passwd"}
`
	entries, dir2, err := Read(writeFolder(t, bad))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Import(t.Context(), pool, nil, entries, dir2)
	for _, want := range []string{"lower-case", "example is empty", "plain file name"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q in: %v", want, err)
		}
	}
	if _, _, err := Read(writeFolder(t, `{"list":"a","surprise":1}`)); err == nil {
		t.Error("an unknown field is a typo")
	}
}

func TestReviewsAndProgress(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	entries, dir, _ := Read(writeFolder(t, twoLists, "abate.png"))
	if _, err := Import(ctx, pool, memStore{}, entries, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO offices (id, name) VALUES (1, 'Main');
		INSERT INTO groups (id, name, discipline, level, learning_type, office_id) VALUES (7, 'SAT 1', 'SAT', '', 'GROUP', 1);
		INSERT INTO teachers (id, first_name, last_name, phone_raw, fired) VALUES (3, 'T', 'Teacher', '', false), (4, 'O', 'Other', '', false);
		INSERT INTO schedule_items (id, group_id, begin_time, end_time, weekdays, classroom, teacher_ids) VALUES (1, 7, '10:00', '12:00', 1, '', '{3}');
		INSERT INTO students (client_id, hh_student_id, first_name, last_name, phone_raw, office_ids)
		VALUES (11, 1, 'Ali', 'Valiyev', '', '{1}'), (12, 2, 'Bek', 'Aliyev', '', '{1}');
		INSERT INTO enrollments (group_id, student_client_id, weekdays) VALUES (7, 11, 1), (7, 12, 1);`); err != nil {
		t.Fatal(err)
	}
	svc := NewService(pool, nil)
	d, _, _ := svc.Deck(ctx, "")
	abate, candid := d.Lists[0].Words[0].ID, d.Lists[1].Words[0].ID
	now := time.Now().UTC().Truncate(time.Second)
	review := func(id, word string, rating int16, at time.Time, days int32) Review {
		return Review{ID: id, WordID: word, Rating: rating, ReviewedAt: at, DurationMs: 2500, State: 2,
			Due: at.Add(time.Duration(days) * 24 * time.Hour), Stability: 3.2, Difficulty: 5.1, ScheduledDays: days}
	}
	batch := []Review{
		review("00000000-0000-4000-8000-000000000001", abate, 3, now.Add(-30*24*time.Hour), 4),
		review("00000000-0000-4000-8000-000000000002", abate, 4, now.Add(-time.Hour), 25),
		review("00000000-0000-4000-8000-000000000003", candid, 1, now, 0),
	}
	if n, err := svc.SaveReviews(ctx, 11, batch); err != nil || n != 3 {
		t.Fatalf("save: %d %v", n, err)
	}
	if n, err := svc.SaveReviews(ctx, 11, batch); err != nil || n != 0 {
		t.Errorf("sent again: %d %v", n, err)
	}
	var bad *ReviewsError
	for name, r := range map[string]Review{
		"rating":    review("00000000-0000-4000-8000-000000000009", abate, 5, now, 1),
		"word":      review("00000000-0000-4000-8000-000000000009", "00000000-0000-4000-8000-0000000000ff", 3, now, 1),
		"future":    review("00000000-0000-4000-8000-000000000009", abate, 3, now.Add(48*time.Hour), 1),
		"not uuids": review("x", abate, 3, now, 1),
	} {
		if _, err := svc.SaveReviews(ctx, 11, []Review{r}); !errors.As(err, &bad) {
			t.Errorf("%s: %v", name, err)
		}
	}
	got, err := svc.Reviews(ctx, 11, 0, 2)
	if err != nil || len(got) != 2 || got[0].Rating != 3 || got[1].ScheduledDays != 25 {
		t.Fatalf("page 1: %+v %v", got, err)
	}
	rest, _ := svc.Reviews(ctx, 11, got[1].Seq, 2)
	if len(rest) != 1 || rest[0].WordID != candid {
		t.Errorf("page 2: %+v", rest)
	}
	if other, _ := svc.Reviews(ctx, 12, 0, 10); len(other) != 0 {
		t.Errorf("another student's: %+v", other)
	}

	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if _, _, err := svc.GroupProgress(ctx, 4, 7, today); !errors.Is(err, ErrNotTeacher) {
		t.Errorf("another teacher: %v", err)
	}
	students, total, err := svc.GroupProgress(ctx, 3, 7, today)
	if err != nil || total != 3 || len(students) != 2 {
		t.Fatalf("progress: %+v %d %v", students, total, err)
	}
	ali := students[1] // by last name: Aliyev, Valiyev
	if ali.ClientID != 11 || ali.Started != 2 || ali.Learned != 1 || ali.ReviewsWeek != 2 || ali.LastReview == nil {
		t.Errorf("Ali: %+v", ali)
	}
	if students[0].Started != 0 || students[0].LastReview != nil {
		t.Errorf("Bek: %+v", students[0])
	}
}
