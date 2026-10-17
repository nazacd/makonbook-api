package practice

import (
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"strings"
)

// The content format every text field uses (the review's tools/prompts/format.md):
// paragraphs, \( \) and \[ \] math, *italic*, **bold**, <u>underline</u>,
// "- " lists, pipe tables and ![alt](r2:key) figures. No other HTML.

// Taxonomy is the digital SAT's domains and skills, per subject.
var Taxonomy = map[string]map[string][]string{
	"english": {
		"Information and Ideas": {"Central Ideas and Details", "Inferences", "Command of Evidence (Textual)",
			"Command of Evidence (Quantitative)"},
		"Craft and Structure":          {"Words in Context", "Text Structure and Purpose", "Cross-Text Connections"},
		"Expression of Ideas":          {"Rhetorical Synthesis", "Transitions"},
		"Standard English Conventions": {"Boundaries", "Form, Structure, and Sense"},
	},
	"math": {
		"Algebra": {"Linear equations in one variable", "Linear functions", "Linear equations in two variables",
			"Systems of two linear equations in two variables", "Linear inequalities in one or two variables"},
		"Advanced Math": {"Nonlinear functions",
			"Nonlinear equations in one variable and systems of equations in two variables", "Equivalent expressions"},
		"Problem-Solving and Data Analysis": {"Ratios, rates, proportional relationships, and units", "Percentages",
			"One-variable data: distributions and measures of center and spread",
			"Two-variable data: models and scatterplots", "Probability and conditional probability",
			"Inference from sample statistics and margin of error",
			"Evaluating statistical claims: observational studies and experiments"},
		"Geometry and Trigonometry": {"Area and volume", "Lines, angles, and triangles",
			"Right triangles and trigonometry", "Circles"},
	},
}

var (
	tagRE     = regexp.MustCompile(`</?([a-zA-Z][a-zA-Z0-9]*)[^>]*>`)
	imageRE   = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]*)\)`)
	tableSep  = regexp.MustCompile(`^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$`)
	numberRE  = regexp.MustCompile(`^-?(\d+(\.\d*)?|\.\d+)(/\d+)?$`)
	figureKey = regexp.MustCompile(`^[A-Za-z0-9._/ ()\-]+$`)
)

// checkText reports what is wrong with one text field, or "".
func checkText(text string, figures map[string]bool) string {
	var open string
	for i := 0; i+1 < len(text); i++ {
		switch pair := text[i : i+2]; pair {
		case `\(`, `\[`:
			if open != "" {
				return "math opened inside math"
			}
			open = pair
			i++
		case `\)`, `\]`:
			if (pair == `\)` && open != `\(`) || (pair == `\]` && open != `\[`) {
				return "a math closing " + pair + " without its opening"
			}
			open = ""
			i++
		}
	}
	if open != "" {
		return "math opened with " + open + " is not closed"
	}
	for _, m := range tagRE.FindAllString(text, -1) {
		if m != "<u>" && m != "</u>" {
			return "only <u> and </u> are allowed, not " + m
		}
	}
	if strings.Count(text, "<u>") != strings.Count(text, "</u>") {
		return "every <u> needs a </u>"
	}
	if strings.Count(text, "**")%2 != 0 {
		return "every ** needs a closing **"
	}
	for _, m := range imageRE.FindAllStringSubmatch(text, -1) {
		key, ok := strings.CutPrefix(m[2], "r2:")
		if !ok || !figures[key] {
			return "the image " + m[2] + " is not one of the question's figures"
		}
	}
	if strings.ContainsRune(text, '�') {
		return "contains a broken character (�)"
	}
	lines := strings.Split(text, "\n")
	for j, line := range lines {
		isRow := strings.HasPrefix(strings.TrimSpace(line), "|")
		startsTable := isRow && (j == 0 || !strings.HasPrefix(strings.TrimSpace(lines[j-1]), "|")) &&
			j+1 < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[j+1]), "|")
		if startsTable && !tableSep.MatchString(lines[j+1]) {
			return "a table's second line must be the separator, like |---|---|"
		}
	}
	return ""
}

// ParseNumber reads a grid-in answer: an integer, a decimal or a fraction.
func ParseNumber(s string) (*big.Rat, bool) {
	s = strings.ReplaceAll(strings.TrimSpace(s), "−", "-")
	if !numberRE.MatchString(s) {
		return nil, false
	}
	r, ok := new(big.Rat).SetString(s)
	return r, ok
}

// Validate checks an edit; the result maps each wrong field to what is wrong.
func (e Edit) Validate(subject, qtype string) map[string]string {
	bad := map[string]string{}
	figures := map[string]bool{}
	for i, f := range e.Figures {
		switch {
		case !figureKey.MatchString(f.Key) || strings.Contains(f.Key, ".."):
			bad[fmt.Sprintf("figures.%d", i)] = "not an image key"
		case strings.TrimSpace(f.Alt) == "":
			bad[fmt.Sprintf("figures.%d", i)] = "describe the image (alt text)"
		}
		figures[f.Key] = true
	}
	used := map[string]bool{}
	check := func(field, text string, required bool) {
		if required && strings.TrimSpace(text) == "" {
			bad[field] = "required"
			return
		}
		if msg := checkText(text, figures); msg != "" {
			bad[field] = msg
		}
		for _, m := range imageRE.FindAllStringSubmatch(text, -1) {
			used[strings.TrimPrefix(m[2], "r2:")] = true
		}
	}
	if e.Passage != nil {
		check("passage", *e.Passage, false)
	}
	check("stem", e.Stem, true)
	check("explanation", e.Explanation, true)

	switch qtype {
	case "mcq":
		for _, l := range []string{"A", "B", "C", "D"} {
			check("choices."+l, e.Choices[l], true)
		}
		if len(e.Choices) != 4 {
			bad["choices"] = "exactly the choices A, B, C and D"
		}
		if !strings.Contains("ABCD", e.Correct) || len(e.Correct) != 1 {
			bad["correct"] = "pick the correct choice"
		}
	case "grid_in":
		if len(e.Accepted) == 0 {
			bad["accepted"] = "give at least one correct answer"
		}
		for _, a := range e.Accepted {
			if _, ok := ParseNumber(a); !ok {
				bad["accepted"] = fmt.Sprintf("%q is not a number (write 17/2, 8.5 or -3)", a)
				break
			}
		}
	}
	for key := range figures {
		if !used[key] {
			bad["figures"] = "every figure must be placed in the text with ![description](r2:" + key + ")"
		}
	}
	skills, ok := Taxonomy[subject][e.Domain]
	if !ok {
		bad["domain"] = "pick a domain"
	} else if !slices.Contains(skills, e.Skill) {
		bad["skill"] = "pick a skill of " + e.Domain
	}
	if e.Difficulty != "" && e.Difficulty != "easy" && e.Difficulty != "medium" && e.Difficulty != "hard" {
		bad["difficulty"] = "easy, medium or hard"
	}
	return bad
}
