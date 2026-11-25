package gamification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nazacd/makonbook-api/internal/gamification/gamificationdb"
)

// Mako's room (spec 28): a study room for the mascot that fills with what
// the student earns. Six slots, each holding one item; every slot has an item
// from day one and others that unlock from the same figures the achievements
// read (level, XP, best streak, word reviews, quests done). All of it is
// cosmetic: nothing here touches a score, a grade or XP.

// RoomSlotDef is a place in the room. The order is the picker's.
type RoomSlotDef struct{ Key, Label string }

var RoomSlots = []RoomSlotDef{
	{"wallpaper", "Wallpaper"},
	{"art", "Wall art"},
	{"rug", "Rug"},
	{"desk", "Furniture"},
	{"corner", "Light"},
	{"plant", "Plant"},
}

// RoomItemDef is something to place in a slot. Key is the figure that
// unlocks it at Target; an empty Key is the slot's default, there from the
// start. The web draws each item by its Code.
type RoomItemDef struct {
	Code, Slot, Name, Blurb, Key string
	Target                       int64
}

var RoomItems = []RoomItemDef{
	{"wp_lavender", "wallpaper", "Lavender wall", "Calm and quiet: a good start.", "", 0},
	{"wp_ikat", "wallpaper", "Atlas ikat", "The flowing silk pattern of Margilan atlas.", keyLevel, 3},
	{"wp_girih", "wallpaper", "Girih tiles", "Eight-point stars from Samarkand's madrasas.", keyStreak, 7},
	{"wp_night", "wallpaper", "Starry night", "For night-owl study sessions.", keyLevel, 8},

	{"art_cork", "art", "Study board", "Notes, plans and a photo or two.", "", 0},
	{"art_suzani", "art", "Suzani", "Hand-embroidered suns from Bukhara.", keyReviews, 100},
	{"art_registan", "art", "Registan print", "Three madrasas under a blue sky.", keyLevel, 5},
	{"art_map", "art", "World map", "Pick where you'll study next.", keyXP, 1000},

	{"rug_mat", "rug", "Round mat", "Soft, simple, comfy.", "", 0},
	{"rug_bukhara", "rug", "Bukhara gilam", "A deep red carpet with gul medallions.", keyStreak, 3},
	{"rug_atlas", "rug", "Atlas runner", "Ikat colours under your feet.", keyQuests, 5},

	{"desk_study", "desk", "Study desk", "Laptop, books and a warm mug.", "", 0},
	{"desk_tapchan", "desk", "Tapchan and choynak", "Tea, cushions and long conversations.", keyQuests, 15},
	{"desk_scholar", "desk", "Scholar's desk", "Big desk, open book, serious plans.", keyLevel, 10},

	{"corner_lamp", "corner", "Floor lamp", "Warm light for evening practice.", "", 0},
	{"corner_chiroq", "corner", "Chiroq lantern", "Pierced metal and coloured glass.", keyQuests, 25},
	{"corner_globe", "corner", "Globe", "The world is waiting.", keyReviews, 500},

	{"plant_cactus", "plant", "Cactus", "Low maintenance, high spirit.", "", 0},
	{"plant_tulips", "plant", "Tulips", "Lola: the first flowers of spring.", keyReviews, 250},
	{"plant_anor", "plant", "Pomegranate tree", "Anor: a sign of plenty and patience.", keyLevel, 12},
}

var (
	// ErrRoomItem: no such slot, no such item, or the item belongs to another slot.
	ErrRoomItem = errors.New("gamification: unknown room item")
	// ErrRoomLocked: the item isn't unlocked yet.
	ErrRoomLocked = errors.New("gamification: room item locked")
)

func roomItem(code string) (RoomItemDef, bool) {
	for _, it := range RoomItems {
		if it.Code == code {
			return it, true
		}
	}
	return RoomItemDef{}, false
}

func roomDefault(slot string) string {
	for _, it := range RoomItems {
		if it.Slot == slot && it.Key == "" {
			return it.Code
		}
	}
	return ""
}

func (it RoomItemDef) unlocked(f figures) bool { return it.Key == "" || f.of(it.Key) >= it.Target }

// What it takes, in words.
func (it RoomItemDef) requirement() string {
	switch it.Key {
	case keyXP:
		return fmt.Sprintf("Earn %s XP", thousands(it.Target))
	case keyLevel:
		return fmt.Sprintf("Reach level %d", it.Target)
	case keyStreak:
		return fmt.Sprintf("Keep a %d-day streak", it.Target)
	case keyReviews:
		return fmt.Sprintf("Review words %s times", thousands(it.Target))
	case keyQuests:
		return fmt.Sprintf("Finish %d quests", it.Target)
	}
	return ""
}

func thousands(n int64) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// Room is GET /v1/me/room.
type Room struct {
	Level         int32      `json:"level"`
	ItemsUnlocked int        `json:"items_unlocked"`
	ItemsTotal    int        `json:"items_total"`
	Slots         []RoomSlot `json:"slots"`
}

