package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"chwr/internal/auth"
	"chwr/internal/domain"
)

// Record is the register record a row parses to — the importer's own shape, so
// that this package does not depend on internal/store. The handler maps it to
// store.CHWInput, which is where district_id is absent by design and derived by
// trigger from the placement.
type Record struct {
	NIN        string        `json:"nin,omitempty"`
	FirstName  string        `json:"first_name"`
	LastName   string        `json:"last_name"`
	Sex        domain.Sex    `json:"sex"`
	Cadre      domain.Cadre  `json:"cadre"`
	AgeYears   *int16        `json:"age_years,omitempty"`
	LocationID int64         `json:"location_id"`
	Profile    ProfileRecord `json:"profile,omitempty"`
}

// Encode is the record as it is stored on the staged row, and read back at
// commit. The commit does not re-parse the file or re-resolve anything: the
// record it writes is the one the report was drawn from.
func (r Record) Encode() ([]byte, error) { return json.Marshal(r) }

// DecodeRecord reads a staged row's record back.
func DecodeRecord(raw []byte) (Record, error) {
	var rec Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return Record{}, fmt.Errorf("decode staged record: %w", err)
	}
	return rec, nil
}

// Staged is one line's verdict: the row as it reaches import_rows, and the
// record a commit would create from it. Record is only meaningful when the
// row's status is importable.
type Staged struct {
	Row    domain.ImportRow
	Record Record
}

// Importer validates a file against the register, inside a scope.
type Importer struct {
	lookup Lookup
	scope  auth.Scope
}

// New returns an importer reading through lookup, confined to sc.
func New(lookup Lookup, sc auth.Scope) *Importer {
	return &Importer{lookup: lookup, scope: sc}
}

// Validate walks the whole file and returns one Staged per non-blank line.
//
// It is two passes. The per-row pass cannot see whether a NIN appears twice in
// the file, and that refusal has to reject both rows — the first is not more
// correct than the second, and importing it would quietly pick a winner.
func (im *Importer) Validate(ctx context.Context, f *File) ([]Staged, error) {
	resolver, err := NewResolver(ctx, im.lookup, im.scope)
	if err != nil {
		return nil, err
	}

	staged := make([]Staged, 0, len(f.Rows))
	for _, row := range f.Rows {
		if row.Blank() {
			continue
		}
		staged = append(staged, im.row(ctx, row, resolver))
	}

	markDuplicateNINs(staged)
	return staged, nil
}

// fields parses the scalar columns of a row: the register record's own values,
// with no reference to the hierarchy or to what is already on the register.
//
// It is separate because a commit rebuilds the record from the staged row
// rather than from the file. That is not a re-validation — the placement comes
// from the row as it was reviewed, not from resolving the names again — it is
// only turning the stored cells back into typed values.
func fields(r Row) (Record, []domain.Problem) {
	var problems []domain.Problem
	add := func(p domain.Problem) { problems = append(problems, p) }

	rec := Record{
		FirstName: r.Value(ColFirstName),
		LastName:  r.Value(ColLastName),
	}
	if rec.FirstName == "" {
		add(domain.Problem{Field: ColFirstName, Code: domain.ProblemRequired,
			Message: "The first name is empty."})
	}
	if rec.LastName == "" {
		add(domain.Problem{Field: ColLastName, Code: domain.ProblemRequired,
			Message: "The last name is empty."})
	}

	if raw := r.Value(ColSex); raw == "" {
		add(domain.Problem{Field: ColSex, Code: domain.ProblemRequired,
			Message: "The sex is empty."})
	} else if sex, ok := domain.ParseSex(raw); ok {
		rec.Sex = sex
	} else {
		add(domain.Problem{Field: ColSex, Code: domain.ProblemBadValue,
			Message: fmt.Sprintf("%q is not male or female.", raw)})
	}

	if p := parseCadre(r.Value(ColCadre), &rec); p != nil {
		add(*p)
	}

	if raw := r.Value(ColAge); raw != "" {
		n, err := strconv.Atoi(raw)
		switch {
		case err != nil:
			add(domain.Problem{Field: ColAge, Code: domain.ProblemBadValue,
				Message: fmt.Sprintf("%q is not a whole number.", raw)})
		case !domain.ValidAge(n):
			add(domain.Problem{Field: ColAge, Code: domain.ProblemBadValue,
				Message: fmt.Sprintf("An age of %d is outside %d to %d.", n, domain.MinAge, domain.MaxAge)})
		default:
			years := int16(n)
			rec.AgeYears = &years
		}
	}

	if raw := r.Value(ColNIN); raw != "" {
		// A spreadsheet cell collects spaces and hyphens a form field does not,
		// so the value is tidied before the shared rule is asked.
		rec.NIN = cleanNIN(raw)
		if !domain.ValidNIN(rec.NIN) {
			rec.NIN = ""
			add(domain.Problem{Field: ColNIN, Code: domain.ProblemBadNIN,
				Message: fmt.Sprintf("%q is not a NIN: 14 characters, two letters, then eleven letters or digits, then a letter.", raw)})
		}
	}

	return rec, problems
}

