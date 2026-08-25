package importer

import (
	"strings"
	"testing"

	"chwr/internal/auth"
	"chwr/internal/domain"
)

// profileHeader is the core columns plus every profile column, which is what
// the downloaded template carries.
var profileHeader = strings.Join([]string{
	"first_name", "last_name", "sex", "cadre", "age_years", "nin",
	"district", "subcounty", "parish", "village", "location_code",
	"phone_owner", "phone_primary", "phone_for_reporting", "phone_alternate",
	"facility", "service_start_year", "households_served", "education",
	"english", "other_languages", "receives_incentive", "incentive_frequency",
	"incentive_amount_ugx", "tools", "tools_functional", "services", "trained",
}, ",") + "\n"

// core is a clean VHT in ABIM; the profile cells are appended per case.
const core = `Grace,Okello,f,vht,34,,ABIM,MORULEM,ALEREK,KANU-EAST,,`

// profile validates one row: the core columns above, then the profile cells.
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

// A file that carries the profile columns and leaves them empty writes no
// profile at all. "Nothing recorded" and "recorded as nothing" are different
// answers, and an all-null row would claim the second.
func TestEmptyProfileColumnsRecordNothing(t *testing.T) {
	s := profile(t, ",,,,,,,,,,,,,,,,", nil)
	if s.Row.Status != domain.RowReady {
		t.Fatalf("status = %s (%v)", s.Row.Status, s.Row.Problems)
	}
	if s.Record.Profile.Answered() {
		t.Errorf("an empty profile was recorded as answered: %+v", s.Record.Profile)
	}
}

func TestProfileFieldsParse(t *testing.T) {
	// phone_owner, phone_primary, phone_for_reporting, phone_alternate,
	// facility, service_start_year, households_served, education, english,
	// other_languages, receives_incentive, incentive_frequency,
	// incentive_amount_ugx, tools, tools_functional, services, trained
	s := profile(t, `yes,0772 123-456,yes,,MORULEM HC III,2019,"1,250",uce,speak;read,`+
		`"Luo, Ateso",yes,monthly,"25,000",bicycle;gumboots,bicycle,iccm;nutrition,iccm`, nil)

	if s.Row.Status != domain.RowReady {
		t.Fatalf("status = %s (%v)", s.Row.Status, s.Row.Problems)
	}
	p := s.Record.Profile

	if !isTrue(p.OwnsPhone) || p.PhonePrimary != "772123456" || !isTrue(p.PhoneForReporting) {
		t.Errorf("phone: owner=%v primary=%q reporting=%v", p.OwnsPhone, p.PhonePrimary, p.PhoneForReporting)
	}
	if p.FacilityID == nil || *p.FacilityID != 101 {
		t.Errorf("facility = %v, want 101", p.FacilityID)
	}
	if p.ServiceStartYear == nil || *p.ServiceStartYear != 2019 {
		t.Errorf("service year = %v", p.ServiceStartYear)
	}
	// Thousands separators are how a spreadsheet writes a number.
	if p.HouseholdsServed == nil || *p.HouseholdsServed != 1250 {
		t.Errorf("households = %v, want 1250", p.HouseholdsServed)
	}
	if p.Education != domain.EducationUCE {
		t.Errorf("education = %q", p.Education)
	}
	// English is a proficiency multi-select: speaks and reads but does not
	// write is a real answer, and the third is a recorded no, not a null.
	if !isTrue(p.EnglishSpeak) || !isTrue(p.EnglishRead) || !isFalse(p.EnglishWrite) {
		t.Errorf("english: speak=%v read=%v write=%v", p.EnglishSpeak, p.EnglishRead, p.EnglishWrite)
	}
	if p.OtherLanguagesRaw != "Luo, Ateso" {
		t.Errorf("other languages = %q — the raw string is kept verbatim", p.OtherLanguagesRaw)
	}
	if !isTrue(p.ReceivesIncentive) || p.IncentiveFrequency != domain.IncentiveMonthly ||
		p.IncentiveAmountUGX == nil || *p.IncentiveAmountUGX != 25000 {
		t.Errorf("incentive: %v %q %v", p.ReceivesIncentive, p.IncentiveFrequency, p.IncentiveAmountUGX)
	}

	if len(p.Tools) != 2 {
		t.Fatalf("tools = %+v, want two", p.Tools)
	}
	for _, tool := range p.Tools {
		want := tool.ToolID == 1 // bicycle is the one named as working
		if tool.Functional == nil || *tool.Functional != want {
			t.Errorf("tool %d functional = %v, want %v", tool.ToolID, tool.Functional, want)
		}
	}
	if len(p.Domains) != 2 {
		t.Fatalf("domains = %+v, want two", p.Domains)
	}
	for _, d := range p.Domains {
		if !d.Provides {
			t.Errorf("domain %d not marked as provided", d.DomainID)
		}
		if want := d.DomainID == 1; d.Trained != want {
			t.Errorf("domain %d trained = %v, want %v", d.DomainID, d.Trained, want)
		}
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
		if got := s.Record.Profile.PhonePrimary; got != "772123456" {
			t.Errorf("%q stored as %q", written, got)
		}
	}

	s := profile(t, "yes,12345,,,,,,,,,,,,,,,", nil)
	if !hasCode(s, domain.ProblemBadValue) {
		t.Errorf("a five-digit number was accepted: %v", codes(s))
	}
}