type RoomSlot struct {
	Key   string     `json:"key"`
	Label string     `json:"label"`
	Items []RoomItem `json:"items"`
}

type RoomItem struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	Blurb    string `json:"blurb"`
	Unlocked bool   `json:"unlocked"`
	Equipped bool   `json:"equipped"`
	// Unlocked since the student last saw the room (never a default).
	IsNew       bool             `json:"is_new"`
	Requirement *RoomRequirement `json:"requirement"`
}

// RoomRequirement is what unlocks an item and how far along the student is.
type RoomRequirement struct {
	Key      string `json:"key"`
	Target   int64  `json:"target"`
	Progress int64  `json:"progress"`
	Label    string `json:"label"`
}

// What is stored for a student: the chosen item per slot and the items seen.
func (s *Service) roomLayout(ctx context.Context, q *gamificationdb.Queries, student int32) (map[string]string, map[string]bool, error) {
	row, err := q.RoomLayout(ctx, student)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, err
	}
	chosen := map[string]string{}
	if len(row.Slots) > 0 {
		if err := json.Unmarshal(row.Slots, &chosen); err != nil {
			return nil, nil, fmt.Errorf("room layout of %d: %w", student, err)
		}
	}
	seen := map[string]bool{}
	for _, c := range row.Seen {
		seen[c] = true
	}
	return chosen, seen, nil
}

func (s *Service) room(ctx context.Context, q *gamificationdb.Queries, student int32, now time.Time) (Room, error) {
	f, _, err := s.figuresOf(ctx, q, student, now)
	if err != nil {
		return Room{}, err
	}
	chosen, seen, err := s.roomLayout(ctx, q, student)
	if err != nil {
		return Room{}, err
	}
	out := Room{Level: int32(f.of(keyLevel)), ItemsTotal: len(RoomItems), Slots: make([]RoomSlot, 0, len(RoomSlots))}
	for _, slot := range RoomSlots {
		// The chosen item, if it is this slot's and still unlocked; else the default.
		equipped := roomDefault(slot.Key)
		if it, ok := roomItem(chosen[slot.Key]); ok && it.Slot == slot.Key && it.unlocked(f) {
			equipped = it.Code
		}
		sv := RoomSlot{Key: slot.Key, Label: slot.Label, Items: []RoomItem{}}
		for _, it := range RoomItems {
			if it.Slot != slot.Key {
				continue
			}
			open := it.unlocked(f)
			iv := RoomItem{Code: it.Code, Name: it.Name, Blurb: it.Blurb, Unlocked: open, Equipped: it.Code == equipped, IsNew: open && it.Key != "" && !seen[it.Code]}
			if it.Key != "" {
				iv.Requirement = &RoomRequirement{Key: it.Key, Target: it.Target, Progress: min(f.of(it.Key), it.Target), Label: it.requirement()}
			}
			if open {
				out.ItemsUnlocked++
			}
			sv.Items = append(sv.Items, iv)
		}
		out.Slots = append(out.Slots, sv)
	}
	return out, nil
}

// Room reads a student's room as of now.
func (s *Service) Room(ctx context.Context, student int32, now time.Time) (Room, error) {
	return s.room(ctx, gamificationdb.New(s.pool), student, now)
}

// EquipRoom places an unlocked item in its slot and returns the room.
// ErrRoomItem for a slot or item that doesn't exist (or an item of another
// slot), ErrRoomLocked for one not unlocked yet.
func (s *Service) EquipRoom(ctx context.Context, student int32, slot, code string, now time.Time) (Room, error) {
	it, ok := roomItem(code)
	if !ok || it.Slot != slot {
		return Room{}, ErrRoomItem
	}
	q := gamificationdb.New(s.pool)
	f, _, err := s.figuresOf(ctx, q, student, now)
	if err != nil {
		return Room{}, err
	}
	if !it.unlocked(f) {
		return Room{}, ErrRoomLocked
	}
	if err := q.SetRoomSlot(ctx, gamificationdb.SetRoomSlotParams{StudentClientID: student, Slot: slot, Item: code}); err != nil {
		return Room{}, err
	}
	return s.room(ctx, q, student, now)
}

// SeenRoom marks every item unlocked so far as shown, so none is new any
// more, and returns the room.
func (s *Service) SeenRoom(ctx context.Context, student int32, now time.Time) (Room, error) {
	q := gamificationdb.New(s.pool)
	r, err := s.room(ctx, q, student, now)
	if err != nil {
		return Room{}, err
	}
	fresh := []string{}
	for i := range r.Slots {
		for j := range r.Slots[i].Items {
			if it := &r.Slots[i].Items[j]; it.IsNew {
				fresh = append(fresh, it.Code)
				it.IsNew = false
			}
		}
	}
	if len(fresh) > 0 {
		if err := q.AddRoomSeen(ctx, gamificationdb.AddRoomSeenParams{StudentClientID: student, Seen: fresh}); err != nil {
			return Room{}, err
		}
	}
	return r, nil
}
