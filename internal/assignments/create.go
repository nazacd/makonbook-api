package assignments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nazacd/makonbook-api/internal/assignments/assignmentsdb"
	"github.com/nazacd/makonbook-api/internal/practice"
	"github.com/nazacd/makonbook-api/internal/roadmap"
)

// Giving, changing and deleting assignments (spec 23.1).

// Input is a new assignment (AssignmentInput in the contract).
type Input struct {
	GroupID       int32        `json:"group_id"`
	Kind          string       `json:"kind"`
	Title         string       `json:"title"`
	Instructions  string       `json:"instructions"`
	OpensAt       *time.Time   `json:"opens_at"`
	DueAt         *time.Time   `json:"due_at"`
	Course        string       `json:"course"`
	LessonID      string       `json:"lesson_id"`
	HomeworkIndex *int         `json:"homework_index"`
	Subject       string       `json:"subject"`
	Skills        []string     `json:"skills"`
	Mix           *roadmap.Mix `json:"mix"`
	QuestionIDs   []string     `json:"question_ids"`
	Test          string       `json:"test"`
	Sections      []string     `json:"sections"`
}

// kindWords name the kinds in notifications.
var kindWords = map[string]string{KindHomework: "homework", KindLevelCheck: "level check", KindExam: "exam"}

// kindLinks are the web app's tabs for each kind.
var kindLinks = map[string]string{KindHomework: "/homework", KindLevelCheck: "/level-checks", KindExam: "/exams"}

