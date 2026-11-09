// Package roadmap is SAT MAKON's curriculum as a route for each student
// (spec 22): courses of units and lessons, onboarding for registered
// students, and each lesson's level check and homework, drawn from the
// question bank and graded on the server.
package roadmap

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/nazacd/makonbook-api/internal/practice"
	"github.com/nazacd/makonbook-api/internal/roadmap/roadmapdb"
)

// Subjects, levels and difficulties, as the curriculum and the bank name them.
var (
	Subjects     = []string{"math", "english"}
	Levels       = []string{"foundation", "pre-sat", "advanced"}
	Difficulties = []string{"easy", "medium", "hard"}
)

// Course is one subject at one level: the countries of its route and its
// units in order. It is imported from SAT MAKON's curriculum files.
type Course struct {
	Subject   string    `json:"subject"`
	Level     string    `json:"level"`
	Countries []Country `json:"countries"`
	Units     []Unit    `json:"units"`
}

// Country is a stretch of the route: units in order.
type Country struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Units []string `json:"units"`
}

// Unit is a stop on the route, with its lessons.
type Unit struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Domain  string   `json:"domain"`
	Summary string   `json:"summary"`
	Stop    Stop     `json:"stop"`
	Lessons []Lesson `json:"lessons"`
}

// Stop is the university a unit is set at.
type Stop struct {
	University string `json:"university"`
	City       string `json:"city"`
	Country    string `json:"country"`
}

// Lesson is what a student studies, then checks with a level check and
// practises with homework. Notes are in the API's content format.
type Lesson struct {
	ID             string     `json:"id"`
	Title          string     `json:"title"`
	Skills         []string   `json:"skills"` // College Board skills, as questions.skill
	Prerequisites  []string   `json:"prerequisites"`
	Minutes        int        `json:"minutes"`
	Objectives     []string   `json:"objectives"`
	Notes          string     `json:"notes"`
	Examples       []Example  `json:"examples"`
	CommonMistakes []string   `json:"common_mistakes"`
	LevelCheck     LevelCheck `json:"level_check"`
	Homework       []Homework `json:"homework"`
}

// Example is a worked problem.
type Example struct {
	Problem string   `json:"problem"`
	Steps   []string `json:"steps"`
	Answer  string   `json:"answer"`
}

// LevelCheck is how many questions of which skills and difficulties check
// a lesson.
type LevelCheck struct {
	Questions int      `json:"questions"`
	Mix       Mix      `json:"mix"`
	Skills    []string `json:"skills"`
}

// Mix is how many questions of each difficulty.
type Mix struct {
	Easy   int `json:"easy"`
	Medium int `json:"medium"`
	Hard   int `json:"hard"`
}

// Counts is the mix by difficulty name.
func (m Mix) Counts() map[string]int {
	return map[string]int{"easy": m.Easy, "medium": m.Medium, "hard": m.Hard}
}

// Homework is a practice set.
type Homework struct {
	Title      string   `json:"title"`
	Skills     []string `json:"skills"`
	Difficulty string   `json:"difficulty"`
	Questions  int      `json:"questions"`
}

// ID is the roadmap id of the course: "<subject>-<level>".
func (c *Course) ID() string { return c.Subject + "-" + c.Level }

// lessons is every lesson in course order, with its unit.
func (c *Course) lessons() []placed {
	var out []placed
	for ui := range c.Units {
		for li := range c.Units[ui].Lessons {
			out = append(out, placed{unit: &c.Units[ui], lesson: &c.Units[ui].Lessons[li]})
		}
	}
	return out
}

type placed struct {
	unit   *Unit
	lesson *Lesson
}

// Lesson finds a lesson of the course, with its unit.
func (c *Course) Lesson(id string) (*Unit, *Lesson, bool) {
	for _, pl := range c.lessons() {
		if pl.lesson.ID == id {
			return pl.unit, pl.lesson, true
		}
	}
	return nil, nil, false
}

// Title is the course's name ("Math · Pre-SAT").
func (c *Course) Title() string { return subjectNames[c.Subject] + " · " + levelNames[c.Level] }

// ParseCourse reads a course file.
func ParseCourse(data []byte) (Course, error) {
	var c Course
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&c); err != nil {
		return Course{}, fmt.Errorf("not a course: %w", err)
	}
	return c, nil
}

