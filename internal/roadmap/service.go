package roadmap

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/practice"
	"github.com/nazacd/makonbook-api/internal/roadmap/roadmapdb"
)

// PassPercent is the level check score that passes a lesson.
const PassPercent = 70

var (
	// ErrNotFound means no such roadmap, lesson or assessment for this person.
	ErrNotFound = errors.New("not found")
	// ErrLocked means the lesson opens once the one before it is done.
	ErrLocked = errors.New("this lesson opens once the one before it is done")
	// ErrEnrolledOnly means the lesson is past what a free account opens.
	ErrEnrolledOnly = errors.New("this part of the roadmap is for SAT MAKON's students")
	// ErrNoQuestions means the bank has no approved question for the set.
	ErrNoQuestions = errors.New("there are no questions for this yet")
	// ErrFinished means the assessment was submitted already.
	ErrFinished = errors.New("this was submitted already")
	// ErrNotNeeded means onboarding is only for registered students.
	ErrNotNeeded = errors.New("onboarding is only for students who signed up themselves")
)

// Bank is what roadmaps read from the question bank (practice).
type Bank interface {
	Candidates(ctx context.Context, subject string, skills []string) ([]practice.Candidate, error)
	ApprovedCounts(ctx context.Context, subject string) (map[string]map[string]int, error)
	BankQuestions(ctx context.Context, ids []string) (map[string]practice.PackageQuestion, error)
	FigureLinks(ctx context.Context, keys []string) (map[string]practice.ImageLink, error)
	SetGoals(ctx context.Context, student int32, in practice.GoalsInput, today time.Time) (practice.Goals, error)
}

// Groups lists a person's current groups (people.Directory).
type Groups interface {
	Groups(ctx context.Context, kind string, id int32, today time.Time) ([]people.GroupInfo, error)
}

// Service is roadmaps: the curriculum, who follows which course, progress,
// onboarding and assessments.
type Service struct {
	pool   *pgxpool.Pool
	bank   Bank
	groups Groups
	mu     sync.Mutex
	cache  map[string]cachedCourse
}

type cachedCourse struct {
	version int32
	course  *Course
}

// NewService builds the service.
func NewService(pool *pgxpool.Pool, bank Bank, groups Groups) *Service {
	return &Service{pool: pool, bank: bank, groups: groups, cache: map[string]cachedCourse{}}
}

// Who is the person asking.
type Who struct {
	Kind       string // people.KindStudent or people.KindTeacher
	ID         int32
	Registered bool // a student who signed up themselves (a free account)
}

// Sources of a roadmap.
const (
	SourceGroup      = "group"
	SourceOnboarding = "onboarding"
)

// Lesson and unit states, and why something is locked.
const (
	StatusDone   = "done"
	StatusOpen   = "open"
	StatusLocked = "locked"

	LockPrevious     = "previous"
	LockEnrolledOnly = "enrolled_only"
)

// assignment is a course a person follows and why.
type assignment struct {
	course *Course
	source string
	group  *string
}

// assigned lists the person's roadmaps, math first, then by level: from
// their groups for enrolled students, from onboarding for registered ones,
// none for teachers. A course that was never imported is left out.
func (s *Service) assigned(ctx context.Context, who Who, today time.Time) ([]assignment, error) {
	if who.Kind != people.KindStudent {
		return nil, nil
	}
	type pick struct {
		id, source string
		group      *string
	}
	var picks []pick
	if who.Registered {
		ids, err := roadmapdb.New(s.pool).Choices(ctx, who.ID)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			picks = append(picks, pick{id: id, source: SourceOnboarding})
		}
	} else {
		groups, err := s.groups.Groups(ctx, who.Kind, who.ID, today)
		if err != nil {
			return nil, err
		}
		for _, g := range groups {
			if id, ok := GroupCourse(g.Discipline, g.Level); ok {
				picks = append(picks, pick{id: id, source: SourceGroup, group: &g.Name})
			}
		}
	}
	var out []assignment
	seen := map[string]bool{}
	for _, p := range picks {
		if seen[p.id] {
			continue
		}
		seen[p.id] = true
		c, err := s.course(ctx, p.id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, assignment{course: c, source: p.source, group: p.group})
	}
	slices.SortStableFunc(out, func(a, b assignment) int { return courseOrder(a.course.ID()) - courseOrder(b.course.ID()) })
	return out, nil
}

