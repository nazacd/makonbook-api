package roadmap

import (
	"context"
	"errors"
	"slices"

	"github.com/nazacd/makonbook-api/internal/roadmap/roadmapdb"
)

// What other features read of the curriculum and of students' progress:
// assignments (spec 23) and a group's roadmap for its teachers (23.6).

// Course returns the live version of a course ("math-pre-sat").
// ErrNotFound if it was never imported.
func (s *Service) Course(ctx context.Context, id string) (*Course, error) {
	return s.course(ctx, id)
}

// Courses returns the live version of every imported course, math first,
// then by level.
func (s *Service) Courses(ctx context.Context) ([]*Course, error) {
	versions, err := s.versions(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(versions))
	for id := range versions {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b string) int { return courseOrder(a) - courseOrder(b) })
	out := make([]*Course, 0, len(ids))
	for _, id := range ids {
		c, err := s.course(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// RosterStudent is a student of a group, as its teachers see them.
type RosterStudent struct {
	ClientID  int32
	FirstName string
	LastName  string
}

// GroupRoadmap is a group's course(s) and each student's standing in them.
type GroupRoadmap struct {
	Courses  []GroupCourseView    `json:"courses"`
	Students []GroupStudentStatus `json:"students"`
}

// GroupCourseView is a course with every lesson in order.
type GroupCourseView struct {
	ID      string            `json:"id"`
	Subject string            `json:"subject"`
	Level   string            `json:"level"`
	Title   string            `json:"title"`
	Lessons []GroupLessonView `json:"lessons"`
}

// GroupLessonView is a lesson of a course, with its unit.
type GroupLessonView struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	UnitID       string `json:"unit_id"`
	UnitTitle    string `json:"unit_title"`
	University   string `json:"university"`
	HomeworkSets int    `json:"homework_sets"`
}

// GroupStudentStatus is a student and their standing per course.
type GroupStudentStatus struct {
	ClientID  int32                        `json:"client_id"`
	FirstName string                       `json:"first_name"`
	LastName  string                       `json:"last_name"`
	Courses   map[string]CourseProgressRow `json:"courses"`
}

// CourseProgressRow is how far a student is in a course; Lessons are in
// the course's order.
type CourseProgressRow struct {
	LessonsDone     int              `json:"lessons_done"`
	LessonsTotal    int              `json:"lessons_total"`
	CurrentLessonID *string          `json:"current_lesson_id"`
	Lessons         []LessonProgress `json:"lessons"`
}

// LessonProgress is a student's standing in one lesson.
type LessonProgress struct {
	Status       string `json:"status"`
	Passed       bool   `json:"passed"`
	Best         *int   `json:"best"`
	HomeworkDone int    `json:"homework_done"`
}

// GroupProgress lays out a group's course (from its discipline, or its
// HolliHop level, as GroupCourse) for each of its students as their own
// roadmap does. A group that maps to no imported course has no courses.
func (s *Service) GroupProgress(ctx context.Context, discipline, level string, roster []RosterStudent) (GroupRoadmap, error) {
	out := GroupRoadmap{Courses: []GroupCourseView{}, Students: make([]GroupStudentStatus, 0, len(roster))}
	var courses []*Course
	if id, ok := GroupCourse(discipline, level); ok {
		c, err := s.course(ctx, id)
		switch {
		case err == nil:
			courses = append(courses, c)
		case !errors.Is(err, ErrNotFound):
			return GroupRoadmap{}, err
		}
	}
	for _, c := range courses {
		cv := GroupCourseView{ID: c.ID(), Subject: c.Subject, Level: c.Level, Title: c.Title(), Lessons: []GroupLessonView{}}
		for _, pl := range c.lessons() {
			cv.Lessons = append(cv.Lessons, GroupLessonView{
				ID: pl.lesson.ID, Title: pl.lesson.Title, UnitID: pl.unit.ID, UnitTitle: pl.unit.Title,
				University: pl.unit.Stop.University, HomeworkSets: len(pl.lesson.Homework),
			})
		}
		out.Courses = append(out.Courses, cv)
	}
	ids := make([]int32, len(roster))
	for i, st := range roster {
		ids[i] = st.ClientID
	}
	progresses := map[int32]progress{}
	if len(courses) > 0 && len(ids) > 0 {
		rows, err := roadmapdb.New(s.pool).ProgressOf(ctx, ids)
		if err != nil {
			return GroupRoadmap{}, err
		}
		for _, r := range rows {
			p, ok := progresses[r.StudentClientID]
			if !ok {
				p = newProgress()
				progresses[r.StudentClientID] = p
			}
			p.add(r.LessonID, r.Kind, int(r.HomeworkIndex), int(r.Best))
		}
	}
	for _, st := range roster {
		row := GroupStudentStatus{ClientID: st.ClientID, FirstName: st.FirstName, LastName: st.LastName, Courses: map[string]CourseProgressRow{}}
		p, ok := progresses[st.ClientID]
		if !ok {
			p = newProgress()
		}
		for _, c := range courses {
			l := lay(c, false, p)
			cp := CourseProgressRow{Lessons: []LessonProgress{}}
			for _, pl := range c.lessons() {
				ls := l.lessons[pl.lesson.ID]
				lp := LessonProgress{Status: ls.status, Passed: p.passed(pl.lesson), HomeworkDone: p.homeworkDone(pl.lesson)}
				if best, ok := p.best[pl.lesson.ID]; ok {
					lp.Best = &best
				}
				cp.LessonsTotal++
				if ls.status == StatusDone {
					cp.LessonsDone++
				} else if cp.CurrentLessonID == nil {
					cp.CurrentLessonID = &pl.lesson.ID
				}
				cp.Lessons = append(cp.Lessons, lp)
			}
			row.Courses[c.ID()] = cp
		}
		out.Students = append(out.Students, row)
	}
	return out, nil
}
