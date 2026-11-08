package roadmap

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nazacd/makonbook-api/internal/practice"
)

func TestValidate(t *testing.T) {
	good := fixture("math", "foundation", "m-")
	if bad := good.Validate(); len(bad) != 0 {
		t.Fatalf("the fixture is invalid: %v", bad)
	}
	cases := map[string]struct {
		change func(c *Course)
		want   string
	}{
		"subject":           {func(c *Course) { c.Subject = "physics" }, "subject"},
		"level":             {func(c *Course) { c.Level = "expert" }, "level"},
		"unit id twice":     {func(c *Course) { c.Units[1].ID = c.Units[0].ID }, "used twice"},
		"lesson id twice":   {func(c *Course) { c.Units[1].Lessons[0].ID = c.Units[0].Lessons[0].ID }, "used twice"},
		"country twice":     {func(c *Course) { c.Countries[1].ID = "uzbekistan" }, "used twice"},
		"unit not in route": {func(c *Course) { c.Countries[1].Units = c.Countries[1].Units[:1] }, "every unit once"},
		"route out of order": {func(c *Course) {
			c.Countries[1].Units = []string{c.Countries[1].Units[1], c.Countries[1].Units[0]}
		}, "every unit once"},
		"unit twice in route": {func(c *Course) { c.Countries[1].Units = append(c.Countries[1].Units, "m-1") }, "every unit once"},
		"mix":                 {func(c *Course) { c.Units[0].Lessons[0].LevelCheck.Mix.Hard = 3 }, "add up"},
		"negative mix": {func(c *Course) {
			c.Units[0].Lessons[0].LevelCheck.Mix = Mix{Easy: 5, Medium: 0, Hard: -1}
		}, "add up"},
		"no skills":           {func(c *Course) { c.Units[0].Lessons[0].Skills = nil }, "skills"},
		"blank skill":         {func(c *Course) { c.Units[0].Lessons[0].LevelCheck.Skills = []string{" "} }, "level check's skills"},
		"homework difficulty": {func(c *Course) { c.Units[0].Lessons[0].Homework[0].Difficulty = "" }, "difficulty"},
		"homework title":      {func(c *Course) { c.Units[0].Lessons[0].Homework[0].Title = "" }, "no title"},
		"homework questions":  {func(c *Course) { c.Units[0].Lessons[0].Homework[0].Questions = 0 }, "needs questions"},
		"homework skills":     {func(c *Course) { c.Units[0].Lessons[0].Homework[0].Skills = []string{} }, "skills"},
		"minutes":             {func(c *Course) { c.Units[0].Lessons[0].Minutes = 0 }, "minutes"},
		"no lessons":          {func(c *Course) { c.Units[2].Lessons = nil }, "no lessons"},
	}
	for name, tc := range cases {
		c := fixture("math", "foundation", "m-")
		tc.change(&c)
		bad := c.Validate()
		if !slices.ContainsFunc(bad, func(p string) bool { return strings.Contains(p, tc.want) }) {
			t.Errorf("%s: got %v, want a problem with %q", name, bad, tc.want)
		}
	}
}

func TestDisciplineCourse(t *testing.T) {
	for discipline, want := range map[string]string{
		"PRE SAT MATH":        "math-pre-sat",
		"Pre-SAT English":     "english-pre-sat",
		"PRESAT ENG":          "english-pre-sat",
		"MATH FOUNDATION":     "math-foundation",
		"Foundation English":  "english-foundation",
		"SAT MATH ADV":        "math-advanced",
		"ADVANCED SAT ENG":    "english-advanced",
		"SAT MATH":            "", // no level
		"FOUNDATION":          "", // no subject
		"IELTS":               "",
		"PREPARATION MATH":    "", // "PRE" only as a word before SAT
		"ADVENTURE MATH CLUB": "",
	} {
		got, ok := DisciplineCourse(discipline)
		if got != want || ok != (want != "") {
			t.Errorf("%q: %q %v, want %q", discipline, got, ok, want)
		}
	}
}

