package domain

import "time"

// What is recorded about a person beyond who they are: how to reach them, who
// to call, what documents they hold, what they studied and where they worked,
// and which languages they use. Each hangs off persons (0003), so it is said
// once about the human being and survives whatever happens to their posting.

// ContactKind is the `contact_kind` enum.
type ContactKind string

const (
	ContactPhone   ContactKind = "phone"
	ContactEmail   ContactKind = "email"
	ContactAddress ContactKind = "address"
)

// ContactKinds lists the members in the order a form offers them.
var ContactKinds = []ContactKind{ContactPhone, ContactEmail, ContactAddress}

// Label is the human-readable kind.
func (k ContactKind) Label() string {
	switch k {
	case ContactPhone:
		return "Phone"
	case ContactEmail:
		return "Email"
	case ContactAddress:
		return "Address"
	}
	return string(k)
}

// Contact is one way to reach a person.
type Contact struct {
	ID    int64
	Kind  ContactKind
	Value string
	// Owned says whether a phone is the person's own or one they can be
	// reached on; nil for anything that is not a phone, or when not asked.
	Owned        *bool
	ForReporting *bool
	IsPrimary    bool
}

// Kin is a next of kin.
type Kin struct {
	ID           int64
	Name         string
	Relationship string
	Phone        string
	IsEmergency  bool
}

// IdentifierType is a row of `identifier_types`.
type IdentifierType struct {
	ID    int16
	Code  string
	Label string
}

// Document is an identity document other than the NIN.
type Document struct {
	ID     int64
	Type   IdentifierType
	Number string
}

// Education is one level of schooling completed.
type Education struct {
	ID            int64
	Level         EducationLevel
	Institution   string
	Qualification string
	YearCompleted *int16
}

// Course is a formal programme of study.
type Course struct {
	ID            int64
	Course        string
	Institution   string
	Qualification string
	StartedOn     *time.Time
	CompletedOn   *time.Time
}

// Training is in-service training.
type Training struct {
	ID        int64
	Title     string
	Provider  string
	StartedOn *time.Time
	EndedOn   *time.Time
	Certified *bool
}

// WorkHistory is work done before or outside the register.
type WorkHistory struct {
	ID        int64
	Employer  string
	Position  string
	StartedOn *time.Time
	EndedOn   *time.Time
}

// Language is a row of `languages`.
type Language struct {
	ID    int16
	Code  string
	Label string
}

// LanguageEnglish is the code of the one language the CHW survey asks about by
// name.
const LanguageEnglish = "english"

// Proficiency is the `proficiency` enum. Empty is "not asked"; ProficiencyNone
// is "asked, cannot".
type Proficiency string

const (
	ProficiencyNone   Proficiency = "none"
	ProficiencyBasic  Proficiency = "basic"
	ProficiencyGood   Proficiency = "good"
	ProficiencyFluent Proficiency = "fluent"
)

// Proficiencies lists the grades in order.
var Proficiencies = []Proficiency{ProficiencyNone, ProficiencyBasic, ProficiencyGood, ProficiencyFluent}

// Valid reports whether p is a grade; the empty "not asked" is not one.
func (p Proficiency) Valid() bool {
	switch p {
	case ProficiencyNone, ProficiencyBasic, ProficiencyGood, ProficiencyFluent:
		return true
	}
	return false
}

// Can reports whether the grade says the person can do it at all.
func (p Proficiency) Can() bool { return p.Valid() && p != ProficiencyNone }

// Label is the human-readable grade.
func (p Proficiency) Label() string {
	switch p {
	case ProficiencyNone:
		return "None"
	case ProficiencyBasic:
		return "Basic"
	case ProficiencyGood:
		return "Good"
	case ProficiencyFluent:
		return "Fluent"
	}
	return "Not asked"
}

// LanguageSkill is one language a person uses, graded per skill.
type LanguageSkill struct {
	ID            int64
	Language      Language
	Understanding Proficiency
	Reading       Proficiency
	Writing       Proficiency
}

// PersonDetails is everything recorded about a person beyond the persons row.
type PersonDetails struct {
	Contacts    []Contact
	Kin         []Kin
	Documents   []Document
	Education   []Education
	Courses     []Course
	Training    []Training
	WorkHistory []WorkHistory
	Languages   []LanguageSkill
}

// Phone is the number to reach the person on: their primary phone, else their
// own, else any.
func (d PersonDetails) Phone() string {
	var own, any string
	for _, c := range d.Contacts {
		if c.Kind != ContactPhone {
			continue
		}
		if c.IsPrimary {
			return c.Value
		}
		if c.Owned != nil && *c.Owned && own == "" {
			own = c.Value
		}
		if any == "" {
			any = c.Value
		}
	}
	if own != "" {
		return own
	}
	return any
}

// HighestEducation is the highest level recorded, empty when none is.
func (d PersonDetails) HighestEducation() EducationLevel {
	var best EducationLevel
	for _, e := range d.Education {
		if best == "" || e.Level.rank() > best.rank() {
			best = e.Level
		}
	}
	return best
}

// Language finds a language skill by code.
func (d PersonDetails) Language(code string) (LanguageSkill, bool) {
	for _, l := range d.Languages {
		if l.Language.Code == code {
			return l, true
		}
	}
	return LanguageSkill{}, false
}
