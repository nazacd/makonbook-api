package assignments

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nazacd/makonbook-api/internal/assignments/assignmentsdb"
	"github.com/nazacd/makonbook-api/internal/practice"
	"github.com/nazacd/makonbook-api/internal/roadmap"
)

// What teachers build assignments from (spec 23.2): drawn sets, the bank's
// questions, the taxonomy and the curriculum; and a group's roadmap (23.6).

// DrawInput asks for a set: a lesson's level check or homework set, custom
// skills and a mix, or given questions.
type DrawInput struct {
	GroupID       *int32       `json:"group_id"`
	Course        string       `json:"course"`
	LessonID      string       `json:"lesson_id"`
	HomeworkIndex *int         `json:"homework_index"`
	Subject       string       `json:"subject"`
	Skills        []string     `json:"skills"`
	Mix           *roadmap.Mix `json:"mix"`
	QuestionIDs   []string     `json:"question_ids"`
}

// DrawResult is a set, not saved.
type DrawResult struct {
	Subject string                        `json:"subject"`
	Skills  []string                      `json:"skills"`
	Mix     *roadmap.Mix                  `json:"mix"` // null for a set given by ids
	Items   []Question                    `json:"items"`
	Images  map[string]practice.ImageLink `json:"images"`
}

// Draw draws a set (with a group: questions none of its students has had
// first), or returns the questions given, in order, whole.
func (s *Service) Draw(ctx context.Context, teacher int32, in DrawInput, day time.Time) (DrawResult, error) {
	out := DrawResult{Skills: []string{}}
	var lesson *roadmap.Lesson
	subject := in.Subject
	if in.Course != "" || in.LessonID != "" {
		if in.Course == "" || in.LessonID == "" {
			return out, invalid("lesson_id", "name both the course and the lesson")
		}
		c, err := s.roadmap.Course(ctx, in.Course)
		if err != nil {
			return out, err // ErrNotFound: 404
		}
		_, l, ok := c.Lesson(in.LessonID)
		if !ok {
			return out, roadmap.ErrNotFound
		}
		lesson, subject = l, c.Subject
	}
	if !slices.Contains(roadmap.Subjects, subject) {
		return out, invalid("subject", "name a lesson (course and lesson_id), or a subject: math or english")
	}
	out.Subject = subject
	if len(in.QuestionIDs) > 0 {
		ids, msg, err := s.checkQuestions(ctx, in.QuestionIDs, subject)
		if err != nil {
			return out, err
		}
		if msg != "" {
			return out, invalid("question_ids", msg)
		}
		if lesson != nil {
			out.Skills = lesson.Skills
		} else if in.Skills != nil {
			out.Skills, _ = cleanSkills(in.Skills)
		}
		out.Items, out.Images, err = s.questions(ctx, idStrings(ids))
		return out, err
	}
	var want map[string]int
	switch {
	case lesson != nil && in.HomeworkIndex != nil:
		if *in.HomeworkIndex < 0 || *in.HomeworkIndex >= len(lesson.Homework) {
			return out, roadmap.ErrNotFound
		}
		h := lesson.Homework[*in.HomeworkIndex]
		mix := mixOf(h.Difficulty, h.Questions)
		out.Skills, out.Mix, want = h.Skills, &mix, mix.Counts()
	case lesson != nil:
		mix := lesson.LevelCheck.Mix
		out.Skills, out.Mix, want = lesson.LevelCheck.Skills, &mix, mix.Counts()
	default:
		skills, msg := cleanSkills(in.Skills)
		switch {
		case msg != "":
			return out, invalid("skills", msg)
		case len(skills) == 0:
			return out, invalid("skills", "name a lesson, skills and a mix, or questions")
		case in.Mix == nil:
			return out, invalid("mix", "say how many questions of each difficulty")
		}
		if msg := checkMix(*in.Mix, true); msg != "" {
			return out, invalid("mix", msg)
		}
		mix := *in.Mix
		out.Skills, out.Mix, want = skills, &mix, mix.Counts()
	}
	seen := map[string]bool{}
	if in.GroupID != nil {
		var err error
		if seen, err = s.groupSeen(ctx, teacher, *in.GroupID, day); err != nil {
			return out, err
		}
	}
	ids, err := s.roadmap.Draw(ctx, subject, out.Skills, want, seen)
	if err != nil {
		return out, err
	}
	out.Items, out.Images, err = s.questions(ctx, ids)
	return out, err
}

