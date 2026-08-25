// Package importer reads a CSV or Excel upload, validates every row, and
// resolves each one's placement against the administrative hierarchy.
//
// It writes nothing. An upload produces staged rows and a verdict per row; a
// commit is a separate act by a human, and lives with the handler that offers
// it. See docs/import.md.
//
// Nothing here holds SQL. The register is reached through Lookup, which
// internal/store satisfies — which is also what lets the whole validation pass
// be tested without a database.
package importer

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strings"
)

// Column is one column of the import template.
type Column struct {
	// Name is the canonical spelling, and the one the template writes.
	Name string
	// Aliases are other spellings accepted from a file. They are the names the
	// ODK export and the districts' own workbooks already use; a district
	// should not have to rename a column to upload the list they hold.
	Aliases []string
	// Required means the file must carry the column at all. Whether a *cell*
	// must be filled is a row-level question, and cadre decides some of it.
	Required bool
	Help     string
}

// Core is the first pass's column vocabulary: the register record itself.
// The profile columns follow on the same machinery; the vocabulary is fixed in
// docs/import.md so the template does not change under people who have already
// started filling it in.
var Core = []Column{
	{Name: "first_name", Aliases: []string{"given_name", "given_names"}, Required: true,
		Help: "Required."},
	{Name: "last_name", Aliases: []string{"other_names", "surname", "family_name"}, Required: true,
		Help: "Required. The source form calls this Other Names."},
	{Name: "sex", Required: true, Help: "male or female."},
	{Name: "cadre", Aliases: []string{"chw_type", "type"}, Required: true,
		Help: "vht or chew. One only."},
	{Name: "age_years", Aliases: []string{"age"},
		Help: "18 to 99, or leave blank."},
	{Name: "nin", Aliases: []string{"national_id", "nin_alternative_no"},
		Help: "14 characters, or leave blank."},
	{Name: "district", Required: true, Help: "Required."},
	{Name: "subcounty", Aliases: []string{"sub_county"}, Required: true, Help: "Required."},
	{Name: "parish", Required: true, Help: "Required. This is where a CHEW is placed."},
	{Name: "village", Help: "Required for a VHT. Leave blank for a CHEW."},
	{Name: "location_code", Aliases: []string{"village_code", "code"},
		Help: "Optional. The official code decides the placement when it is given."},
}

// Profile is the optional survey attributes, in the order the source form asks
// them. Every one of them may be blank, and blank is not "no": an empty cell
// leaves the column NULL, and only an explicit no writes false. A record
// imported from a file that never asked about incentives must not come back as
// a CHW who said they receive none.
var Profile = []Column{
	{Name: "phone_owner", Aliases: []string{"owns_phone", "phones", "has_phone"},
		Help: "yes or no. Blank means it was not asked."},
	{Name: "phone_primary", Aliases: []string{"phone_number", "phone"},
		Help: "Their own number, if they own a phone."},
	{Name: "phone_for_reporting", Aliases: []string{"phone_reporting"},
		Help: "yes or no — is that phone used for reporting."},
	{Name: "phone_alternate", Aliases: []string{"other_number"},
		Help: "A number to reach them on when they own no phone."},
	{Name: "facility", Aliases: []string{"health_facility", "facility_name"},
		Help: "Name of the facility they report to. Must be in their own district."},
	{Name: "service_start_year", Aliases: []string{"service_year", "year_started"},
		Help: "Year of first appointment, 1960 to 2100."},
	{Name: "households_served", Aliases: []string{"households"},
		Help: "3 to 100,000."},
	{Name: "education", Help: "none, ple, uce, uace or tertiary."},
	{Name: "english", Aliases: []string{"english_proficiency"},
		Help: "Any of speak; read; write, separated by semicolons. none is a recorded no."},
	{Name: "other_languages", Aliases: []string{"other_language", "languages"},
		Help: "Free text, kept as written."},
	{Name: "receives_incentive", Aliases: []string{"recieve_financial", "receives_financial"},
		Help: "yes or no."},
	{Name: "incentive_frequency", Aliases: []string{"frequency"},
		Help: "monthly, quarterly, annually or one_off. Only with yes above."},
	{Name: "incentive_amount_ugx", Aliases: []string{"financial_incentive", "incentive_amount"},
		Help: "1,000 to 500,000 UGX. Only with yes above."},
	{Name: "tools", Aliases: []string{"tool"},
		Help: "Semicolon-separated: bicycle; gumboots; thermometer; medicine_box; muac_tape; torch; register."},
	{Name: "tools_functional", Aliases: []string{"tool_functional"},
		Help: "Which of those are working. Must be among the tools held."},
	{Name: "services", Aliases: []string{"service_domains"},
		Help: "Semicolon-separated service domains offered."},
	{Name: "trained", Aliases: []string{"training"},
		Help: "Which of those they were trained on in the last 2 years."},
}

// Canonical profile column names.
const (
	ColPhoneOwner      = "phone_owner"
	ColPhonePrimary    = "phone_primary"
	ColPhoneReporting  = "phone_for_reporting"
	ColPhoneAlternate  = "phone_alternate"
	ColFacility        = "facility"
	ColServiceYear     = "service_start_year"
	ColHouseholds      = "households_served"
	ColEducation       = "education"
	ColEnglish         = "english"
	ColOtherLanguages  = "other_languages"
	ColIncentive       = "receives_incentive"
	ColIncentiveFreq   = "incentive_frequency"
	ColIncentiveAmount = "incentive_amount_ugx"
	ColTools           = "tools"
	ColToolsFunctional = "tools_functional"
	ColServices        = "services"
	ColTrained         = "trained"
)

