package domain

import (
	"regexp"
	"strings"
)

// Field rules shared by the CHW form and the bulk importer.
//
// Only the rules live here, not the messages: a form says "Enter the first
// name" and an import report says `row 412 · first_name · empty`. Sharing the
// rule is what stops the two from drifting, so that the importer can never
// accept what the form refuses; sharing the sentence would make both worse.
//
// The cleanup around a rule is the caller's, and it differs on purpose. A
// spreadsheet cell collects spaces and stray punctuation that a form field
// does not, so the importer tidies harder before it asks.

// Age bounds, mirroring the CHECK on chws.age_years. The lower bound is the
// source form's own: a CHW is an adult.
const (
	MinAge = 18
	MaxAge = 99
)

// ninPattern is the CHECK on chws.nin, repeated so a typo comes back as a
// field message rather than a constraint violation. The schema is still the
// enforcement.
var ninPattern = regexp.MustCompile(`^[A-Z]{2}[A-Z0-9]{11}[A-Z]$`)

// ValidNIN reports whether a NIN has the national form: fourteen characters,
// two letters, eleven letters or digits, then a letter. Callers uppercase
// first; the pattern does not do it for them, because a value that has to be
// changed to pass is a value worth reporting as changed.
func ValidNIN(nin string) bool { return ninPattern.MatchString(nin) }

// chwCodePattern is the CHECK on chws.chw_code: three letters of district,
// five digits of serial. The database assigns the value, so this never
// validates one on the way in — it is how a search box recognises that what was
// typed is a code and not a name.
var chwCodePattern = regexp.MustCompile(`^[A-Z]{3}[0-9]{5}$`)

// NormalizeCHWCode returns s as a canonical CHW code and reports whether it is
// one. Reading is forgiving on purpose: a code arrives copied off a printed
// list or said down a phone, so case, spaces and the hyphen someone added to
// make it readable are all stripped before the shape is checked.
//
// Unlike ValidNIN this normalizes for its caller. A NIN that had to be changed
// to pass is worth reporting as changed; a code that had to be changed to be
// looked up is just someone typing.
func NormalizeCHWCode(s string) (string, bool) {
	s = strings.ToUpper(strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' || r == '\t' {
			return -1
		}
		return r
	}, s))
	return s, chwCodePattern.MatchString(s)
}

// ValidAge reports whether an age is one the register will hold.
func ValidAge(years int) bool { return years >= MinAge && years <= MaxAge }

// ParseCadre reads a cadre from a source that spells it however it likes. The
// register stores a closed two-value enum, but the ODK form's choice list was
// written by hand and the export carries "VHT", "vht", "CHEW" and "CHW" for
// the same two things — see docs/odk-mapping.md.
//
// It does not decide what to do with a value carrying two cadres or an "other":
// that is a refusal the caller reports, not a parse.
func ParseCadre(s string) (Cadre, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "vht", "village health team":
		return CadreVHT, true
	case "chew", "chw", "community health extension worker":
		return CadreCHEW, true
	}
	return "", false
}

// ParseSex reads a sex from the same kind of source. A single letter is what a
// paper form collects and what a clerk types.
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
