package domain

import (
	"strings"
	"time"
)

// EducationLevel is the `education_level` enum: the highest level completed.
type EducationLevel string

const (
	EducationNone     EducationLevel = "none"
	EducationPLE      EducationLevel = "ple"
	EducationUCE      EducationLevel = "uce"
	EducationUACE     EducationLevel = "uace"
	EducationTertiary EducationLevel = "tertiary"
)

// EducationLevels lists the members in ascending order, as the form offers them.
var EducationLevels = []EducationLevel{
	EducationNone, EducationPLE, EducationUCE, EducationUACE, EducationTertiary,
}

// Valid reports whether e is one of the enum members.
func (e EducationLevel) Valid() bool {
	for _, known := range EducationLevels {
		if e == known {
			return true
		}
	}
	return false
}

// Label spells the Ugandan certificate out: the abbreviations are unambiguous
// locally but the register is also read by people who do not know them.
func (e EducationLevel) Label() string {
	switch e {
	case EducationNone:
		return "No formal education"
	case EducationPLE:
		return "PLE — primary"
	case EducationUCE:
		return "UCE — ordinary level"
	case EducationUACE:
		return "UACE — advanced level"
	case EducationTertiary:
		return "Tertiary"
	}
	return string(e)
}

// IncentiveFrequency is the `incentive_frequency` enum: how often an incentive
// is received, not how much.
type IncentiveFrequency string

const (
	IncentiveMonthly   IncentiveFrequency = "monthly"
	IncentiveQuarterly IncentiveFrequency = "quarterly"
	IncentiveAnnually  IncentiveFrequency = "annually"
	IncentiveOneOff    IncentiveFrequency = "one_off"
)

// IncentiveFrequencies lists the members from most to least frequent.
var IncentiveFrequencies = []IncentiveFrequency{
	IncentiveMonthly, IncentiveQuarterly, IncentiveAnnually, IncentiveOneOff,
}

// Valid reports whether f is one of the enum members.
func (f IncentiveFrequency) Valid() bool {
	for _, known := range IncentiveFrequencies {
		if f == known {
			return true
		}
	}
	return false
}

// Label is the human-readable frequency.
func (f IncentiveFrequency) Label() string {
	switch f {
	case IncentiveMonthly:
		return "Monthly"
	case IncentiveQuarterly:
		return "Quarterly"
	case IncentiveAnnually:
		return "Annually"
	case IncentiveOneOff:
		return "One-off"
	}
	return string(f)
}

// Profile is `chw_profiles`: the optional survey attributes, all nullable,
// because a record imported from the ODK export may answer none of them.
//
// Pointers rather than zero values throughout: "0 households" and "not asked"
// are different answers, and the register has to be able to tell them apart.
type Profile struct {
	CHWID int64
	// Exists is false when the CHW has no profile row yet. The form treats
	// that as "nothing recorded", not as an error.
	Exists bool

	// The form asks whether the CHW owns a phone and then branches; the two
	// numbers are alternatives, not two lines for one person.
	OwnsPhone         *bool
	PhonePrimary      string
	PhoneForReporting *bool
	PhoneAlternate    string

	FacilityID       *int64
	FacilityName     string // joined for display
	ServiceStartYear *int16
	HouseholdsServed *int32
	Education        EducationLevel

	// The form collects English as a multi-select (Speak / Read / Write /
	// None), not a yes/no, so the three are kept apart.
	EnglishSpeak *bool
	EnglishRead  *bool
	EnglishWrite *bool
	// OtherLanguagesRaw is free text, kept verbatim. Parsed values belong in
	// chw_languages, which the importer fills; there is no vocabulary to pick
	// from yet.
	OtherLanguagesRaw string

	ReceivesIncentive  *bool
	IncentiveFrequency IncentiveFrequency
	IncentiveAmountUGX *int32

	// Supervision is year + month by decision: the ODK form records it per
	// service domain and carries no date at all, so this only ever fills in
	// through the web UI. The stored date is the first of the month.
	ReceivedSupervision *bool
	LastSupervisedOn    *time.Time

	UpdatedBy *int64
	UpdatedAt time.Time
}

// EnglishSummary lists the proficiencies that were recorded, in the order the
// form asks them. The form collects a multi-select, so "speaks and reads but
// does not write" is a real and common answer.
func (p Profile) EnglishSummary() string {
	var parts []string
	if isTrue(p.EnglishSpeak) {
		parts = append(parts, "speaks")
	}
	if isTrue(p.EnglishRead) {
		parts = append(parts, "reads")
	}
	if isTrue(p.EnglishWrite) {
		parts = append(parts, "writes")
	}
	return strings.Join(parts, ", ")
}

// SpeaksEnglish is the plain view of the three proficiency flags.
func (p Profile) SpeaksEnglish() bool {
	return isTrue(p.EnglishSpeak) || isTrue(p.EnglishRead) || isTrue(p.EnglishWrite)
}

// Phone is the number to reach the CHW on, whichever branch they fall in.
func (p Profile) Phone() string {
	if p.PhonePrimary != "" {
		return p.PhonePrimary
	}
	return p.PhoneAlternate
}

// Answered reports whether anything at all has been recorded, so the detail
// page can say "nothing recorded yet" rather than a table of dashes.
func (p Profile) Answered() bool {
	return p.Exists && (p.OwnsPhone != nil || p.FacilityID != nil || p.ServiceStartYear != nil ||
		p.HouseholdsServed != nil || p.Education != "" || p.EnglishSpeak != nil ||
		p.OtherLanguagesRaw != "" || p.ReceivesIncentive != nil || p.ReceivedSupervision != nil)
}

func isTrue(b *bool) bool { return b != nil && *b }

// Tool is a row of the `tools` vocabulary — the kit a CHW may hold.
type Tool struct {
	ID    int16
	Slug  string
	Label string
}

// CHWTool is one tool as it stands for one CHW. Functionality lives on the
// junction row because the form choice-filters it to tools already held: a
// single global flag could not say which tool is broken.
type CHWTool struct {
	Tool
	Held bool
	// Functional is nil when the tool is held but its condition was not asked.
	Functional *bool
}

// ServiceDomain is a row of the `service_domains` vocabulary.
type ServiceDomain struct {
	ID    int16
	Slug  string
	Label string
}

// CHWServiceDomain is one service domain for one CHW. The form nests the two
// questions — trained is a subset of provides — and
// `trained_implies_provides` enforces that in the schema.
type CHWServiceDomain struct {
	ServiceDomain
	Provides bool
	Trained  bool // in the last two years
}