// Validate lists what is wrong with a course (nothing: it can be imported).
func (c *Course) Validate() []string {
	var bad []string
	fail := func(format string, args ...any) { bad = append(bad, fmt.Sprintf(format, args...)) }
	if !slices.Contains(Subjects, c.Subject) {
		fail("subject %q is not math or english", c.Subject)
	}
	if !slices.Contains(Levels, c.Level) {
		fail("level %q is not foundation, pre-sat or advanced", c.Level)
	}
	if len(c.Units) == 0 {
		fail("no units")
	}
	units, lessons := map[string]bool{}, map[string]bool{}
	var order []string
	for i, u := range c.Units {
		where := fmt.Sprintf("unit %d (%s)", i+1, u.ID)
		switch {
		case strings.TrimSpace(u.ID) == "":
			fail("unit %d has no id", i+1)
		case units[u.ID]:
			fail("%s: the id is used twice", where)
		}
		units[u.ID] = true
		order = append(order, u.ID)
		if strings.TrimSpace(u.Title) == "" {
			fail("%s has no title", where)
		}
		if strings.TrimSpace(u.Stop.University) == "" {
			fail("%s: its stop has no university", where)
		}
		if len(u.Lessons) == 0 {
			fail("%s has no lessons", where)
		}
		for j, l := range u.Lessons {
			lw := fmt.Sprintf("lesson %s", l.ID)
			switch {
			case strings.TrimSpace(l.ID) == "":
				fail("%s: lesson %d has no id", where, j+1)
				lw = fmt.Sprintf("%s lesson %d", where, j+1)
			case lessons[l.ID] || units[l.ID]:
				fail("%s: the id is used twice", lw)
			}
			lessons[l.ID] = true
			bad = append(bad, l.validate(lw)...)
		}
	}
	countries := map[string]bool{}
	var route []string
	if len(c.Countries) == 0 {
		fail("no countries")
	}
	for i, co := range c.Countries {
		where := fmt.Sprintf("country %d (%s)", i+1, co.ID)
		switch {
		case strings.TrimSpace(co.ID) == "":
			fail("country %d has no id", i+1)
		case countries[co.ID]:
			fail("%s: the id is used twice", where)
		}
		countries[co.ID] = true
		if strings.TrimSpace(co.Name) == "" {
			fail("%s has no name", where)
		}
		if len(co.Units) == 0 {
			fail("%s has no units", where)
		}
		route = append(route, co.Units...)
	}
	if len(c.Countries) > 0 && !slices.Equal(route, order) {
		fail("the countries must list every unit once, in the units' order")
	}
	return bad
}

func (l *Lesson) validate(where string) []string {
	var bad []string
	fail := func(format string, args ...any) { bad = append(bad, where+": "+fmt.Sprintf(format, args...)) }
	if strings.TrimSpace(l.Title) == "" {
		fail("no title")
	}
	if l.Minutes <= 0 {
		fail("minutes must be more than 0")
	}
	if !skillsOK(l.Skills) {
		fail("skills must be a list of skill names")
	}
	for i, e := range l.Examples {
		if strings.TrimSpace(e.Problem) == "" || strings.TrimSpace(e.Answer) == "" {
			fail("example %d needs a problem and an answer", i+1)
		}
	}
	lc := l.LevelCheck
	if lc.Questions <= 0 {
		fail("the level check needs questions")
	}
	if lc.Mix.Easy < 0 || lc.Mix.Medium < 0 || lc.Mix.Hard < 0 || lc.Mix.Easy+lc.Mix.Medium+lc.Mix.Hard != lc.Questions {
		fail("the level check's mix must add up to its %d questions", lc.Questions)
	}
	if !skillsOK(lc.Skills) {
		fail("the level check's skills must be a list of skill names")
	}
	for i, h := range l.Homework {
		hw := fmt.Sprintf("homework %d", i+1)
		if strings.TrimSpace(h.Title) == "" {
			fail("%s has no title", hw)
		}
		if !skillsOK(h.Skills) {
			fail("%s: skills must be a list of skill names", hw)
		}
		if !slices.Contains(Difficulties, h.Difficulty) {
			fail("%s: difficulty must be easy, medium or hard", hw)
		}
		if h.Questions <= 0 {
			fail("%s needs questions", hw)
		}
	}
	return bad
}

func skillsOK(skills []string) bool {
	if len(skills) == 0 {
		return false
	}
	for _, s := range skills {
		if strings.TrimSpace(s) == "" {
			return false
		}
	}
	return true
}

// InvalidError is a curriculum that can't be imported, problem by problem.
type InvalidError struct{ Problems []string }

func (e *InvalidError) Error() string {
	return fmt.Sprintf("the curriculum has %d problems:\n  %s", len(e.Problems), strings.Join(e.Problems, "\n  "))
}

// Imported is what an import did to a course.
type Imported struct {
	ID      string // the roadmap id
	Version int32
	Changed bool // a new version; false: the same as the latest
}

