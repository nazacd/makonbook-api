package practice

import (
	"math"
	"strings"
)

// Section timing, as in Bluebook: Reading and Writing 2 × 32 minutes,
// Math 2 × 35 minutes.
var moduleMinutes = map[string]int{"english": 32, "math": 35}

// scaleAnchors are (raw score, section score) points of the estimated
// 200–800 scale (spec 9.3), from College Board's published practice tests.
// The SAT scores adaptively (IRT) and our tests aren't adaptive, so this is
// an estimate; the scale is built into each package, so the device and the
// server agree, and changing it changes only later versions.
var scaleAnchors = map[string][][2]float64{
	"english": {{0, 200}, {5, 250}, {10, 320}, {15, 380}, {20, 430}, {25, 480}, {30, 530}, {35, 580}, {40, 630},
		{45, 680}, {50, 740}, {52, 770}, {54, 800}},
	"math": {{0, 200}, {5, 280}, {10, 360}, {15, 430}, {20, 490}, {25, 550}, {30, 610}, {35, 670}, {40, 740},
		{42, 770}, {43, 790}, {44, 800}},
}

// Scale is the section score for each raw score 0…questions: the anchors
// stretched to the section's length, rounded to 10 as the SAT reports them.
func Scale(subject string, questions int) []int {
	anchors := scaleAnchors[subject]
	if len(anchors) == 0 || questions <= 0 {
		return nil
	}
	full := anchors[len(anchors)-1][0]
	out := make([]int, questions+1)
	for raw := range out {
		x := float64(raw) * full / float64(questions)
		y := anchors[len(anchors)-1][1]
		for i := 1; i < len(anchors); i++ {
			a, b := anchors[i-1], anchors[i]
			if x <= b[0] {
				y = a[1] + (x-a[0])*(b[1]-a[1])/(b[0]-a[0])
				break
			}
		}
		out[raw] = int(math.Round(y/10)) * 10
	}
	return out
}

// Correct says whether a student's answer is right: the letter for a
// multiple-choice question; for a grid-in, one of the accepted forms or the
// same number (0.5, .5 and 1/2 are one answer).
func (q PackageQuestion) Correct(answer string) bool {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return false
	}
	if q.Type == "mcq" {
		return q.Key != nil && strings.EqualFold(answer, *q.Key)
	}
	norm := func(s string) string {
		return strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(s), " ", ""), "−", "-")
	}
	given, isNum := ParseNumber(answer)
	for _, a := range q.Accepted {
		if norm(a) == norm(answer) {
			return true
		}
		if want, ok := ParseNumber(a); ok && isNum && want.Cmp(given) == 0 {
			return true
		}
	}
	return false
}
