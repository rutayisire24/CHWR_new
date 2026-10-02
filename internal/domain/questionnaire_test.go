package domain

import (
	"strings"
	"testing"
	"time"
)

func f64(v float64) *float64 { return &v }

// baseline is a slice of the CHW baseline survey, shaped as 0006 seeds it.
func baseline() Profile {
	yesNo := []Option{{ID: 1, Code: "yes", Prompt: "Yes"}, {ID: 2, Code: "no", Prompt: "No"}}
	tools := []Option{{ID: 0, Code: NoneOption, Prompt: "None"},
		{ID: 1, Code: "bicycle", Prompt: "Bicycle"}, {ID: 6, Code: "torch", Prompt: "Torch"}}
	return Profile{Code: "chw_baseline", Questions: []Question{
		{Code: "owns_phone", Prompt: "Do you own a phone?", Closed: true, DataType: DataYesNo, Options: yesNo},
		{Code: "phone_for_reporting", Prompt: "Used for reporting?", Closed: true, DataType: DataYesNo, Options: yesNo,
			DependsOn: "owns_phone", DependsOnOption: "yes"},
		{Code: "households_served", Prompt: "Households", DataType: DataInteger, Min: f64(3), Max: f64(100000)},
		{Code: "receives_incentive", Prompt: "Incentive?", Closed: true, DataType: DataYesNo, Options: yesNo},
		{Code: "incentive_frequency", Prompt: "How often?", Closed: true, DataType: DataCharacter,
			Options: []Option{{ID: 1, Code: "monthly", Prompt: "Monthly", Aliases: []string{"month"}},
				{ID: 3, Code: "annually", Prompt: "Annually", Aliases: []string{"yearly"}}},
			DependsOn: "receives_incentive", DependsOnOption: "yes"},
		{Code: "last_supervised_on", Prompt: "Last supervised", DataType: DataMonth},
		{Code: "tools_held", Prompt: "Tools held", Multi: true, Closed: true, DataType: DataCharacter, Options: tools},
		{Code: "tools_functional", Prompt: "Tools working", Multi: true, Closed: true, DataType: DataCharacter, Options: tools,
			SubsetOf: "tools_held"},
	}}
}

func TestParseReadsWhatPeopleWrite(t *testing.T) {
	p := baseline()
	cases := []struct {
		question, raw string
		want          string // values joined by ;, "" for no answer
		bad           bool
	}{
		{"owns_phone", "Y", "yes", false},
		{"owns_phone", "no", "no", false},
		{"owns_phone", "", "", false},
		{"owns_phone", "maybe", "", true},
		{"incentive_frequency", "Yearly", "annually", false},
		{"incentive_frequency", "MONTHLY", "monthly", false},
		{"incentive_frequency", "weekly", "", true},
		{"households_served", "1,200", "1200", false},
		{"households_served", "2", "", true},
		{"households_served", "many", "", true},
		{"last_supervised_on", "2026-03", "2026-03-01", false},
		{"last_supervised_on", "March 2026", "2026-03-01", false},
		{"last_supervised_on", "2026-03-17", "", true},
		{"tools_held", "Bicycle; torch, bicycle", "bicycle;torch", false},
		{"tools_held", "None", "none", false},
		{"tools_held", "bicycle;hammer", "", true},
	}
	for _, c := range cases {
		q, _ := p.Question(c.question)
		got, err := q.Parse(c.raw)
		if (err != nil) != c.bad {
			t.Errorf("%s %q: err = %v, want bad=%v", c.question, c.raw, err, c.bad)
			continue
		}
		if !c.bad && strings.Join(got, ";") != c.want {
			t.Errorf("%s %q = %q, want %q", c.question, c.raw, strings.Join(got, ";"), c.want)
		}
	}
}

