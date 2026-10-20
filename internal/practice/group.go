package practice

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/nazacd/makonbook-api/internal/practice/practicedb"
)

// GroupReviewer is the name on what the teachers' Telegram group decides
// (spec 9.4): the versions its approval makes, and its approvals.
const GroupReviewer = "Teachers' group"

// ApproveByGroup applies the AI's fix (from test's review) to the question
// and approves it, as the teachers' group voted on version. A fix that
// changes nothing just approves. ErrConflict if the question changed since
// the vote began; *InvalidError if the fixed question fails the checks.
func (s *Service) ApproveByGroup(ctx context.Context, id string, version int32, test, approvedBy string) error {
	uid, ok := parseUUID(id)
	if !ok {
		return ErrNotFound
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := practicedb.New(tx)
		cur, err := q.LockQuestion(ctx, uid)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if cur.Version != version || cur.Status == "approved" {
			return ErrConflict
		}
		reviews, err := q.QuestionReviews(ctx, uid)
		if err != nil {
			return err
		}
		var f reviewFields
		for _, r := range reviews {
			if r.Code == test {
				if err := json.Unmarshal(r.Record, &f); err != nil {
					return err
				}
			}
		}
		e, err := editOf(cur)
		if err != nil {
			return err
		}
		e, changed, err := withFix(e, cur.Subject, cur.Type, f)
		if err != nil {
			return err
		}
		if changed {
			if err := save(ctx, q, cur, e, "group", GroupReviewer); err != nil {
				return err
			}
		}
		return q.ApproveQuestion(ctx, practicedb.ApproveQuestionParams{ID: uid, ApprovedBy: optText(approvedBy)})
	})
}

// GroupPost is what the group's post of a question says about it.
type GroupPost struct {
	Place   string   // e.g. "DAY3 · Reading and Writing · Module 2 · Question 4"
	Risk    string   // red | yellow
	Kinds   []string // the fix's kinds: corrected, restored, invented, redrawn
	Note    string   // the fix's note
	Changes bool     // false: the AI found nothing to change
}

// GroupReady says whether the question can go to the group, as test's
// review has it: there is a fix, no person needs to step in, and the fixed
// question passes the checks a save does, so the group's approval can
// apply it.
func (s *Service) GroupReady(ctx context.Context, id, test string) (GroupPost, error) {
	q, err := s.Question(ctx, id)
	if err != nil {
		return GroupPost{}, err
	}
	f, err := reviewFor(q, test)
	if err != nil {
		return GroupPost{}, err
	}
	if f.Fix == nil {
		return GroupPost{}, ErrNoFix
	}
	if f.Fix.NeedsPerson != nil && strings.TrimSpace(*f.Fix.NeedsPerson) != "" {
		return GroupPost{}, errors.New("a person must step in: " + *f.Fix.NeedsPerson)
	}
	e := Edit{
		Passage: q.Passage, Stem: q.Stem, Choices: copyChoices(q.Choices), Figures: q.Figures, Correct: deref(q.Correct),
		Accepted: q.Accepted, Explanation: q.Explanation, Domain: q.Domain, Skill: q.Skill, Difficulty: deref(q.Difficulty),
	}
	_, changed, err := withFix(e, q.Subject, q.Type, f)
	if err != nil {
		return GroupPost{}, err
	}
	if len(q.Places) == 0 {
		return GroupPost{}, errors.New("the question is in no test")
	}
	place := q.Places[0]
	for _, p := range q.Places {
		if p.Test == test {
			place = p
		}
	}
	return GroupPost{Place: placeLabel(place), Risk: deref(q.AIRisk), Kinds: f.Fix.Kinds, Note: f.Fix.Note, Changes: changed}, nil
}

// withFix is e with the review's fix applied (changed: it changes
// something), checked as a save would be.
func withFix(e Edit, subject, qtype string, f reviewFields) (Edit, bool, error) {
	changed := f.Fix != nil && len(f.Fix.Changes) > 0
	if changed {
		if !applyFix(&e, qtype, f.Fix.Changes) {
			return e, false, &InvalidError{Fields: map[string]string{"fix": "the fix names fields this question doesn't have"}}
		}
		e.Figures = placedFigures(e, f.Fix.Figures)
	}
	e.normalize(qtype)
	if bad := e.Validate(subject, qtype); len(bad) > 0 {
		return e, false, &InvalidError{Fields: bad}
	}
	return e, changed, nil
}

// placedFigures is the question's figures after a fix: the ones it had and
// the ones the fix drew, as far as the text still shows them.
func placedFigures(e Edit, drawn []Figure) []Figure {
	texts := []string{e.Stem}
	if e.Passage != nil {
		texts = append(texts, *e.Passage)
	}
	for _, t := range e.Choices {
		texts = append(texts, t)
	}
	shown := map[string]string{} // key → its description in the text
	for _, t := range texts {
		for _, m := range imageRE.FindAllStringSubmatch(t, -1) {
			if key, ok := strings.CutPrefix(m[2], "r2:"); ok {
				shown[key] = m[1]
			}
		}
	}
	out := []Figure{}
	for _, f := range append(append([]Figure{}, e.Figures...), drawn...) {
		if alt, ok := shown[f.Key]; ok {
			if f.Alt == "" {
				f.Alt = alt
			}
			out = append(out, f)
			delete(shown, f.Key)
		}
	}
	return out
}
