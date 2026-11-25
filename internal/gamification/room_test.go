package gamification

import (
	"errors"
	"testing"
	"time"

	"github.com/nazacd/makonbook-api/internal/platform/testdb"
)

func TestRoomCatalogue(t *testing.T) {
	slots := map[string]int{}
	for _, s := range RoomSlots {
		slots[s.Key] = 0
	}
	seen := map[string]bool{}
	for _, it := range RoomItems {
		if seen[it.Code] {
			t.Errorf("%s twice", it.Code)
		}
		seen[it.Code] = true
		if _, ok := slots[it.Slot]; !ok {
			t.Errorf("%s: no slot %q", it.Code, it.Slot)
		}
		if it.Key == "" {
			slots[it.Slot]++
		} else if it.Target <= 0 || it.requirement() == "" {
			t.Errorf("%s: requirement %q at %d", it.Code, it.Key, it.Target)
		}
	}
	for slot, defaults := range slots {
		if defaults != 1 {
			t.Errorf("slot %s has %d defaults, want exactly one", slot, defaults)
		}
	}
	if thousands(999) != "999" || thousands(1000) != "1,000" || thousands(1234567) != "1,234,567" {
		t.Error("thousands")
	}
}

func TestRoom(t *testing.T) {
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
	item := func(r Room, code string) RoomItem {
		for _, s := range r.Slots {
			for _, it := range s.Items {
				if it.Code == code {
					return it
				}
			}
		}
		t.Fatalf("no item %s", code)
		return RoomItem{}
	}

	// From day one: every slot's default placed, nothing else open, nothing new.
	r, err := svc.Room(ctx, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if r.Level != 1 || r.ItemsUnlocked != 6 || r.ItemsTotal != len(RoomItems) || len(r.Slots) != 6 {
		t.Fatalf("a new room: %+v", r)
	}
	for _, code := range []string{"wp_lavender", "art_cork", "rug_mat", "desk_study", "corner_lamp", "plant_cactus"} {
		if it := item(r, code); !it.Unlocked || !it.Equipped || it.IsNew || it.Requirement != nil {
			t.Fatalf("default %s: %+v", code, it)
		}
	}
	if it := item(r, "wp_ikat"); it.Unlocked || it.Equipped || it.IsNew || it.Requirement == nil || it.Requirement.Label != "Reach level 3" || it.Requirement.Progress != 1 || it.Requirement.Target != 3 {
		t.Fatalf("locked ikat: %+v %+v", it, it.Requirement)
	}

	// Only what exists, in its own slot, and only once unlocked.
	if _, err := svc.EquipRoom(ctx, 11, "wallpaper", "wp_nothing", now); !errors.Is(err, ErrRoomItem) {
		t.Fatalf("an unknown item: %v", err)
	}
	if _, err := svc.EquipRoom(ctx, 11, "rug", "wp_lavender", now); !errors.Is(err, ErrRoomItem) {
		t.Fatalf("an item of another slot: %v", err)
	}
	if _, err := svc.EquipRoom(ctx, 11, "wallpaper", "wp_ikat", now); !errors.Is(err, ErrRoomLocked) {
		t.Fatalf("a locked item: %v", err)
	}

	// A test yesterday and 60 reviews today: 272 XP, level 3.
	if err := svc.Award(ctx, AttemptEvent(11, "7b3b5b0e-0000-4000-8000-000000000001", "Day 1", 12, 20, 0, 7, now.AddDate(0, 0, -1))); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if err := svc.Award(ctx, ReviewsEvent(11, svc.DayOf(now), 30, now.Add(time.Duration(i)*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}
	later := now.Add(time.Hour)
	r, err = svc.Room(ctx, 11, later)
	if err != nil {
		t.Fatal(err)
	}
	if it := item(r, "wp_ikat"); r.Level != 3 || r.ItemsUnlocked != 7 || !it.Unlocked || !it.IsNew || it.Equipped {
		t.Fatalf("level 3: %+v, ikat %+v", r, it)
	}
	// Two active days aren't a three-day streak; 60 reviews aren't 100.
	if item(r, "rug_bukhara").Unlocked || item(r, "art_suzani").Unlocked || item(r, "art_suzani").Requirement.Progress != 60 {
		t.Fatalf("still locked: %+v %+v", item(r, "rug_bukhara"), item(r, "art_suzani").Requirement)
	}

	// Placed: the slot holds it, the others keep theirs.
	r, err = svc.EquipRoom(ctx, 11, "wallpaper", "wp_ikat", later)
	if err != nil {
		t.Fatal(err)
	}
	if !item(r, "wp_ikat").Equipped || item(r, "wp_lavender").Equipped || !item(r, "rug_mat").Equipped {
		t.Fatalf("after placing: %+v", r.Slots[0])
	}

	// Seen: new no more, and it stays so.
	if r, err = svc.SeenRoom(ctx, 11, later); err != nil || item(r, "wp_ikat").IsNew {
		t.Fatalf("seen: %v %+v", err, item(r, "wp_ikat"))
	}
	if r, _ = svc.Room(ctx, 11, later); item(r, "wp_ikat").IsNew || !item(r, "wp_ikat").Equipped {
		t.Fatalf("read again: %+v", item(r, "wp_ikat"))
	}

	// The ledger wiped (a backfill from no history): what was placed is locked
	// again, so the slot shows its default.
	if _, err := svc.Backfill(ctx, nil); err != nil {
		t.Fatal(err)
	}
	r, _ = svc.Room(ctx, 11, later)
	if item(r, "wp_ikat").Unlocked || item(r, "wp_ikat").Equipped || !item(r, "wp_lavender").Equipped || r.ItemsUnlocked != 6 {
		t.Fatalf("after the wipe: %+v", r.Slots[0])
	}
}
