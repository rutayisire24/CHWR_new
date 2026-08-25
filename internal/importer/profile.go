package importer

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"chwr/internal/domain"
)

// The optional survey attributes, as one row supplies them. Every field is a
// pointer or an empty-able value, because blank and "no" are different answers
// the whole way down: an empty cell leaves the column NULL, and only an explicit
// no writes false.
type ProfileRecord struct {
	OwnsPhone         *bool  `json:"owns_phone,omitempty"`
	PhonePrimary      string `json:"phone_primary,omitempty"`
	PhoneForReporting *bool  `json:"phone_for_reporting,omitempty"`
	PhoneAlternate    string `json:"phone_alternate,omitempty"`

	FacilityID       *int64                `json:"facility_id,omitempty"`
	ServiceStartYear *int16                `json:"service_start_year,omitempty"`
	HouseholdsServed *int32                `json:"households_served,omitempty"`
	Education        domain.EducationLevel `json:"education,omitempty"`

	EnglishSpeak      *bool  `json:"english_speak,omitempty"`
	EnglishRead       *bool  `json:"english_read,omitempty"`
	EnglishWrite      *bool  `json:"english_write,omitempty"`
	OtherLanguagesRaw string `json:"other_languages_raw,omitempty"`

	ReceivesIncentive  *bool                     `json:"receives_incentive,omitempty"`
	IncentiveFrequency domain.IncentiveFrequency `json:"incentive_frequency,omitempty"`
	IncentiveAmountUGX *int32                    `json:"incentive_amount_ugx,omitempty"`

	Tools   []ToolChoice   `json:"tools,omitempty"`
	Domains []DomainChoice `json:"domains,omitempty"`
}

// ToolChoice is one tool held, with its condition where the file said.
type ToolChoice struct {
	ToolID     int16 `json:"tool_id"`
	Functional *bool `json:"functional,omitempty"`
}

// DomainChoice is one service domain offered, and whether they were trained on
// it in the last two years.
type DomainChoice struct {
	DomainID int16 `json:"domain_id"`
	Provides bool  `json:"provides"`
	Trained  bool  `json:"trained"`
}

// Answered reports whether the row said anything at all about the profile. A
// file carrying only the core columns writes no chw_profiles row rather than a
// row of nulls, which keeps a plain register import as fast as it was and keeps
// "nothing recorded" distinguishable from "recorded as nothing".
func (p ProfileRecord) Answered() bool {
	return p.OwnsPhone != nil || p.PhonePrimary != "" || p.PhoneForReporting != nil ||
		p.PhoneAlternate != "" || p.FacilityID != nil || p.ServiceStartYear != nil ||
		p.HouseholdsServed != nil || p.Education != "" ||
		p.EnglishSpeak != nil || p.EnglishRead != nil || p.EnglishWrite != nil ||
		p.OtherLanguagesRaw != "" || p.ReceivesIncentive != nil ||
		p.IncentiveFrequency != "" || p.IncentiveAmountUGX != nil ||
		len(p.Tools) > 0 || len(p.Domains) > 0
}

