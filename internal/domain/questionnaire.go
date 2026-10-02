package domain

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A profile is a questionnaire a health worker answers — the CHW baseline
// survey is the first. Questions live in a shared pool; a profile picks them,
// in order, with the branching between them; a worker's answers are one dated
// submission, and what the register shows is the latest. See
// migrations/0006_questionnaire.sql.
//
// The schema checks every answer as it is written and every submission as it
// commits. The same rules are here so a form can say which field is wrong and
// an import can say which column, instead of either reaching the operator as a
// constraint violation. The schema is still the enforcement.

// DataType is a value_data_types code: what an open answer must parse as.
type DataType string

const (
	DataText      DataType = "text"
	DataBoolean   DataType = "boolean"
	DataYesNo     DataType = "yes_no"
	DataInteger   DataType = "integer"
	DataNumeric   DataType = "numeric"
	DataCharacter DataType = "character"
	DataDate      DataType = "date"
	DataMonth     DataType = "month"
)

// NoneOption is the code of a multi-select's recorded empty answer. No rows
// for a question means it was not asked; the `none` row means it was, and the
// answer was nothing. Its option id is always 0.
const NoneOption = "none"

// Option is one choice of a closed question. The ID is what a response
// stores, so it never changes; the Code is what an import and the export
// spell; the Prompt is what a form shows.
type Option struct {
	ID      int32    `json:"id"`
	Code    string   `json:"code"`
	Prompt  string   `json:"prompt"`
	Aliases []string `json:"aliases,omitempty"`
}

// Question is a question as one profile asks it: the pool's question with the
// profile's code, order and branching.
type Question struct {
	ID     int32 // profile_questions.id
	PoolID int32 // question_pool.id
	Code   string
	Prompt string
	Help   string

	Multi    bool
	Closed   bool
	DataType DataType
	Options  []Option
	Min, Max *float64
	Pattern  string

	Required  bool
	SortOrder int16
	// Active is false for a retired question: its old answers still read
	// back, but no form offers it and no import accepts it.
	Active bool
	// DependsOn names the question, in the same profile, whose answer opens
	// this one; DependsOnOption is the option code that does.
	DependsOn       string
	DependsOnOption string
	// SubsetOf names the multi-select whose answers bound this one's.
	SubsetOf string
}

// Profile is a questionnaire and its questions, in order.
type Profile struct {
	ID          int16
	Code        string
	Name        string
	Description string
	Active      bool
	Questions   []Question
}

// Answers is a submission's answers by question code. A closed answer is the
// option's code; an open one is its canonical text — a whole number without
// separators, a date as YYYY-MM-DD, a month as its first day. A question with
// no entry was not asked.
type Answers map[string][]string

// Submission is one dated set of answers to one profile.
type Submission struct {
	ID             int64
	HealthWorkerID int64
	ProfileID      int16
	CapturedOn     time.Time
	Source         string // form | import
	Answers        Answers
	CreatedBy      *int64
	CreatedOn      time.Time
}

// Exists reports whether the worker has answered this profile at all.
func (s Submission) Exists() bool { return s.ID != 0 }

