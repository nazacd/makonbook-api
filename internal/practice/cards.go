package practice

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/nazacd/makonbook-api/internal/practice/practicedb"
)

// Question pictures (spec 9.4): a question drawn from a fixed template, like
// Bluebook, at the moment it's needed. The Telegram group gets the bank's
// original and the AI's fix side by side; the editor's Export image draws
// what is in the form. The page is web/admin's src/card; Card is its data.

// Pictures draws a page in a headless browser (internal/platform/render).
type Pictures interface {
	Picture(ctx context.Context, html, fn string, data any) ([]byte, error)
}

// ErrPicturesOff means the server can't draw pictures (RENDERER_URL).
var ErrPicturesOff = errors.New("question pictures aren't set up on the server")

// ErrNoOriginal / ErrNoFix: the question's review has no bank text or no fix.
var (
	ErrNoOriginal = errors.New("the review has no copy of the bank's question; load its original.jsonl")
	ErrNoFix      = errors.New("the review has no fix yet; run the fix step")
)

// CardContent is a question in the content format.
type CardContent struct {
	Passage  *string           `json:"passage"`
	Stem     string            `json:"stem"`
	Choices  map[string]string `json:"choices"`
	Correct  *string           `json:"correct"`
	Accepted []string          `json:"accepted"`
}

// CardBank is the bank's own question and its screenshots, as data URLs.
type CardBank struct {
	Passage  *string           `json:"passage"`
	Question string            `json:"question"`
	Choices  map[string]string `json:"choices"`
	Answer   string            `json:"answer"`
	Images   map[string]string `json:"images"`
}

// PictureVersion tells versions of the picture page apart: pictures drawn
// with another version look different. Empty without pictures.
func (s *Service) PictureVersion() string {
	if s.page == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s.page))
	return hex.EncodeToString(sum[:8])
}

// Card is what the picture page draws (web/admin's src/card/types.ts).
type Card struct {
	Kind    string            `json:"kind"` // original | fix | question
	Place   string            `json:"place"`
	Subject string            `json:"subject"`
	Type    string            `json:"type"`
	Content *CardContent      `json:"content,omitempty"`
	Base    *CardContent      `json:"base,omitempty"`
	Bank    *CardBank         `json:"bank,omitempty"`
	Figures map[string]string `json:"figures"`
}