// profileFields reads the optional attributes of one row.
//
// A bad value here refuses the whole row, as it does anywhere else in the file.
// The alternative — importing the CHW and dropping the field that would not
// parse — is what the profile *form* does when a hidden branch is posted, and it
// is exactly wrong on an import: nothing is dropped silently, and a district
// that wrote a phone number is owed either the number or a reason.
func (im *Importer) profileFields(ctx context.Context, r Row, districtID int64, res *Resolver) (ProfileRecord, []domain.Problem) {
	var rec ProfileRecord
	var problems []domain.Problem
	add := func(field string, code domain.ProblemCode, format string, args ...any) {
		problems = append(problems, domain.Problem{
			Field: field, Code: code, Message: fmt.Sprintf(format, args...)})
	}

	tri := func(column string) *bool {
		raw := r.Value(column)
		value, ok := domain.ParseTriState(raw)
		if !ok {
			add(column, domain.ProblemBadValue, "%q is not yes or no.", raw)
		}
		return value
	}

	rec.OwnsPhone = tri(ColPhoneOwner)
	rec.PhoneForReporting = tri(ColPhoneReporting)
	rec.PhonePrimary = phoneOrProblem(r, ColPhonePrimary, add)
	rec.PhoneAlternate = phoneOrProblem(r, ColPhoneAlternate, add)

	// The form's own branch, as a schema constraint: the two numbers are
	// alternatives, not two lines for one person. phone_branch_exclusive would
	// raise on the crossing, and a raise reaches the operator as a 500.
	switch {
	case isTrue(rec.OwnsPhone) && rec.PhoneAlternate != "":
		add(ColPhoneAlternate, domain.ProblemBadValue,
			"%s is the number for a CHW who owns no phone, and this one does. Put it in %s.",
			ColPhoneAlternate, ColPhonePrimary)
	case isFalse(rec.OwnsPhone) && rec.PhonePrimary != "":
		add(ColPhonePrimary, domain.ProblemBadValue,
			"This CHW owns no phone, so their number belongs in %s.", ColPhoneAlternate)
	case isFalse(rec.OwnsPhone) && rec.PhoneForReporting != nil:
		add(ColPhoneReporting, domain.ProblemBadValue,
			"This CHW owns no phone, so there is no phone to report on.")
	}

	rec.ServiceStartYear = int16OrProblem(r, ColServiceYear, 1960, 2100, add)
	rec.HouseholdsServed = int32OrProblem(r, ColHouseholds, 3, 100000, add)

	if raw := r.Value(ColEducation); raw != "" {
		if level, ok := domain.ParseEducation(raw); ok {
			rec.Education = level
		} else {
			add(ColEducation, domain.ProblemBadValue,
				"%q is not one of none, ple, uce, uace or tertiary.", raw)
		}
	}

	rec.EnglishSpeak, rec.EnglishRead, rec.EnglishWrite = parseEnglish(r.Value(ColEnglish), add)
	rec.OtherLanguagesRaw = r.Value(ColOtherLanguages)

	rec.ReceivesIncentive = tri(ColIncentive)
	if raw := r.Value(ColIncentiveFreq); raw != "" {
		if freq, ok := domain.ParseIncentiveFrequency(raw); ok {
			rec.IncentiveFrequency = freq
		} else {
			add(ColIncentiveFreq, domain.ProblemBadValue,
				"%q is not monthly, quarterly, annually or one_off.", raw)
		}
	}
	rec.IncentiveAmountUGX = int32OrProblem(r, ColIncentiveAmount, 1000, 500000, add)

	// incentive_details_require_yes: an amount or a frequency is a detail of a
	// yes, and means nothing without one.
	if !isTrue(rec.ReceivesIncentive) {
		if rec.IncentiveFrequency != "" {
			add(ColIncentiveFreq, domain.ProblemBadValue,
				"A frequency needs %s to say yes.", ColIncentive)
		}
		if rec.IncentiveAmountUGX != nil {
			add(ColIncentiveAmount, domain.ProblemBadValue,
				"An amount needs %s to say yes.", ColIncentive)
		}
	}

	rec.Tools = im.parseTools(ctx, r, add)
	rec.Domains = im.parseDomains(ctx, r, add)

	if name := r.Value(ColFacility); name != "" {
		rec.FacilityID = im.resolveFacility(ctx, name, districtID, res, add)
	}

	return rec, problems
}

type addProblem func(field string, code domain.ProblemCode, format string, args ...any)

// parseEnglish reads the proficiency multi-select. The form collects Speak /
// Read / Write / None rather than a yes/no, so "speaks and reads but does not
// write" is a real and common answer and the three are kept apart.
//
// `none` is a recorded no on all three; a blank cell is three NULLs.
func parseEnglish(raw string, add addProblem) (speak, read, write *bool) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil, nil
	}

	no, yes := false, true
	speak, read, write = &no, &no, &no
	for _, part := range splitList(raw) {
		switch strings.ToLower(part) {
		case "none":
			// Already all false; naming it alongside a proficiency is a
			// contradiction rather than a preference.
		case "speak", "speaks", "ably speak":
			speak = &yes
		case "read", "reads", "ably read":
			read = &yes
		case "write", "writes", "ably write":
			write = &yes
		default:
			add(ColEnglish, domain.ProblemBadValue,
				"%q is not speak, read, write or none.", part)
			return nil, nil, nil
		}
	}
	return speak, read, write
}

// parseTools reads the kit a CHW holds, and which of it works.
//
// Functionality lives per tool because the source form choice-filters it to
// tools already held: a single global flag could not say which tool is broken.
// A tool named as working that is not held is that filter violated, and is
// refused rather than quietly added to the set.
func (im *Importer) parseTools(ctx context.Context, r Row, add addProblem) []ToolChoice {
	held := splitList(r.Value(ColTools))
	working := splitList(r.Value(ColToolsFunctional))
	if len(held) == 0 && len(working) == 0 {
		return nil
	}

	vocab, err := im.lookup.Tools(ctx)
	if err != nil {
		add(ColTools, domain.ProblemBadValue, "The tool list could not be read. Try again.")
		return nil
	}
	byName := make(map[string]int16, len(vocab)*2)
	for _, t := range vocab {
		byName[normalizeName(t.Slug)] = t.ID
		byName[normalizeName(t.Label)] = t.ID
	}

	// The choice list's `None` means "no tools" — the empty set, not a tool.
	ids, ok := lookupAll(held, byName, ColTools, "tool", add)
	if !ok {
		return nil
	}
	functional, ok := lookupAll(working, byName, ColToolsFunctional, "tool", add)
	if !ok {
		return nil
	}

	out := make([]ToolChoice, 0, len(ids))
	for _, id := range ids {
		choice := ToolChoice{ToolID: id}
		// A tool held but not named as working is not thereby broken: the
		// condition was simply not recorded for it.
		if len(working) > 0 {
			works := contains(functional, id)
			choice.Functional = &works
		}
		out = append(out, choice)
	}
	for _, id := range functional {
		if !contains(ids, id) {
			add(ColToolsFunctional, domain.ProblemBadValue,
				"%s names a tool that is not in %s.", ColToolsFunctional, ColTools)
			return nil
		}
	}
	return out
}