// Create gives one of the teacher's groups an assignment and notifies the
// students enrolled in it now.
func (s *Service) Create(ctx context.Context, teacher int32, in Input, day time.Time) (Detail, error) {
	if in.GroupID == 0 {
		return Detail{}, invalid("group_id", "choose a group")
	}
	if err := s.teaches(ctx, teacher, in.GroupID, day); err != nil {
		return Detail{}, err
	}
	p, err := s.check(ctx, in)
	if err != nil {
		return Detail{}, err
	}
	p.GroupID, p.TeacherID = in.GroupID, teacher
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Detail{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := assignmentsdb.New(tx)
	r, err := q.InsertAssignment(ctx, p)
	if err != nil {
		return Detail{}, err
	}
	group, err := q.GetGroup(ctx, r.GroupID)
	if err != nil {
		return Detail{}, err
	}
	roster, err := q.Rosters(ctx, assignmentsdb.RostersParams{GroupIds: []int32{r.GroupID}, Today: date(day)})
	if err != nil {
		return Detail{}, err
	}
	title := "New " + kindWords[r.Kind] + ": " + r.Title
	body := group.Name + " · Due " + r.DueAt.Time.In(s.location).Format("2 January, 15:04")
	if s.now().Before(r.OpensAt.Time) {
		body = group.Name + " · Opens " + r.OpensAt.Time.In(s.location).Format("2 January, 15:04") +
			" · Due " + r.DueAt.Time.In(s.location).Format("2 January, 15:04")
	}
	for _, st := range roster {
		err := q.InsertNotification(ctx, assignmentsdb.InsertNotificationParams{
			PersonKind: "student", PersonID: st.ClientID, Kind: "assignment", Title: title, Body: body,
			Link: pgtype.Text{String: kindLinks[r.Kind], Valid: true}, AssignmentID: r.ID,
		})
		if err != nil {
			return Detail{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Detail{}, err
	}
	return s.detail(ctx, r, day)
}

// check validates a new assignment and fills what it defaults to.
func (s *Service) check(ctx context.Context, in Input) (assignmentsdb.InsertAssignmentParams, error) {
	bad := map[string]string{}
	p := assignmentsdb.InsertAssignmentParams{
		Kind: in.Kind, Instructions: strings.TrimSpace(in.Instructions), Sections: []string{}, Skills: []string{},
		QuestionIds: []pgtype.UUID{},
	}
	title := strings.TrimSpace(in.Title)
	if utf8.RuneCountInString(title) > maxTitle {
		bad["title"] = fmt.Sprintf("at most %d characters", maxTitle)
	}
	if utf8.RuneCountInString(p.Instructions) > maxInstructions {
		bad["instructions"] = fmt.Sprintf("at most %d characters", maxInstructions)
	}
	opens := s.now()
	if in.OpensAt != nil {
		opens = *in.OpensAt
	}
	switch {
	case in.DueAt == nil:
		bad["due_at"] = "set a deadline"
	case !in.DueAt.After(opens):
		bad["due_at"] = "the deadline must be after the assignment opens"
	default:
		p.DueAt = timestamptz(*in.DueAt)
	}
	p.OpensAt = timestamptz(opens)
	defaultTitle := ""
	switch in.Kind {
	case KindLevelCheck, KindHomework:
		subject := in.Subject
		if in.LessonID != "" || in.Kind == KindLevelCheck {
			c, l, msgs, err := s.lessonFor(ctx, in.Course, in.LessonID)
			if err != nil {
				return p, err
			}
			for k, v := range msgs {
				bad[k] = v
			}
			if l != nil {
				subject = c.Subject
				p.Course, p.LessonID = text(c.ID()), text(l.ID)
				if in.Kind == KindLevelCheck {
					p.Skills = l.LevelCheck.Skills
					p.Mix, _ = json.Marshal(l.LevelCheck.Mix)
					defaultTitle = l.Title + " · Level check"
				} else {
					switch {
					case in.HomeworkIndex == nil:
						bad["homework_index"] = "choose one of the lesson's homework sets"
					case *in.HomeworkIndex < 0 || *in.HomeworkIndex >= len(l.Homework):
						bad["homework_index"] = fmt.Sprintf("the lesson has %d homework sets", len(l.Homework))
					default:
						h := l.Homework[*in.HomeworkIndex]
						p.HomeworkIndex = pgtype.Int2{Int16: int16(*in.HomeworkIndex), Valid: true} //nolint:gosec // a handful of sets
						p.Skills = h.Skills
						p.Mix, _ = json.Marshal(mixOf(h.Difficulty, h.Questions))
						defaultTitle = h.Title
					}
				}
			}
		} else {
			if !slices.Contains(roadmap.Subjects, subject) {
				bad["subject"] = "math or english"
			}
			skills, msg := cleanSkills(in.Skills)
			if msg != "" {
				bad["skills"] = msg
			}
			p.Skills = skills
			if in.Mix != nil {
				if msg := checkMix(*in.Mix, false); msg != "" {
					bad["mix"] = msg
				} else {
					p.Mix, _ = json.Marshal(in.Mix)
				}
			}
			defaultTitle = "Homework"
		}
		p.Subject = subject
		ids, msg, err := s.checkQuestions(ctx, in.QuestionIDs, subject)
		if err != nil {
			return p, err
		}
		if msg != "" {
			bad["question_ids"] = msg
		}
		p.QuestionIds = ids
	case KindExam:
		pkg, sections, msgs, err := s.examFor(ctx, in.Test, in.Sections)
		if err != nil {
			return p, err
		}
		for k, v := range msgs {
			bad[k] = v
		}
		if pkg != nil {
			p.TestCode = text(pkg.Code)
			defaultTitle = pkg.Title
		}
		if len(sections) > 0 {
			p.Sections = sections
			p.Subject = sections[0]
		}
	default:
		bad["kind"] = "homework, level_check or exam"
	}
	if title == "" {
		title = defaultTitle
	}
	if utf8.RuneCountInString(title) > maxTitle {
		title = string([]rune(title)[:maxTitle])
	}
	p.Title = title
	if len(bad) > 0 {
		return p, &InvalidError{Fields: bad}
	}
	return p, nil
}

// lessonFor finds a curriculum lesson; msgs say what is wrong with the
// course or lesson id.
func (s *Service) lessonFor(ctx context.Context, course, lessonID string) (*roadmap.Course, *roadmap.Lesson, map[string]string, error) {
	switch {
	case course == "":
		return nil, nil, map[string]string{"course": "choose a course"}, nil
	case lessonID == "":
		return nil, nil, map[string]string{"lesson_id": "choose a lesson"}, nil
	}
	c, err := s.roadmap.Course(ctx, course)
	if errors.Is(err, roadmap.ErrNotFound) {
		return nil, nil, map[string]string{"course": "no such course"}, nil
	}
	if err != nil {
		return nil, nil, nil, err
	}
	_, l, ok := c.Lesson(lessonID)
	if !ok {
		return nil, nil, map[string]string{"lesson_id": "no such lesson in " + course}, nil
	}
	return c, l, nil, nil
}

// checkQuestions checks a set: 1–60 approved questions of the subject, no
// repeats. msg says what is wrong.
func (s *Service) checkQuestions(ctx context.Context, ids []string, subject string) ([]pgtype.UUID, string, error) {
	switch {
	case len(ids) == 0:
		return nil, "add at least one question", nil
	case len(ids) > maxQuestions:
		return nil, fmt.Sprintf("at most %d questions", maxQuestions), nil
	}
	out := make([]pgtype.UUID, len(ids))
	seen := map[string]bool{}
	for i, id := range ids {
		u, ok := parseUUID(id)
		if !ok {
			return nil, fmt.Sprintf("%q is not a question id", id), nil
		}
		if seen[uuidString(u)] {
			return nil, "a question is in the set twice", nil
		}
		seen[uuidString(u)] = true
		out[i] = u
	}
	rows, err := assignmentsdb.New(s.pool).ApprovedSubjects(ctx, out)
	if err != nil {
		return nil, "", err
	}
	subjects := map[string]string{}
	for _, r := range rows {
		subjects[uuidString(r.ID)] = r.Subject
	}
	for _, u := range out {
		id := uuidString(u)
		switch sub, ok := subjects[id]; {
		case !ok:
			return nil, fmt.Sprintf("question %s isn't an approved question", id), nil
		case subject != "" && sub != subject:
			return nil, fmt.Sprintf("question %s isn't a %s question", id, subject), nil
		}
	}
	return out, "", nil
}

// examFor finds a published test and checks the sections asked, returning
// them in the test's order.
func (s *Service) examFor(ctx context.Context, code string, sections []string) (*practice.Package, []string, map[string]string, error) {
	bad := map[string]string{}
	if code == "" {
		bad["test"] = "choose a mock test"
		return nil, nil, bad, nil
	}
	pkg, err := s.practice.LatestPackage(ctx, code)
	if errors.Is(err, practice.ErrNotFound) {
		bad["test"] = "no published test " + code
		return nil, nil, bad, nil
	}
	if err != nil {
		return nil, nil, nil, err
	}
	if len(sections) == 0 {
		bad["sections"] = "choose english, math or both"
		return &pkg, nil, bad, nil
	}
	var out []string
	for _, sec := range pkg.Sections {
		if slices.Contains(sections, sec.Subject) {
			out = append(out, sec.Subject)
		}
	}
	for i, sec := range sections {
		if !slices.Contains(out, sec) || slices.Index(sections, sec) != i {
			bad["sections"] = fmt.Sprintf("%q isn't a section of %s, or is named twice", sec, pkg.Code)
			return &pkg, nil, bad, nil
		}
	}
	return &pkg, out, bad, nil
}

func cleanSkills(skills []string) ([]string, string) {
	out := []string{}
	for _, sk := range skills {
		sk = strings.TrimSpace(sk)
		if sk == "" {
			return nil, "a skill has no name"
		}
		if !slices.Contains(out, sk) {
			out = append(out, sk)
		}
	}
	return out, ""
}

// checkMix says what is wrong with a mix; needed: it must ask for 1–60.
func checkMix(m roadmap.Mix, needed bool) string {
	if m.Easy < 0 || m.Medium < 0 || m.Hard < 0 {
		return "counts can't be negative"
	}
	if n := m.Easy + m.Medium + m.Hard; n > maxQuestions || (needed && n == 0) {
		return fmt.Sprintf("ask for 1 to %d questions", maxQuestions)
	}
	return ""
}

// mixOf is a homework set's difficulty and size as a mix.
func mixOf(difficulty string, n int) roadmap.Mix {
	var m roadmap.Mix
	switch difficulty {
	case "easy":
		m.Easy = n
	case "medium":
		m.Medium = n
	case "hard":
		m.Hard = n
	}
	return m
}

// Update is a change to an assignment (AssignmentUpdate in the contract).
type Update struct {
	Title        *string    `json:"title"`
	Instructions *string    `json:"instructions"`
	OpensAt      *time.Time `json:"opens_at"`
	DueAt        *time.Time `json:"due_at"`
	Closed       *bool      `json:"closed"`
}

// Update changes an assignment's title, instructions or dates, or closes
// (or reopens) it.
func (s *Service) Update(ctx context.Context, teacher int32, id string, in Update, day time.Time) (Detail, error) {
	r, err := s.access(ctx, teacher, id, day)
	if err != nil {
		return Detail{}, err
	}
	bad := map[string]string{}
	var title, instructions string
	if in.Title == nil {
		bad["title"] = "required"
	} else if title = strings.TrimSpace(*in.Title); utf8.RuneCountInString(title) == 0 || utf8.RuneCountInString(title) > maxTitle {
		bad["title"] = fmt.Sprintf("1 to %d characters", maxTitle)
	}
	if in.Instructions == nil {
		bad["instructions"] = "required"
	} else if instructions = strings.TrimSpace(*in.Instructions); utf8.RuneCountInString(instructions) > maxInstructions {
		bad["instructions"] = fmt.Sprintf("at most %d characters", maxInstructions)
	}
	switch {
	case in.OpensAt == nil:
		bad["opens_at"] = "required"
	case in.DueAt == nil:
		bad["due_at"] = "required"
	case !in.DueAt.After(*in.OpensAt):
		bad["due_at"] = "the deadline must be after the assignment opens"
	}
	if in.Closed == nil {
		bad["closed"] = "required"
	}
	if len(bad) > 0 {
		return Detail{}, &InvalidError{Fields: bad}
	}
	r, err = assignmentsdb.New(s.pool).UpdateAssignment(ctx, assignmentsdb.UpdateAssignmentParams{
		ID: r.ID, Title: title, Instructions: instructions, OpensAt: timestamptz(*in.OpensAt), DueAt: timestamptz(*in.DueAt),
		Closed: *in.Closed,
	})
	if err != nil {
		return Detail{}, err
	}
	return s.detail(ctx, r, day)
}

// Delete removes an assignment nobody has started (ErrHasWork otherwise),
// with its notifications.
func (s *Service) Delete(ctx context.Context, teacher int32, id string, day time.Time) error {
	r, err := s.access(ctx, teacher, id, day)
	if err != nil {
		return err
	}
	q := assignmentsdb.New(s.pool)
	started, err := q.HasAssessments(ctx, r.ID)
	if err != nil {
		return err
	}
	if !started && r.Kind == KindExam {
		f, err := s.gather(ctx, []assignmentsdb.Assignment{r}, day, nil)
		if err != nil {
			return err
		}
		for _, st := range f.rosters[r.GroupID] {
			if standing(r, f, st.ClientID).hasWork {
				started = true
				break
			}
		}
	}
	if started {
		return ErrHasWork
	}
	return q.DeleteAssignment(ctx, r.ID)
}