// row validates one line. Every field is checked, not just up to the first
// failure: a report that stopped early would take four uploads to surface four
// problems in one row.
func (im *Importer) row(ctx context.Context, r Row, resolver *Resolver) Staged {
	out := Staged{Row: domain.ImportRow{Number: r.Number, Raw: r.Raw()}}

	rec, problems := fields(r)
	add := func(p domain.Problem) { problems = append(problems, p) }
	placement, locationProblems := resolver.Resolve(ctx, r, rec.Cadre)
	for _, p := range locationProblems {
		add(p)
	}
	if placement != nil {
		rec.LocationID = placement.LocationID
		if p := checkPlacementLevel(r, rec.Cadre, placement); p != nil {
			add(*p)
			placement = nil
		}
	}

	// The optional attributes. The facility among them is matched inside the
	// CHW's own district, so this runs after the placement has said which.
	districtID := int64(0)
	if placement != nil {
		districtID = placement.DistrictID
	}
	profile, profileProblems := im.profileFields(ctx, r, districtID, resolver)
	for _, p := range profileProblems {
		add(p)
	}
	rec.Profile = profile

	// Against the register. Both of these read it, so they are asked only once
	// the row is otherwise sound — there is nothing to compare a nameless row
	// against, and no reason to spend the query.
	if rec.NIN != "" {
		if existing, err := im.lookup.CHWWithNIN(ctx, rec.NIN); err == nil {
			add(domain.Problem{Field: ColNIN, Code: domain.ProblemDuplicateNIN,
				Message: fmt.Sprintf("%s is already on the register carrying that NIN.", existing.FullName())})
		} else if !errors.Is(err, domain.ErrNotFound) {
			add(domain.Problem{Field: ColNIN, Code: domain.ProblemDuplicateNIN,
				Message: "That NIN could not be checked. Try again."})
		}
	}
	if placement != nil && rec.FirstName != "" && rec.LastName != "" {
		matches, err := im.lookup.NamesAt(ctx, im.scope, rec.LocationID, rec.FirstName, rec.LastName)
		if err == nil && len(matches) > 0 {
			add(domain.Problem{Field: ColLastName, Code: domain.ProblemPossibleDuplicate,
				Message: fmt.Sprintf("%s is already recorded at %s. Check this is a different person.",
					matches[0].FullName(), matches[0].LocationName)})
		}
	}

	out.Row.Problems = problems
	out.Row.Status = verdict(problems)
	if out.Row.Status.Importable() {
		out.Row.LocationID = &rec.LocationID
		out.Record = rec
		if encoded, err := rec.Encode(); err == nil {
			out.Row.Record = encoded
		} else {
			// Unreachable for a record made of strings, numbers and bools, but
			// a row that cannot be stored must not be reported as ready.
			out.Row.Status = domain.RowRejected
			out.Row.LocationID = nil
			out.Record = Record{}
			out.Row.Problems = append(problems, domain.Problem{
				Code: domain.ProblemBadValue, Message: "This row could not be stored for review."})
		}
	}
	return out
}

// verdict reads the problems and says what the row is. One warning-only code
// exists, so a row carrying nothing worse is imported with its warning shown.
func verdict(problems []domain.Problem) domain.RowStatus {
	status := domain.RowReady
	for _, p := range problems {
		if !p.Code.Warning() {
			return domain.RowRejected
		}
		status = domain.RowWarning
	}
	return status
}

