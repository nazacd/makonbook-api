package practice

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nazacd/makonbook-api/internal/practice/practicedb"
)

// The bank as roadmaps (spec 22) read it: approved questions by skill and
// difficulty, a question as a student gets it, and links to its figures.

// Candidate is an approved question a roadmap assessment may use.
type Candidate struct {
	ID         string
	Difficulty string // "easy", "medium", "hard"; "" when not estimated
}

// Candidates lists the approved questions of a subject with one of skills
// (in any case).
func (s *Service) Candidates(ctx context.Context, subject string, skills []string) ([]Candidate, error) {
	lower := make([]string, len(skills))
	for i, sk := range skills {
		lower[i] = strings.ToLower(sk)
	}
	rows, err := practicedb.New(s.pool).Candidates(ctx, practicedb.CandidatesParams{Subject: subject, Skills: lower})
	if err != nil {
		return nil, err
	}
	out := make([]Candidate, len(rows))
	for i, r := range rows {
		out[i] = Candidate{ID: uuidString(r.ID), Difficulty: r.Difficulty.String}
	}
	return out, nil
}

// ApprovedCounts counts a subject's approved questions by skill (in lower
// case), then difficulty ("" when not estimated).
func (s *Service) ApprovedCounts(ctx context.Context, subject string) (map[string]map[string]int, error) {
	rows, err := practicedb.New(s.pool).ApprovedCounts(ctx, subject)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]int{}
	for _, r := range rows {
		skill := strings.ToLower(r.Skill)
		if out[skill] == nil {
			out[skill] = map[string]int{}
		}
		out[skill][r.Difficulty] += int(r.N)
	}
	return out, nil
}

// BankQuestions returns questions by id as a student gets them (with their
// key and explanation; the caller hides those until they're needed), and the
// subject of each. Position is left 0. Missing ids are left out.
func (s *Service) BankQuestions(ctx context.Context, ids []string) (map[string]PackageQuestion, error) {
	uuids := make([]pgtype.UUID, 0, len(ids))
	for _, id := range ids {
		if u, ok := parseUUID(id); ok {
			uuids = append(uuids, u)
		}
	}
	rows, err := practicedb.New(s.pool).BankQuestions(ctx, uuids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]PackageQuestion, len(rows))
	for _, row := range rows {
		e, err := editOf(row)
		if err != nil {
			return nil, fmt.Errorf("question %s: %w", uuidString(row.ID), err)
		}
		e.normalize(row.Type)
		out[uuidString(row.ID)] = PackageQuestion{
			ID: uuidString(row.ID), Version: row.Version, Type: row.Type,
			Passage: e.Passage, Stem: e.Stem, Choices: e.Choices, Figures: e.Figures, Key: textPtr(row.Correct),
			Accepted: e.Accepted, Explanation: e.Explanation, Domain: e.Domain, Skill: e.Skill,
			Difficulty: textPtr(row.Difficulty),
		}
	}
	return out, nil
}

// ImageLink is a figure's file as a browser loads it: a link that works for
// LinkTTL.
type ImageLink struct {
	URL    string `json:"url"`
	Width  int32  `json:"width"`
	Height int32  `json:"height"`
}

// FigureLinks links each figure key to its published file, copying the
// figure into MakonBook's bucket first if no test published it yet. A
// figure that can't be had (images not configured, missing from the bank)
// is left out and logged: the question still shows, without it.
func (s *Service) FigureLinks(ctx context.Context, keys []string) (map[string]ImageLink, error) {
	out := map[string]ImageLink{}
	if len(keys) == 0 || s.signer == nil {
		return out, nil
	}
	q := practicedb.New(s.pool)
	rows, err := q.FigureFiles(ctx, keys)
	if err != nil {
		return nil, err
	}
	files := map[string]PackageImage{}
	for _, r := range rows {
		files[r.Key] = PackageImage{File: r.File, Width: r.Width, Height: r.Height}
	}
	for _, key := range keys {
		img, ok := files[key]
		if !ok {
			if img, err = s.publishImage(ctx, q, key); err != nil {
				slog.WarnContext(ctx, "practice: a figure for a roadmap assessment", "key", key, "err", err)
				continue
			}
			if err := q.InsertFigureFile(ctx, practicedb.InsertFigureFileParams{Key: key, File: img.File}); err != nil {
				return nil, err
			}
		}
		url, err := s.signer.PresignGet(ImagePrefix+img.File, LinkTTL)
		if err != nil {
			return nil, err
		}
		out[key] = ImageLink{URL: url, Width: img.Width, Height: img.Height}
	}
	return out, nil
}
