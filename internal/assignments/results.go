package assignments

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/nazacd/makonbook-api/internal/assignments/assignmentsdb"
	"github.com/nazacd/makonbook-api/internal/practice"
	"github.com/nazacd/makonbook-api/internal/roadmap"
)

// Results for the group's teachers (spec 23.4).

// StudentResult is a student of the group and their standing.
type StudentResult struct {
	Standing
	ClientID  int32  `json:"client_id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

// QuestionStats is how the group answered one question.
type QuestionStats struct {
	QuestionID string         `json:"question_id"`
	Position   int            `json:"position"`
	Subject    string         `json:"subject"`
	Module     *int16         `json:"module"`
	Skill      string         `json:"skill"`
	Difficulty *string        `json:"difficulty"`
	Answered   int            `json:"answered"`
	Correct    int            `json:"correct"`
	Answers    map[string]int `json:"answers"`
	AvgSeconds *float64       `json:"avg_seconds"`
}

// SkillStats is the wrong answers of one skill.
type SkillStats struct {
	Skill string `json:"skill"`
	Wrong int    `json:"wrong"`
	Total int    `json:"total"`
}

// Results is how the group did.
type Results struct {
	Assignment Assignment      `json:"assignment"`
	Students   []StudentResult `json:"students"`
	Questions  []QuestionStats `json:"questions"`
	Skills     []SkillStats    `json:"skills"`
}

// Results returns each student's standing, each question's answers and the
// weakest skills, over the work that counts (the first finished).
func (s *Service) Results(ctx context.Context, teacher int32, id string, day time.Time) (Results, error) {
	r, err := s.access(ctx, teacher, id, day)
	if err != nil {
		return Results{}, err
	}
	f, err := s.gather(ctx, []assignmentsdb.Assignment{r}, day, nil)
	if err != nil {
		return Results{}, err
	}
	a, err := s.view(ctx, r, f)
	if err != nil {
		return Results{}, err
	}
	out := Results{Assignment: a, Students: []StudentResult{}, Questions: []QuestionStats{}, Skills: []SkillStats{}}
	var standings []Standing
	for _, st := range f.rosters[r.GroupID] {
		sd := standing(r, f, st.ClientID)
		standings = append(standings, sd)
		out.Students = append(out.Students, StudentResult{Standing: sd, ClientID: st.ClientID, FirstName: st.FirstName, LastName: st.LastName})
	}
	var tally []*tallied
	if r.Kind == KindExam {
		tally = examTally(r, f.packages[r.TestCode.String], standings)
	} else {
		bank, err := s.practice.BankQuestions(ctx, idStrings(r.QuestionIds))
		if err != nil {
			return Results{}, err
		}
		tally = setTally(r, bank, standings)
	}
	skills := map[string]*SkillStats{}
	for _, t := range tally {
		q := t.stats
		if t.seconds > 0 {
			avg := float64(t.secondsSum) / float64(t.seconds)
			q.AvgSeconds = &avg
		}
		out.Questions = append(out.Questions, q)
		sk := skills[q.Skill]
		if sk == nil {
			sk = &SkillStats{Skill: q.Skill}
			skills[q.Skill] = sk
		}
		sk.Total += q.Answered
		sk.Wrong += q.Answered - q.Correct
	}
	for _, sk := range skills {
		out.Skills = append(out.Skills, *sk)
	}
	slices.SortFunc(out.Skills, func(a, b SkillStats) int {
		return cmp.Or(b.Wrong-a.Wrong, strings.Compare(a.Skill, b.Skill))
	})
	return out, nil
}

// tallied is a question's statistics as they are counted.
type tallied struct {
	stats      QuestionStats
	pq         practice.PackageQuestion
	secondsSum int
	seconds    int // answers with a time
}

func newTally(pq practice.PackageQuestion, position int, subject string, module *int16) *tallied {
	t := &tallied{pq: pq, stats: QuestionStats{
		QuestionID: pq.ID, Position: position, Subject: subject, Module: module, Skill: pq.Skill, Difficulty: pq.Difficulty,
		Answers: map[string]int{"blank": 0},
	}}
	if pq.Type == "mcq" {
		for _, l := range []string{"A", "B", "C", "D"} {
			t.stats.Answers[l] = 0
		}
	} else {
		t.stats.Answers["correct"], t.stats.Answers["wrong"] = 0, 0
	}
	return t
}

// add counts one student's answer (nil: blank) and seconds (nil: no time).
func (t *tallied) add(given *string, seconds *int) {
	t.stats.Answered++
	right := given != nil && t.pq.Correct(*given)
	if right {
		t.stats.Correct++
	}
	switch {
	case given == nil || strings.TrimSpace(*given) == "":
		t.stats.Answers["blank"]++
	case t.pq.Type == "mcq":
		t.stats.Answers[strings.ToUpper(strings.TrimSpace(*given))]++
	case right:
		t.stats.Answers["correct"]++
	default:
		t.stats.Answers["wrong"]++
	}
	if seconds != nil {
		t.secondsSum += *seconds
		t.seconds++
	}
}

// setTally counts a homework's or level check's questions over each
// student's counted assessment. Questions gone from the bank are left out.
func setTally(r assignmentsdb.Assignment, bank map[string]practice.PackageQuestion, standings []Standing) []*tallied {
	var out []*tallied
	for i, id := range idStrings(r.QuestionIds) {
		pq, ok := bank[id]
		if !ok {
			continue
		}
		t := newTally(pq, i+1, r.Subject, nil)
		for _, st := range standings {
			w := st.counted
			if w == nil {
				continue
			}
			var seconds *int
			if sec, ok := w.times[id]; ok {
				seconds = &sec
			}
			t.add(w.answers[id], seconds)
		}
		out = append(out, t)
	}
	return out
}

// examTally counts the assigned sections' questions of the test's latest
// version over each student's counted attempt, by their place in the test.
func examTally(r assignmentsdb.Assignment, pkg *practice.Package, standings []Standing) []*tallied {
	if pkg == nil {
		return nil
	}
	type slot struct {
		subject          string
		module, position int16
	}
	var out []*tallied
	index := map[slot]*tallied{}
	for _, sec := range pkg.Sections {
		if !slices.Contains(r.Sections, sec.Subject) {
			continue
		}
		for _, m := range sec.Modules {
			for _, pq := range m.Questions {
				module := m.Module
				t := newTally(pq, len(out)+1, sec.Subject, &module)
				out = append(out, t)
				index[slot{sec.Subject, m.Module, pq.Position}] = t
			}
		}
	}
	for _, st := range standings {
		a := st.attempt
		if a == nil {
			continue
		}
		given := map[slot]practice.Answer{}
		for _, ans := range a.answers {
			given[slot{ans.Subject, ans.Module, ans.Position}] = ans
		}
		for k, t := range index {
			ans, ok := given[k]
			if !ok {
				t.add(nil, nil)
				continue
			}
			sec := int(ans.Seconds)
			t.add(ans.Answer, &sec)
		}
	}
	return out
}

// Work is one student's work on an assignment: the assessment (homework,
// level check) or the attempt (exam) that counts; both nil before they
// finish.
type Work struct {
	Assessment *roadmap.Assessment  `json:"assessment"`
	Attempt    *practice.OwnAttempt `json:"attempt"`
}

// StudentWork returns a student's counted work. ErrNotFound when the
// student isn't in the group now.
func (s *Service) StudentWork(ctx context.Context, teacher int32, id string, student int32, day time.Time) (Work, error) {
	r, err := s.access(ctx, teacher, id, day)
	if err != nil {
		return Work{}, err
	}
	f, err := s.gather(ctx, []assignmentsdb.Assignment{r}, day, nil)
	if err != nil {
		return Work{}, err
	}
	if !slices.ContainsFunc(f.rosters[r.GroupID], func(st rosterStudent) bool { return st.ClientID == student }) {
		return Work{}, ErrNotFound
	}
	st := standing(r, f, student)
	var out Work
	switch {
	case st.counted != nil:
		a, err := s.roadmap.AssessmentOf(ctx, st.counted.id)
		if err != nil {
			return Work{}, err
		}
		out.Assessment = &a
	case st.attempt != nil:
		answers := st.attempt.answers
		if answers == nil {
			answers = []practice.Answer{}
		}
		out.Attempt = &practice.OwnAttempt{Attempt: st.attempt.Attempt, Answers: answers}
	}
	return out, nil
}