// What the schema refuses, the profile reports first, by question.
func TestCheckReportsWhatTheSchemaRefuses(t *testing.T) {
	p := baseline()
	cases := []struct {
		name    string
		answers Answers
		refused string // the question reported, "" for none
	}{
		{"a valid submission", Answers{"owns_phone": {"yes"}, "phone_for_reporting": {"no"},
			"tools_held": {"bicycle", "torch"}, "tools_functional": {"torch"}}, ""},
		{"no and not asked are both fine", Answers{"owns_phone": {"no"}}, ""},
		{"a branch answered while closed", Answers{"owns_phone": {"no"}, "phone_for_reporting": {"yes"}}, "phone_for_reporting"},
		{"a branch answered with its opener unasked", Answers{"incentive_frequency": {"monthly"}}, "incentive_frequency"},
		{"two answers to one question", Answers{"owns_phone": {"yes", "no"}}, "owns_phone"},
		{"a functional tool not held", Answers{"tools_held": {"bicycle"}, "tools_functional": {"torch"}}, "tools_functional"},
		{"none beside a tool", Answers{"tools_held": {"none", "bicycle"}}, "tools_held"},
		{"none functional while holding tools", Answers{"tools_held": {"bicycle"}, "tools_functional": {"none"}}, ""},
		{"out of range", Answers{"households_served": {"1"}}, "households_served"},
		{"a question the profile lacks", Answers{"favourite_colour": {"red"}}, "favourite_colour"},
	}
	for _, c := range cases {
		problems := p.Check(c.answers)
		switch {
		case c.refused == "" && len(problems) > 0:
			t.Errorf("%s: refused %+v", c.name, problems)
		case c.refused != "" && (len(problems) == 0 || problems[0].Question != c.refused):
			t.Errorf("%s: problems %+v, want one on %s", c.name, problems, c.refused)
		}
	}
}

// A form posts every field, hidden or not; what sits in a closed branch is the
// form's residue and is dropped, not refused.
func TestPruneDropsClosedBranches(t *testing.T) {
	p := baseline()
	got := p.Prune(Answers{
		"owns_phone": {"no"}, "phone_for_reporting": {"yes"},
		"receives_incentive": {"yes"}, "incentive_frequency": {"monthly"},
		"tools_held": {"bicycle"}, "tools_functional": {"bicycle", "torch"},
		"households_served": {},
	})
	if _, ok := got["phone_for_reporting"]; ok {
		t.Error("an answer in a closed branch survived")
	}
	if got.One("incentive_frequency") != "monthly" {
		t.Error("an answer in an open branch was dropped")
	}
	if strings.Join(got["tools_functional"], ";") != "bicycle" {
		t.Errorf("tools_functional = %v, want the held one only", got["tools_functional"])
	}
	if _, ok := got["households_served"]; ok {
		t.Error("an empty answer survived as an answered question")
	}
	if problems := p.Check(got); len(problems) > 0 {
		t.Errorf("a pruned submission still fails Check: %+v", problems)
	}
}

func TestAgeComesFromTheBirthDate(t *testing.T) {
	dob := time.Date(1990, time.October, 3, 0, 0, 0, 0, time.UTC)
	p := Person{DOB: &dob}
	if got := *p.AgeOn(time.Date(2026, time.October, 2, 0, 0, 0, 0, time.UTC)); got != 35 {
		t.Errorf("the day before the birthday: %d, want 35", got)
	}
	if got := *p.AgeOn(time.Date(2026, time.October, 3, 0, 0, 0, 0, time.UTC)); got != 36 {
		t.Errorf("on the birthday: %d, want 36", got)
	}
	if (Person{}).AgeOn(time.Now()) != nil {
		t.Error("a person with no birth date has an age")
	}
	// An age stated today estimates a birth date that gives that age back,
	// whatever the day of the year.
	for _, on := range []time.Time{
		time.Date(2026, time.January, 2, 0, 0, 0, 0, time.UTC),
		time.Date(2026, time.December, 30, 0, 0, 0, 0, time.UTC),
	} {
		est := EstimateDOB(30, on)
		if got := *(Person{DOB: &est}).AgeOn(on); got != 30 && got != 29 {
			t.Errorf("an estimate from 30 on %s gives %d", on.Format(time.DateOnly), got)
		}
	}
}

func TestPersonDetailsPhone(t *testing.T) {
	yes, no := true, false
	d := PersonDetails{Contacts: []Contact{
		{Kind: ContactEmail, Value: "a@b.ug", IsPrimary: true},
		{Kind: ContactPhone, Value: "700111222", Owned: &no},
		{Kind: ContactPhone, Value: "772123456", Owned: &yes},
	}}
	if got := d.Phone(); got != "772123456" {
		t.Errorf("Phone = %q, want their own over a borrowed one", got)
	}
	if got := (PersonDetails{}).Phone(); got != "" {
		t.Errorf("Phone = %q with none recorded", got)
	}
}
