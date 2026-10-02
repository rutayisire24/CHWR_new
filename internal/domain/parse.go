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

// Age bounds. The lower bound is the source form's own: a community health
// worker is an adult. The schema stores a birth date and cannot hold a rule
// relative to today, so these are the register's rule alone.
const (
	MinAge = 18
	MaxAge = 99
)

// ninPattern is the CHECK on persons.nin, repeated so a typo comes back
// as a field message rather than a constraint violation. The schema is still
// the enforcement.
var ninPattern = regexp.MustCompile(`^[A-Z]{2}[A-Z0-9]{11}[A-Z]$`)

// ValidNIN reports whether a NIN has the national form: fourteen characters,
// two letters, eleven letters or digits, then a letter. Callers uppercase
// first; the pattern does not do it for them, because a value that has to be
// changed to pass is a value worth reporting as changed.
func ValidNIN(nin string) bool { return ninPattern.MatchString(nin) }

// workerCodePattern is the CHECK on health_workers.worker_code: three letters of district,
// five digits of serial. The database assigns the value, so this never
// validates one on the way in — it is how a search box recognises that what was
// typed is a code and not a name.
var workerCodePattern = regexp.MustCompile(`^[A-Z]{3}[0-9]{5}$`)

// NormalizeWorkerCode returns s as a canonical worker code and reports whether it is
// one. Reading is forgiving on purpose: a code arrives copied off a printed
// list or said down a phone, so case, spaces and the hyphen someone added to
// make it readable are all stripped before the shape is checked.
//
// Unlike ValidNIN this normalizes for its caller. A NIN that had to be changed
// to pass is worth reporting as changed; a code that had to be changed to be
// looked up is just someone typing.
func NormalizeWorkerCode(s string) (string, bool) {
	s = strings.ToUpper(strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' || r == '\t' {
			return -1
		}
		return r
	}, s))
	return s, workerCodePattern.MatchString(s)
}

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
// register's own: "no" and "not asked" are different answers, and an imported
// record that answered nothing must not come back as one that answered no.
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
