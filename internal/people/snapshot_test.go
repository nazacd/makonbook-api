package people

import (
	"testing"
	"time"
)

func day(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestActiveTeacherIDs(t *testing.T) {
	s := Snapshot{
		Today: day("2026-10-10"),
		Teachers: []Teacher{
			{ID: 1}, {ID: 2}, {ID: 3}, {ID: 4}, {ID: 5}, {ID: 6, Fired: true}, {ID: 7},
		},
		Groups: []Group{{Schedule: []ScheduleItem{
			{TeacherIDs: []int32{1}, BeginDate: day("2026-09-01")},                             // running, open-ended
			{TeacherIDs: []int32{2}, BeginDate: day("2026-09-01"), EndDate: day("2026-10-05")}, // ended 5 days ago: grace
			{TeacherIDs: []int32{3}, BeginDate: day("2026-06-01"), EndDate: day("2026-09-30")}, // ended 10 days ago
			{TeacherIDs: []int32{4}, BeginDate: day("2026-10-20")},                             // starts in 10 days
			{TeacherIDs: []int32{5}, BeginDate: day("2026-11-30")},                             // starts in 51 days
			{TeacherIDs: []int32{6}}, // fired
		}}},
	}

	got := ActiveTeacherIDs(s)
	want := map[int32]bool{1: true, 2: true, 4: true}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for id := range want {
		if !got[id] {
			t.Errorf("teacher %d should be active", id)
		}
	}
}