// Import stores each course as a new version (unless it's the same as the
// latest), all or none. Every course must pass Validate, no course may
// come twice, and a lesson id may belong to only one course (progress is
// kept by lesson id).
func (s *Service) Import(ctx context.Context, courses []Course) ([]Imported, error) {
	var problems []string
	seen := map[string]bool{}
	owner := map[string]string{} // lesson id → course
	for i := range courses {
		c := &courses[i]
		for _, p := range c.Validate() {
			problems = append(problems, c.ID()+": "+p)
		}
		if seen[c.ID()] {
			problems = append(problems, c.ID()+": the course comes twice")
		}
		seen[c.ID()] = true
		for _, pl := range c.lessons() {
			if o, ok := owner[pl.lesson.ID]; ok && o != c.ID() {
				problems = append(problems, fmt.Sprintf("%s: lesson %s is also in %s", c.ID(), pl.lesson.ID, o))
			}
			owner[pl.lesson.ID] = c.ID()
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := roadmapdb.New(tx)
	if _, err := tx.Exec(ctx, "LOCK TABLE curriculum_versions IN SHARE ROW EXCLUSIVE MODE"); err != nil {
		return nil, err
	}
	latest, err := q.LatestCourses(ctx)
	if err != nil {
		return nil, err
	}
	stored := map[string]roadmapdb.LatestCoursesRow{}
	for _, row := range latest {
		id := row.Subject + "-" + row.Level
		stored[id] = row
		if seen[id] {
			continue
		}
		var other Course
		if err := json.Unmarshal(row.Content, &other); err != nil {
			return nil, fmt.Errorf("stored course %s: %w", id, err)
		}
		for _, pl := range other.lessons() {
			if o, ok := owner[pl.lesson.ID]; ok {
				problems = append(problems, fmt.Sprintf("%s: lesson %s is already in %s", o, pl.lesson.ID, id))
			}
		}
	}
	if len(problems) > 0 {
		return nil, &InvalidError{problems}
	}
	var out []Imported
	for i := range courses {
		c := &courses[i]
		body, err := json.Marshal(c)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(body)
		hash := hex.EncodeToString(sum[:])
		prev, had := stored[c.ID()]
		if had && prev.ContentHash == hash {
			out = append(out, Imported{ID: c.ID(), Version: prev.Version})
			continue
		}
		version := prev.Version + 1
		err = q.InsertCourseVersion(ctx, roadmapdb.InsertCourseVersionParams{
			Subject: c.Subject, Level: c.Level, Version: version, Content: body, ContentHash: hash,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, Imported{ID: c.ID(), Version: version, Changed: true})
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// course returns the live version of a course, from the cache when it is
// the latest. ErrNotFound if it was never imported.
func (s *Service) course(ctx context.Context, id string) (*Course, error) {
	subject, level, ok := splitID(id)
	if !ok {
		return nil, ErrNotFound
	}
	versions, err := s.versions(ctx)
	if err != nil {
		return nil, err
	}
	v, ok := versions[id]
	if !ok {
		return nil, ErrNotFound
	}
	s.mu.Lock()
	cached, ok := s.cache[id]
	s.mu.Unlock()
	if ok && cached.version == v {
		return cached.course, nil
	}
	body, err := roadmapdb.New(s.pool).CourseContent(ctx, roadmapdb.CourseContentParams{Subject: subject, Level: level, Version: v})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c := new(Course)
	if err := json.Unmarshal(body, c); err != nil {
		return nil, fmt.Errorf("course %s: %w", id, err)
	}
	s.mu.Lock()
	s.cache[id] = cachedCourse{version: v, course: c}
	s.mu.Unlock()
	return c, nil
}

// versions is the latest version of each imported course, by roadmap id.
func (s *Service) versions(ctx context.Context) (map[string]int32, error) {
	rows, err := roadmapdb.New(s.pool).LatestVersions(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]int32, len(rows))
	for _, r := range rows {
		out[r.Subject+"-"+r.Level] = r.Version
	}
	return out, nil
}

// splitID splits a roadmap id into its subject and level.
func splitID(id string) (subject, level string, ok bool) {
	subject, level, ok = strings.Cut(id, "-")
	return subject, level, ok && slices.Contains(Subjects, subject) && slices.Contains(Levels, level)
}

// countryCodes maps the curriculum's country ids to ISO 3166-1 alpha-2
// codes (lower case), for flags.
var countryCodes = map[string]string{
	"uzbekistan": "uz", "south-korea": "kr", "japan": "jp", "singapore": "sg", "turkey": "tr",
	"germany": "de", "switzerland": "ch", "united-kingdom": "gb", "canada": "ca",
	"united-states": "us", "usa": "us",
}

// Coverage is how well the bank covers a course's assessments.
type Coverage struct {
	ID      string        `json:"id"`
	Version int32         `json:"version"`
	Sets    []CoverageSet `json:"sets"`
	Unknown []string      `json:"unknown_skills"` // skills with no approved question at all
	Short   int           `json:"short"`          // sets that can't be filled at their difficulties
}

// CoverageSet is one level check or homework set.
type CoverageSet struct {
	LessonID string `json:"lesson_id"`
	Set      string `json:"set"` // "level check", "homework 1", …
	// Need is per difficulty: wanted and approved questions of the skills.
	Need []Need `json:"need"`
	// Any is the approved questions of the skills at any difficulty.
	Any   int  `json:"any"`
	Short bool `json:"short"`
}

// Need is a difficulty a set asks for.
type Need struct {
	Difficulty string `json:"difficulty"`
	Want       int    `json:"want"`
	Have       int    `json:"have"`
}

// Coverage reports, for every imported course (or those named), how many
// approved questions there are for each set's skills and difficulties.
func (s *Service) Coverage(ctx context.Context, ids ...string) ([]Coverage, error) {
	versions, err := s.versions(ctx)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		for id := range versions {
			ids = append(ids, id)
		}
		slices.SortFunc(ids, func(a, b string) int { return courseOrder(a) - courseOrder(b) })
	}
	counts := map[string]map[string]map[string]int{}
	var out []Coverage
	for _, id := range ids {
		c, err := s.course(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", id, err)
		}
		if counts[c.Subject] == nil {
			if counts[c.Subject], err = s.bank.ApprovedCounts(ctx, c.Subject); err != nil {
				return nil, err
			}
		}
		out = append(out, coverageOf(c, versions[id], counts[c.Subject]))
	}
	return out, nil
}

func coverageOf(c *Course, version int32, counts map[string]map[string]int) Coverage {
	cov := Coverage{ID: c.ID(), Version: version, Unknown: []string{}}
	unknown := map[string]bool{}
	set := func(lesson, name string, named []string, want map[string]int) {
		cs := CoverageSet{LessonID: lesson, Set: name}
		skills := resolveSkills(c.Subject, named)
		for _, sk := range skills {
			for _, n := range counts[sk] {
				cs.Any += n
			}
		}
		for _, sk := range named {
			found := false
			for _, r := range resolveSkills(c.Subject, []string{sk}) {
				found = found || len(counts[r]) > 0
			}
			if !found && !unknown[sk] {
				unknown[sk] = true
				cov.Unknown = append(cov.Unknown, sk)
			}
		}
		for _, d := range Difficulties {
			if want[d] == 0 {
				continue
			}
			n := Need{Difficulty: d, Want: want[d]}
			for _, sk := range skills {
				n.Have += counts[sk][d]
			}
			if n.Have < n.Want {
				cs.Short = true
			}
			cs.Need = append(cs.Need, n)
		}
		if cs.Short {
			cov.Short++
		}
		cov.Sets = append(cov.Sets, cs)
	}
	for _, pl := range c.lessons() {
		l := pl.lesson
		set(l.ID, "level check", l.LevelCheck.Skills, l.LevelCheck.Mix.Counts())
		for i, h := range l.Homework {
			set(l.ID, fmt.Sprintf("homework %d", i+1), h.Skills, map[string]int{h.Difficulty: h.Questions})
		}
	}
	return cov
}

// courseOrder sorts courses math before english, then by level.
func courseOrder(id string) int {
	subject, level, _ := splitID(id)
	return slices.Index(Subjects, subject)*10 + slices.Index(Levels, level)
}

// resolveSkills turns a set's skill names into the bank's (lower case, the
// way Candidates and ApprovedCounts compare them): a name matches the
// digital SAT's skill of the same name in any case, and a name the bank
// splits ("Command of Evidence") stands for each of its parts ("Command of
// Evidence (Textual)", "… (Quantitative)"). Other names stay as they are.
func resolveSkills(subject string, skills []string) []string {
	var out []string
	add := func(s string) {
		if s = strings.ToLower(s); !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	for _, sk := range skills {
		name := strings.ToLower(strings.TrimSpace(sk))
		var exact string
		var parts []string
		for _, names := range practice.Taxonomy[subject] {
			for _, n := range names {
				switch l := strings.ToLower(n); {
				case l == name:
					exact = n
				case strings.HasPrefix(l, name+" ("):
					parts = append(parts, n)
				}
			}
		}
		switch {
		case exact != "":
			add(exact)
		case len(parts) > 0:
			slices.Sort(parts)
			for _, p := range parts {
				add(p)
			}
		default:
			add(name)
		}
	}
	return out
}