// phone_branch_exclusive: the two numbers are alternatives, not two lines for
// one person. The profile form silently drops the crossing value; an import
// must not, because nothing is dropped silently.
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
	if !hasCode(s, domain.ProblemBadValue) {
		t.Errorf("codes = %v", codes(s))
	}

	// The legitimate no-phone shape.
	s = profile(t, "no,,,772999888,,,,,,,,,,,,,", nil)
	if s.Row.Status != domain.RowReady {
		t.Errorf("a CHW with no phone and a fallback number was refused: %v", s.Row.Problems)
	}
}

// incentive_details_require_yes: an amount is a detail of a yes and means
// nothing without one.
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

	s := profile(t, ",,,,,,,,,,yes,quarterly,50000,,,,", nil)
	if s.Row.Status != domain.RowReady {
		t.Errorf("a complete incentive answer was refused: %v", s.Row.Problems)
	}

	// The bounds are the schema's own.
	s = profile(t, ",,,,,,,,,,yes,monthly,999,,,,", nil)
	if !hasCode(s, domain.ProblemBadValue) {
		t.Errorf("an amount below the minimum was accepted")
	}
}

// trained_implies_provides is the form's own choice_filter as a database
// invariant, and the importer says so before the CHECK has to.
func TestTrainedMustBeAmongTheServicesOffered(t *testing.T) {
	s := profile(t, ",,,,,,,,,,,,,,,iccm,nutrition", nil)
	if !hasCode(s, domain.ProblemBadValue) {
		t.Fatalf("codes = %v", codes(s))
	}
	if !strings.Contains(s.Row.Summary(), "cannot be trained on a service they do not offer") {
		t.Errorf("the message does not say why: %q", s.Row.Summary())
	}
}

// The same nesting for tools: functionality is choice-filtered to tools held.
func TestFunctionalToolsMustBeHeld(t *testing.T) {
	s := profile(t, ",,,,,,,,,,,,,bicycle,gumboots,,", nil)
	if !hasCode(s, domain.ProblemBadValue) {
		t.Fatalf("codes = %v", codes(s))
	}

	// A tool held but not named as working is not thereby broken — but when the
	// column was filled in at all, the ones left out are recorded as not working.
	s = profile(t, ",,,,,,,,,,,,,bicycle;gumboots,bicycle,,", nil)
	if s.Row.Status != domain.RowReady {
		t.Fatalf("status = %s (%v)", s.Row.Status, s.Row.Problems)
	}
	for _, tool := range s.Record.Profile.Tools {
		if tool.Functional == nil {
			t.Errorf("tool %d has no condition though the column was filled in", tool.ToolID)
		}
	}

	// With the column left empty, the condition was not asked at all.
	s = profile(t, ",,,,,,,,,,,,,bicycle;gumboots,,,", nil)
	for _, tool := range s.Record.Profile.Tools {
		if tool.Functional != nil {
			t.Errorf("tool %d was recorded as %v though nobody asked", tool.ToolID, *tool.Functional)
		}
	}
}

