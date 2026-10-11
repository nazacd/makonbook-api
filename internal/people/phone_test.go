package people

import (
	"testing"
)

func TestNormalizePhone(t *testing.T) {
	cases := []struct {
		raw  string
		want string
		ok   bool
	}{
		{"+998901234567", "+998901234567", true},
		{"901234567", "+998901234567", true},           // bare national number
		{"+998 (90) 123-45-67", "+998901234567", true}, // punctuation
		{"998901234567", "+998901234567", true},
		{"+1 331 300 1922", "+13313001922", true}, // foreign numbers are fine
		{"  ", "", false},
		{"+98978884464", "", false},   // typo: wrong length after +98
		{"+99998071167", "", false},   // country code that doesn't exist
		{"+9989537668944", "", false}, // too long
		{"+998 646 57 68", "", false}, // too short
		{"not a number", "", false},
	}
	for _, c := range cases {
		got, ok := NormalizePhone(c.raw)
		if got != c.want || ok != c.ok {
			t.Errorf("NormalizePhone(%q) = %q, %v; want %q, %v", c.raw, got, ok, c.want, c.ok)
		}
	}
}

func TestPlanLoginPhones(t *testing.T) {
	login, issues := PlanLoginPhones([]PersonPhone{
		{KindStudent, 1, "+998901111111", "A"},
		{KindStudent, 2, "", "B"},
		{KindStudent, 3, "+998 646", "C"},
		{KindStudent, 4, "+998902222222", "Sibling One"},
		{KindStudent, 5, "902222222", "Sibling Two"}, // same number, other format
		{KindTeacher, 9, "+998903333333", "T"},
		{KindTeacher, 10, "+998902222222", "Teacher Too"}, // three-way clash
	})

	wantLogin := map[PersonKey]string{
		{KindStudent, 1}: "+998901111111",
		{KindTeacher, 9}: "+998903333333",
	}
	if len(login) != len(wantLogin) {
		t.Fatalf("login = %v, want %v", login, wantLogin)
	}
	for k, v := range wantLogin {
		if login[k] != v {
			t.Errorf("login[%v] = %q, want %q", k, login[k], v)
		}
	}

	want := []struct {
		key  PersonKey
		kind string
	}{
		{PersonKey{KindStudent, 2}, IssuePhoneMissing},
		{PersonKey{KindStudent, 3}, IssuePhoneInvalid},
		{PersonKey{KindStudent, 4}, IssuePhoneDuplicate},
		{PersonKey{KindStudent, 5}, IssuePhoneDuplicate},
		{PersonKey{KindTeacher, 10}, IssuePhoneDuplicate},
	}
	if len(issues) != len(want) {
		t.Fatalf("issues = %+v", issues)
	}
	for i, w := range want {
		if issues[i].Person != w.key || issues[i].Kind != w.kind {
			t.Errorf("issue %d = %+v, want %v %s", i, issues[i], w.key, w.kind)
		}
	}
	if d := issues[2].Detail; d != "mobile +998902222222 is also used by student 5 (Sibling Two), teacher 10 (Teacher Too)" {
		t.Errorf("duplicate detail = %q", d)
	}
}
