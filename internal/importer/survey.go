package importer

import (
	"context"
	"fmt"
	"strings"

	"hwr/internal/domain"
)

// Survey is the questionnaire the survey columns answer — the CHW baseline —
// and the cadres it applies to.
type Survey struct {
	Profile domain.Profile
	Cadres  []int16
}

// Applies reports whether a worker in this cadre answers the survey.
func (s Survey) Applies(cadreID int16) bool {
	for _, id := range s.Cadres {
		if id == cadreID {
			return true
		}
	}
	return false
}

// PersonRecord is what the survey columns say about the person rather than
// about their work: the phones, the highest education, and English. Each lands
// on the person's own records (persons' satellites), not on a submission.
type PersonRecord struct {
	// PhoneOwn is the number of a phone they own; PhoneAlternate one they can
	// be reached on that is someone else's. The form asks which applies, so a
	// row carries one or the other.
	PhoneOwn       string                `json:"phone_own,omitempty"`
	PhoneAlternate string                `json:"phone_alternate,omitempty"`
	Education      domain.EducationLevel `json:"education,omitempty"`
	English        *EnglishRecord        `json:"english,omitempty"`
}

// EnglishRecord is English graded per skill. The source form asks a
// multi-select (speak / read / write / none), so a skill named is graded
// basic — the least the form's answer asserts — and a skill not named, on a
// row that answered at all, is none.
type EnglishRecord struct {
	Understanding domain.Proficiency `json:"understanding"`
	Reading       domain.Proficiency `json:"reading"`
	Writing       domain.Proficiency `json:"writing"`
}

// Answered reports whether the row said anything about the person.
func (p PersonRecord) Answered() bool {
	return p.PhoneOwn != "" || p.PhoneAlternate != "" || p.Education != "" || p.English != nil
}

// surveyFields reads the optional attributes of one row: the answers to the
// baseline survey, and the person's details beside them.
//
// A bad value here refuses the whole row, as it does anywhere else in the file.
// The alternative — importing the worker and dropping the field that would not
// parse — is what the survey *form* does when a hidden branch is posted, and it
// is exactly wrong on an import: nothing is dropped silently, and a district
// that wrote a phone number is owed either the number or a reason.
func surveyFields(r Row, survey domain.Profile) (domain.Answers, PersonRecord, []domain.Problem) {
	var problems []domain.Problem
	add := func(field string, code domain.ProblemCode, format string, args ...any) {
		problems = append(problems, domain.Problem{
			Field: field, Code: code, Message: fmt.Sprintf(format, args...)})
	}

	answers := domain.Answers{}
	columnOf := map[string]string{}
	for _, c := range surveyColumns {
		columnOf[c.Question] = c.Column
		raw := r.Value(c.Column)
		if raw == "" {
			continue
		}
		q, ok := survey.Question(c.Question)
		if !ok || !q.Active {
			add(c.Column, domain.ProblemBadValue,
				"The survey no longer asks this; leave %s blank.", c.Column)
			continue
		}
		values, err := q.Parse(raw)
		if err != nil {
			add(c.Column, domain.ProblemBadValue, "%s", err.Error())
			continue
		}
		if len(values) > 0 {
			answers[c.Question] = values
		}
	}
	// The survey's own rules across columns: a branch answered without the
	// answer that opens it, a subset outside its parent, `none` beside a choice.
	// Reported against the column, in the column's words.
	for _, p := range survey.Check(answers) {
		column := columnOf[p.Question]
		if column == "" {
			column = p.Question
		}
		add(column, domain.ProblemBadValue, "%s", p.Message)
	}

	var person PersonRecord
	person.PhoneOwn = phoneOrProblem(r, ColPhonePrimary, add)
	person.PhoneAlternate = phoneOrProblem(r, ColPhoneAlternate, add)
	// The form's own branch: the two numbers are alternatives, not two lines
	// for one person, and which one is asked depends on owning a phone.
	switch owns := answers.One("owns_phone"); {
	case owns == "yes" && person.PhoneAlternate != "":
		add(ColPhoneAlternate, domain.ProblemBadValue,
			"%s is the number for a CHW who owns no phone, and this one does. Put it in %s.",
			ColPhoneAlternate, ColPhonePrimary)
	case owns == "no" && person.PhoneOwn != "":
		add(ColPhonePrimary, domain.ProblemBadValue,
			"This CHW owns no phone, so their number belongs in %s.", ColPhoneAlternate)
	}

	if raw := r.Value(ColEducation); raw != "" {
		if level, ok := domain.ParseEducation(raw); ok {
			person.Education = level
		} else {
			add(ColEducation, domain.ProblemBadValue,
				"%q is not one of none, ple, uce, uace or tertiary.", raw)
		}
	}
	person.English = parseEnglish(r.Value(ColEnglish), add)

	return answers, person, problems
}

