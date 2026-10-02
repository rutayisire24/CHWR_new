package domain

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

// rank orders the levels, so the highest of several can be found.
func (e EducationLevel) rank() int {
	for i, known := range EducationLevels {
		if e == known {
			return i
		}
	}
	return -1
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

// Facility is a health facility as the importer and the picker see it. The
// register loads the whole Master Facility List; which ones a deployment may
// attach to is decided by district, not by this shape.
type Facility struct {
	ID        int64
	Name      string
	Ownership string
}

// Tool is a row of the `tools` vocabulary — kit a health worker may be given.
type Tool struct {
	ID    int16
	Code  string
	Label string
}

// Service is a row of the `services` vocabulary — what a health worker may
// report having given.
type Service struct {
	ID    int16
	Code  string
	Label string
}