func TestOnboardingLevels(t *testing.T) {
	n := func(v int) *int { return &v }
	valid := func() Onboarding {
		return Onboarding{
			Graduation: "2028", SATDates: []string{"2027-03-13", "2026-12-05", "2027-03-13"},
			Target: Scores{Total: n(1400), Math: n(720), RW: n(680)}, Focus: "both",
			Schedule: Schedule{Days: []string{"sat", "mon"}, Minutes: 30},
		}
	}
	o := valid()
	if err := o.check(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(o.SATDates, []string{"2026-12-05", "2027-03-13"}) || !slices.Equal(o.Schedule.Days, []string{"mon", "sat"}) {
		t.Errorf("not put in order: %v %v", o.SATDates, o.Schedule.Days)
	}
	if got := o.courses(); !slices.Equal(got, []string{"math-foundation", "english-foundation"}) {
		t.Errorf("no score: %v", got)
	}
	today := time.Date(2026, 12, 6, 0, 0, 0, 0, time.UTC)
	if d := o.examDate(today); d == nil || *d != "2027-03-13" {
		t.Errorf("exam date: %v", d)
	}
	if d := o.examDate(time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC)); d != nil {
		t.Errorf("exam date after them all: %v", *d)
	}

	for _, tc := range []struct {
		current *Scores
		focus   string
		want    []string
	}{
		{&Scores{Total: n(890)}, "both", []string{"math-foundation", "english-foundation"}}, // 445 each
		{&Scores{Total: n(900)}, "both", []string{"math-pre-sat", "english-pre-sat"}},       // 450 each
		{&Scores{Total: n(1190)}, "math", []string{"math-pre-sat"}},                         // 595
		{&Scores{Total: n(1200)}, "english", []string{"english-advanced"}},                  // 600
		{&Scores{Math: n(440), RW: n(600)}, "both", []string{"math-foundation", "english-advanced"}},
		{&Scores{Math: n(590), RW: n(450)}, "both", []string{"math-pre-sat", "english-pre-sat"}},
		{&Scores{Math: n(800), RW: n(200)}, "math", []string{"math-advanced"}},
	} {
		o := valid()
		o.Current, o.Focus = tc.current, tc.focus
		if err := o.check(); err != nil {
			t.Fatalf("%+v: %v", tc.current, err)
		}
		if got := o.courses(); !slices.Equal(got, tc.want) {
			t.Errorf("%+v %s: %v, want %v", tc.current, tc.focus, got, tc.want)
		}
	}

	for name, change := range map[string]func(o *Onboarding){
		"graduation":      func(o *Onboarding) { o.Graduation = "2026" },
		"date":            func(o *Onboarding) { o.SATDates = []string{"2027-13-01"} },
		"current both":    func(o *Onboarding) { o.Current = &Scores{Total: n(1000), Math: n(500)} },
		"current empty":   func(o *Onboarding) { o.Current = &Scores{} },
		"current half":    func(o *Onboarding) { o.Current = &Scores{Math: n(500)} },
		"current total":   func(o *Onboarding) { o.Current = &Scores{Total: n(390)} },
		"current section": func(o *Onboarding) { o.Current = &Scores{Math: n(810), RW: n(500)} },
		"current step":    func(o *Onboarding) { o.Current = &Scores{Math: n(505), RW: n(500)} },
		"target missing":  func(o *Onboarding) { o.Target.RW = nil },
		"target sum":      func(o *Onboarding) { o.Target.Total = n(1410) },
		"focus":           func(o *Onboarding) { o.Focus = "physics" },
		"no days":         func(o *Onboarding) { o.Schedule.Days = nil },
		"unknown day":     func(o *Onboarding) { o.Schedule.Days = []string{"mon", "funday"} },
		"minutes":         func(o *Onboarding) { o.Schedule.Minutes = 20 },
	} {
		o := valid()
		change(&o)
		if err := o.check(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestResolveSkills(t *testing.T) {
	got := resolveSkills("english", []string{"Command of Evidence", "words in context", "Something Else"})
	want := []string{"command of evidence (quantitative)", "command of evidence (textual)", "words in context", "something else"}
	if !slices.Equal(got, want) {
		t.Errorf("english: %v", got)
	}
	got = resolveSkills("math", []string{"One-Variable Data: Distributions and Measures of Center and Spread", "Linear functions"})
	want = []string{"one-variable data: distributions and measures of center and spread", "linear functions"}
	if !slices.Equal(got, want) {
		t.Errorf("math: %v", got)
	}
}

func TestChoose(t *testing.T) {
	keep := func(int, func(i, j int)) {} // no shuffle: the order given
	cands := func(spec map[string]int) []practice.Candidate {
		var out []practice.Candidate
		for _, d := range append(slices.Clone(Difficulties), "") {
			for i := range spec[d] {
				out = append(out, practice.Candidate{ID: d + string(rune('a'+i)), Difficulty: d})
			}
		}
		return out
	}
	diffs := func(ids []string) string { return strings.Join(ids, " ") }

	// Enough of everything: exactly the mix, easy first.
	got := choose(cands(map[string]int{"easy": 3, "medium": 3, "hard": 3}), nil, map[string]int{"easy": 2, "medium": 1, "hard": 1}, keep)
	if diffs(got) != "easya easyb mediuma harda" {
		t.Errorf("enough: %v", got)
	}

	// Short of easy: the rest from medium (its neighbour), not from hard.
	got = choose(cands(map[string]int{"easy": 1, "medium": 3, "hard": 3}), nil, map[string]int{"easy": 3, "hard": 1}, keep)
	if diffs(got) != "easya mediuma mediumb harda" {
		t.Errorf("neighbour: %v", got)
	}

	// Short of medium and its neighbours: anything of the skills, even not estimated.
	got = choose(cands(map[string]int{"medium": 1, "": 2}), nil, map[string]int{"medium": 3}, keep)
	if len(got) != 3 || got[0] != "mediuma" {
		t.Errorf("any difficulty: %v", got)
	}

	// Questions already had come last: fresh neighbours before seen exact ones.
	seen := map[string]bool{"easya": true, "easyb": true}
	got = choose(cands(map[string]int{"easy": 3, "medium": 2}), seen, map[string]int{"easy": 3}, keep)
	if diffs(got) != "easyc mediuma mediumb" {
		t.Errorf("fresh first: %v", got)
	}
	got = choose(cands(map[string]int{"easy": 3}), seen, map[string]int{"easy": 3}, keep)
	if diffs(got) != "easyc easya easyb" {
		t.Errorf("seen when nothing else: %v", got)
	}

	// Not enough at all: fewer; none at all: none. Never twice the same.
	got = choose(cands(map[string]int{"hard": 2}), nil, map[string]int{"easy": 2, "medium": 2, "hard": 2}, keep)
	if len(got) != 2 {
		t.Errorf("fewer: %v", got)
	}
	if got := choose(nil, nil, map[string]int{"easy": 2}, keep); len(got) != 0 {
		t.Errorf("none: %v", got)
	}
	got = choose(cands(map[string]int{"easy": 5, "medium": 5, "hard": 5}), nil, map[string]int{"easy": 6, "medium": 6, "hard": 1}, keep)
	slices.Sort(got)
	if len(got) != 13 || len(slices.Compact(got)) != 13 {
		t.Errorf("repeats: %v", got)
	}
}

func TestLay(t *testing.T) {
	c := fixture("math", "foundation", "m-")
	p := progress{best: map[string]int{}, homework: map[string]map[int]bool{}}
	status := func(l layout) string {
		var b strings.Builder
		for _, u := range l.units {
			b.WriteString(u.unit.ID + ":" + u.status + lockOf(u.lock) + " [")
			for _, ls := range u.unit.Lessons {
				s := l.lessons[ls.ID]
				b.WriteString(" " + s.status + lockOf(s.lock))
			}
			b.WriteString(" ] ")
		}
		return strings.TrimSpace(b.String())
	}

	// Nothing done: the first lesson is open, the rest wait for the one before.
	if got := status(lay(&c, false, p)); got != "m-1:open [ open locked/previous ] m-2:locked/previous [ locked/previous ] m-3:locked/previous [ locked/previous ]" {
		t.Errorf("new: %s", got)
	}

	// Passed but homework left: not done. 69 % doesn't pass.
	p.best["m-1-1"] = 100
	if got := lay(&c, false, p).lessons["m-1-2"].status; got != StatusLocked {
		t.Errorf("homework left: next is %s", got)
	}
	p.best["m-1-1"], p.homework["m-1-1"] = 69, map[int]bool{0: true}
	if got := lay(&c, false, p).lessons["m-1-1"].status; got != StatusOpen {
		t.Errorf("69 %%: %s", got)
	}
	p.best["m-1-1"] = 70
	if got := status(lay(&c, false, p)); got != "m-1:open [ done open ] m-2:locked/previous [ locked/previous ] m-3:locked/previous [ locked/previous ]" {
		t.Errorf("one done: %s", got)
	}

	// Every set of the second lesson: then the unit is done and the next opens.
	p.best["m-1-2"], p.homework["m-1-2"] = 90, map[int]bool{0: true}
	if got := lay(&c, false, p).lessons["m-1-2"].status; got != StatusOpen {
		t.Errorf("one set of two: %s", got)
	}
	p.homework["m-1-2"][1] = true
	if got := status(lay(&c, false, p)); got != "m-1:done [ done done ] m-2:open [ open ] m-3:locked/previous [ locked/previous ]" {
		t.Errorf("unit done: %s", got)
	}

	// A free account: only the first country, whatever the progress.
	p.best["m-2-1"], p.homework["m-2-1"] = 100, map[int]bool{0: true}
	l := lay(&c, true, p)
	if got := status(l); got != "m-1:done [ done done ] m-2:locked/enrolled_only [ locked/enrolled_only ] m-3:locked/enrolled_only [ locked/enrolled_only ]" {
		t.Errorf("free: %s", got)
	}
	if l.freeUnits == nil || *l.freeUnits != 1 {
		t.Errorf("free units: %v", l.freeUnits)
	}
	sum := summaryOf(assignment{course: &c, source: SourceOnboarding}, l)
	if sum.LessonsDone != 2 || sum.UnitsDone != 1 || sum.LessonsTotal != 4 || sum.UnitsTotal != 3 || sum.Current == nil ||
		sum.Current.LessonID != "m-2-1" || sum.Title != "Math · Foundation" {
		t.Errorf("free summary: %+v", sum)
	}

	// Enrolled, everything done: no current lesson.
	p.best["m-3-1"], p.homework["m-3-1"] = 100, map[int]bool{0: true}
	sum = summaryOf(assignment{course: &c, source: SourceGroup}, lay(&c, false, p))
	if sum.Current != nil || sum.LessonsDone != 4 || sum.UnitsDone != 3 || sum.FreeUnits != nil {
		t.Errorf("all done: %+v", sum)
	}
}

func lockOf(l *string) string {
	if l == nil {
		return ""
	}
	return "/" + *l
}
