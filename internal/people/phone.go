package people

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nyaruka/phonenumbers"
)

// Person kinds, as stored in data_quality_issues.person_kind.
const (
	KindStudent = "student"
	KindTeacher = "teacher"
)

// Issue kinds, as stored in data_quality_issues.kind.
const (
	IssuePhoneMissing   = "phone_missing"
	IssuePhoneInvalid   = "phone_invalid"
	IssuePhoneDuplicate = "phone_duplicate"
)

// NormalizePhone turns a number as typed in HolliHop into E.164
// ("+998901234567"). Numbers without a country code are read as Uzbek;
// foreign numbers are accepted when written with their country code. ok is
// false for numbers libphonenumber does not consider valid (wrong length,
// non-existent operator code, garbage).
func NormalizePhone(raw string) (e164 string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	num, err := phonenumbers.Parse(raw, "UZ")
	if err != nil || !phonenumbers.IsValidNumber(num) {
		return "", false
	}
	return phonenumbers.Format(num, phonenumbers.E164), true
}

// PersonPhone is an active person's HolliHop mobile number.
type PersonPhone struct {
	Kind     string // KindStudent or KindTeacher
	ID       int32
	PhoneRaw string
	Name     string
}

// PersonKey identifies a student or teacher.
type PersonKey struct {
	Kind string
	ID   int32
}

// Issue is a problem that stops a person from logging in.
type Issue struct {
	Person PersonKey
	Kind   string
	Detail string
}

// PlanLoginPhones decides which active people may log in with which number
// (spec 5). A number shared by two or more active people goes to none of
// them — the sync never guesses — and each of them gets an issue.
func PlanLoginPhones(people []PersonPhone) (login map[PersonKey]string, issues []Issue) {
	login = make(map[PersonKey]string)
	byNumber := make(map[string][]PersonPhone)

	for _, p := range people {
		key := PersonKey{p.Kind, p.ID}
		if strings.TrimSpace(p.PhoneRaw) == "" {
			issues = append(issues, Issue{key, IssuePhoneMissing, "no mobile number in HolliHop"})
			continue
		}
		e164, ok := NormalizePhone(p.PhoneRaw)
		if !ok {
			issues = append(issues, Issue{key, IssuePhoneInvalid, fmt.Sprintf("mobile %q is not a valid phone number", p.PhoneRaw)})
			continue
		}
		byNumber[e164] = append(byNumber[e164], p)
	}

	for number, holders := range byNumber {
		if len(holders) == 1 {
			login[PersonKey{holders[0].Kind, holders[0].ID}] = number
			continue
		}
		for _, p := range holders {
			var others []string
			for _, o := range holders {
				if o.Kind != p.Kind || o.ID != p.ID {
					others = append(others, fmt.Sprintf("%s %d (%s)", o.Kind, o.ID, o.Name))
				}
			}
			sort.Strings(others)
			issues = append(issues, Issue{
				Person: PersonKey{p.Kind, p.ID},
				Kind:   IssuePhoneDuplicate,
				Detail: fmt.Sprintf("mobile %s is also used by %s", number, strings.Join(others, ", ")),
			})
		}
	}

	sort.Slice(issues, func(i, j int) bool {
		a, b := issues[i], issues[j]
		if a.Person.Kind != b.Person.Kind {
			return a.Person.Kind < b.Person.Kind
		}
		if a.Person.ID != b.Person.ID {
			return a.Person.ID < b.Person.ID
		}
		return a.Kind < b.Kind
	})
	return login, issues
}
