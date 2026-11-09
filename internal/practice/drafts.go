package practice

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nazacd/makonbook-api/internal/practice/practicedb"
)

// Draft is a question written outside the AI review (a teacher's, or one
// drafted for teachers to check): one line of the file `makonbook practice
// add` reads. It has the fields of an editor's save plus its subject and
// type, and no figures (there is no way to upload them from a file).
type Draft struct {
	Subject string `json:"subject"`
	Type    string `json:"type"`
	Edit
}

// DraftsAdded says what AddDrafts did: the new questions' ids, in the file's
// order, and the lines whose question was already stored.
type DraftsAdded struct {
	IDs     []string
	Repeats []int
}

// ReadDrafts parses and checks a drafts file. Every line must pass the checks
// an editor's save runs, and have a difficulty (roadmap sets draw by it).
func ReadDrafts(r io.Reader) ([]Draft, error) {
	var drafts []Draft
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(line))
		dec.DisallowUnknownFields()
		var d Draft
		if err := dec.Decode(&d); err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		if err := d.check(); err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		drafts = append(drafts, d)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(drafts) == 0 {
		return nil, errors.New("no questions")
	}
	return drafts, nil
}

func (d *Draft) check() error {
	if d.Subject != "math" && d.Subject != "english" {
		return fmt.Errorf("subject %q (math or english)", d.Subject)
	}
	if d.Type != "mcq" && d.Type != "grid_in" {
		return fmt.Errorf("type %q (mcq or grid_in)", d.Type)
	}
	if len(d.Figures) > 0 {
		return errors.New("figures can't be added from a file: add the question without one and upload it in the editor")
	}
	d.normalize(d.Type)
	bad := d.Validate(d.Subject, d.Type)
	if d.Difficulty == "" {
		bad["difficulty"] = "easy, medium or hard"
	}
	if len(bad) > 0 {
		var parts []string
		for _, f := range slices.Sorted(maps.Keys(bad)) {
			parts = append(parts, f+": "+bad[f])
		}
		return errors.New(strings.Join(parts, "; "))
	}
	return nil
}

// hash is the stored questions' content hash, so a draft that repeats a
// question already in the bank (or in the same file) is stored once.
func (d Draft) hash() string {
	var r Record
	r.Source.Subject, r.Type, r.Passage, r.Stem, r.Choices = d.Subject, d.Type, d.Passage, d.Stem, d.Choices
	return r.ContentHash()
}

// AddDrafts stores checked drafts in one transaction, each in review with a
// first version marked "draft", for an editor to approve in the admin panel.
func AddDrafts(ctx context.Context, pool *pgxpool.Pool, drafts []Draft) (DraftsAdded, error) {
	var out DraftsAdded
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		out = DraftsAdded{}
		q := practicedb.New(tx)
		for i, d := range drafts {
			p := practicedb.InsertDraftParams{
				Subject: d.Subject, Type: d.Type, ContentHash: d.hash(), Passage: optText(deref(d.Passage)),
				Stem: d.Stem, Explanation: d.Explanation, Domain: d.Domain, Skill: d.Skill,
				Difficulty: optText(d.Difficulty),
			}
			if d.Type == "mcq" {
				p.Choices, _ = json.Marshal(d.Choices)
				p.Correct = optText(d.Correct)
			} else {
				p.Accepted = d.Accepted
			}
			id, err := q.InsertDraft(ctx, p)
			if errors.Is(err, pgx.ErrNoRows) {
				out.Repeats = append(out.Repeats, i+1)
				continue
			}
			if err != nil {
				return err
			}
			err = q.InsertVersion(ctx, practicedb.InsertVersionParams{
				QuestionID: id, Version: 1, Content: d.snapshot(d.Type), Source: "draft",
			})
			if err != nil {
				return err
			}
			out.IDs = append(out.IDs, uuidString(id))
		}
		return nil
	})
	return out, err
}
