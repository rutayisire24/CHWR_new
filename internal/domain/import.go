package domain

import "time"

// Bulk import entities. The importer produces these, the store persists them,
// and the report renders them; putting the vocabulary here is what keeps
// internal/importer and internal/store from having to import each other.
//
// docs/import.md is the design this implements.

// BatchStatus is the `import_batch_status` enum: where an upload has got to.
// An upload writes nothing to the register — it stages rows and waits for a
// human to commit or discard them.
type BatchStatus string

const (
	BatchPending   BatchStatus = "pending"
	BatchCommitted BatchStatus = "committed"
	BatchDiscarded BatchStatus = "discarded"
)

// Label is the human-readable status.
func (b BatchStatus) Label() string {
	switch b {
	case BatchPending:
		return "Awaiting review"
	case BatchCommitted:
		return "Imported"
	case BatchDiscarded:
		return "Discarded"
	}
	return string(b)
}

// RowStatus is the `import_row_status` enum.
//
// ready, warning and rejected are the verdicts validation produces. imported,
// skipped and failed are what a commit turns the first two into: a warned row
// is imported unless the operator asked to skip its kind, and a failed row is
// one that passed validation and then lost a race at commit.
type RowStatus string

const (
	RowReady    RowStatus = "ready"
	RowWarning  RowStatus = "warning"
	RowRejected RowStatus = "rejected"
	RowImported RowStatus = "imported"
	RowSkipped  RowStatus = "skipped"
	RowFailed   RowStatus = "failed"
)

// Importable reports whether a commit should attempt this row. Warned rows are
// importable: a name already on the register at that location is a warning
// because two people in one village genuinely share a name.
func (r RowStatus) Importable() bool { return r == RowReady || r == RowWarning }

// Refused reports whether the row is one the register turned away — the set
// that has to be explainable, and that reaches import_quarantine.
func (r RowStatus) Refused() bool { return r == RowRejected || r == RowFailed }

// ProblemCode names what is wrong with a row. The set is closed and is the
// vocabulary docs/import.md documents; the message beside it is for a human,
// the code is for grouping the report.
type ProblemCode string

const (
	// Field-level.
	ProblemRequired   ProblemCode = "required"
	ProblemBadValue   ProblemCode = "bad_value"
	ProblemBadNIN     ProblemCode = "bad_nin"
	ProblemCadreMulti ProblemCode = "cadre_multi"

	// Placement.
	ProblemLocationMissing ProblemCode = "location_missing"
	ProblemLocationAmbig   ProblemCode = "location_ambiguous"
	ProblemCodeUnknown     ProblemCode = "location_code_unknown"
	ProblemCodeMismatch    ProblemCode = "location_code_mismatch"
	ProblemPlacementLevel  ProblemCode = "placement_level"
	ProblemOutsideScope    ProblemCode = "outside_scope"

	// Identity, against the file and against the register.
	ProblemDuplicateNIN       ProblemCode = "duplicate_nin"
	ProblemDuplicateNINInFile ProblemCode = "duplicate_nin_in_file"
	ProblemPossibleDuplicate  ProblemCode = "possible_duplicate"

	// Raised at commit, not at validation: the row was acceptable when the
	// report was produced and the register moved underneath it.
	ProblemLostRace ProblemCode = "lost_race"
)

// Warning reports whether the code lets the row through. Exactly one does:
// a duplicate name at a location is a warning, which is why chws_dup_probe_idx
// exists and why the CHW form asks for a second submit rather than refusing.
func (c ProblemCode) Warning() bool { return c == ProblemPossibleDuplicate }

// Problem is one thing wrong with one row. It is stored as JSONB, so the tags
// are the wire format and renaming one is a migration in all but name.
type Problem struct {
	// Field is the column it is about, empty when it is about the whole row.
	Field   string      `json:"field,omitempty"`
	Code    ProblemCode `json:"code"`
	Message string      `json:"message"`
	// Candidates carries the places an ambiguous name could have meant, so the
	// operator picks rather than guesses. Empty for every other code.
	Candidates []Candidate `json:"candidates,omitempty"`
}

// Candidate is one location an ambiguous name matched. The path is spelled out
// because "which BUHOBA A" is only answerable with the chain above it, and the
// code is what the operator puts in the location_code column to settle it.
type Candidate struct {
	LocationID int64  `json:"location_id"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	Code       string `json:"code"`
}

// Import file formats. The column is CHECKed against exactly these two.
const (
	FormatCSV  = "csv"
	FormatXLSX = "xlsx"
)

// Batch is one upload: a file, the scope it was uploaded under, and the tally
// of what its rows came to.
type Batch struct {
	ID       int64
	Filename string
	Format   string

	UploadedBy   int64
	UploaderName string // joined for display
	// DistrictID is the uploader's scope at upload time, nil for a national
	// one. It is recorded rather than re-derived because a user's role can
	// change afterwards and the batch's reach cannot.
	DistrictID   *int64
	DistrictName string

	Status BatchStatus
	// Columns is the header as the file spelled it, in order. errors.csv is
	// rebuilt from this, so a district gets their own columns back.
	Columns []string

	Total    int
	Ready    int
	Warning  int
	Rejected int
	Imported int
	// SkipDuplicates is the choice made at commit. It is kept because it
	// explains the gap between Warning and Imported a month later.
	SkipDuplicates bool

	CreatedAt   time.Time
	CommittedAt *time.Time
}

// Pending reports whether the batch is still awaiting a decision.
func (b Batch) Pending() bool { return b.Status == BatchPending }

// Committable reports whether there is anything to commit. A file whose every
// row was refused is discarded, not committed.
func (b Batch) Committable() bool { return b.Pending() && b.Ready+b.Warning > 0 }

// Acceptable is how many rows a commit would attempt.
func (b Batch) Acceptable() int { return b.Ready + b.Warning }

// ImportRow is one staged line of the file.
type ImportRow struct {
	BatchID int64
	// Number is the line in the source file, counting the header, so a message
	// points at what the operator sees in their spreadsheet.
	Number int
	// Raw is the line exactly as it arrived: keys as the file spelled them,
	// values untouched. A refusal has to be explainable a month later, and
	// "what did the file actually say" is the first question.
	Raw    map[string]string
	Status RowStatus

	// LocationID is the resolved placement, nil when resolution failed.
	LocationID *int64
	// Record is the resolved register record this row will create, as JSON.
	// It is stored beside Raw rather than rebuilt at commit: a facility is
	// resolved by name within the CHW's district, so re-resolving it later
	// would answer from a register that has moved since the report. Nil for a
	// refused row.
	Record   []byte
	Problems []Problem
	// CHWID is set once the row becomes a register record.
	CHWID *int64
}

// Blocking returns the problems that refused the row, leaving out the ones
// that only warned.
func (r ImportRow) Blocking() []Problem {
	var out []Problem
	for _, p := range r.Problems {
		if !p.Code.Warning() {
			out = append(out, p)
		}
	}
	return out
}

// Summary is the one-line reason a report gives for a refusal.
func (r ImportRow) Summary() string {
	for _, p := range r.Blocking() {
		return p.Message
	}
	for _, p := range r.Problems {
		return p.Message
	}
	return ""
}
