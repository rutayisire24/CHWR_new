package importer

import (
	"context"
	"strings"
	"testing"

	"chwr/internal/auth"
	"chwr/internal/domain"
)

const header = "first_name,last_name,sex,cadre,age_years,nin,district,subcounty,parish,village,location_code\n"

// file reads a CSV body with the standard header in front of it.
func file(t *testing.T, body string) *File {
	t.Helper()
	f, err := ReadCSV("upload.csv", strings.NewReader(header+body))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return f
}

// validate runs the whole pass inside a scope, against an empty register.
func validate(t *testing.T, sc auth.Scope, body string, lookup *fakeLookup) []Staged {
	t.Helper()
	if lookup == nil {
		lookup = &fakeLookup{}
	}
	staged, err := New(lookup, sc).Validate(context.Background(), file(t, body))
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	return staged
}

// only returns the single staged row a one-line body produces.
func only(t *testing.T, staged []Staged) Staged {
	t.Helper()
	if len(staged) != 1 {
		t.Fatalf("staged %d rows, want 1", len(staged))
	}
	return staged[0]
}

// codes lists a row's problem codes, for comparing against what a case expects.
func codes(s Staged) []domain.ProblemCode {
	out := make([]domain.ProblemCode, 0, len(s.Row.Problems))
	for _, p := range s.Row.Problems {
		out = append(out, p.Code)
	}
	return out
}