// parseDomains reads the services offered and the subset trained on.
//
// trained_implies_provides is the form's own choice_filter as a database
// invariant: a CHW cannot be trained on a service they do not offer.
func (im *Importer) parseDomains(ctx context.Context, r Row, add addProblem) []DomainChoice {
	provides := splitList(r.Value(ColServices))
	trained := splitList(r.Value(ColTrained))
	if len(provides) == 0 && len(trained) == 0 {
		return nil
	}

	vocab, err := im.lookup.ServiceDomains(ctx)
	if err != nil {
		add(ColServices, domain.ProblemBadValue, "The service list could not be read. Try again.")
		return nil
	}
	byName := make(map[string]int16, len(vocab)*2)
	for _, d := range vocab {
		byName[normalizeName(d.Slug)] = d.ID
		byName[normalizeName(d.Label)] = d.ID
	}

	offered, ok := lookupAll(provides, byName, ColServices, "service", add)
	if !ok {
		return nil
	}
	taught, ok := lookupAll(trained, byName, ColTrained, "service", add)
	if !ok {
		return nil
	}
	for _, id := range taught {
		if !contains(offered, id) {
			add(ColTrained, domain.ProblemBadValue,
				"%s names a service that is not in %s. A CHW cannot be trained on a service they do not offer.",
				ColTrained, ColServices)
			return nil
		}
	}

	out := make([]DomainChoice, 0, len(offered))
	for _, id := range offered {
		out = append(out, DomainChoice{DomainID: id, Provides: true, Trained: contains(taught, id)})
	}
	return out
}

// resolveFacility finds the facility a CHW reports to, by name, within their
// own district.
//
// The district is the CHW's, not the uploader's: chw_profiles_facility_district
// refuses a cross-district attachment in both directions, and a facility of that
// name elsewhere in the country is not the one they meant.
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
			"No facility called %s in this CHW's district.", name)
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

// lookupAll maps a list of names onto vocabulary ids, refusing the row on the
// first it does not know rather than importing a partial set.
func lookupAll(names []string, byName map[string]int16, column, noun string, add addProblem) ([]int16, bool) {
	var out []int16
	for _, name := range names {
		if normalizeName(name) == "NONE" {
			continue // the choice list's None means the empty set
		}
		id, ok := byName[normalizeName(name)]
		if !ok {
			add(column, domain.ProblemBadValue, "%q is not a %s the register knows.", name, noun)
			return nil, false
		}
		if !contains(out, id) {
			out = append(out, id)
		}
	}
	return out, true
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

func int16OrProblem(r Row, column string, min, max int, add addProblem) *int16 {
	n, ok := boundedInt(r, column, min, max, add)
	if !ok {
		return nil
	}
	value := int16(n)
	return &value
}

func int32OrProblem(r Row, column string, min, max int, add addProblem) *int32 {
	n, ok := boundedInt(r, column, min, max, add)
	if !ok {
		return nil
	}
	value := int32(n)
	return &value
}

// boundedInt reads a number inside the range the schema's CHECK uses, so the
// operator gets the bound rather than a constraint violation.
func boundedInt(r Row, column string, min, max int, add addProblem) (int, bool) {
	raw := r.Value(column)
	if raw == "" {
		return 0, false
	}
	n, err := strconv.Atoi(strings.ReplaceAll(strings.ReplaceAll(raw, ",", ""), " ", ""))
	switch {
	case err != nil:
		add(column, domain.ProblemBadValue, "%q is not a whole number.", raw)
		return 0, false
	case n < min || n > max:
		add(column, domain.ProblemBadValue, "%d is outside %d to %d.", n, min, max)
		return 0, false
	}
	return n, true
}

func contains(ids []int16, want int16) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func isTrue(b *bool) bool  { return b != nil && *b }
func isFalse(b *bool) bool { return b != nil && !*b }