// Picture draws a question: "original" (the bank's own, from a review),
// "fix" (the AI's fixed version, its changes highlighted) or "question"
// (edit, as the editor has it). test picks the review and the place shown
// when the question is in several tests. It returns the PNG and a file name.
func (s *Service) Picture(ctx context.Context, id, kind, test string, edit *Edit) ([]byte, string, error) {
	if s.pictures == nil || s.page == "" {
		return nil, "", ErrPicturesOff
	}
	q, err := s.Question(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if len(q.Places) == 0 {
		return nil, "", errors.New("the question is in no test")
	}
	place := q.Places[0]
	for _, p := range q.Places {
		if p.Test == test {
			place = p
		}
	}
	card := Card{Kind: kind, Place: placeLabel(place), Subject: q.Subject, Type: q.Type, Figures: map[string]string{}}
	now := contentOf(Edit{
		Passage: q.Passage, Stem: q.Stem, Choices: q.Choices, Correct: deref(q.Correct), Accepted: q.Accepted,
	})
	review, err := reviewFor(q, place.Test)
	if err != nil && kind != "question" {
		return nil, "", err
	}
	switch kind {
	case "question":
		if edit == nil {
			card.Content = &now
		} else {
			e := *edit
			e.normalize(q.Type)
			c := contentOf(e)
			card.Content = &c
		}
	case "fix":
		if review.Fix == nil || len(review.Fix.Changes) == 0 {
			return nil, "", ErrNoFix
		}
		e := Edit{Passage: q.Passage, Stem: q.Stem, Choices: copyChoices(q.Choices), Correct: deref(q.Correct), Accepted: q.Accepted}
		if !applyFix(&e, q.Type, review.Fix.Changes) {
			return nil, "", fmt.Errorf("the fix names fields a %s question doesn't have", q.Type)
		}
		fixed := contentOf(e)
		card.Content, card.Base = &fixed, &now
	case "original":
		if review.Original == nil {
			return nil, "", ErrNoOriginal
		}
		o := review.Original
		card.Bank = &CardBank{Passage: o.Passage, Question: o.Question, Choices: o.Choices, Answer: o.Answer, Images: map[string]string{}}
		for slot, key := range review.Source.Images {
			if card.Bank.Images[slot], err = s.dataURL(ctx, key); err != nil {
				return nil, "", err
			}
		}
	default:
		return nil, "", fmt.Errorf("unknown picture %q", kind)
	}
	if card.Content != nil {
		if err := s.figuresOf(ctx, card.Content, card.Figures); err != nil {
			return nil, "", err
		}
	}
	png, err := s.pictures.Picture(ctx, s.page, "renderCard", card)
	if err != nil {
		return nil, "", err
	}
	return png, fileName(place, kind), nil
}

// QuestionID finds a question by its place in a reviewed test: the test's
// code and the questionbank's qid (e.g. DAY3, english-m2-04).
func (s *Service) QuestionID(ctx context.Context, test, qid string) (string, error) {
	id, err := practicedb.New(s.pool).QuestionByQID(ctx, practicedb.QuestionByQIDParams{Code: test, Qid: qid})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return uuidString(id), err
}

// reviewFields is what a picture reads from a review record.
type reviewFields struct {
	Source struct {
		Images map[string]string `json:"images"` // the bank's screenshots: "question" or a letter → key
	} `json:"source"`
	Original *struct {
		Passage  *string           `json:"passage"`
		Question string            `json:"question"`
		Choices  map[string]string `json:"choices"`
		Answer   string            `json:"answer"`
	} `json:"original"`
	Fix *struct {
		Kinds       []string                   `json:"kinds"`
		Changes     map[string]json.RawMessage `json:"changes"`
		Figures     []Figure                   `json:"figures"`
		Note        string                     `json:"note"`
		NeedsPerson *string                    `json:"needs_person"`
	} `json:"fix"`
}

// reviewFor reads the question's review from test (or its first one).
func reviewFor(q Question, test string) (reviewFields, error) {
	var f reviewFields
	if len(q.Reviews) == 0 {
		return f, errors.New("the question has no AI review")
	}
	rec := q.Reviews[0].Record
	for _, r := range q.Reviews {
		if r.Test == test {
			rec = r.Record
		}
	}
	return f, json.Unmarshal(rec, &f)
}

func contentOf(e Edit) CardContent {
	c := CardContent{Passage: e.Passage, Stem: e.Stem, Choices: e.Choices, Accepted: e.Accepted}
	if e.Correct != "" {
		c.Correct = &e.Correct
	}
	return c
}

func copyChoices(c map[string]string) map[string]string {
	if c == nil {
		return nil
	}
	out := make(map[string]string, len(c))
	for k, v := range c {
		out[k] = v
	}
	return out
}

// figuresOf puts every image the content shows into figures, as data URLs.
func (s *Service) figuresOf(ctx context.Context, c *CardContent, figures map[string]string) error {
	texts := []string{c.Stem}
	if c.Passage != nil {
		texts = append(texts, *c.Passage)
	}
	for _, t := range c.Choices {
		texts = append(texts, t)
	}
	for _, t := range texts {
		for _, m := range imageRE.FindAllStringSubmatch(t, -1) {
			key, ok := strings.CutPrefix(m[2], "r2:")
			if !ok || figures[key] != "" {
				continue
			}
			url, err := s.dataURL(ctx, key)
			if err != nil {
				return err
			}
			figures[key] = url
		}
	}
	return nil
}

// dataURL reads an image into a data URL; anything but a raster image is
// left out (the page shows its description instead).
func (s *Service) dataURL(ctx context.Context, key string) (string, error) {
	obj, err := s.Figure(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("image %s: %w", key, err)
	}
	ct := obj.ContentType
	if !strings.HasPrefix(ct, "image/") || strings.Contains(ct, "svg") {
		return "", nil
	}
	return "data:" + ct + ";base64," + base64.StdEncoding.EncodeToString(obj.Body), nil
}

// placeLabel: "DAY3 · Reading and Writing · Module 2 · Question 4".
func placeLabel(p Place) string {
	subject := "Math"
	if p.Subject == "english" {
		subject = "Reading and Writing"
	}
	return fmt.Sprintf("%s · %s · Module %d · Question %d", p.Test, subject, p.Module, p.Position)
}

// fileName: "DAY3-RW-M2-Q04-fix.png".
func fileName(p Place, kind string) string {
	subject := "Math"
	if p.Subject == "english" {
		subject = "RW"
	}
	name := fmt.Sprintf("%s-%s-M%d-Q%02d", p.Test, subject, p.Module, p.Position)
	if kind != "question" {
		name += "-" + kind
	}
	return name + ".png"
}