type addProblem func(field string, code domain.ProblemCode, format string, args ...any)

// parseEnglish reads the proficiency multi-select. `none` is a recorded none
// on all three; a blank cell is no record at all.
func parseEnglish(raw string, add addProblem) *EnglishRecord {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	e := EnglishRecord{domain.ProficiencyNone, domain.ProficiencyNone, domain.ProficiencyNone}
	for _, part := range splitList(raw) {
		switch strings.ToLower(part) {
		case "none":
			// Already all none; naming it alongside a skill is a
			// contradiction rather than a preference.
		case "speak", "speaks", "ably speak":
			e.Understanding = domain.ProficiencyBasic
		case "read", "reads", "ably read":
			e.Reading = domain.ProficiencyBasic
		case "write", "writes", "ably write":
			e.Writing = domain.ProficiencyBasic
		default:
			add(ColEnglish, domain.ProblemBadValue,
				"%q is not speak, read, write or none.", part)
			return nil
		}
	}
	return &e
}

// resolveFacility finds the facility a deployment is supervised by, by name,
// within its own district.
//
// The district is the deployment's, not the uploader's:
// deployments_facility_district_trg refuses a cross-district attachment, and a
// facility of that name elsewhere in the country is not the one they meant.
func (im *Importer) resolveFacility(ctx context.Context, name string, districtID int64,
	res *Resolver, add addProblem) *int64 {

	if districtID == 0 {
		// The placement did not resolve, so there is no district to search.
		// That row already carries its own problem.
		return nil
	}

	facilities, err := res.facilitiesIn(ctx, districtID)
	if err != nil {
		add(ColFacility, domain.ProblemBadValue, "The facility list could not be read. Try again.")
		return nil
	}

	wanted := normalizeName(name)
	var found []domain.Facility
	for _, f := range facilities {
		if normalizeName(f.Name) == wanted {
			found = append(found, f)
		}
	}
	switch len(found) {
	case 0:
		add(ColFacility, domain.ProblemLocationMissing,
			"No facility called %s in this district.", name)
		return nil
	case 1:
		id := found[0].ID
		return &id
	}
	add(ColFacility, domain.ProblemLocationAmbig,
		"More than one facility in this district is called %s.", name)
	return nil
}

// splitList reads a semicolon-separated cell. Commas are accepted too, because
// a spreadsheet column that is not quoted cannot hold one and someone will try
// anyway; empty members are dropped rather than reported, since a trailing
// separator is a typing habit.
func splitList(raw string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ';' || r == ',' || r == '|' }) {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// phoneOrProblem reads a number the way people say it and stores the nine
// digits the schema's CHECK insists on.
func phoneOrProblem(r Row, column string, add addProblem) string {
	raw := r.Value(column)
	if raw == "" {
		return ""
	}
	digits := phoneDigits(raw)
	if len(digits) != 9 {
		add(column, domain.ProblemBadValue,
			"%q is not a nine-digit number once the spaces, dashes and country code come off.", raw)
		return ""
	}
	return digits
}

// phoneDigits strips what people type around a number: spaces, dashes, a
// leading zero, a +256 country code.
func phoneDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	out := strings.TrimPrefix(b.String(), "256")
	return strings.TrimPrefix(out, "0")
}