// One is a single-value answer, or "" when there is none.
func (a Answers) One(code string) string {
	if v := a[code]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// Has reports whether a question was answered with a given value.
func (a Answers) Has(code, value string) bool { return slices.Contains(a[code], value) }

// Answered reports whether anything at all was answered.
func (a Answers) Answered() bool {
	for _, v := range a {
		if len(v) > 0 {
			return true
		}
	}
	return false
}

// Question finds a question by code.
func (p Profile) Question(code string) (Question, bool) {
	for _, q := range p.Questions {
		if q.Code == code {
			return q, true
		}
	}
	return Question{}, false
}

// Option finds a choice by code.
func (q Question) Option(code string) (Option, bool) {
	for _, o := range q.Options {
		if o.Code == code {
			return o, true
		}
	}
	return Option{}, false
}

// OptionByID finds a choice by the id a response stores.
func (q Question) OptionByID(id int32) (Option, bool) {
	for _, o := range q.Options {
		if o.ID == id {
			return o, true
		}
	}
	return Option{}, false
}

// Label is how an answer reads on a page: a choice's prompt, or the value.
func (q Question) Label(value string) string {
	if q.Closed {
		if o, ok := q.Option(value); ok {
			return o.Prompt
		}
	}
	if q.DataType == DataMonth {
		if t, err := time.Parse(time.DateOnly, value); err == nil {
			return t.Format("January 2006")
		}
	}
	return value
}

// Asked reports whether a question is open given the other answers: it has no
// branch, or the question its branch hangs on is itself asked and was answered
// with the option that opens it.
func (p Profile) Asked(code string, a Answers) bool {
	for range len(p.Questions) + 1 { // a chain longer than the profile is a cycle
		q, ok := p.Question(code)
		if !ok {
			return false
		}
		if q.DependsOn == "" {
			return true
		}
		if !a.Has(q.DependsOn, q.DependsOnOption) {
			return false
		}
		code = q.DependsOn
	}
	return false
}

// AnswerProblem is one answer the profile refuses, by question code.
type AnswerProblem struct {
	Question string
	Message  string
}

// Check reports every answer the schema would refuse: an unknown question, a
// value of the wrong kind or out of range, more than one answer to a single
// question, an answer inside a branch that is closed, a subset answer outside
// its parent's, and `none` beside anything else. A required question that is
// open and unanswered is reported too.
func (p Profile) Check(a Answers) []AnswerProblem {
	var out []AnswerProblem
	add := func(code, format string, args ...any) {
		out = append(out, AnswerProblem{Question: code, Message: fmt.Sprintf(format, args...)})
	}
	for code := range a {
		if _, ok := p.Question(code); !ok {
			add(code, "This profile has no question %q.", code)
		}
	}
	for _, q := range p.Questions {
		values := a[q.Code]
		if len(values) == 0 {
			if q.Required && p.Asked(q.Code, a) {
				add(q.Code, "%s needs an answer.", q.Prompt)
			}
			continue
		}
		if !q.Multi && len(values) > 1 {
			add(q.Code, "%s takes one answer.", q.Prompt)
			continue
		}
		for _, v := range values {
			if _, err := q.parseOne(v); err != nil {
				add(q.Code, "%s", err.Error())
			}
		}
		if q.Multi && slices.Contains(values, NoneOption) && len(values) > 1 {
			add(q.Code, "None is an answer on its own.")
		}
		if q.DependsOn != "" && !p.Asked(q.Code, a) {
			parent, _ := p.Question(q.DependsOn)
			opt, _ := parent.Option(q.DependsOnOption)
			add(q.Code, "This is only asked when %q is answered %s.", parent.Prompt, strings.ToLower(opt.Prompt))
		}
		if q.SubsetOf != "" {
			for _, v := range values {
				if v != NoneOption && !a.Has(q.SubsetOf, v) {
					parent, _ := p.Question(q.SubsetOf)
					add(q.Code, "%s is not among the answers to %q.", q.Label(v), parent.Prompt)
					break
				}
			}
		}
	}
	return out
}

// Prune drops what a form must not save: answers inside a branch that is
// closed and subset answers outside their parent's. A form posts every field
// on the page, hidden or not, so a value left in a hidden branch is the form's
// residue rather than the user's answer; an import gets Check instead, because
// there a value in the wrong place is something someone wrote.
func (p Profile) Prune(a Answers) Answers {
	out := make(Answers, len(a))
	for code, values := range a {
		if len(values) > 0 {
			out[code] = values
		}
	}
	for _, q := range p.Questions {
		if !p.Asked(q.Code, out) {
			delete(out, q.Code)
		}
	}
	for _, q := range p.Questions {
		if q.SubsetOf == "" || len(out[q.Code]) == 0 {
			continue
		}
		var kept []string
		for _, v := range out[q.Code] {
			if v == NoneOption || out.Has(q.SubsetOf, v) {
				kept = append(kept, v)
			}
		}
		if len(kept) == 0 {
			delete(out, q.Code)
		} else {
			out[q.Code] = kept
		}
	}
	return out
}

// Parse reads what someone typed or a file holds into canonical values. A
// blank is no answer. A multi-select splits on semicolons (commas and bars
// too, because someone will use them); a single answer is read whole.
func (q Question) Parse(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := []string{raw}
	if q.Multi {
		parts = nil
		for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ';' || r == ',' || r == '|' }) {
			if part = strings.TrimSpace(part); part != "" {
				parts = append(parts, part)
			}
		}
	}
	var out []string
	for _, part := range parts {
		v, err := q.parseOne(part)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out, nil
}

