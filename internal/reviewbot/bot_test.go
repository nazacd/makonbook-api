package reviewbot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nazacd/makonbook-api/internal/platform/telegram"
	"github.com/nazacd/makonbook-api/internal/platform/testdb"
	"github.com/nazacd/makonbook-api/internal/practice"
)

// fakeTelegram records what the bot sends.
type fakeTelegram struct {
	next     int64
	photos   [][]telegram.Photo
	messages []string // chat: text
	edits    map[int64]string
	kbs      map[int64]telegram.Keyboard
	answers  []string         // text or link
	redrawn  map[int64]string // photo message: picture, caption
}

func (f *fakeTelegram) id() int64 { f.next++; return f.next }

func (f *fakeTelegram) Me(context.Context) (telegram.User, error) {
	return telegram.User{Username: "makonbook_review_bot"}, nil
}

func (f *fakeTelegram) Updates(context.Context, int64, time.Duration) ([]telegram.Update, error) {
	return nil, nil
}

func (f *fakeTelegram) SendPhotos(_ context.Context, _ int64, p []telegram.Photo) ([]telegram.Message, error) {
	f.photos = append(f.photos, p)
	out := make([]telegram.Message, len(p))
	for i := range p {
		out[i].MessageID = f.id()
	}
	return out, nil
}

func (f *fakeTelegram) EditPhoto(_ context.Context, _, messageID int64, p telegram.Photo) error {
	f.redrawn[messageID] = string(p.PNG) + ", " + p.Caption
	return nil
}

func (f *fakeTelegram) SendMessage(_ context.Context, chatID int64, text string, _ int64, _ telegram.Keyboard) (telegram.Message, error) {
	f.messages = append(f.messages, fmt.Sprintf("%d: %s", chatID, text))
	return telegram.Message{MessageID: f.id()}, nil
}

func (f *fakeTelegram) EditMessage(_ context.Context, _, messageID int64, text string, kb telegram.Keyboard) error {
	f.edits[messageID], f.kbs[messageID] = text, kb
	return nil
}

func (f *fakeTelegram) AnswerCallback(_ context.Context, _, text, link string) error {
	f.answers = append(f.answers, text+link)
	return nil
}

// drawing is the real practice service, without a browser.
type drawing struct {
	*practice.Service
	version string
}

func (d *drawing) PictureVersion() string { return d.version }

func (d *drawing) Picture(_ context.Context, _, kind, _ string, _ *practice.Edit) ([]byte, string, error) {
	return []byte(kind), kind + ".png", nil
}

const group = -100123

