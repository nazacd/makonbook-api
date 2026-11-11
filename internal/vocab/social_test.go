package vocab

import (
	"errors"
	"testing"
	"time"

	"github.com/nazacd/makonbook-api/internal/platform/testdb"
)

func TestGamesLeaderboardAndDecks(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	entries, dir, err := Read(writeFolder(t, twoLists, "abate.png"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Import(ctx, pool, memStore{}, entries, dir); err != nil {
		t.Fatal(err)
	}
	// Ali and Bek share group 7; Dana is in no group (a free account).
	if _, err := pool.Exec(ctx, `
		INSERT INTO offices (id, name) VALUES (1, 'Main');
		INSERT INTO groups (id, name, discipline, level, learning_type, office_id) VALUES (7, 'SAT 1', 'SAT', '', 'GROUP', 1);
		INSERT INTO teachers (id, first_name, last_name, phone_raw, fired) VALUES (3, 'T', 'Teacher', '', false);
		INSERT INTO schedule_items (id, group_id, begin_time, end_time, weekdays, classroom, teacher_ids) VALUES (1, 7, '10:00', '12:00', 1, '', '{3}');
		INSERT INTO students (client_id, hh_student_id, first_name, last_name, phone_raw, office_ids)
		VALUES (11, 1, 'Ali', 'Valiyev', '', '{1}'), (12, 2, 'Bek', 'Aliyev', '', '{1}'), (13, 3, 'Dana', 'Karimova', '', '{1}');
		INSERT INTO enrollments (group_id, student_client_id, weekdays) VALUES (7, 11, 1), (7, 12, 1);`); err != nil {
		t.Fatal(err)
	}
	svc := NewService(pool, nil)
	loc, _ := time.LoadLocation("Asia/Tashkent")
	now := time.Now().Truncate(time.Second)
	d, _, _ := svc.Deck(ctx, "")
	abate, candid := d.Lists[0].Words[0].ID, d.Lists[1].Words[0].ID

	// Games: points are two a right answer, a day's games at most 100.
	game := func(id, kind string, score, total int32) GameResult {
		return GameResult{ID: id, Game: kind, Score: score, Total: total, DurationMs: 40_000, Missed: []string{abate}, PlayedAt: now}
	}
	var batch []GameResult
	for i := range 6 {
		batch = append(batch, game("00000000-0000-4000-8000-0000000000"+string(rune('a'+i))+"1", "gap", 10, 10))
	}
	if n, err := svc.SaveGames(ctx, 11, batch, loc); err != nil || n != 6 {
		t.Fatalf("save games: %d %v", n, err)
	}
	if n, _ := svc.SaveGames(ctx, 11, batch, loc); n != 0 {
		t.Errorf("sent again: %d", n)
	}
	var bad *GamesError
	for name, g := range map[string]GameResult{
		"game":   game("00000000-0000-4000-8000-000000000091", "chess", 1, 1),
		"score":  game("00000000-0000-4000-8000-000000000092", "pairs", 11, 10),
		"total":  game("00000000-0000-4000-8000-000000000093", "unscramble", 9, 9),
		"old":    {ID: "00000000-0000-4000-8000-000000000094", Game: "gap", Score: 1, Total: 1, PlayedAt: now.AddDate(0, 0, -40)},
		"missed": {ID: "00000000-0000-4000-8000-000000000095", Game: "gap", Score: 1, Total: 1, PlayedAt: now, Missed: []string{"x"}},
	} {
		if _, err := svc.SaveGames(ctx, 11, []GameResult{g}, loc); !errors.As(err, &bad) {
			t.Errorf("%s: %v", name, err)
		}
	}
	pairs := game("00000000-0000-4000-8000-0000000000b9", "pairs", 9, 10)
	pairs.Mistakes = 1
	if _, err := svc.SaveGames(ctx, 12, []GameResult{pairs}, loc); err != nil {
		t.Fatal(err)
	}
	stats, err := svc.GameStats(ctx, 12)
	if err != nil || stats["pairs"].Plays != 1 || *stats["pairs"].Best != 45_000 || stats["gap"].Best != nil {
		t.Errorf("stats: %+v %v", stats, err)
	}

	// A review is a point a word a day: Dana reviews abate twice today.
	r := func(id, word string) Review {
		return Review{ID: id, WordID: word, Rating: 3, ReviewedAt: now, DurationMs: 1000, State: 1, Due: now.Add(time.Hour), Stability: 1, Difficulty: 5}
	}
	if _, err := svc.SaveReviews(ctx, 13, []Review{r("00000000-0000-4000-8000-0000000000c1", abate), r("00000000-0000-4000-8000-0000000000c2", abate), r("00000000-0000-4000-8000-0000000000c3", candid)}); err != nil {
		t.Fatal(err)
	}
	all, err := svc.Leaderboard(ctx, 13, "all", now, loc)
	if err != nil || all.Learners != 3 || len(all.Entries) != 3 {
		t.Fatalf("all: %+v %v", all, err)
	}
	if e := all.Entries[0]; e.Name != "Ali V." || e.Points != 100 || e.Rank != 1 {
		t.Errorf("first: %+v", e)
	}
	if all.Me == nil || all.Me.Name != "Dana K." || all.Me.Points != 2 || all.Me.Rank != 3 || !all.Me.Me {
		t.Errorf("me: %+v", all.Me)
	}
	groups, err := svc.Leaderboard(ctx, 12, "groups", now, loc)
	if err != nil || groups.Learners != 2 || groups.Entries[1].Name != "Bek Aliyev" || groups.Entries[1].Points != 18 {
		t.Errorf("groups: %+v %v", groups, err)
	}

	// The teacher sees games and points.
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	students, _, _ := svc.GroupProgress(ctx, 3, 7, today)
	if err := svc.AddGameStats(ctx, students, now, loc); err != nil || students[1].GamesWeek != 6 || students[1].PointsMonth != 100 {
		t.Errorf("progress: %+v %v", students, err)
	}
	recent, err := svc.GroupGames(ctx, 3, 7, today, 10)
	if err != nil || len(recent) != 7 || len(recent[0].Missed) != 1 || recent[0].Missed[0].Word != "abate" {
		t.Errorf("recent: %+v %v", recent, err)
	}

	// Decks: Dana makes one; it is private, then shared.
	words := []DeckWordInput{{Word: "zeal", Definition: "great energy"}, {Word: "quaint", Definition: "attractively old"}}
	if _, err := svc.CreateDeck(ctx, 13, DeckInput{Title: " ", Words: words}); !errors.As(err, new(*DeckError)) {
		t.Errorf("no title: %v", err)
	}
	if _, err := svc.CreateDeck(ctx, 13, DeckInput{Title: "Twice", Words: append(words, DeckWordInput{Word: "Zeal", Definition: "again"})}); !errors.As(err, new(*DeckError)) {
		t.Errorf("twice: %v", err)
	}
	id, err := svc.CreateDeck(ctx, 13, DeckInput{Title: "  My   words ", Words: words})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Publish(ctx, 13, id, true); !errors.Is(err, ErrDeckTooSmall) {
		t.Errorf("small: %v", err)
	}
	if _, _, err := svc.DeckDetail(ctx, 11, false, id); !errors.Is(err, ErrDeckNotFound) {
		t.Errorf("private seen: %v", err)
	}
	mine, _ := svc.MyDecks(ctx, 13)
	zeal := mine[0].Words[0].ID
	if mine[0].Title != "My words" || mine[0].WordCount != 2 {
		t.Fatalf("mine: %+v", mine)
	}
	// Edit: zeal keeps its id (matched by id), quaint by spelling; three added.
	five := []DeckWordInput{{ID: zeal, Word: "zeal", Definition: "eager energy"}, {Word: "quaint", Definition: "old"}, {Word: "wane", Definition: "to shrink"}, {Word: "vex", Definition: "to annoy"}, {Word: "terse", Definition: "short"}}
	if err := svc.UpdateDeck(ctx, 11, id, DeckInput{Title: "x", Words: five}); !errors.Is(err, ErrNotOwner) {
		t.Errorf("not owner: %v", err)
	}
	if err := svc.UpdateDeck(ctx, 13, id, DeckInput{Title: "My words", Words: five}); err != nil {
		t.Fatal(err)
	}
	mine, _ = svc.MyDecks(ctx, 13)
	if mine[0].WordCount != 5 || mine[0].Words[0].ID != zeal || mine[0].Words[0].Definition != "eager energy" {
		t.Errorf("edited: %+v", mine[0].Words)
	}
	if err := svc.Publish(ctx, 13, id, true); err != nil {
		t.Fatal(err)
	}
	// Ali adds it and reviews its words; Dana can't add her own.
	if err := svc.AddDeck(ctx, 13, id, true); !errors.Is(err, ErrOwnDeck) {
		t.Errorf("own: %v", err)
	}
	if _, err := svc.SaveReviews(ctx, 11, []Review{r("00000000-0000-4000-8000-0000000000d1", zeal)}); !errors.As(err, new(*ReviewsError)) {
		t.Errorf("review before adding: %v", err)
	}
	if err := svc.AddDeck(ctx, 11, id, true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveReviews(ctx, 11, []Review{r("00000000-0000-4000-8000-0000000000d1", zeal)}); err != nil {
		t.Errorf("review after adding: %v", err)
	}
	community, _ := svc.Community(ctx, 11, false, false, "words")
	if len(community) != 1 || !community[0].Added || community[0].Adds != 1 || community[0].WordCount != 5 || community[0].Owner != "Dana K." {
		t.Errorf("community: %+v", community)
	}
	// Three reports take it down; a teacher puts it back.
	for _, s := range []int32{11, 12, 11} {
		if err := svc.ReportDeck(ctx, s, id, "rude words"); err != nil {
			t.Fatal(err)
		}
	}
	if c, _ := svc.Community(ctx, 12, false, false, ""); len(c) != 1 {
		t.Errorf("two reports: %+v", c)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO students (client_id, hh_student_id, first_name, last_name, phone_raw, office_ids) VALUES (14, 4, 'E', 'F', '', '{1}')`); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReportDeck(ctx, 14, id, "spam"); err != nil {
		t.Fatal(err)
	}
	if c, _ := svc.Community(ctx, 12, false, false, ""); len(c) != 0 {
		t.Errorf("hidden still listed: %+v", c)
	}
	if my, _ := svc.MyDecks(ctx, 11); len(my) != 0 {
		t.Errorf("hidden still added: %+v", my)
	}
	queue, _ := svc.Community(ctx, 0, true, true, "")
	if len(queue) != 1 || !queue[0].Hidden || *queue[0].Reports != 3 {
		t.Errorf("moderation: %+v", queue)
	}
	if _, reasons, err := svc.DeckDetail(ctx, 0, true, id); err != nil || len(reasons) != 3 {
		t.Errorf("reasons: %v %v", reasons, err)
	}
	if err := svc.Moderate(ctx, 3, id, false, ""); err != nil {
		t.Fatal(err)
	}
	if q, _ := svc.Community(ctx, 0, true, true, ""); len(q) != 0 {
		t.Errorf("after keeping: %+v", q)
	}
	if err := svc.Moderate(ctx, 3, id, true, "Not SAT words"); err != nil {
		t.Fatal(err)
	}
	mine, _ = svc.MyDecks(ctx, 13)
	if !mine[0].Hidden || mine[0].HiddenReason == nil || *mine[0].HiddenReason != "Not SAT words" {
		t.Errorf("owner sees why: %+v", mine[0])
	}
	if err := svc.DeleteDeck(ctx, 13, id); err != nil {
		t.Fatal(err)
	}
	if mine, _ = svc.MyDecks(ctx, 13); len(mine) != 0 {
		t.Errorf("deleted: %+v", mine)
	}
}