// Slugs are what the template documents; labels are what someone writes who
// read the form instead.
func TestToolsAndServicesAcceptSlugOrLabel(t *testing.T) {
	s := profile(t, ",,,,,,,,,,,,,VHT Reporting Tools,,Maternal and Newborn Health,", nil)
	if s.Row.Status != domain.RowReady {
		t.Fatalf("status = %s (%v)", s.Row.Status, s.Row.Problems)
	}
	if len(s.Record.Profile.Tools) != 1 || s.Record.Profile.Tools[0].ToolID != 7 {
		t.Errorf("tools = %+v", s.Record.Profile.Tools)
	}
	if len(s.Record.Profile.Domains) != 1 || s.Record.Profile.Domains[0].DomainID != 2 {
		t.Errorf("domains = %+v", s.Record.Profile.Domains)
	}

	// A tool the register does not know refuses the row rather than importing
	// a partial set.
	s = profile(t, ",,,,,,,,,,,,,bicycle;helicopter,,,", nil)
	if !hasCode(s, domain.ProblemBadValue) {
		t.Errorf("codes = %v", codes(s))
	}
}

// The choice list's None means "no tools" — the empty set, not a tool.
func TestNoneIsTheEmptySet(t *testing.T) {
	s := profile(t, ",,,,,,,,,,,,,none,,,", nil)
	if s.Row.Status != domain.RowReady {
		t.Fatalf("status = %s (%v)", s.Row.Status, s.Row.Problems)
	}
	if len(s.Record.Profile.Tools) != 0 {
		t.Errorf("None became %+v", s.Record.Profile.Tools)
	}
}

// english=none is a recorded no on all three, which is not the same as a blank
// cell.
func TestEnglishNoneIsARecordedNo(t *testing.T) {
	s := profile(t, ",,,,,,,,none,,,,,,,,", nil)
	p := s.Record.Profile
	if !isFalse(p.EnglishSpeak) || !isFalse(p.EnglishRead) || !isFalse(p.EnglishWrite) {
		t.Errorf("none gave %v/%v/%v, want three recorded noes", p.EnglishSpeak, p.EnglishRead, p.EnglishWrite)
	}

	s = profile(t, ",,,,,,,,,,,,,,,,", nil)
	p = s.Record.Profile
	if p.EnglishSpeak != nil || p.EnglishRead != nil || p.EnglishWrite != nil {
		t.Errorf("a blank cell gave %v/%v/%v, want three nulls", p.EnglishSpeak, p.EnglishRead, p.EnglishWrite)
	}

	s = profile(t, ",,,,,,,,fluent,,,,,,,,", nil)
	if !hasCode(s, domain.ProblemBadValue) {
		t.Errorf("codes = %v", codes(s))
	}
}

// A facility is matched inside the CHW's own district, because
// chw_profiles_facility_district refuses a cross-district attachment in both
// directions and a facility of that name elsewhere is not the one they meant.
func TestFacilityIsMatchedInTheCHWsOwnDistrict(t *testing.T) {
	s := profile(t, ",,,,GULU REGIONAL REFERRAL,,,,,,,,,,,,", nil)
	if !hasCode(s, domain.ProblemLocationMissing) {
		t.Fatalf("a facility from another district was accepted: %v", codes(s))
	}
	if !strings.Contains(s.Row.Summary(), "this CHW's district") {
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
	if s.Record.Profile.FacilityID != nil {
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
		`yes,one_off,1000,thermometer,thermometer,nutrition,nutrition`, nil)
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
	if back.Profile.FacilityID == nil || *back.Profile.FacilityID != 100 {
		t.Errorf("facility did not survive: %v", back.Profile.FacilityID)
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
