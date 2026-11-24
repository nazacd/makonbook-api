package gamification

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nazacd/makonbook-api/internal/platform/testdb"
)

func TestLevels(t *testing.T) {
	for _, tc := range []struct {
		xp    int64
		level int32
	}{{0, 1}, {99, 1}, {100, 2}, {249, 2}, {250, 3}, {700, 5}, {2700, 10}, {16200, 25}} {
		if got := Level(tc.xp); got != tc.level {
			t.Errorf("Level(%d) = %d, want %d", tc.xp, got, tc.level)
		}
	}
	if LevelTitle(1) != "Starter" || LevelTitle(4) != "Learner" || LevelTitle(25) != "Master" || LevelTitle(30) != "Master" {
		t.Error("titles")
	}
}

func TestLongestRun(t *testing.T) {
	if LongestRun(nil) != 0 || LongestRun([]bool{false}) != 0 || LongestRun([]bool{true, true, false, true, true, true}) != 3 || LongestRun([]bool{true, true}) != 2 {
		t.Fatal("runs")
	}
}

func TestStreaks(t *testing.T) {
	day := func(s string) pgtype.Date {
		d, _ := time.Parse("2006-01-02", s)
		return pgtype.Date{Time: d, Valid: true}
	}
	today, _ := time.Parse("2006-01-02", "2026-11-20")
	// Newest first, as the query returns them.
	days := []pgtype.Date{day("2026-11-19"), day("2026-11-18"), day("2026-11-17"), day("2026-11-10"), day("2026-11-09"), day("2026-11-08"), day("2026-11-07")}
	cur, best := streaks(days, today)
	if cur != 3 || best != 4 {
		t.Fatalf("yesterday's run: current %d best %d, want 3 and 4", cur, best)
	}
	cur, best = streaks(append([]pgtype.Date{day("2026-11-20")}, days...), today)
	if cur != 4 || best != 4 {
		t.Fatalf("today's run: current %d best %d, want 4 and 4", cur, best)
	}
	if cur, _ := streaks([]pgtype.Date{day("2026-11-17")}, today); cur != 0 {
		t.Fatalf("a gap: current %d, want 0", cur)
	}
}

func TestAwardAndState(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `
		INSERT INTO offices (id, name) VALUES (1, 'Main');
		INSERT INTO students (client_id, hh_student_id, first_name, last_name, phone_raw, office_ids)
		VALUES (11, 1, 'Ali', 'Valiyev', '', '{1}');`); err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("Asia/Tashkent")
	svc := NewService(pool, loc, nil)
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, loc)
	yesterday := now.AddDate(0, 0, -1)

	// A test yesterday, twice: counted once.
	ev := AttemptEvent(11, "7b3b5b0e-0000-4000-8000-000000000001", "Day 1", 12, 20, 0, 7, yesterday)
	for range 2 {
		if err := svc.Award(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	st, err := svc.State(ctx, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	// 30 + 12, plus yesterday's daily quests: 10 right answers (30) and one finished (40).
	if st.XPTotal != 42+30+40 || st.XPToday != 0 {
		t.Fatalf("xp %d today %d", st.XPTotal, st.XPToday)
	}
	if st.Level != 2 || st.LevelTitle != "Starter" || st.Streak.Current != 1 || !st.Streak.AtRisk || st.Streak.ActiveToday {
		t.Fatalf("level %d %s streak %+v", st.Level, st.LevelTitle, st.Streak)
	}
	if len(st.Streak.Days) != 14 || !st.Streak.Days[12].Active || st.Streak.Days[13].Active {
		t.Fatalf("days %+v", st.Streak.Days)
	}
	// Today's daily quests are fresh; the week's right answers carry yesterday's 12.
	if st.Quests.Daily[0].Progress != 0 || st.Quests.Weekly[0].Progress != 12 || st.Quests.Weekly[2].Progress != 1 {
		t.Fatalf("quests %+v", st.Quests)
	}
	unlocked := map[string]bool{}
	for _, a := range st.Achievements {
		if a.UnlockedAt != nil {
			unlocked[a.Code] = true
		}
	}
	// First XP and the first mock test; nothing else yet.
	if !unlocked["first_xp"] || !unlocked["pencil_age"] || unlocked["xp_500"] || len(unlocked) != 2 {
		t.Fatalf("achievements %v", unlocked)
	}
	for _, a := range st.Achievements {
		if a.Code == "night_owl" && (a.Title != "???" || a.UnlockedAt != nil) {
			t.Fatalf("a secret shows itself: %+v", a)
		}
		if a.Code == "level_10" && a.Tier != TierChallenge {
			t.Fatalf("tier %+v", a)
		}
	}

	// Reviews today, in two batches: the day's row accumulates, capped at 40.
	day := svc.DayOf(now)
	if err := svc.Award(ctx, ReviewsEvent(11, day, 30, now)); err != nil {
		t.Fatal(err)
	}
	if err := svc.Award(ctx, ReviewsEvent(11, day, 30, now.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	st, err = svc.State(ctx, 11, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	// 40 for the reviews, 30 for today's "10 words reviewed", 90 for the week's 60.
	if st.XPToday != 40+30+90 || !st.Streak.ActiveToday || st.Streak.Current != 2 || st.Streak.AtRisk {
		t.Fatalf("after reviews: today %d streak %+v", st.XPToday, st.Streak)
	}
	if !st.LeveledToday {
		t.Fatal("112 → 272 XP crosses level 3")
	}
	if len(st.Recent) == 0 || st.Recent[0].Amount == 0 {
		t.Fatalf("recent %+v", st.Recent)
	}
	for _, a := range st.Achievements {
		if a.Code == "getting_words" && a.UnlockedAt == nil {
			t.Fatal("60 reviews should have unlocked Getting Words")
		}
	}

	// The backfill from an empty history wipes it all.
	if n, err := svc.Backfill(ctx, nil); err != nil || n != 0 {
		t.Fatalf("backfill: %d %v", n, err)
	}
	st, _ = svc.State(ctx, 11, now)
	if st.XPTotal != 0 {
		t.Fatalf("after the wipe: %d", st.XPTotal)
	}
}