// parseOne reads one value. It is also the check Check runs on an answer that
// is already canonical, which it returns unchanged.
func (q Question) parseOne(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if q.Closed {
		if q.DataType == DataYesNo {
			if b, ok := ParseTriState(raw); ok && b != nil {
				if *b {
					return "yes", nil
				}
				return "no", nil
			}
		}
		folded := foldSeparators(raw)
		for _, o := range q.Options {
			if folded == foldSeparators(o.Code) || folded == foldSeparators(o.Prompt) {
				return o.Code, nil
			}
			for _, alias := range o.Aliases {
				if folded == foldSeparators(alias) {
					return o.Code, nil
				}
			}
		}
		codes := make([]string, len(q.Options))
		for i, o := range q.Options {
			codes[i] = o.Code
		}
		return "", fmt.Errorf("%q is not one of %s.", raw, strings.Join(codes, ", "))
	}

	switch q.DataType {
	case DataInteger, DataNumeric:
		cleaned := strings.NewReplacer(",", "", " ", "", "_", "").Replace(raw)
		n, err := strconv.ParseFloat(cleaned, 64)
		if err != nil || (q.DataType == DataInteger && n != float64(int64(n))) {
			kind := "a whole number"
			if q.DataType == DataNumeric {
				kind = "a number"
			}
			return "", fmt.Errorf("%q is not %s.", raw, kind)
		}
		if (q.Min != nil && n < *q.Min) || (q.Max != nil && n > *q.Max) {
			return "", fmt.Errorf("%s is outside %s to %s.", cleaned, bound(q.Min), bound(q.Max))
		}
		if q.DataType == DataInteger {
			return strconv.FormatInt(int64(n), 10), nil
		}
		return strconv.FormatFloat(n, 'f', -1, 64), nil
	case DataBoolean:
		if b, ok := ParseTriState(raw); ok && b != nil {
			return strconv.FormatBool(*b), nil
		}
		return "", fmt.Errorf("%q is not yes or no.", raw)
	case DataDate:
		t, ok := ParseDate(raw)
		if !ok {
			return "", fmt.Errorf("%q is not a date (YYYY-MM-DD).", raw)
		}
		return t.Format(time.DateOnly), nil
	case DataMonth:
		t, ok := ParseMonth(raw)
		if !ok {
			return "", fmt.Errorf("%q is not a month (YYYY-MM).", raw)
		}
		return t.Format(time.DateOnly), nil
	}
	if q.Pattern != "" && !matches(q.Pattern, raw) {
		return "", fmt.Errorf("%q is not in the expected form.", raw)
	}
	return raw, nil
}

func bound(f *float64) string {
	if f == nil {
		return "any"
	}
	return strconv.FormatFloat(*f, 'f', -1, 64)
}

// ParseDate reads a date as an ISO date or as day/month/year, the two forms a
// spreadsheet in Uganda produces.
func ParseDate(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	for _, layout := range []string{time.DateOnly, "2/1/2006", "02/01/2006", "2-1-2006", "2 Jan 2006", "2 January 2006"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// ParseMonth reads a month — YYYY-MM, a full date on its first day, or a month
// name and year — and returns its first day.
func ParseMonth(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	for _, layout := range []string{"2006-01", "01/2006", "1/2006", "Jan 2006", "January 2006"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, true
		}
	}
	if t, ok := ParseDate(raw); ok && t.Day() == 1 {
		return t, true
	}
	return time.Time{}, false
}

// matches applies a question's pattern. The schema applies it with
// PostgreSQL's regular expressions; the two agree on the simple patterns a
// question carries, and the schema is the one that decides.
func matches(pattern, s string) bool {
	re, err := regexp.Compile(pattern)
	return err == nil && re.MatchString(s)
}
