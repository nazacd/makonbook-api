package practice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/nazacd/makonbook-api/internal/practice/practicedb"
)

// YellowApprover is the name on the approvals ApproveYellow makes (the
// editor shows "Approved by …").
const YellowApprover = "the bulk approval of yellow fixes"

// What ApproveYellow does with a yellow question: approve it, or leave it for
// an editor, and why.
const (
	YellowApproved     = "approved"
	YellowWouldApprove = "would be approved"
	YellowNoFix        = "no fix yet (run the fix step)"
	YellowPerson       = "the fix step says a person must step in"
	YellowDisagree     = "its reviews' fixes differ"
	YellowMadeUp       = "the fix invents text or redraws a figure"
	YellowAnswer       = "the fix changes the answer"
	YellowCantApply    = "the fixed question fails the checks"
	yellowChanged      = "changed meanwhile"
)

// YellowResult is one yellow question and what ApproveYellow did with it.
type YellowResult struct {
	Place  string // where it first appears, e.g. "DAY3 math-m1-07"
	ID     string
	Result string
}

// Approved reports whether the question was (or would be) approved.
func (r YellowResult) Approved() bool {
	return r.Result == YellowApproved || r.Result == YellowWouldApprove
}

// ApproveYellow does for yellow questions what the teachers' group does
// (ApproveByGroup): it applies the fix step's fix and approves the question.
// Only questions nobody has touched (in review, version 1), in the tests
// given (all when none), and only when the fix is safe to take unseen: every
// review of the question has the same fix, no person is asked to step in, it
// only corrects or restores text (nothing invented, no figure redrawn), it
// keeps the answer, and the fixed question passes an editor's checks. A fix
// that changes nothing just approves. Every other yellow question stays in
// review, and red ones are never touched. The fix is a version of its own
// (by the AI review), so an editor can see and undo it. With apply false
// nothing is stored: it only reports what it would do.
func (im Importer) ApproveYellow(ctx context.Context, tests []string, apply bool) ([]YellowResult, error) {
	codes := make([]string, 0, len(tests))
	for _, t := range tests {
		codes = append(codes, strings.ToUpper(strings.ReplaceAll(t, " ", "")))
	}
	rows, err := practicedb.New(im.Pool).YellowQuestions(ctx, codes)
	if err != nil {
		return nil, err
	}
	out := make([]YellowResult, 0, len(rows))
	for _, row := range rows {
		res := YellowResult{Place: row.Place, ID: uuidString(row.ID)}
		err := pgx.BeginFunc(ctx, im.Pool, func(tx pgx.Tx) error {
			q := practicedb.New(tx)
			cur, err := q.LockQuestion(ctx, row.ID)
			if err != nil {
				return err
			}
			if cur.AiRisk.String != "yellow" || cur.Status != "review" || cur.Version != 1 {
				res.Result = yellowChanged
				return nil
			}
			reviews, err := q.QuestionReviews(ctx, row.ID)
			if err != nil {
				return err
			}
			e, changed, why, err := yellowFix(cur, reviews)
			if err != nil || why != "" {
				res.Result = why
				return err
			}
			if !apply {
				res.Result = YellowWouldApprove
				return nil
			}
			if changed {
				if err := save(ctx, q, cur, e, "ai_fix", AIFixer); err != nil {
					return err
				}
			}
			res.Result = YellowApproved
			return q.ApproveQuestion(ctx, practicedb.ApproveQuestionParams{ID: cur.ID, ApprovedBy: optText(YellowApprover)})
		})
		var invalid *InvalidError
		if errors.As(err, &invalid) { // e.g. a figure the fix drew isn't uploaded
			res.Result, err = YellowCantApply+": "+invalidFields(invalid), nil
		}
		if err != nil {
			return out, fmt.Errorf("%s (%s): %w", res.Place, res.ID, err)
		}
		out = append(out, res)
	}
	return out, nil
}

// yellowFix is the question with its reviews' fix applied (changed: the fix
// changes something), or why the fix isn't safe to take unseen.
func yellowFix(cur practicedb.Question, reviews []practicedb.QuestionReviewsRow) (Edit, bool, string, error) {
	var fix *reviewFields
	var first []byte
	for _, r := range reviews {
		var f reviewFields
		if err := json.Unmarshal(r.Record, &f); err != nil {
			return Edit{}, false, "", fmt.Errorf("%s %s review: %w", r.Code, r.Qid, err)
		}
		if f.Fix == nil {
			if r.Risk == "green" { // a repeat another test's review found fine: nothing to fix there
				continue
			}
			return Edit{}, false, YellowNoFix, nil
		}
		if f.Fix.NeedsPerson != nil && strings.TrimSpace(*f.Fix.NeedsPerson) != "" {
			return Edit{}, false, YellowPerson, nil
		}
		same, err := fixKey(f)
		if err != nil {
			return Edit{}, false, "", err
		}
		if fix == nil {
			fix, first = &f, same
		} else if !bytes.Equal(first, same) {
			return Edit{}, false, YellowDisagree, nil
		}
	}
	if fix == nil {
		return Edit{}, false, YellowNoFix, nil
	}
	if slices.ContainsFunc(fix.Fix.Kinds, func(k string) bool { return k == "invented" || k == "redrawn" }) || len(fix.Fix.Figures) > 0 {
		return Edit{}, false, YellowMadeUp, nil
	}
	e, err := editOf(cur)
	if err != nil {
		return e, false, "", err
	}
	correct, accepted := e.Correct, slices.Clone(e.Accepted)
	e, changed, err := withFix(e, cur.Subject, cur.Type, *fix)
	var invalid *InvalidError
	if errors.As(err, &invalid) {
		return e, false, YellowCantApply + ": " + invalidFields(invalid), nil
	}
	if err != nil {
		return e, false, "", err
	}
	if e.Correct != correct || !slices.Equal(e.Accepted, accepted) {
		return e, false, YellowAnswer, nil
	}
	return e, changed, "", nil
}

// fixKey is a review's fix in a form two equal fixes share.
func fixKey(f reviewFields) ([]byte, error) {
	changes := map[string]any{}
	for k, raw := range f.Fix.Changes {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("fix change %s: %w", k, err)
		}
		changes[k] = v
	}
	return json.Marshal(map[string]any{"changes": changes, "figures": f.Fix.Figures})
}

// invalidFields lists the fields an editor's checks refused, in order.
func invalidFields(e *InvalidError) string {
	fields := make([]string, 0, len(e.Fields))
	for f := range e.Fields {
		fields = append(fields, f)
	}
	slices.Sort(fields)
	return strings.Join(fields, ", ")
}