// groupSeen is the questions a group's current students have had in any
// roadmap assessment, and those the group was given.
func (s *Service) groupSeen(ctx context.Context, teacher, group int32, day time.Time) (map[string]bool, error) {
	if err := s.teaches(ctx, teacher, group, day); err != nil {
		return nil, err
	}
	q := assignmentsdb.New(s.pool)
	roster, err := q.Rosters(ctx, assignmentsdb.RostersParams{GroupIds: []int32{group}, Today: date(day)})
	if err != nil {
		return nil, err
	}
	students := make([]int32, len(roster))
	for i, r := range roster {
		students[i] = r.ClientID
	}
	seen := map[string]bool{}
	had, err := q.SeenByStudents(ctx, students)
	if err != nil {
		return nil, err
	}
	given, err := q.GroupQuestions(ctx, group)
	if err != nil {
		return nil, err
	}
	for _, id := range append(had, given...) {
		seen[uuidString(id)] = true
	}
	return seen, nil
}

// Questions lists approved questions to swap into a set.
func (s *Service) Questions(ctx context.Context, f practice.QuestionFilter) ([]practice.QuestionPreview, int, error) {
	return s.practice.ApprovedQuestions(ctx, f)
}

// domainOrder is the digital SAT's order of domains.
var domainOrder = map[string][]string{
	"english": {"Information and Ideas", "Craft and Structure", "Expression of Ideas", "Standard English Conventions"},
	"math":    {"Algebra", "Advanced Math", "Problem-Solving and Data Analysis", "Geometry and Trigonometry"},
}

// TaxonomyDomain is a domain's skills with their approved questions.
type TaxonomyDomain struct {
	Domain string          `json:"domain"`
	Skills []TaxonomySkill `json:"skills"`
}

// TaxonomySkill is a skill and its approved questions per difficulty.
type TaxonomySkill struct {
	Skill  string `json:"skill"`
	Easy   int    `json:"easy"`
	Medium int    `json:"medium"`
	Hard   int    `json:"hard"`
	Total  int    `json:"total"` // unestimated ones included
}

// Taxonomy is each subject's domains and skills (practice.Taxonomy) with
// their approved questions.
func (s *Service) Taxonomy(ctx context.Context) (map[string][]TaxonomyDomain, error) {
	out := map[string][]TaxonomyDomain{}
	for _, subject := range roadmap.Subjects {
		counts, err := s.practice.ApprovedCounts(ctx, subject)
		if err != nil {
			return nil, err
		}
		domains := slices.Clone(domainOrder[subject])
		var others []string
		for d := range practice.Taxonomy[subject] {
			if !slices.Contains(domains, d) {
				others = append(others, d)
			}
		}
		slices.Sort(others)
		list := []TaxonomyDomain{}
		for _, d := range append(domains, others...) {
			names, ok := practice.Taxonomy[subject][d]
			if !ok {
				continue
			}
			td := TaxonomyDomain{Domain: d, Skills: []TaxonomySkill{}}
			for _, sk := range names {
				c := counts[strings.ToLower(sk)]
				ts := TaxonomySkill{Skill: sk, Easy: c["easy"], Medium: c["medium"], Hard: c["hard"]}
				for _, n := range c {
					ts.Total += n
				}
				td.Skills = append(td.Skills, ts)
			}
			list = append(list, td)
		}
		out[subject] = list
	}
	return out, nil
}