func hasCode(s Staged, want domain.ProblemCode) bool {
	for _, c := range codes(s) {
		if c == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- the header

func TestHeaderIsOrderIndependentAndForgiving(t *testing.T) {
	h := ReadHeader([]string{"Other Names", "FIRST-NAME", "Sex", "CHW_Type", "District",
		"Sub County", "Parish", "notes"})

	for column, position := range map[string]int{
		ColLastName: 0, ColFirstName: 1, ColSex: 2, ColCadre: 3,
		ColDistrict: 4, ColSubcounty: 5, ColParish: 6,
	} {
		if got, ok := h.index[column]; !ok || got != position {
			t.Errorf("%s at %d (%v), want %d", column, got, ok, position)
		}
	}

	// A district's own working column is carried, not refused — but it is
	// named, so nobody assumes it was stored.
	if unknown := h.Unknown(); len(unknown) != 1 || unknown[0] != "notes" {
		t.Errorf("Unknown() = %v, want [notes]", unknown)
	}
}

// A file missing a required column is refused whole, before a row is staged:
// there is nothing to review when the columns are wrong.
func TestMissingRequiredColumnsRefusesTheFile(t *testing.T) {
	_, err := ReadCSV("x.csv", strings.NewReader("first_name,sex,cadre,district,subcounty,parish\nGrace,f,vht,ABIM,MORULEM,ALEREK\n"))
	var missing *MissingColumnsError
	if !asMissing(err, &missing) {
		t.Fatalf("err = %v, want MissingColumnsError", err)
	}
	if len(missing.Columns) != 1 || missing.Columns[0] != ColLastName {
		t.Errorf("missing = %v, want [last_name]", missing.Columns)
	}
}

// location_code says where every row goes, so requiring the name columns beside
// it would be asking for the answer twice.
func TestLocationCodeStandsInForTheNameColumns(t *testing.T) {
	f, err := ReadCSV("x.csv", strings.NewReader(
		"first_name,last_name,sex,cadre,location_code\nGrace,Okello,f,vht,09523504038002\n"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(f.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(f.Rows))
	}
}

// Excel writes a byte-order mark in front of a UTF-8 CSV. Left in place it
// becomes part of the first header cell and the file is refused for missing a
// column it plainly has.
func TestByteOrderMarkIsNotPartOfTheFirstColumn(t *testing.T) {
	body := "\xef\xbb\xbf" + header + "Grace,Okello,f,vht,,,ABIM,MORULEM,ALEREK,KANU-EAST,\n"
	f, err := ReadCSV("bom.csv", strings.NewReader(body))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !f.Header.Has(ColFirstName) {
		t.Fatal("the BOM swallowed the first column")
	}
}

// The template's own example row, and the trailing rows a spreadsheet keeps
// after someone clears their contents, carry no identity at all. They are
// skipped rather than refused.
func TestBlankRowsAreSkippedNotRefused(t *testing.T) {
	staged := validate(t, auth.National(),
		",,,,,,ABIM,,,,\n"+
			"Grace,Okello,f,vht,,,ABIM,MORULEM,ALEREK,KANU-EAST,\n"+
			",,,,,,,,,,\n", nil)

	if len(staged) != 1 {
		t.Fatalf("staged %d rows, want 1 — the template's example row was not skipped", len(staged))
	}
	if staged[0].Row.Number != 3 {
		t.Errorf("row number = %d, want 3: the number is the line in the file", staged[0].Row.Number)
	}
}

// ------------------------------------------------------------------ the row

func TestAcceptsACleanVHTAndCHEW(t *testing.T) {
	staged := validate(t, auth.National(),
		"Grace,Okello,female,vht,34,CM90210987654X,ABIM,MORULEM,ALEREK,KANU-EAST,\n"+
			"Moses,Ojok,M,CHEW,,,ABIM,MORULEM,ALEREK,,\n", nil)

	if len(staged) != 2 {
		t.Fatalf("staged %d rows, want 2", len(staged))
	}
	vht, chew := staged[0], staged[1]

	if vht.Row.Status != domain.RowReady {
		t.Fatalf("VHT status = %s (%v)", vht.Row.Status, vht.Row.Problems)
	}
	if vht.Record.LocationID != kanu {
		t.Errorf("VHT placed at %d, want the village %d", vht.Record.LocationID, kanu)
	}
	if vht.Record.Sex != domain.SexFemale || vht.Record.Cadre != domain.CadreVHT {
		t.Errorf("VHT parsed as %s/%s", vht.Record.Sex, vht.Record.Cadre)
	}
	if vht.Record.AgeYears == nil || *vht.Record.AgeYears != 34 {
		t.Errorf("age = %v, want 34", vht.Record.AgeYears)
	}

	if chew.Row.Status != domain.RowReady {
		t.Fatalf("CHEW status = %s (%v)", chew.Row.Status, chew.Row.Problems)
	}
	// Cadre decides placement: a CHEW stops at the parish.
	if chew.Record.LocationID != alerek {
		t.Errorf("CHEW placed at %d, want the parish %d", chew.Record.LocationID, alerek)
	}
	if chew.Record.Cadre != domain.CadreCHEW {
		t.Errorf("CHW read as %s, want chew", chew.Record.Cadre)
	}
}

func TestRefusesEveryBadFieldAtOnce(t *testing.T) {
	// A report that stopped at the first problem would take four uploads to
	// surface four problems in one row.
	s := only(t, validate(t, auth.National(),
		",,squid,nurse,7,NOTANIN,ABIM,MORULEM,ALEREK,KANU-EAST,\n", nil))

	if s.Row.Status != domain.RowRejected {
		t.Fatalf("status = %s", s.Row.Status)
	}
	for _, want := range []domain.ProblemCode{
		domain.ProblemRequired, domain.ProblemBadValue, domain.ProblemBadNIN,
	} {
		if !hasCode(s, want) {
			t.Errorf("missing %s in %v", want, codes(s))
		}
	}
	if len(s.Row.Problems) < 5 {
		t.Errorf("collected %d problems, want one per bad field: %v", len(s.Row.Problems), codes(s))
	}
}

// The source form allowed a multi-select with an "other" box; the register
// stores one closed enum. Keeping the first value would erase the record of a
// CHW who did not fit the model at collection time.
func TestCadreCarryingMoreThanOneIsRefusedNotTruncated(t *testing.T) {
	// The commas are doubled quotes: these are single CSV cells, not two.
	for _, cadre := range []string{"vht chew", "vht;chew", `"vht,other"`, "CHEW / VHT"} {
		s := only(t, validate(t, auth.National(),
			"Grace,Okello,f,"+cadre+",,,ABIM,MORULEM,ALEREK,KANU-EAST,\n", nil))
		if !hasCode(s, domain.ProblemCadreMulti) {
			t.Errorf("cadre %q gave %v, want cadre_multi", cadre, codes(s))
		}
		if s.Record.Cadre != "" {
			t.Errorf("cadre %q was truncated to %q", cadre, s.Record.Cadre)
		}
	}

	// A value that is not a cadre at all is a different answer.
	s := only(t, validate(t, auth.National(),
		"Grace,Okello,f,midwife,,,ABIM,MORULEM,ALEREK,KANU-EAST,\n", nil))
	if !hasCode(s, domain.ProblemBadValue) {
		t.Errorf("midwife gave %v, want bad_value", codes(s))
	}
}

func TestCadreDecidesPlacementLevel(t *testing.T) {
	// A CHEW handed a village.
	s := only(t, validate(t, auth.National(),
		"Moses,Ojok,m,chew,,,ABIM,MORULEM,ALEREK,KANU-EAST,\n", nil))
	if !hasCode(s, domain.ProblemPlacementLevel) {
		t.Errorf("CHEW with a village gave %v", codes(s))
	}
	if !strings.Contains(s.Row.Summary(), "Leave the village column blank") {
		t.Errorf("message does not say what to do: %q", s.Row.Summary())
	}

	// A VHT given only a parish.
	s = only(t, validate(t, auth.National(),
		"Grace,Okello,f,vht,,,ABIM,MORULEM,ALEREK,,\n", nil))
	if !hasCode(s, domain.ProblemPlacementLevel) {
		t.Errorf("VHT without a village gave %v", codes(s))
	}
	if !strings.Contains(s.Row.Summary(), "Name the village") {
		t.Errorf("message does not say what to do: %q", s.Row.Summary())
	}
}

// -------------------------------------------------------------- the location

func TestNamesMatchThroughPunctuationAndCase(t *testing.T) {
	s := only(t, validate(t, auth.National(),
		"Grace,Okello,f,vht,,,  abim ,Morulem,alerek,Kanu East,\n", nil))
	if s.Row.Status != domain.RowReady {
		t.Fatalf("status = %s (%v)", s.Row.Status, s.Row.Problems)
	}
	if s.Record.LocationID != kanu {
		t.Errorf("placed at %d, want KANU-EAST %d", s.Record.LocationID, kanu)
	}
}

func TestUnknownVillageIsRefusedWithItsParentNamed(t *testing.T) {
	s := only(t, validate(t, auth.National(),
		"Grace,Okello,f,vht,,,ABIM,MORULEM,ALEREK,NOWHERE,\n", nil))
	if !hasCode(s, domain.ProblemLocationMissing) {
		t.Fatalf("codes = %v", codes(s))
	}
	if !strings.Contains(s.Row.Summary(), "ALEREK") {
		t.Errorf("message does not name the parish searched: %q", s.Row.Summary())
	}
}

// Two villages in one parish genuinely share a name. Nothing guesses; the
// operator is handed both, with the chain above them and the code that settles
// it.
func TestAmbiguousVillageOffersBothCandidates(t *testing.T) {
	s := only(t, validate(t, auth.National(),
		"Grace,Okello,f,vht,,,ABIM,MORULEM,ALEREK,BUHOBA A,\n", nil))

	if !hasCode(s, domain.ProblemLocationAmbig) {
		t.Fatalf("codes = %v", codes(s))
	}
	if s.Record.LocationID != 0 {
		t.Fatal("an ambiguous name was resolved anyway")
	}
	candidates := s.Row.Problems[0].Candidates
	if len(candidates) != 2 {
		t.Fatalf("candidates = %d, want 2", len(candidates))
	}
	for _, c := range candidates {
		if c.Code == "" {
			t.Error("a candidate carries no code, so there is no way to choose it")
		}
		if !strings.Contains(c.Path, "ALEREK") {
			t.Errorf("candidate path %q does not place it", c.Path)
		}
	}
	if !strings.Contains(s.Row.Summary(), ColCode) {
		t.Errorf("message does not name the escape hatch: %q", s.Row.Summary())
	}
}

// The remedy for the case above, in the same release: the code says which.
func TestLocationCodeSettlesAnAmbiguousName(t *testing.T) {
	s := only(t, validate(t, auth.National(),
		"Grace,Okello,f,vht,,,ABIM,MORULEM,ALEREK,BUHOBA A,09523504038003\n", nil))
	if s.Row.Status != domain.RowReady {
		t.Fatalf("status = %s (%v)", s.Row.Status, s.Row.Problems)
	}
	if s.Record.LocationID != buhobaA2 {
		t.Errorf("placed at %d, want the village with code 003 (%d)", s.Record.LocationID, buhobaA2)
	}
}

func TestUnknownLocationCode(t *testing.T) {
	s := only(t, validate(t, auth.National(),
		"Grace,Okello,f,vht,,,ABIM,MORULEM,ALEREK,BUHOBA A,00000000000000\n", nil))
	if !hasCode(s, domain.ProblemCodeUnknown) {
		t.Errorf("codes = %v", codes(s))
	}
}

// Code is the identity, name is the label — but a label that contradicts the
// identity is not ignorable: one of the two is wrong and nothing in the file
// says which.
func TestLocationCodeContradictingItsNamesIsRefusedWithBothReadings(t *testing.T) {
	s := only(t, validate(t, auth.National(),
		"Grace,Okello,f,vht,,,ABIM,MORULEM,OKUDI,BUHOBA A,09523504038002\n", nil))

	if !hasCode(s, domain.ProblemCodeMismatch) {
		t.Fatalf("codes = %v", codes(s))
	}
	message := s.Row.Summary()
	if !strings.Contains(message, "ALEREK") || !strings.Contains(message, "OKUDI") {
		t.Errorf("message carries only one reading, so the fix is not one edit: %q", message)
	}
}

func TestPunctuationIsNotACodeMismatch(t *testing.T) {
	s := only(t, validate(t, auth.National(),
		"Grace,Okello,f,vht,,,abim,morulem,alerek,buhoba-a,09523504038002\n", nil))
	if s.Row.Status != domain.RowReady {
		t.Fatalf("a difference of case and punctuation was read as a contradiction: %v", s.Row.Problems)
	}
}

// -------------------------------------------------------------- the district

// The whole point: a district manager's file cannot reach another district.
func TestADistrictUploadCannotNameAnotherDistrict(t *testing.T) {
	staged := validate(t, auth.District(abim),
		"Grace,Okello,f,vht,,,ABIM,MORULEM,ALEREK,KANU-EAST,\n"+
			"Sarah,Akello,f,vht,,,GULU,BUNGATIRA,PAWEL,LAYIBI,\n", nil)

	if staged[0].Row.Status != domain.RowReady {
		t.Fatalf("the ABIM row was refused: %v", staged[0].Row.Problems)
	}
	if !hasCode(staged[1], domain.ProblemOutsideScope) {
		t.Fatalf("the GULU row gave %v, want outside_scope", codes(staged[1]))
	}
	// The report must not name the district, or its locations: a district user
	// would otherwise map the country by probing names.
	message := staged[1].Row.Summary()
	for _, leak := range []string{"GULU", "BUNGATIRA", "PAWEL", "LAYIBI"} {
		if strings.Contains(strings.ToUpper(message), leak) {
			t.Errorf("the refusal names %s, which is outside the uploader's district: %q", leak, message)
		}
	}
}

// The same rule through the escape hatch: a code is not a way around the scope.
func TestALocationCodeCannotReachAnotherDistrict(t *testing.T) {
	s := only(t, validate(t, auth.District(abim),
		"Grace,Okello,f,vht,,,,,,,10224101010001\n", nil))

	if !hasCode(s, domain.ProblemOutsideScope) {
		t.Fatalf("codes = %v, want outside_scope", codes(s))
	}
	if strings.Contains(strings.ToUpper(s.Row.Summary()), "LAYIBI") {
		t.Errorf("the refusal names the location it resolved to: %q", s.Row.Summary())
	}
}

// ------------------------------------------------------------- the register

func TestDuplicateNINAgainstTheRegisterNamesTheRecord(t *testing.T) {
	lookup := &fakeLookup{nins: map[string]domain.CHW{
		"CM90210987654X": {ID: 7, FirstName: "Betty", LastName: "Aber"},
	}}
	s := only(t, validate(t, auth.National(),
		"Grace,Okello,f,vht,,CM90210987654X,ABIM,MORULEM,ALEREK,KANU-EAST,\n", lookup))

	if !hasCode(s, domain.ProblemDuplicateNIN) {
		t.Fatalf("codes = %v", codes(s))
	}
	if !strings.Contains(s.Row.Summary(), "Betty Aber") {
		t.Errorf("the refusal does not name the record it collides with: %q", s.Row.Summary())
	}
}

// Both rows go. The first is not more correct than the second, and importing it
// would be picking a winner quietly.
func TestTwoRowsSharingANINRejectEachOther(t *testing.T) {
	staged := validate(t, auth.National(),
		"Grace,Okello,f,vht,,CM90210987654X,ABIM,MORULEM,ALEREK,KANU-EAST,\n"+
			"Sarah,Akello,f,vht,,CM 9021 0987654 X,ABIM,MORULEM,ALEREK,KANU-EAST,\n", nil)

	for i, s := range staged {
		if s.Row.Status != domain.RowRejected {
			t.Errorf("row %d status = %s, want rejected", i, s.Row.Status)
		}
		if !hasCode(s, domain.ProblemDuplicateNINInFile) {
			t.Errorf("row %d codes = %v", i, codes(s))
		}
		if s.Record.LocationID != 0 {
			t.Errorf("row %d kept a placement after being refused", i)
		}
		// The message names both lines, so neither has to be hunted for.
		if !strings.Contains(s.Row.Summary(), "2, 3") {
			t.Errorf("row %d message does not name both lines: %q", i, s.Row.Summary())
		}
	}
}

// A spreadsheet cell collects spaces a form field does not. The rule is the
// same; only the tidying differs.
func TestNINIsTidiedBeforeTheSharedRuleIsAsked(t *testing.T) {
	s := only(t, validate(t, auth.National(),
		"Grace,Okello,f,vht,,cm 90210-987654 x,ABIM,MORULEM,ALEREK,KANU-EAST,\n", nil))
	if s.Row.Status != domain.RowReady {
		t.Fatalf("status = %s (%v)", s.Row.Status, s.Row.Problems)
	}
	if s.Record.NIN != "CM90210987654X" {
		t.Errorf("NIN stored as %q", s.Record.NIN)
	}
}

// Two people in one village genuinely share a name, which is why the register's
// own form warns and asks for a second submit rather than refusing.
func TestDuplicateNameAtALocationWarnsAndStillImports(t *testing.T) {
	lookup := &fakeLookup{names: map[int64][]domain.CHW{
		kanu: {{ID: 9, FirstName: "Grace", LastName: "Okello", LocationName: "KANU-EAST"}},
	}}
	s := only(t, validate(t, auth.National(),
		"Grace,Okello,f,vht,,,ABIM,MORULEM,ALEREK,KANU-EAST,\n", lookup))

	if s.Row.Status != domain.RowWarning {
		t.Fatalf("status = %s, want warning", s.Row.Status)
	}
	if !s.Row.Status.Importable() {
		t.Error("a warned row is not importable")
	}
	if s.Record.LocationID != kanu {
		t.Error("a warned row lost its placement")
	}
	if len(s.Row.Blocking()) != 0 {
		t.Errorf("a warning counted as blocking: %v", s.Row.Blocking())
	}
}

// -------------------------------------------------------------------- shape

// A file is usually one district's worth of CHWs, so the same parish's villages
// are asked for hundreds of times over.
func TestTheResolverCachesTheCascade(t *testing.T) {
	lookup := &fakeLookup{}
	var body strings.Builder
	for i := 0; i < 50; i++ {
		body.WriteString("Grace,Okello,f,vht,,,ABIM,MORULEM,ALEREK,KANU-EAST,\n")
	}
	validate(t, auth.National(), body.String(), lookup)

	// Three rungs — subcounty, parish, village — asked once each.
	if lookup.calls != 3 {
		t.Errorf("ChildrenAt called %d times for 50 identical rows, want 3", lookup.calls)
	}
}

func TestFileLimits(t *testing.T) {
	var body strings.Builder
	body.WriteString(header)
	for i := 0; i <= MaxRows; i++ {
		body.WriteString("Grace,Okello,f,vht,,,ABIM,MORULEM,ALEREK,KANU-EAST,\n")
	}
	if _, err := ReadCSV("big.csv", strings.NewReader(body.String())); err != ErrTooManyRows {
		t.Errorf("err = %v, want ErrTooManyRows", err)
	}

	if _, err := ReadCSV("empty.csv", strings.NewReader("")); err != ErrNoHeader {
		t.Errorf("empty file err = %v, want ErrNoHeader", err)
	}
	// A template downloaded and uploaded again, with nothing added to it.
	if _, err := ReadCSV("template.csv", strings.NewReader(header+",,,,,,ABIM,,,,\n")); err != ErrNoRows {
		t.Errorf("template-only err = %v, want ErrNoRows", err)
	}
}

func TestFormatFor(t *testing.T) {
	cases := map[string]string{
		"register.csv": domain.FormatCSV, "REGISTER.CSV": domain.FormatCSV,
		"register.xlsx": domain.FormatXLSX, "book.xlsm": domain.FormatXLSX,
	}
	for name, want := range cases {
		if got, ok := FormatFor(name); !ok || got != want {
			t.Errorf("FormatFor(%q) = (%q, %v), want %q", name, got, ok, want)
		}
	}
	for _, name := range []string{"register.ods", "register.pdf", "register", ""} {
		if _, ok := FormatFor(name); ok {
			t.Errorf("FormatFor(%q) accepted", name)
		}
	}
}

// The template is a file someone fills in and uploads back, so it has to read
// as a file with no rows rather than as a file with one bad row.
func TestTemplateRoundTrips(t *testing.T) {
	blank := Template("ABIM")
	f, err := ReadCSV("template.csv", strings.NewReader(string(blank)))
	if err != ErrNoRows {
		t.Fatalf("err = %v, want ErrNoRows", err)
	}
	if !strings.Contains(string(blank), "ABIM") {
		t.Error("the district was not filled into the example row")
	}
	_ = f

	// With a row added beneath it, the example row is still skipped. The row is
	// built against the template's own header, which is the whole vocabulary —
	// this is the file a district actually fills in.
	cells := make([]string, len(All))
	for i, c := range All {
		switch c.Name {
		case ColFirstName:
			cells[i] = "Grace"
		case ColLastName:
			cells[i] = "Okello"
		case ColSex:
			cells[i] = "f"
		case ColCadre:
			cells[i] = "vht"
		case ColDistrict:
			cells[i] = "ABIM"
		case ColSubcounty:
			cells[i] = "MORULEM"
		case ColParish:
			cells[i] = "ALEREK"
		case ColVillage:
			cells[i] = "KANU-EAST"
		}
	}
	filled, err := ReadCSV("filled.csv",
		strings.NewReader(string(blank)+strings.Join(cells, ",")+"\n"))
	if err != nil {
		t.Fatalf("read the filled template: %v", err)
	}
	staged, err := New(&fakeLookup{}, auth.National()).Validate(context.Background(), filled)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(staged) != 1 {
		t.Fatalf("staged %d rows, want 1 — the example row was not skipped", len(staged))
	}
	if staged[0].Row.Status != domain.RowReady {
		t.Fatalf("status = %s (%v)", staged[0].Row.Status, staged[0].Row.Problems)
	}
}

func asMissing(err error, target **MissingColumnsError) bool {
	m, ok := err.(*MissingColumnsError)
	if ok {
		*target = m
	}
	return ok
}