// parseCadre reads the cadre column, and tells a value it does not know from a
// value carrying more than one cadre.
//
// The difference matters. The source form's widget was a select_multiple with
// an "other" free-text box, and the register stores a single closed enum. A row
// with two cadres, or with a cadre and some free text beside it, is refused
// rather than truncated: keeping the first value would erase the record of a
// CHW who did not fit the two-value model at collection time.
func parseCadre(raw string, rec *Record) *domain.Problem {
	if raw == "" {
		return &domain.Problem{Field: ColCadre, Code: domain.ProblemRequired,
			Message: "The cadre is empty."}
	}
	if cadre, ok := domain.ParseCadre(raw); ok {
		rec.Cadre = cadre
		return nil
	}

	// It did not parse whole. If any part of it is a cadre, the cell holds
	// more than the register can store.
	known := 0
	for _, token := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '/' || r == '|' || r == ' ' || r == '\t'
	}) {
		if _, ok := domain.ParseCadre(token); ok {
			known++
		}
	}
	if known > 0 {
		return &domain.Problem{Field: ColCadre, Code: domain.ProblemCadreMulti,
			Message: fmt.Sprintf("%q holds more than the register can store. A CHW is recorded as one cadre: vht or chew.", raw)}
	}
	return &domain.Problem{Field: ColCadre, Code: domain.ProblemBadValue,
		Message: fmt.Sprintf("%q is not vht or chew.", raw)}
}

// checkPlacementLevel holds the rule that cadre decides placement: a CHEW
// serves a parish, a VHT a village. chws_set_placement is still the
// enforcement; this is what turns it into an instruction.
func checkPlacementLevel(r Row, cadre domain.Cadre, placement *Placement) *domain.Problem {
	if !cadre.Valid() {
		return nil // the cadre column already carries its own problem
	}
	want := cadre.PlacementLevel()
	if placement.Level == want {
		return nil
	}

	switch {
	case cadre == domain.CadreCHEW && r.Value(ColVillage) != "":
		return &domain.Problem{Field: ColVillage, Code: domain.ProblemPlacementLevel,
			Message: "A CHEW is placed at parish level. Leave the village column blank."}
	case cadre == domain.CadreVHT && placement.Level == domain.LevelParish:
		return &domain.Problem{Field: ColVillage, Code: domain.ProblemPlacementLevel,
			Message: "A VHT is placed at village level. Name the village."}
	}
	return &domain.Problem{Field: ColVillage, Code: domain.ProblemPlacementLevel,
		Message: fmt.Sprintf("A %s is placed at %s level, and that location is a %s.",
			cadre.Label(), want, placement.Level)}
}

// markDuplicateNINs refuses every row of a NIN that appears more than once in
// one file. Both rows go: the first is not more correct than the second, and
// importing it would be picking a winner quietly.
func markDuplicateNINs(staged []Staged) {
	byNIN := make(map[string][]int)
	for i, s := range staged {
		if s.Record.NIN != "" {
			byNIN[s.Record.NIN] = append(byNIN[s.Record.NIN], i)
		}
	}

	for nin, indexes := range byNIN {
		if len(indexes) < 2 {
			continue
		}
		lines := make([]string, 0, len(indexes))
		for _, i := range indexes {
			lines = append(lines, strconv.Itoa(staged[i].Row.Number))
		}
		for _, i := range indexes {
			staged[i].Row.Problems = append(staged[i].Row.Problems, domain.Problem{
				Field: ColNIN, Code: domain.ProblemDuplicateNINInFile,
				Message: fmt.Sprintf("The NIN %s is on lines %s of this file. A NIN belongs to one person.",
					nin, strings.Join(lines, ", ")),
			})
			staged[i].Row.Status = domain.RowRejected
			staged[i].Row.LocationID = nil
			staged[i].Row.Record = nil
			staged[i].Record = Record{}
		}
	}
}

// cleanNIN strips what a spreadsheet adds. The rule itself is domain.ValidNIN,
// shared with the CHW form; only the tidying is the importer's, because a form
// field does not collect the spaces a printed card is read out with.
func cleanNIN(raw string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(raw) {
		if r != ' ' && r != '-' && r != '\t' && r != '/' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