// testDay writes a test of 98 questions in which some math ones are yellow
// or red with a fix, and returns it loaded.
func testDay(t *testing.T, code string, edit func(qid string, rec map[string]any)) practice.Test {
	t.Helper()
	var lines []string
	for _, subject := range []string{"english", "math"} {
		size, domain, skill := 27, "Craft and Structure", "Words in Context"
		if subject == "math" {
			size, domain, skill = 22, "Algebra", "Linear functions"
		}
		for module := 1; module <= 2; module++ {
			for n := 1; n <= size; n++ {
				qid := fmt.Sprintf("%s-m%d-%02d", subject, module, n)
				rec := map[string]any{
					"qid":    qid,
					"source": map[string]any{"subject": subject, "csv_id": n, "test_id": code, "module": fmt.Sprintf("module_%d", module), "number": n},
					"type":   "mcq", "passage": nil, "stem": fmt.Sprintf("Question %s of %s?", qid, code),
					"choices": map[string]string{"A": "a", "B": "b", "C": "c", "D": "d"},
					"figures": []any{}, "key": "B", "accepted": []string{}, "verdict": "key_correct", "risk": "green",
					"explanation": map[string]any{"text": "Because.\n\n**Answer:** B"},
					"tags":        map[string]any{"domain": domain, "skill": skill, "difficulty": "easy"},
				}
				if edit != nil {
					edit(qid, rec)
				}
				b, _ := json.Marshal(rec)
				lines = append(lines, string(b))
			}
		}
	}
	path := filepath.Join(t.TempDir(), code, "questions.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	test, err := practice.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	return test
}

func TestGroupReview(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	day := testDay(t, "DAY2", func(qid string, rec map[string]any) {
		switch qid {
		case "math-m1-01": // yellow, a typo fixed
			rec["risk"] = "yellow"
			rec["fix"] = map[string]any{"kinds": []string{"corrected"}, "changes": map[string]string{"choices.C": "c fixed"}, "note": "C had a typo."}
		case "math-m1-02": // red, words restored
			rec["risk"], rec["verdict"] = "red", "broken"
			rec["fix"] = map[string]any{"kinds": []string{"restored"}, "changes": map[string]string{"stem": "Restored question?"}, "note": "Words were missing."}
		case "math-m1-03": // yellow, nothing to change
			rec["risk"] = "yellow"
			rec["fix"] = map[string]any{"kinds": []string{}, "changes": map[string]string{}, "note": "A solver was unsure; it is fine."}
		case "math-m1-04": // a person must replace a photo: not for the group
			rec["risk"] = "yellow"
			rec["fix"] = map[string]any{"kinds": []string{}, "changes": map[string]string{}, "needs_person": "Replace the photo.", "note": "x"}
		case "math-m1-05": // yellow without a fix yet: not for the group
			rec["risk"] = "yellow"
		}
	})
	if _, err := (practice.Importer{Pool: pool}).Import(ctx, day, false); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE review_bot SET max_open = 3`); err != nil {
		t.Fatal(err)
	}
	tg := &fakeTelegram{edits: map[int64]string{}, kbs: map[int64]telegram.Keyboard{}, redrawn: map[int64]string{}}
	svc := practice.NewService(pool, nil)
	tashkent, _ := time.LoadLocation("Asia/Tashkent")
	pictures := &drawing{Service: svc, version: "v1"}
	b := New(pool, tg, pictures, group, tashkent, slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.username, b.pause = "makonbook_review_bot", 0
	at := func(h, m int) time.Time { return time.Date(2026, 10, 21, h, m, 0, 0, tashkent) }
	count := func(sql string, args ...any) int {
		var n int
		if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Night: nothing. 09:00: the first question in test order, both pictures.
	if err := b.Tick(ctx, at(2, 0)); err != nil || len(tg.photos) != 0 {
		t.Fatalf("night: %v %d", err, len(tg.photos))
	}
	if err := b.Tick(ctx, at(9, 0)); err != nil {
		t.Fatal(err)
	}
	if len(tg.photos) != 1 || len(tg.photos[0]) != 2 || string(tg.photos[0][0].PNG) != "original" || string(tg.photos[0][1].PNG) != "fix" {
		t.Fatalf("first post: %+v", tg.photos)
	}
	if !strings.Contains(tg.messages[0], "Module 1 · Question 1") || !strings.Contains(tg.messages[0], "AI fix: Corrected") ||
		!strings.Contains(tg.messages[0], "Approved at 5 ✅ or denied at 5 ❌") {
		t.Errorf("vote message: %s", tg.messages[0])
	}
	// The same hour again: nothing more.
	if err := b.Tick(ctx, at(9, 40)); err != nil || len(tg.photos) != 1 {
		t.Fatalf("same hour: %v %d", err, len(tg.photos))
	}
	// One an hour, never a burst: 10:00 and 13:00 add one each (13:00 is
	// no longer a top-up). The one needing a person and the one without a
	// fix are never posted.
	for _, h := range []int{10, 13} {
		if err := b.Tick(ctx, at(h, 2)); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(`SELECT count(*) FROM group_posts WHERE status = 'open'`); n != 3 || len(tg.photos) != 3 || len(tg.photos[2]) != 1 {
		t.Fatalf("hourly: %d open, %d posts", n, len(tg.photos))
	}
	if n := count(`SELECT count(*) FROM group_posts WHERE qid IN ('math-m1-04', 'math-m1-05')`); n != 0 {
		t.Error("posted a question a person must handle, or one without a fix")
	}

	// The same picture page: nothing to redraw. A new one (an update): every
	// open post's photos are replaced in place, once.
	if err := b.Redraw(ctx); err != nil || len(tg.redrawn) != 0 {
		t.Fatalf("same page: %v %v", err, tg.redrawn)
	}
	pictures.version = "v2"
	if err := b.Redraw(ctx); err != nil {
		t.Fatal(err)
	}
	sent := len(tg.photos[0]) + len(tg.photos[1]) + len(tg.photos[2])
	if len(tg.redrawn) != sent || tg.redrawn[1] != "original, Original (the bank's)" || tg.redrawn[2] != "fix, AI fix (changes highlighted)" {
		t.Fatalf("redrawn: %v (sent %d)", tg.redrawn, sent)
	}
	clear(tg.redrawn)
	if err := b.Redraw(ctx); err != nil || len(tg.redrawn) != 0 {
		t.Fatalf("redrew twice: %v %v", err, tg.redrawn)
	}

	post := func(qid string) (id, vote int64) {
		if err := pool.QueryRow(ctx, `SELECT id, vote_id FROM group_posts WHERE qid = $1`, qid).Scan(&id, &vote); err != nil {
			t.Fatal(err)
		}
		return id, vote
	}
	press := func(user int64, data string, chat int64) {
		t.Helper()
		err := b.Handle(ctx, telegram.Update{CallbackQuery: &telegram.CallbackQuery{
			ID: "q", From: telegram.User{ID: user, FirstName: fmt.Sprint("Teacher ", user)}, Data: data,
			Message: &telegram.Message{Chat: telegram.Chat{ID: chat}},
		}})
		if err != nil {
			t.Fatal(err)
		}
	}

	// Yellow: 5 approvals apply the fix and approve. A changed vote counts
	// once; a press from another chat doesn't count.
	yellow, yellowVote := post("math-m1-01")
	press(1, fmt.Sprintf("d:%d", yellow), group)
	press(1, fmt.Sprintf("a:%d", yellow), group)
	press(9, fmt.Sprintf("a:%d", yellow), -999)
	for u := int64(2); u <= 4; u++ {
		press(u, fmt.Sprintf("a:%d", yellow), group)
	}
	if !strings.Contains(tg.edits[yellowVote], "Votes so far: 4.") || tg.kbs[yellowVote] == nil {
		t.Errorf("open vote: %q", tg.edits[yellowVote])
	}
	press(5, fmt.Sprintf("a:%d", yellow), group)
	if !strings.Contains(tg.edits[yellowVote], "Approved by the group</b>: 5 for, 0 against") || tg.kbs[yellowVote] != nil {
		t.Errorf("decided: %q %v", tg.edits[yellowVote], tg.kbs[yellowVote])
	}
	if n := count(`SELECT count(*) FROM questions q JOIN question_versions v ON v.question_id = q.id AND v.version = 2
		WHERE q.choices->>'C' = 'c fixed' AND q.status = 'approved' AND q.approved_by LIKE 'Teachers'' group (5 for%' AND v.source = 'group'`); n != 1 {
		t.Error("the fix wasn't applied and approved")
	}
	press(6, fmt.Sprintf("a:%d", yellow), group)
	if last := tg.answers[len(tg.answers)-1]; last != "This vote is closed." {
		t.Errorf("late vote: %q", last)
	}

	// Red: 5 denials send it to the editors; each who denied is asked why in
	// a private chat, through a link.
	red, redVote := post("math-m1-02")
	for u := int64(11); u <= 15; u++ {
		press(u, fmt.Sprintf("d:%d", red), group)
	}
	if want := fmt.Sprintf("https://t.me/makonbook_review_bot?start=deny_%d", red); tg.answers[len(tg.answers)-1] != want {
		t.Errorf("deny link: %q", tg.answers[len(tg.answers)-1])
	}
	if !strings.Contains(tg.edits[redVote], "Denied by the group</b>: 0 for, 5 against") {
		t.Errorf("denied: %q", tg.edits[redVote])
	}
	if n := count(`SELECT count(*) FROM questions q JOIN group_posts p ON p.question_id = q.id WHERE p.id = $1 AND q.status = 'review' AND q.version = 1`, red); n != 1 {
		t.Error("a denied question changed")
	}
	say := func(user int64, text string) string {
		t.Helper()
		err := b.Handle(ctx, telegram.Update{Message: &telegram.Message{
			From: &telegram.User{ID: user, FirstName: "Teacher"}, Chat: telegram.Chat{ID: user, Type: "private"}, Text: text,
		}})
		if err != nil {
			t.Fatal(err)
		}
		return tg.messages[len(tg.messages)-1]
	}
	if r := say(11, fmt.Sprintf("/start deny_%d", red)); !strings.Contains(r, "Why?") || !strings.Contains(r, "DAY2 · Math · Module 1 · Question 2") {
		t.Errorf("asked why: %q", r)
	}
	if r := say(11, "The restored words are wrong."); !strings.Contains(r, "editors will see it") {
		t.Errorf("reason: %q", r)
	}
	if r := say(1, fmt.Sprintf("/start deny_%d", red)); !strings.Contains(r, "haven't denied") {
		t.Errorf("someone who didn't deny: %q", r)
	}
	if n := count(`SELECT count(*) FROM group_deny_reasons WHERE post_id = $1 AND reason = 'The restored words are wrong.'`, red); n != 1 {
		t.Error("the reason wasn't kept")
	}

	// An editor approves the third in the panel: the next tick closes its vote.
	third, thirdVote := post("math-m1-03")
	if _, err := pool.Exec(ctx, `UPDATE questions SET status = 'approved', approved_at = now(), approved_by = 'Editor'
		WHERE id = (SELECT question_id FROM group_posts WHERE id = $1)`, third); err != nil {
		t.Fatal(err)
	}
	if err := b.Tick(ctx, at(14, 1)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tg.edits[thirdVote], "Closed: the question was changed or decided in the admin panel") {
		t.Errorf("overtaken: %q", tg.edits[thirdVote])
	}
	if n := count(`SELECT count(*) FROM group_posts WHERE status = 'open'`); n != 0 {
		t.Errorf("open after all that: %d", n)
	}
}

// With the open limit reached the bot waits; a post that gets too few votes
// within vote_hours closes for the editors, and the next hour's question goes
// out in its place.
func TestStalePostsClose(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	day := testDay(t, "DAY3", func(qid string, rec map[string]any) {
		if strings.HasPrefix(qid, "math-m1-0") {
			rec["risk"] = "yellow"
			rec["fix"] = map[string]any{"kinds": []string{"corrected"}, "changes": map[string]string{"choices.C": "c fixed"}, "note": "C had a typo."}
		}
	})
	if _, err := (practice.Importer{Pool: pool}).Import(ctx, day, false); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE review_bot SET max_open = 2`); err != nil {
		t.Fatal(err)
	}
	tg := &fakeTelegram{edits: map[int64]string{}, kbs: map[int64]telegram.Keyboard{}, redrawn: map[int64]string{}}
	tashkent, _ := time.LoadLocation("Asia/Tashkent")
	b := New(pool, tg, &drawing{Service: practice.NewService(pool, nil), version: "v1"}, group, tashkent, slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.username, b.pause = "makonbook_review_bot", 0
	at := func(h int) time.Time { return time.Date(2026, 10, 21, h, 1, 0, 0, tashkent) }
	open := func() (n int) {
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM group_posts WHERE status = 'open'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	for _, h := range []int{9, 10, 11} {
		if err := b.Tick(ctx, at(h)); err != nil {
			t.Fatal(err)
		}
	}
	if open() != 2 || len(tg.photos) != 2 {
		t.Fatalf("at the limit: %d open, %d posted", open(), len(tg.photos))
	}
	// The first post has been up for 49 hours with one vote.
	var first, firstVote int64
	if err := pool.QueryRow(ctx, `UPDATE group_posts SET posted_at = $1, approvals = 1
		WHERE id = (SELECT min(id) FROM group_posts) RETURNING id, vote_id`, at(12).Add(-49*time.Hour)).Scan(&first, &firstVote); err != nil {
		t.Fatal(err)
	}
	if err := b.Tick(ctx, at(12)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tg.edits[firstVote], "not enough votes in 48 hours (1 for, 0 against)") || tg.kbs[firstVote] != nil {
		t.Errorf("expired: %q", tg.edits[firstVote])
	}
	if open() != 2 || len(tg.photos) != 3 {
		t.Errorf("the next one didn't take its place: %d open, %d posted", open(), len(tg.photos))
	}
	err := b.Handle(ctx, telegram.Update{CallbackQuery: &telegram.CallbackQuery{
		ID: "q", From: telegram.User{ID: 7, FirstName: "Teacher"}, Data: fmt.Sprintf("a:%d", first),
		Message: &telegram.Message{Chat: telegram.Chat{ID: group}},
	}})
	if err != nil || tg.answers[len(tg.answers)-1] != "This vote is closed." {
		t.Errorf("a vote after it closed: %v %q", err, tg.answers[len(tg.answers)-1])
	}
}
