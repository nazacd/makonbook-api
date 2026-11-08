package roadmap

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The tests use a small made-up course, never SAT MAKON's curriculum.

// fixture is a course of three units in two countries: the first country
// has one unit (two lessons), the second two units of one lesson each.
// Lesson ids start with prefix.
func fixture(subject, level, prefix string) Course {
	skill := "Linear functions"
	if subject == "english" {
		skill = "Words in Context"
	}
	lesson := func(id string, sets int) Lesson {
		l := Lesson{
			ID: id, Title: "Lesson " + id, Skills: []string{skill}, Prerequisites: []string{}, Minutes: 30,
			Objectives: []string{"Do the thing"}, Notes: "Some notes with \\(x\\).",
			Examples:       []Example{{Problem: "What is 1 + 1?", Steps: []string{"Add."}, Answer: "2"}},
			CommonMistakes: []string{"Adding wrong"},
			LevelCheck:     LevelCheck{Questions: 4, Mix: Mix{Easy: 2, Medium: 1, Hard: 1}, Skills: []string{skill}},
		}
		for i := range sets {
			l.Homework = append(l.Homework, Homework{Title: fmt.Sprintf("Set %d", i+1), Skills: []string{skill}, Difficulty: "easy", Questions: 2})
		}
		return l
	}
	unit := func(id string, lessons ...Lesson) Unit {
		return Unit{ID: id, Title: "Unit " + id, Domain: "Algebra", Summary: "A unit.",
			Stop: Stop{University: "Example University", City: "Exampleton", Country: "Nowhere"}, Lessons: lessons}
	}
	return Course{
		Subject: subject, Level: level,
		Countries: []Country{
			{ID: "uzbekistan", Name: "Uzbekistan", Units: []string{prefix + "1"}},
			{ID: "atlantis", Name: "Atlantis", Units: []string{prefix + "2", prefix + "3"}},
		},
		Units: []Unit{
			unit(prefix+"1", lesson(prefix+"1-1", 1), lesson(prefix+"1-2", 2)),
			unit(prefix+"2", lesson(prefix+"2-1", 1)),
			unit(prefix+"3", lesson(prefix+"3-1", 1)),
		},
	}
}

// addQuestions puts n approved questions of a skill and difficulty in the
// bank ("" difficulty: not estimated). Multiple choice, B is right.
func addQuestions(t *testing.T, pool *pgxpool.Pool, subject, skill, difficulty string, n int) []string {
	t.Helper()
	var ids []string
	for range n {
		var id string
		var diff any
		if difficulty != "" {
			diff = difficulty
		}
		err := pool.QueryRow(context.Background(), `
			INSERT INTO questions (subject, type, content_hash, stem, choices, correct, explanation, domain, skill, difficulty, status, approved_at)
			VALUES ($1, 'mcq', gen_random_uuid()::text, 'Which one?', '{"A": "1", "B": "2", "C": "3", "D": "4"}', 'B',
			        'Because.', 'Algebra', $2, $3, 'approved', now())
			RETURNING id::text`, subject, skill, diff).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}
