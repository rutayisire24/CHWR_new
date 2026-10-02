package importer

import (
	"strings"
	"testing"

	"hwr/internal/auth"
	"hwr/internal/domain"
)

// profileHeader is the core columns plus every survey column, which is what
// the downloaded template carries.
var profileHeader = strings.Join([]string{
	"first_name", "last_name", "sex", "cadre", "age_years", "nin",
	"district", "subcounty", "parish", "village", "location_code",
	"phone_owner", "phone_primary", "phone_for_reporting", "phone_alternate",
	"facility", "service_start_year", "households_served", "education",
	"english", "other_languages", "receives_incentive", "incentive_frequency",
	"incentive_amount_ugx", "tools", "tools_functional", "services", "trained",
}, ",") + "\n"

// core is a clean VHT in ABIM; the survey cells are appended per case.
const core = `Grace,Okello,f,vht,34,,ABIM,MORULEM,ALEREK,KANU-EAST,,`

// profile validates one row: the core columns above, then the survey cells.
func profile(t *testing.T, cells string, lookup *fakeLookup) Staged {
	t.Helper()
	if lookup == nil {
		lookup = &fakeLookup{}
	}
	f, err := ReadCSV("p.csv", strings.NewReader(profileHeader+core+cells+"\n"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	staged, err := New(lookup, auth.National()).Validate(t.Context(), f)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	return only(t, staged)
}

// A file that carries the survey columns and leaves them empty records
// nothing at all. "Nothing recorded" and "recorded as nothing" are different
// answers, and a submission of empties would claim the second.
func TestEmptySurveyColumnsRecordNothing(t *testing.T) {
	s := profile(t, ",,,,,,,,,,,,,,,,", nil)
	if s.Row.Status != domain.RowReady {
		t.Fatalf("status = %s (%v)", s.Row.Status, s.Row.Problems)
	}
	if s.Record.Answers.Answered() || s.Record.Person.Answered() {
		t.Errorf("an empty survey was recorded as answered: %+v %+v", s.Record.Answers, s.Record.Person)
	}
}

func TestSurveyFieldsParse(t *testing.T) {
	// phone_owner, phone_primary, phone_for_reporting, phone_alternate,
	// facility, service_start_year, households_served, education, english,
	// other_languages, receives_incentive, incentive_frequency,
	// incentive_amount_ugx, tools, tools_functional, services, trained
	s := profile(t, `yes,0772 123-456,yes,,MORULEM HC III,2019,"1,250",uce,speak;read,`+
		`"Luo, Ateso",yes,monthly,"25,000",bicycle;gumboots,bicycle,iccm;nutrition,iccm`, nil)

	if s.Row.Status != domain.RowReady {
		t.Fatalf("status = %s (%v)", s.Row.Status, s.Row.Problems)
	}
	a, person := s.Record.Answers, s.Record.Person
	want := domain.Answers{
		"owns_phone": {"yes"}, "phone_for_reporting": {"yes"},
		"service_start_year": {"2019"},
		// Thousands separators are how a spreadsheet writes a number.
		"households_served": {"1250"},
		// The free text is kept verbatim.
		"other_languages":    {"Luo, Ateso"},
		"receives_incentive": {"yes"}, "incentive_frequency": {"monthly"}, "incentive_amount_ugx": {"25000"},
		"tools_held": {"bicycle", "gumboots"}, "tools_functional": {"bicycle"},
		"services_provided": {"iccm", "nutrition"}, "services_trained": {"iccm"},
	}
	for code, values := range want {
		if strings.Join(a[code], ";") != strings.Join(values, ";") {
			t.Errorf("%s = %v, want %v", code, a[code], values)
		}
	}
	if len(a) != len(want) {
		t.Errorf("answers = %v, want exactly %v", a, want)
	}
	// The facility is an attachment on the posting, not a survey answer, so it
	// lands on the deployment section of the record.
	if s.Record.Deployment.FacilityID == nil || *s.Record.Deployment.FacilityID != 101 {
		t.Errorf("facility = %v, want 101", s.Record.Deployment.FacilityID)
	}
	// The phone and the schooling are the person's, not the survey's.
	if person.PhoneOwn != "772123456" || person.PhoneAlternate != "" || person.Education != domain.EducationUCE {
		t.Errorf("person = %+v", person)
	}
	// English is a proficiency multi-select: speaks and reads but does not
	// write is a real answer, and the third is a recorded none, not a blank.
	if e := person.English; e == nil || e.Understanding != domain.ProficiencyBasic ||
		e.Reading != domain.ProficiencyBasic || e.Writing != domain.ProficiencyNone {
		t.Errorf("english = %+v", person.English)
	}
}

// The register stores nine digits. People write the number the way they say it.
func TestPhoneNumbersAreTidiedNotRefused(t *testing.T) {
	for _, written := range []string{"0772123456", "772123456", "+256 772 123 456", "0772-123-456"} {
		s := profile(t, "yes,"+written+",,,,,,,,,,,,,,,", nil)
		if s.Row.Status != domain.RowReady {
			t.Errorf("%q was refused: %v", written, s.Row.Problems)
			continue
		}
		if got := s.Record.Person.PhoneOwn; got != "772123456" {
			t.Errorf("%q stored as %q", written, got)
		}
	}

	s := profile(t, "yes,12345,,,,,,,,,,,,,,,", nil)
	if !hasCode(s, domain.ProblemBadValue) {
		t.Errorf("a five-digit number was accepted: %v", codes(s))
	}
}

// The two numbers are alternatives, not two lines for one person. The survey
// form silently drops the crossing value; an import must not, because nothing
// is dropped silently.
func TestPhoneBranchIsRefusedNotDropped(t *testing.T) {
	// Owns a phone, and an alternate number as well.
	s := profile(t, "yes,772123456,,772999888,,,,,,,,,,,,,", nil)
	if !hasCode(s, domain.ProblemBadValue) {
		t.Fatalf("codes = %v", codes(s))
	}
	if !strings.Contains(s.Row.Summary(), ColPhonePrimary) {
		t.Errorf("the message does not say where the number belongs: %q", s.Row.Summary())
	}

	// Owns no phone, but a primary number is given.
	s = profile(t, "no,772123456,,,,,,,,,,,,,,,", nil)
	if !hasCode(s, domain.ProblemBadValue) {
		t.Errorf("codes = %v", codes(s))
	}

	// Owns no phone, but a reporting flag is given: there is no phone to report on.
	s = profile(t, "no,,yes,772999888,,,,,,,,,,,,,", nil)
	if !hasCode(s, domain.ProblemBadValue) || !onField(s, ColPhoneReporting) {
		t.Errorf("codes = %v, problems %+v", codes(s), s.Row.Problems)
	}

	// The legitimate no-phone shape.
	s = profile(t, "no,,,772999888,,,,,,,,,,,,,", nil)
	if s.Row.Status != domain.RowReady || s.Record.Person.PhoneAlternate != "772999888" {
		t.Errorf("a CHW with no phone and a fallback number was refused: %v", s.Row.Problems)
	}
}

// An amount or a frequency is a detail of a yes and means nothing without one.
func TestIncentiveDetailsNeedTheYes(t *testing.T) {
	for _, cells := range []string{
		",,,,,,,,,,no,monthly,,,,,",    // no, with a frequency
		",,,,,,,,,,no,,25000,,,,",      // no, with an amount
		",,,,,,,,,,,monthly,25000,,,,", // not asked, with both
	} {
		s := profile(t, cells, nil)
		if !hasCode(s, domain.ProblemBadValue) {
			t.Errorf("%q gave %v, want a refusal", cells, codes(s))
		}
	}

	s := profile(t, ",,,,,,,,,,yes,yearly,50000,,,,", nil)
	if s.Row.Status != domain.RowReady || s.Record.Answers.One("incentive_frequency") != "annually" {
		t.Errorf("a complete incentive answer, frequency by alias: %v %v", s.Row.Problems, s.Record.Answers)
	}

	// The bounds are the question's own.
	s = profile(t, ",,,,,,,,,,yes,monthly,999,,,,", nil)
	if !hasCode(s, domain.ProblemBadValue) {
		t.Errorf("an amount below the minimum was accepted")
	}
}

// Trained is choice-filtered to the services offered, and the importer says so
// before the schema has to.
func TestTrainedMustBeAmongTheServicesOffered(t *testing.T) {
	s := profile(t, ",,,,,,,,,,,,,,,iccm,nutrition", nil)
	if !hasCode(s, domain.ProblemBadValue) {
		t.Fatalf("codes = %v", codes(s))
	}
	if !strings.Contains(s.Row.Summary(), "not among the answers") || !onField(s, ColTrained) {
		t.Errorf("the message does not say why, or where: %+v", s.Row.Problems)
	}
}

// The same nesting for tools: functionality is choice-filtered to tools held.
func TestFunctionalToolsMustBeHeld(t *testing.T) {
	s := profile(t, ",,,,,,,,,,,,,bicycle,gumboots,,", nil)
	if !hasCode(s, domain.ProblemBadValue) {
		t.Fatalf("codes = %v", codes(s))
	}

	s = profile(t, ",,,,,,,,,,,,,bicycle;gumboots,bicycle,,", nil)
	if s.Row.Status != domain.RowReady || strings.Join(s.Record.Answers["tools_functional"], ";") != "bicycle" {
		t.Fatalf("status = %s (%v), answers %v", s.Row.Status, s.Row.Problems, s.Record.Answers)
	}

	// With the column left empty, the condition was not asked at all.
	s = profile(t, ",,,,,,,,,,,,,bicycle;gumboots,,,", nil)
	if _, asked := s.Record.Answers["tools_functional"]; asked {
		t.Error("tools_functional was recorded though nobody asked")
	}
}

// Codes are what the template documents; labels are what someone writes who
// read the form instead.
func TestToolsAndServicesAcceptCodeOrLabel(t *testing.T) {
	s := profile(t, ",,,,,,,,,,,,,VHT Reporting Tools,,Maternal and Newborn Health,", nil)
	if s.Row.Status != domain.RowReady {
		t.Fatalf("status = %s (%v)", s.Row.Status, s.Row.Problems)
	}
	if got := s.Record.Answers; got.One("tools_held") != "register" || got.One("services_provided") != "maternal_newborn" {
		t.Errorf("answers = %v", got)
	}

	// A tool the survey does not offer refuses the row rather than importing
	// a partial set.
	s = profile(t, ",,,,,,,,,,,,,bicycle;helicopter,,,", nil)
	if !hasCode(s, domain.ProblemBadValue) {
		t.Errorf("codes = %v", codes(s))
	}
}

// The choice list's None is a recorded answer of nothing — which an empty cell
// is not — and it stands alone.
func TestNoneIsARecordedEmptyAnswer(t *testing.T) {
	s := profile(t, ",,,,,,,,,,,,,none,,,", nil)
	if s.Row.Status != domain.RowReady {
		t.Fatalf("status = %s (%v)", s.Row.Status, s.Row.Problems)
	}
	if got := s.Record.Answers["tools_held"]; len(got) != 1 || got[0] != domain.NoneOption {
		t.Errorf("None became %v", got)
	}
	s = profile(t, ",,,,,,,,,,,,,none;bicycle,,,", nil)
	if !hasCode(s, domain.ProblemBadValue) {
		t.Errorf("none beside a tool gave %v", codes(s))
	}
}

// english=none is a recorded none on all three, which is not the same as a
// blank cell.
func TestEnglishNoneIsARecordedNone(t *testing.T) {
	s := profile(t, ",,,,,,,,none,,,,,,,,", nil)
	if e := s.Record.Person.English; e == nil || e.Understanding != domain.ProficiencyNone ||
		e.Reading != domain.ProficiencyNone || e.Writing != domain.ProficiencyNone {
		t.Errorf("none gave %+v, want three recorded nones", s.Record.Person.English)
	}

	s = profile(t, ",,,,,,,,,,,,,,,,", nil)
	if s.Record.Person.English != nil {
		t.Errorf("a blank cell gave %+v, want no record", s.Record.Person.English)
	}

	s = profile(t, ",,,,,,,,fluent,,,,,,,,", nil)
	if !hasCode(s, domain.ProblemBadValue) {
		t.Errorf("codes = %v", codes(s))
	}
}

// The register's own export carries supervision, and it imports.
func TestSupervisionImports(t *testing.T) {
	header := strings.Join([]string{"first_name", "last_name", "sex", "cadre", "age_years", "nin",
		"district", "subcounty", "parish", "village", "location_code",
		"received_supervision", "last_supervised_on"}, ",") + "\n"
	read := func(cells string) Staged {
		f, err := ReadCSV("s.csv", strings.NewReader(header+core+cells+"\n"))
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		staged, err := New(&fakeLookup{}, auth.National()).Validate(t.Context(), f)
		if err != nil {
			t.Fatalf("validate: %v", err)
		}
		return only(t, staged)
	}
	if s := read("yes,2026-03"); s.Row.Status != domain.RowReady || s.Record.Answers.One("last_supervised_on") != "2026-03-01" {
		t.Errorf("a supervision month: %s %v %v", s.Row.Status, s.Row.Problems, s.Record.Answers)
	}
	if s := read("no,2026-03"); !hasCode(s, domain.ProblemBadValue) {
		t.Errorf("a month with no supervision gave %v", codes(s))
	}
}

// A facility is matched inside the deployment's own district, because
// deployments_facility_district_trg refuses a cross-district attachment and a
// facility of that name elsewhere is not the one they meant.
func TestFacilityIsMatchedInItsOwnDistrict(t *testing.T) {
	s := profile(t, ",,,,GULU REGIONAL REFERRAL,,,,,,,,,,,,", nil)
	if !hasCode(s, domain.ProblemLocationMissing) {
		t.Fatalf("a facility from another district was accepted: %v", codes(s))
	}
	if !strings.Contains(s.Row.Summary(), "this district") {
		t.Errorf("message: %q", s.Row.Summary())
	}

	s = profile(t, ",,,,Morulem HC III,,,,,,,,,,,,", nil)
	if s.Row.Status != domain.RowReady {
		t.Errorf("case and spacing should not matter: %v", s.Row.Problems)
	}
}

// Two facilities of one name in one district cannot happen — facilities are
// unique on (district_id, name) — but nothing may pick one arbitrarily if it
// ever does.
func TestAmbiguousFacilityIsRefused(t *testing.T) {
	s := profile(t, ",,,,TWIN CLINIC,,,,,,,,,,,,", nil)
	if !hasCode(s, domain.ProblemLocationAmbig) {
		t.Errorf("codes = %v", codes(s))
	}
	if s.Record.Deployment.FacilityID != nil {
		t.Error("an ambiguous facility was resolved anyway")
	}
}

// A file is usually one district's worth of CHWs, so the facility list is read
// once for the whole upload rather than once a row.
func TestFacilityListIsCachedPerFile(t *testing.T) {
	lookup := &fakeLookup{}
	var body strings.Builder
	body.WriteString(profileHeader)
	for i := 0; i < 20; i++ {
		body.WriteString(core + ",,,,ABIM HOSPITAL,,,,,,,,,,,,\n")
	}
	f, err := ReadCSV("many.csv", strings.NewReader(body.String()))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, err := New(lookup, auth.National()).Validate(t.Context(), f); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if lookup.facilityCalls != 1 {
		t.Errorf("FacilitiesIn called %d times for 20 rows in one district, want 1", lookup.facilityCalls)
	}
}

// The record a commit writes is the one the report was drawn from, carried on
// the staged row rather than rebuilt.
func TestTheStagedRecordRoundTrips(t *testing.T) {
	s := profile(t, `yes,772123456,no,,ABIM HOSPITAL,2020,900,ple,write,Lugbara,`+
		`yes,once,1000,thermometer,thermometer,nutrition,nutrition`, nil)
	if s.Row.Status != domain.RowReady {
		t.Fatalf("status = %s (%v)", s.Row.Status, s.Row.Problems)
	}
	if len(s.Row.Record) == 0 {
		t.Fatal("the staged row carries no record")
	}

	back, err := DecodeRecord(s.Row.Record)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !sameRecord(back, s.Record) {
		t.Errorf("the record did not survive the round trip:\n staged  %+v\n decoded %+v", s.Record, back)
	}
	if back.Deployment.FacilityID == nil || *back.Deployment.FacilityID != 100 {
		t.Errorf("facility did not survive: %v", back.Deployment.FacilityID)
	}
}

// A refused row carries no record: there is nothing for a commit to write, and
// a leftover one would be a way for the two to disagree.
func TestARefusedRowCarriesNoRecord(t *testing.T) {
	s := profile(t, "yes,not-a-number,,,,,,,,,,,,,,,", nil)
	if s.Row.Status != domain.RowRejected {
		t.Fatalf("status = %s", s.Row.Status)
	}
	if len(s.Row.Record) != 0 {
		t.Errorf("a refused row carries a record: %s", s.Row.Record)
	}
}

// The survey is asked of the cadres it applies to. A worker in another cadre
// answering it is refused, not stored and not silently dropped.
func TestSurveyColumnsOnACadreItSkipsAreRefused(t *testing.T) {
	f, err := ReadCSV("p.csv", strings.NewReader(profileHeader+
		"Ruth,Akello,f,ha,,,ABIM,MORULEM,,,,no,,,,,,,,,,,,,,,,\n"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	staged, err := New(&fakeLookup{}, auth.National()).Validate(t.Context(), f)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	s := only(t, staged)
	if !hasCode(s, domain.ProblemProfileCategory) {
		t.Errorf("profile on a Health Assistant gave %v", codes(s))
	}
	if s.Record.Answers.Answered() {
		t.Error("a refused row still carries answers")
	}

	// The same row with the survey left blank is fine; the person's details
	// apply to anyone and are kept.
	f, _ = ReadCSV("p.csv", strings.NewReader(profileHeader+
		"Ruth,Akello,f,ha,,,ABIM,MORULEM,,,,,772123456,,,,,,uce,,,,,,,,,\n"))
	staged, _ = New(&fakeLookup{}, auth.National()).Validate(t.Context(), f)
	if s := only(t, staged); s.Row.Status != domain.RowReady || s.Record.Person.PhoneOwn != "772123456" {
		t.Errorf("blank survey on a Health Assistant: %s (%v) %+v", s.Row.Status, s.Row.Problems, s.Record.Person)
	}
}

// onField reports whether a row's problems name a column.
func onField(s Staged, column string) bool {
	for _, p := range s.Row.Problems {
		if p.Field == column {
			return true
		}
	}
	return false
}
