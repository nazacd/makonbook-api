package practice

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/nazacd/makonbook-api/internal/practice/practicedb"
)

// AIFixer is the name on the versions the AI review's fixes make.
const AIFixer = "AI review"

// FixSummary is what ApplyAIFixes did.
type FixSummary struct {
	Applied int // fixed, still approved
	Review  int // the fix couldn't be applied whole: sent back to review
}

// ApplyAIFixes applies the fix the AI review proposed to each question it
// approved (green): a typo, a missing word. It is a new version, so the
// history shows the change and an editor can undo it. A fix that can't be
// applied whole, or that leaves the question failing the checks, sends the
// question to review instead. Safe to run again: a fixed question is skipped.
func (im Importer) ApplyAIFixes(ctx context.Context) (FixSummary, error) {
	var sum FixSummary
	pending, err := practicedb.New(im.Pool).PendingAIFixes(ctx)
	if err != nil {
		return sum, err
	}
	for _, p := range pending {
		err := pgx.BeginFunc(ctx, im.Pool, func(tx pgx.Tx) error {
			q := practicedb.New(tx)
			cur, err := q.LockQuestion(ctx, p.ID)
			if err != nil {
				return err
			}
			if !cur.AutoApproved || cur.Version != 1 {
				return nil // changed meanwhile
			}
			e, err := editOf(cur)
			if err != nil {
				return err
			}
			var fix map[string]json.RawMessage
			ok := json.Unmarshal(p.Fix, &fix) == nil && applyFix(&e, cur.Type, fix)
			if ok {
				e.normalize(cur.Type)
				ok = len(e.Validate(cur.Subject, cur.Type)) == 0
			}
			if !ok {
				sum.Review++
				return q.UnapproveQuestion(ctx, p.ID)
			}
			ap := practicedb.ApplyAIFixParams{ID: p.ID, Passage: optText(deref(e.Passage)), Stem: e.Stem}
			if cur.Type == "mcq" {
				ap.Choices, _ = json.Marshal(e.Choices)
				ap.Correct = optText(e.Correct)
			} else {
				ap.Accepted = e.Accepted
			}
			version, err := q.ApplyAIFix(ctx, ap)
			if err != nil {
				return err
			}
			sum.Applied++
			return q.InsertVersion(ctx, practicedb.InsertVersionParams{
				QuestionID: p.ID, Version: version, Content: e.snapshot(cur.Type), Source: "ai_fix", CreatedBy: optText(AIFixer),
			})
		})
		if err != nil {
			return sum, fmt.Errorf("fix %s: %w", uuidString(p.ID), err)
		}
	}
	return sum, nil
}

// applyFix patches the content with a proposed fix: {"stem": "…"},
// {"choices.B": "…"}, {"key": "C"}, {"accepted": ["3/4", ".75"]}. False if
// it names anything else.
func applyFix(e *Edit, qtype string, fix map[string]json.RawMessage) bool {
	if len(fix) == 0 {
		return false
	}
	for field, raw := range fix {
		var text string
		switch {
		case field == "accepted" && qtype == "grid_in":
			if json.Unmarshal(raw, &e.Accepted) != nil {
				return false
			}
			continue
		case json.Unmarshal(raw, &text) != nil:
			return false
		}
		letter, isChoice := strings.CutPrefix(field, "choices.")
		switch {
		case field == "passage":
			e.Passage = &text
		case field == "stem":
			e.Stem = text
		case field == "key" && qtype == "mcq":
			e.Correct = text
		case isChoice && qtype == "mcq" && e.Choices != nil && len(letter) == 1 && letter >= "A" && letter <= "D":
			e.Choices[letter] = text
		default:
			return false
		}
	}
	return true
}