// All is every column the register understands, core first. There is no
// `support_supervision`: the source form records supervision per service domain
// and carries no date, so last_supervised_on fills only through the UI.
var All = append(append([]Column{}, Core...), Profile...)

// Canonical column names, so a rule reads as a name rather than a string.
const (
	ColFirstName = "first_name"
	ColLastName  = "last_name"
	ColSex       = "sex"
	ColCadre     = "cadre"
	ColAge       = "age_years"
	ColNIN       = "nin"
	ColDistrict  = "district"
	ColSubcounty = "subcounty"
	ColParish    = "parish"
	ColVillage   = "village"
	ColCode      = "location_code"
)

// canonical maps every accepted spelling, normalized, to its column name.
var canonical = func() map[string]string {
	m := make(map[string]string, len(All)*2)
	for _, c := range All {
		m[normalizeHeader(c.Name)] = c.Name
		for _, alias := range c.Aliases {
			m[normalizeHeader(alias)] = c.Name
		}
	}
	return m
}()

// normalizeHeader folds a header cell to the form the vocabulary is keyed by:
// lower case, trimmed, and any run of spaces, hyphens or underscores collapsed
// to one underscore. "First Name", "first-name" and "FIRST_NAME" are the same
// column, because they are the same column to the person who typed them.
func normalizeHeader(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastUnderscore := false
	for _, r := range s {
		switch {
		case r == ' ' || r == '-' || r == '_' || r == '\t':
			if !lastUnderscore && b.Len() > 0 {
				b.WriteByte('_')
				lastUnderscore = true
			}
		default:
			b.WriteRune(r)
			lastUnderscore = false
		}
	}
	return strings.TrimSuffix(b.String(), "_")
}

// Header is a file's first row: which canonical column each position holds,
// and the spellings the file used, in order.
type Header struct {
	// Spelled is the header exactly as the file wrote it, in order. errors.csv
	// is rebuilt from this, so a district gets their own columns back.
	Spelled []string
	// column[i] is the canonical name of position i, empty when the file's
	// column is not one of ours.
	column []string
	// index maps a canonical name to its position.
	index map[string]int
}

// ReadHeader interprets a file's first row.
func ReadHeader(cells []string) Header {
	h := Header{
		Spelled: append([]string(nil), cells...),
		column:  make([]string, len(cells)),
		index:   make(map[string]int, len(cells)),
	}
	for i, cell := range cells {
		name, ok := canonical[normalizeHeader(cell)]
		if !ok {
			continue // an unknown column is carried, not refused
		}
		h.column[i] = name
		// First spelling wins: a file with two columns mapping to one of ours
		// is answered by the leftmost, and the duplicate is reported as unused.
		if _, seen := h.index[name]; !seen {
			h.index[name] = i
		}
	}
	return h
}

// Has reports whether the file carries a column.
func (h Header) Has(name string) bool { _, ok := h.index[name]; return ok }

// ValueOf reads a canonical column out of a stored raw row, which is keyed by
// the file's own spelling. It is how anything downstream of import_rows.raw
// finds a value without knowing how the district wrote the header.
func (h Header) ValueOf(raw map[string]string, column string) string {
	i, ok := h.index[column]
	if !ok || i >= len(h.Spelled) {
		return ""
	}
	return strings.TrimSpace(raw[h.Spelled[i]])
}

// Unknown lists the file's columns that are not part of the vocabulary. They
// are ignored rather than refused — a district's own working columns should not
// stop an import — but the report names them, because nobody should assume
// they were stored.
func (h Header) Unknown() []string {
	var out []string
	for i, name := range h.column {
		if name == "" && strings.TrimSpace(h.Spelled[i]) != "" {
			out = append(out, h.Spelled[i])
		}
	}
	return out
}

// Missing lists the required columns the file does not carry.
//
// The placement columns are required as a set with one alternative: a file that
// carries location_code has already said where every row goes, and needing the
// four name columns beside it would be asking for the answer twice.
func (h Header) Missing() []string {
	var out []string
	for _, c := range All {
		if !c.Required || h.Has(c.Name) {
			continue
		}
		switch c.Name {
		case ColDistrict, ColSubcounty, ColParish:
			if h.Has(ColCode) {
				continue
			}
		}
		out = append(out, c.Name)
	}
	return out
}

// Template is the blank file offered for download. districtName is the
// uploader's own district, filled into the one example row so the column is
// shown holding the value it wants; a national uploader gets it empty.
//
// The example row carries no name, no NIN and no cadre, which is exactly the
// shape Row.Blank refuses to import — so a template uploaded with rows added
// beneath it does not also import its own example.
func Template(districtName string) []byte {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)

	header := make([]string, len(All))
	example := make([]string, len(All))
	for i, c := range All {
		header[i] = c.Name
		if c.Name == ColDistrict {
			example[i] = districtName
		}
	}
	_ = w.Write(header)
	_ = w.Write(example)
	w.Flush()
	return buf.Bytes()
}

// TemplateFilename names the download. A file called template.csv in a
// downloads folder beside eleven others is a file nobody can find again.
func TemplateFilename(districtName string) string {
	if districtName == "" {
		return "chw-import-template.csv"
	}
	return fmt.Sprintf("chw-import-template-%s.csv",
		strings.ToLower(strings.ReplaceAll(districtName, " ", "-")))
}