// DisciplineCourse is the course a group's HolliHop discipline teaches
// ("PRE SAT MATH" → "math-pre-sat"): the subject from MATH or ENGLISH (ENG),
// the level from FOUNDATION, PRE SAT (PRE-SAT, PRESAT) or ADV (ADVANCED).
// A discipline without both gives none.
func DisciplineCourse(discipline string) (string, bool) {
	subject, level := courseWords(discipline)
	if subject == "" || level == "" {
		return "", false
	}
	return subject + "-" + level, true
}

// GroupCourse is the course a group teaches: its discipline's, or when the
// discipline names the subject but no level ("SAT MATH"), the subject with
// HolliHop's group level read the same way (D38). Without a subject, or
// still without a level, none.
func GroupCourse(discipline, level string) (string, bool) {
	if id, ok := DisciplineCourse(discipline); ok {
		return id, true
	}
	subject, _ := courseWords(discipline)
	_, lvl := courseWords(level)
	if subject == "" || lvl == "" {
		return "", false
	}
	return subject + "-" + lvl, true
}

// courseWords reads a subject and a level from HolliHop's words.
func courseWords(s string) (subject, level string) {
	words := strings.FieldsFunc(strings.ToUpper(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	for i, w := range words {
		switch w {
		case "MATH", "MATHS", "MATHEMATICS":
			subject = "math"
		case "ENGLISH", "ENG":
			subject = "english"
		case "FOUNDATION":
			level = "foundation"
		case "PRESAT":
			level = "pre-sat"
		case "PRE":
			if i+1 < len(words) && words[i+1] == "SAT" {
				level = "pre-sat"
			}
		case "ADV", "ADVANCED":
			level = "advanced"
		}
	}
	return subject, level
}

// progress is what a person has finished, by lesson id (so it holds
// across roadmaps and curriculum versions).
type progress struct {
	best     map[string]int          // lesson → best level check percent
	homework map[string]map[int]bool // lesson → finished sets
}

func newProgress() progress {
	return progress{best: map[string]int{}, homework: map[string]map[int]bool{}}
}

// add counts a finished level check or homework set's best.
func (p progress) add(lesson, kind string, index, best int) {
	switch kind {
	case kindLevelCheck:
		p.best[lesson] = best
	case kindHomework:
		if p.homework[lesson] == nil {
			p.homework[lesson] = map[int]bool{}
		}
		p.homework[lesson][index] = true
	}
}

func (s *Service) progress(ctx context.Context, student int32) (progress, error) {
	rows, err := roadmapdb.New(s.pool).Progress(ctx, student)
	if err != nil {
		return progress{}, err
	}
	p := newProgress()
	for _, r := range rows {
		p.add(r.LessonID, r.Kind, int(r.HomeworkIndex), int(r.Best))
	}
	return p, nil
}

// passed: the lesson's level check was passed.
func (p progress) passed(l *Lesson) bool {
	best, ok := p.best[l.ID]
	return ok && best >= PassPercent
}

// homeworkDone counts the lesson's finished homework sets.
func (p progress) homeworkDone(l *Lesson) int {
	n := 0
	for i := range l.Homework {
		if p.homework[l.ID][i] {
			n++
		}
	}
	return n
}

// done: the level check is passed and every homework set finished.
func (p progress) done(l *Lesson) bool {
	return p.passed(l) && p.homeworkDone(l) == len(l.Homework)
}

// layout is a course's units and lessons with their states for a person.
type layout struct {
	units     []unitState
	lessons   map[string]lessonState
	freeUnits *int
}

type unitState struct {
	unit   *Unit
	status string
	lock   *string
}

type lessonState struct {
	unit   *Unit
	lesson *Lesson
	status string
	lock   *string
}

// lay works out what is done, open and locked. The first lesson is open;
// each later one opens once the lesson before it (in course order) is done.
// A free account sees only the first country's units; the rest are locked
// whatever the progress.
func lay(c *Course, free bool, p progress) layout {
	l := layout{lessons: map[string]lessonState{}}
	open := map[string]bool{}
	if free && len(c.Countries) > 0 {
		n := len(c.Countries[0].Units)
		l.freeUnits = &n
		for _, id := range c.Countries[0].Units {
			open[id] = true
		}
	}
	prevDone := true
	for ui := range c.Units {
		u := &c.Units[ui]
		us := unitState{unit: u}
		if free && !open[u.ID] {
			us.status, us.lock = StatusLocked, ptr(LockEnrolledOnly)
		}
		allDone, anyOpen := true, false
		for li := range u.Lessons {
			ls := lessonState{unit: u, lesson: &u.Lessons[li]}
			done := p.done(ls.lesson)
			switch {
			case us.lock != nil:
				ls.status, ls.lock = StatusLocked, ptr(LockEnrolledOnly)
			case done:
				ls.status = StatusDone
			case prevDone:
				ls.status = StatusOpen
			default:
				ls.status, ls.lock = StatusLocked, ptr(LockPrevious)
			}
			prevDone = done
			allDone = allDone && ls.status == StatusDone
			anyOpen = anyOpen || ls.status != StatusLocked
			l.lessons[ls.lesson.ID] = ls
		}
		if us.lock == nil {
			switch {
			case allDone:
				us.status = StatusDone
			case anyOpen:
				us.status = StatusOpen
			default:
				us.status, us.lock = StatusLocked, ptr(LockPrevious)
			}
		}
		l.units = append(l.units, us)
	}
	return l
}

// Summary is a roadmap at a glance.
type Summary struct {
	ID           string   `json:"id"`
	Subject      string   `json:"subject"`
	Level        string   `json:"level"`
	Title        string   `json:"title"`
	Source       string   `json:"source"`
	GroupName    *string  `json:"group_name"`
	LessonsDone  int      `json:"lessons_done"`
	LessonsTotal int      `json:"lessons_total"`
	UnitsDone    int      `json:"units_done"`
	UnitsTotal   int      `json:"units_total"`
	Current      *Current `json:"current"`
	FreeUnits    *int     `json:"free_units"`
}

// Current is the first lesson not done yet.
type Current struct {
	UnitID      string `json:"unit_id"`
	UnitTitle   string `json:"unit_title"`
	LessonID    string `json:"lesson_id"`
	LessonTitle string `json:"lesson_title"`
	University  string `json:"university"`
	// LogoURL is the university's logo (/v1/logos/…, same origin), or null.
	LogoURL *string `json:"logo_url"`
}

var subjectNames = map[string]string{"math": "Math", "english": "English"}
var levelNames = map[string]string{"foundation": "Foundation", "pre-sat": "Pre-SAT", "advanced": "Advanced"}

func summaryOf(a assignment, l layout, logos map[string]string) Summary {
	c := a.course
	sum := Summary{
		ID: c.ID(), Subject: c.Subject, Level: c.Level, Title: subjectNames[c.Subject] + " · " + levelNames[c.Level],
		Source: a.source, GroupName: a.group, UnitsTotal: len(c.Units), FreeUnits: l.freeUnits,
	}
	for _, us := range l.units {
		if us.status == StatusDone {
			sum.UnitsDone++
		}
	}
	for _, pl := range c.lessons() {
		sum.LessonsTotal++
		ls := l.lessons[pl.lesson.ID]
		if ls.status == StatusDone {
			sum.LessonsDone++
		} else if sum.Current == nil {
			sum.Current = &Current{
				UnitID: pl.unit.ID, UnitTitle: pl.unit.Title, LessonID: pl.lesson.ID, LessonTitle: pl.lesson.Title,
				University: pl.unit.Stop.University, LogoURL: logoURL(logos, pl.unit.Stop.University),
			}
		}
	}
	return sum
}

// Roadmaps lists the person's roadmaps.
func (s *Service) Roadmaps(ctx context.Context, who Who, today time.Time) ([]Summary, error) {
	as, err := s.assigned(ctx, who, today)
	if err != nil {
		return nil, err
	}
	out := []Summary{}
	if len(as) == 0 {
		return out, nil
	}
	p, err := s.progress(ctx, who.ID)
	if err != nil {
		return nil, err
	}
	logos, err := s.logoHashes(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range as {
		out = append(out, summaryOf(a, lay(a.course, who.Registered, p), logos))
	}
	return out, nil
}

// Detail is a roadmap with its countries, units and lessons.
type Detail struct {
	Summary
	Countries []CountryView `json:"countries"`
	Units     []UnitView    `json:"units"`
}

// CountryView is a country of the route; Code is its ISO code (lower case)
// for a flag, null when unknown.
type CountryView struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Code  *string  `json:"code"`
	Units []string `json:"units"`
}

// UnitView is a unit and its lessons' states.
type UnitView struct {
	ID      string       `json:"id"`
	Title   string       `json:"title"`
	Domain  string       `json:"domain"`
	Summary string       `json:"summary"`
	Stop    StopView     `json:"stop"`
	Status  string       `json:"status"`
	Lock    *string      `json:"lock"`
	Lessons []LessonItem `json:"lessons"`
}

// StopView is a unit's stop with its university's logo (/v1/logos/…, same
// origin), or null.
type StopView struct {
	Stop
	LogoURL *string `json:"logo_url"`
}

// LessonItem is a lesson on the map.
type LessonItem struct {
	ID         string          `json:"id"`
	Title      string          `json:"title"`
	Minutes    int             `json:"minutes"`
	Status     string          `json:"status"`
	Lock       *string         `json:"lock"`
	LevelCheck LevelCheckBrief `json:"level_check"`
	Homework   HomeworkBrief   `json:"homework"`
}

// LevelCheckBrief is a lesson's level check on the map.
type LevelCheckBrief struct {
	Questions int  `json:"questions"`
	Best      *int `json:"best"` // percent; null before the first
	Passed    bool `json:"passed"`
}

// HomeworkBrief is how many of a lesson's homework sets are finished.
type HomeworkBrief struct {
	Sets int `json:"sets"`
	Done int `json:"done"`
}

// find is one of the person's roadmaps with their progress and its layout.
func (s *Service) find(ctx context.Context, who Who, id string, today time.Time) (assignment, progress, layout, error) {
	as, err := s.assigned(ctx, who, today)
	if err != nil {
		return assignment{}, progress{}, layout{}, err
	}
	i := slices.IndexFunc(as, func(a assignment) bool { return a.course.ID() == id })
	if i < 0 {
		return assignment{}, progress{}, layout{}, ErrNotFound
	}
	p, err := s.progress(ctx, who.ID)
	if err != nil {
		return assignment{}, progress{}, layout{}, err
	}
	return as[i], p, lay(as[i].course, who.Registered, p), nil
}

// Roadmap returns one of the person's roadmaps. ErrNotFound if it isn't one.
func (s *Service) Roadmap(ctx context.Context, who Who, id string, today time.Time) (Detail, error) {
	a, p, l, err := s.find(ctx, who, id, today)
	if err != nil {
		return Detail{}, err
	}
	c := a.course
	logos, err := s.logoHashes(ctx)
	if err != nil {
		return Detail{}, err
	}
	d := Detail{Summary: summaryOf(a, l, logos), Countries: []CountryView{}, Units: []UnitView{}}
	for _, co := range c.Countries {
		cv := CountryView{ID: co.ID, Name: co.Name, Units: co.Units}
		if code, ok := countryCodes[co.ID]; ok {
			cv.Code = &code
		}
		d.Countries = append(d.Countries, cv)
	}
	for _, us := range l.units {
		u := us.unit
		uv := UnitView{
			ID: u.ID, Title: u.Title, Domain: u.Domain, Summary: u.Summary, Stop: StopView{Stop: u.Stop, LogoURL: logoURL(logos, u.Stop.University)},
			Status: us.status, Lock: us.lock,
			Lessons: []LessonItem{},
		}
		for li := range u.Lessons {
			lesson := &u.Lessons[li]
			ls := l.lessons[lesson.ID]
			item := LessonItem{
				ID: lesson.ID, Title: lesson.Title, Minutes: lesson.Minutes, Status: ls.status, Lock: ls.lock,
				LevelCheck: LevelCheckBrief{Questions: lesson.LevelCheck.Questions, Passed: p.passed(lesson)},
				Homework:   HomeworkBrief{Sets: len(lesson.Homework), Done: p.homeworkDone(lesson)},
			}
			if best, ok := p.best[lesson.ID]; ok {
				item.LevelCheck.Best = &best
			}
			uv.Lessons = append(uv.Lessons, item)
		}
		d.Units = append(d.Units, uv)
	}
	return d, nil
}

// LessonDetail is a lesson's content and its assessments.
type LessonDetail struct {
	Lesson     LessonContent    `json:"lesson"`
	LevelCheck LevelCheckDetail `json:"level_check"`
	Homework   []HomeworkDetail `json:"homework"`
}

// LessonContent is what a student studies.
type LessonContent struct {
	ID             string    `json:"id"`
	Title          string    `json:"title"`
	UnitID         string    `json:"unit_id"`
	Minutes        int       `json:"minutes"`
	Skills         []string  `json:"skills"`
	Objectives     []string  `json:"objectives"`
	Notes          string    `json:"notes"`
	Examples       []Example `json:"examples"`
	CommonMistakes []string  `json:"common_mistakes"`
	Status         string    `json:"status"`
	Lock           *string   `json:"lock"`
}

// LevelCheckDetail is a lesson's level check and the person's tries.
type LevelCheckDetail struct {
	Questions   int          `json:"questions"`
	Mix         Mix          `json:"mix"`
	PassPercent int          `json:"pass_percent"`
	Best        *int         `json:"best"`
	Passed      bool         `json:"passed"`
	Attempts    []AttemptRef `json:"attempts"` // finished, newest first
	Open        *OpenRef     `json:"open"`     // the one in progress
}

// HomeworkDetail is a homework set and the person's latest finished try.
type HomeworkDetail struct {
	Index      int         `json:"index"`
	Title      string      `json:"title"`
	Difficulty string      `json:"difficulty"`
	Questions  int         `json:"questions"`
	Done       bool        `json:"done"`
	Attempt    *AttemptRef `json:"attempt"`
	Open       *OpenRef    `json:"open"`
}

// AttemptRef is a finished assessment.
type AttemptRef struct {
	ID         string    `json:"id"`
	Percent    int       `json:"percent"`
	FinishedAt time.Time `json:"finished_at"`
}

// OpenRef is an assessment in progress.
type OpenRef struct {
	ID string `json:"id"`
}

// lessonIn finds an open (or done) lesson of one of the person's roadmaps:
// ErrNotFound, ErrLocked or ErrEnrolledOnly otherwise.
func (s *Service) lessonIn(ctx context.Context, who Who, roadmapID, lessonID string, today time.Time) (assignment, progress, lessonState, error) {
	a, p, l, err := s.find(ctx, who, roadmapID, today)
	if err != nil {
		return a, p, lessonState{}, err
	}
	ls, ok := l.lessons[lessonID]
	switch {
	case !ok:
		return a, p, ls, ErrNotFound
	case ls.lock != nil && *ls.lock == LockEnrolledOnly:
		return a, p, ls, ErrEnrolledOnly
	case ls.lock != nil:
		return a, p, ls, ErrLocked
	}
	return a, p, ls, nil
}

// Lesson returns a lesson of one of the person's roadmaps. A locked lesson
// stays hidden: ErrLocked or ErrEnrolledOnly.
func (s *Service) Lesson(ctx context.Context, who Who, roadmapID, lessonID string, today time.Time) (LessonDetail, error) {
	_, p, ls, err := s.lessonIn(ctx, who, roadmapID, lessonID, today)
	if err != nil {
		return LessonDetail{}, err
	}
	l := ls.lesson
	d := LessonDetail{
		Lesson: LessonContent{
			ID: l.ID, Title: l.Title, UnitID: ls.unit.ID, Minutes: l.Minutes, Skills: orEmpty(l.Skills),
			Objectives: orEmpty(l.Objectives), Notes: l.Notes, Examples: l.Examples, CommonMistakes: orEmpty(l.CommonMistakes),
			Status: ls.status, Lock: ls.lock,
		},
		LevelCheck: LevelCheckDetail{
			Questions: l.LevelCheck.Questions, Mix: l.LevelCheck.Mix, PassPercent: PassPercent, Passed: p.passed(l),
			Attempts: []AttemptRef{},
		},
		Homework: []HomeworkDetail{},
	}
	if d.Lesson.Examples == nil {
		d.Lesson.Examples = []Example{}
	}
	if best, ok := p.best[l.ID]; ok {
		d.LevelCheck.Best = &best
	}
	rows, err := roadmapdb.New(s.pool).LessonAssessments(ctx, roadmapdb.LessonAssessmentsParams{Student: who.ID, LessonID: text(l.ID)})
	if err != nil {
		return LessonDetail{}, err
	}
	for i, h := range l.Homework {
		d.Homework = append(d.Homework, HomeworkDetail{
			Index: i, Title: h.Title, Difficulty: h.Difficulty, Questions: h.Questions, Done: p.homework[l.ID][i],
		})
	}
	for _, r := range rows { // oldest first
		id := uuidString(r.ID)
		switch {
		case r.Kind == kindLevelCheck && r.FinishedAt.Valid:
			d.LevelCheck.Attempts = append([]AttemptRef{{ID: id, Percent: int(r.Percent.Int16), FinishedAt: r.FinishedAt.Time.UTC()}},
				d.LevelCheck.Attempts...)
		case r.Kind == kindLevelCheck:
			if d.LevelCheck.Open == nil || r.AssignmentID.Valid { // an assigned one is the one Start continues
				d.LevelCheck.Open = &OpenRef{ID: id}
			}
		case r.Kind == kindHomework && r.HomeworkIndex.Valid && int(r.HomeworkIndex.Int16) < len(d.Homework):
			h := &d.Homework[r.HomeworkIndex.Int16]
			if r.FinishedAt.Valid {
				h.Attempt = &AttemptRef{ID: id, Percent: int(r.Percent.Int16), FinishedAt: r.FinishedAt.Time.UTC()}
			} else if h.Open == nil || r.AssignmentID.Valid {
				h.Open = &OpenRef{ID: id}
			}
		}
	}
	return d, nil
}

func ptr[T any](v T) *T { return &v }

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