// CurriculumCourse is a course with its lessons' set specs.
type CurriculumCourse struct {
	ID      string           `json:"id"`
	Subject string           `json:"subject"`
	Level   string           `json:"level"`
	Title   string           `json:"title"`
	Units   []CurriculumUnit `json:"units"`
}

// CurriculumUnit is a unit and its lessons.
type CurriculumUnit struct {
	ID         string             `json:"id"`
	Title      string             `json:"title"`
	University string             `json:"university"`
	Lessons    []CurriculumLesson `json:"lessons"`
}

// CurriculumLesson is a lesson's level check and homework specs.
type CurriculumLesson struct {
	ID         string               `json:"id"`
	Title      string               `json:"title"`
	Skills     []string             `json:"skills"`
	LevelCheck roadmap.LevelCheck   `json:"level_check"`
	Homework   []CurriculumHomework `json:"homework"`
}

// CurriculumHomework is a homework set's spec.
type CurriculumHomework struct {
	Index      int      `json:"index"`
	Title      string   `json:"title"`
	Difficulty string   `json:"difficulty"`
	Questions  int      `json:"questions"`
	Skills     []string `json:"skills"`
}

// Curriculum lists every imported course, math first, with every lesson's
// level check and homework specs.
func (s *Service) Curriculum(ctx context.Context) ([]CurriculumCourse, error) {
	courses, err := s.roadmap.Courses(ctx)
	if err != nil {
		return nil, err
	}
	out := []CurriculumCourse{}
	for _, c := range courses {
		cc := CurriculumCourse{ID: c.ID(), Subject: c.Subject, Level: c.Level, Title: c.Title(), Units: []CurriculumUnit{}}
		for _, u := range c.Units {
			cu := CurriculumUnit{ID: u.ID, Title: u.Title, University: u.Stop.University, Lessons: []CurriculumLesson{}}
			for _, l := range u.Lessons {
				cl := CurriculumLesson{ID: l.ID, Title: l.Title, Skills: orEmpty(l.Skills), LevelCheck: l.LevelCheck, Homework: []CurriculumHomework{}}
				cl.LevelCheck.Skills = orEmpty(cl.LevelCheck.Skills)
				for i, h := range l.Homework {
					cl.Homework = append(cl.Homework, CurriculumHomework{Index: i, Title: h.Title, Difficulty: h.Difficulty, Questions: h.Questions, Skills: orEmpty(h.Skills)})
				}
				cu.Lessons = append(cu.Lessons, cl)
			}
			cc.Units = append(cc.Units, cu)
		}
		out = append(out, cc)
	}
	return out, nil
}

// GroupRoadmap is the group's course(s) and each current student's standing,
// for its teachers.
func (s *Service) GroupRoadmap(ctx context.Context, teacher, group int32, day time.Time) (roadmap.GroupRoadmap, error) {
	if err := s.teaches(ctx, teacher, group, day); err != nil {
		return roadmap.GroupRoadmap{}, err
	}
	q := assignmentsdb.New(s.pool)
	g, err := q.GetGroup(ctx, group)
	if errors.Is(err, pgx.ErrNoRows) {
		return roadmap.GroupRoadmap{}, ErrForbidden
	}
	if err != nil {
		return roadmap.GroupRoadmap{}, err
	}
	rows, err := q.Rosters(ctx, assignmentsdb.RostersParams{GroupIds: []int32{group}, Today: date(day)})
	if err != nil {
		return roadmap.GroupRoadmap{}, err
	}
	roster := make([]roadmap.RosterStudent, len(rows))
	for i, r := range rows {
		roster[i] = roadmap.RosterStudent{ClientID: r.ClientID, FirstName: r.FirstName, LastName: r.LastName}
	}
	out, err := s.roadmap.GroupProgress(ctx, g.Discipline, g.Level, roster)
	if err != nil {
		return out, fmt.Errorf("group %d: %w", group, err)
	}
	return out, nil
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
