package domain

import (
	"regexp"
	"strings"
)

// Field rules shared by the worker form and the bulk importer.
//
// Only the rules live here, not the messages: a form says "Enter the first
// name" and an import report says `row 412 · first_name · empty`. Sharing the
// rule is what stops the two from drifting, so that the importer can never
// accept what the form refuses; sharing the sentence would make both worse.
//
// The cleanup around a rule is the caller's, and it differs on purpose. A
// spreadsheet cell collects spaces and stray punctuation that a form field
// does not, so the importer tidies harder before it asks.

// Age bounds, mirroring the CHECK on health_workers.age_years. The lower bound
// is the source form's own: a community health worker is an adult.
const (
	MinAge = 18
	MaxAge = 99
)

// ninPattern is the CHECK on health_workers.nin, repeated so a typo comes back
// as a field message rather than a constraint violation. The schema is still
// the enforcement.
var ninPattern = regexp.MustCompile(`^[A-Z]{2}[A-Z0-9]{11}[A-Z]$`)

// ValidNIN reports whether a NIN has the national form: fourteen characters,
// two letters, eleven letters or digits, then a letter. Callers uppercase
// first; the pattern does not do it for them, because a value that has to be
// changed to pass is a value worth reporting as changed.
func ValidNIN(nin string) bool { return ninPattern.MatchString(nin) }

// ValidAge reports whether an age is one the register will hold.
func ValidAge(years int) bool { return years >= MinAge && years <= MaxAge }

// ParseSex reads a sex from a source that spells it however it likes. A single
// letter is what a paper form collects and what a clerk types.
func ParseSex(s string) (Sex, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "male", "m":
		return SexMale, true
	case "female", "f":
		return SexFemale, true
	}
	return "", false
}

// ParseTriState reads a yes / no / not-answered cell. The distinction is the
// register's own: every profile column is nullable because "no" and "not asked"
// are different answers, and an imported record that answered nothing must not
// come back as one that answered no.
func ParseTriState(s string) (*bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return nil, true
	case "yes", "y", "true", "t", "1":
		yes := true
		return &yes, true
	case "no", "n", "false", "f", "0":
		no := false
		return &no, true
	}
	return nil, false
}

// ParseEducation reads the highest level completed. The enum values are the
// Ugandan certificates; the aliases are what someone writes when they have not
// read the template.
func ParseEducation(s string) (EducationLevel, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "none", "no formal education", "no education":
		return EducationNone, true
	case "ple", "primary":
		return EducationPLE, true
	case "uce", "o level", "o-level", "ordinary", "ordinary level", "secondary":
		return EducationUCE, true
	case "uace", "a level", "a-level", "advanced", "advanced level":
		return EducationUACE, true
	case "tertiary", "university", "college":
		return EducationTertiary, true
	}
	return "", false
}

// ParseIncentiveFrequency reads how often an incentive arrives, not how much.
func ParseIncentiveFrequency(s string) (IncentiveFrequency, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "monthly", "month":
		return IncentiveMonthly, true
	case "quarterly", "quarter":
		return IncentiveQuarterly, true
	case "annually", "annual", "yearly", "year":
		return IncentiveAnnually, true
	case "one_off", "one off", "one-off", "once", "oneoff":
		return IncentiveOneOff, true
	}
	return "", false
}
