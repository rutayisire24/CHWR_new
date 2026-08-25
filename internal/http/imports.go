package http

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"chwr/internal/auth"
	"chwr/internal/domain"
	"chwr/internal/importer"
	"chwr/internal/store"
)

// registerLookup is the adapter between the importer and the store. It exists
// so internal/importer can be tested without a database: everything it reads
// arrives through this interface, and the tests supply their own.
type registerLookup struct{ store *store.Store }

func (l registerLookup) Districts(ctx context.Context, sc auth.Scope) ([]domain.Place, error) {
	districts, err := l.store.Locations.Districts(ctx, sc)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Place, 0, len(districts))
	for _, d := range districts {
		out = append(out, domain.Place{ID: d.ID, Level: domain.LevelDistrict, Name: d.Name})
	}
	return out, nil
}

func (l registerLookup) ChildrenAt(ctx context.Context, sc auth.Scope, ancestorID int64, level domain.Level) ([]domain.Place, error) {
	return l.store.Locations.ChildrenAt(ctx, sc, ancestorID, level)
}

func (l registerLookup) ByCode(ctx context.Context, code string) (int64, error) {
	return l.store.Locations.ByCode(ctx, code)
}

func (l registerLookup) Ancestors(ctx context.Context, sc auth.Scope, id int64) ([]domain.Place, error) {
	return l.store.Locations.Ancestors(ctx, sc, id)
}

// CHWWithNIN asks nationally on purpose. chws_nin_uniq is a national index, so
// a NIN held in another district is still a duplicate and the insert would
// still fail; asking inside the scope would report it as available and then
// lose the row at commit. The message the importer builds from this names the
// record but not its district — see docs/import.md.
func (l registerLookup) CHWWithNIN(ctx context.Context, nin string) (domain.CHW, error) {
	return l.store.CHWs.ByNIN(ctx, auth.National(), nin)
}

func (l registerLookup) NamesAt(ctx context.Context, sc auth.Scope, locationID int64, first, last string) ([]domain.CHW, error) {
	return l.store.CHWs.PossibleDuplicates(ctx, sc, locationID, first, last, 0)
}

// Tools and ServiceDomains read the vocabularies through the same queries the
// profile form uses, asked about nobody: CHW id 0 matches no junction row, so
// what comes back is the plain list.
func (l registerLookup) Tools(ctx context.Context) ([]domain.Tool, error) {
	held, err := l.store.Profiles.Tools(ctx, 0)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Tool, 0, len(held))
	for _, t := range held {
		out = append(out, t.Tool)
	}
	return out, nil
}

func (l registerLookup) ServiceDomains(ctx context.Context) ([]domain.ServiceDomain, error) {
	offered, err := l.store.Profiles.ServiceDomains(ctx, 0)
	if err != nil {
		return nil, err
	}
	out := make([]domain.ServiceDomain, 0, len(offered))
	for _, d := range offered {
		out = append(out, d.ServiceDomain)
	}
	return out, nil
}

func (l registerLookup) FacilitiesIn(ctx context.Context, sc auth.Scope, districtID int64) ([]domain.Facility, error) {
	facilities, err := l.store.Profiles.FacilitiesIn(ctx, sc, districtID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Facility, 0, len(facilities))
	for _, f := range facilities {
		out = append(out, domain.Facility{ID: f.ID, Name: f.Name, Ownership: f.Ownership})
	}
	return out, nil
}

type importsPage struct {
	Batches  []domain.Batch
	District string
	// Problems is a file-level refusal — the upload never got as far as a
	// batch, so there is no report to send the operator to.
	Problems []string
	MaxRows  int
	Columns  []importer.Column
}

type importReportPage struct {
	Batch domain.Batch
	// Refused and Warned are shown in full up to a cap; errors.csv carries
	// every one of them.
	Refused  []reportRow
	Warned   []reportRow
	Shown    int
	Unknown  []string
	CanWrite bool
}

// reportRow is one staged row as the report shows it. Name is resolved here
// rather than in the template because raw is keyed by the file's own spelling
// of its header — a template asking for `first_name` would come up empty for
// the district that wrote "Given Names".
type reportRow struct {
	Number   int
	Name     string
	Problems []domain.Problem
}

// reportRows converts staged rows for display, reading names through the
// header the batch kept.
func toReportRows(columns []string, rows []domain.ImportRow) []reportRow {
	header := importer.ReadHeader(columns)
	out := make([]reportRow, 0, len(rows))
	for _, row := range rows {
		name := strings.TrimSpace(
			header.ValueOf(row.Raw, importer.ColFirstName) + " " +
				header.ValueOf(row.Raw, importer.ColLastName))
		if name == "" {
			// A row refused for having no name at all still needs a handle.
			name = header.ValueOf(row.Raw, importer.ColNIN)
		}
		out = append(out, reportRow{Number: row.Number, Name: name, Problems: row.Problems})
	}
	return out
}

// reportRows caps what the report renders. Three thousand refusals is a file to
// fix in a spreadsheet, not a page to scroll, and errors.csv is the thing that
// carries all of them.
const reportRows = 200

func (s *Server) importsList(w http.ResponseWriter, r *http.Request) {
	s.renderImports(w, r, http.StatusOK, nil)
}

// renderImports draws the upload page, optionally with the reasons a file was
// turned away before it became a batch.
func (s *Server) renderImports(w http.ResponseWriter, r *http.Request, status int, problems []string) {
	sc := auth.ScopeFrom(r.Context())

	batches, err := s.store.Imports.List(r.Context(), sc, 25)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	s.render(w, r, status, "imports", importsPage{
		Batches:  batches,
		District: auth.MustUser(r.Context()).DistrictName,
		Problems: problems,
		MaxRows:  importer.MaxRows,
		Columns:  importer.All,
	})
}

// importTemplate serves the blank file. A district user's arrives with their
// own district in the example row, which is also the row Row.Blank skips, so
// uploading the template unchanged imports nothing.
func (s *Server) importTemplate(w http.ResponseWriter, r *http.Request) {
	district := auth.MustUser(r.Context()).DistrictName
	body := importer.Template(district)

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+importer.TemplateFilename(district)+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// importUpload reads the file, validates every row against the register, and
// stages the result. It writes nothing to `chws`: a commit is a separate act by
// a human, after they have read the report.
func (s *Server) importUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sc := auth.ScopeFrom(ctx)
	actor := auth.MustUser(ctx)

	// The CSRF middleware has already parsed the multipart body, which is why
	// the file is here at all — ParseForm alone does not read one.
	file, header, err := r.FormFile("file")
	if err != nil {
		s.renderImports(w, r, http.StatusUnprocessableEntity, []string{"Choose a file to upload."})
		return
	}
	defer file.Close()

	format, ok := importer.FormatFor(header.Filename)
	if !ok {
		s.renderImports(w, r, http.StatusUnprocessableEntity, []string{
			"That is not a file this can read. Save it as CSV, or upload the .xlsx workbook itself."})
		return
	}
	if header.Size > importer.MaxFileBytes {
		s.renderImports(w, r, http.StatusUnprocessableEntity, []string{
			fmt.Sprintf("That file is %s. The limit is %s — a file larger than that is a migration rather than an import.",
				megabytes(header.Size), megabytes(importer.MaxFileBytes))})
		return
	}

	parsed, err := importer.Read(header.Filename, format, file)
	if err != nil {
		s.renderImports(w, r, http.StatusUnprocessableEntity, fileProblems(err))
		return
	}

	// The batch is opened before validation so that the file, its columns and
	// its uploader are recorded even if what follows fails. Its district comes
	// from the Scope, never from the file.
	batch, err := s.store.Imports.Create(ctx, sc, actor,
		header.Filename, format, parsed.Header.Spelled, clientIP(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}

	staged, err := importer.New(registerLookup{s.store}, sc).Validate(ctx, parsed)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	rows := make([]domain.ImportRow, 0, len(staged))
	for _, st := range staged {
		rows = append(rows, st.Row)
	}
	if err := s.store.Imports.Stage(ctx, sc, batch.ID, rows); err != nil {
		s.fail(w, r, err)
		return
	}

	http.Redirect(w, r, importPath(batch.ID), http.StatusSeeOther)
}

// fileProblems turns a file-level refusal into the sentences the upload page
// lists. These stop the whole upload: there is nothing to review when the
// columns are wrong, and a report of three thousand identical failures is not
// a report.
func fileProblems(err error) []string {
	var missing *importer.MissingColumnsError
	switch {
	case errors.As(err, &missing):
		out := make([]string, 0, len(missing.Columns)+1)
		out = append(out, "The file is missing columns the register needs:")
		for _, column := range missing.Columns {
			out = append(out, column)
		}
		return out
	case errors.Is(err, importer.ErrTooManyRows):
		return []string{importer.ErrTooManyRows.Error() +
			". A file larger than that belongs on the seeding path, where it can be checked against its source first."}
	case errors.Is(err, importer.ErrNoRows):
		return []string{"The file has a header but no CHWs under it."}
	case errors.Is(err, importer.ErrNoHeader):
		return []string{"The file is empty."}
	case errors.Is(err, importer.ErrNoSheet):
		return []string{"That workbook has no sheets."}
	}
	slog.Error("import file unreadable", "err", err)
	return []string{"That file could not be read. If it came from Excel, try saving it again as CSV."}
}

func (s *Server) importShow(w http.ResponseWriter, r *http.Request) {
	batch, ok := s.batchOr404(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	sc := auth.ScopeFrom(ctx)

	refused, err := s.store.Imports.Rows(ctx, sc, batch.ID,
		[]domain.RowStatus{domain.RowRejected, domain.RowFailed}, 0, reportRows)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	warned, err := s.store.Imports.Rows(ctx, sc, batch.ID,
		[]domain.RowStatus{domain.RowWarning}, 0, reportRows)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	// Which of the file's columns were not ours is re-derived from the header
	// the batch kept, rather than stored a second time.
	unknown := importer.ReadHeader(batch.Columns).Unknown()

	s.render(w, r, http.StatusOK, "import_report", importReportPage{
		Batch:    batch,
		Refused:  toReportRows(batch.Columns, refused),
		Warned:   toReportRows(batch.Columns, warned),
		Shown:    reportRows,
		Unknown:  unknown,
		CanWrite: auth.Can(auth.MustUser(ctx).Role, auth.CapCHWCreate),
	})
}

// importErrors re-emits the refused rows with their original columns and an
// added reason. The district fixes that file and uploads it again; nobody has
// to find row 412 in the original by counting.
func (s *Server) importErrors(w http.ResponseWriter, r *http.Request) {
	batch, ok := s.batchOr404(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	sc := auth.ScopeFrom(ctx)

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="errors-%d-%s.csv"`, batch.ID, csvSafe(batch.Filename)))

	out := csv.NewWriter(w)
	defer out.Flush()
	_ = out.Write(append([]string{"line", "error"}, batch.Columns...))

	// Paged by row number: the whole point of errors.csv is that it carries
	// every refusal, including the ones the report itself capped.
	after := 0
	for {
		rows, err := s.store.Imports.Rows(ctx, sc, batch.ID,
			[]domain.RowStatus{domain.RowRejected, domain.RowFailed}, after, 1000)
		if err != nil {
			// The header is already written, so there is no error page to
			// show. Ending the file short is the honest failure; the log
			// carries why.
			slog.Error("errors.csv truncated", "batch", batch.ID, "err", err)
			return
		}
		if len(rows) == 0 {
			return
		}
		for _, row := range rows {
			record := make([]string, 0, len(batch.Columns)+2)
			record = append(record, strconv.Itoa(row.Number), row.Summary())
			for _, column := range batch.Columns {
				record = append(record, row.Raw[column])
			}
			_ = out.Write(record)
			after = row.Number
		}
		out.Flush()
	}
}

func (s *Server) importCommit(w http.ResponseWriter, r *http.Request) {
	batch, ok := s.batchOr404(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	sc := auth.ScopeFrom(ctx)
	actor := auth.MustUser(ctx)

	if !batch.Pending() {
		setFlash(w, s.secure(), "warn", "That upload has already been decided.")
		http.Redirect(w, r, importPath(batch.ID), http.StatusSeeOther)
		return
	}

	// One commit at a time. The run takes tens of seconds at the row cap, and
	// a page doing nothing for that long invites a second click; two runs
	// walking the same batch would both create the CHWs on the rows neither had
	// marked yet.
	claimed, err := s.store.Imports.Claim(ctx, sc, batch.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !claimed {
		setFlash(w, s.secure(), "warn",
			"That upload is already being imported. This page will show the result when it finishes.")
		http.Redirect(w, r, importPath(batch.ID), http.StatusSeeOther)
		return
	}

	skipDuplicates := trimmed(r, "skip_duplicates") != ""
	result, err := s.runCommit(ctx, sc, actor, batch, skipDuplicates, clientIP(r))
	if err != nil {
		// The rows already written stay written and stay marked; the batch goes
		// back to pending so the rest can be picked up rather than stranded.
		if release := s.store.Imports.Release(ctx, batch.ID); release != nil {
			slog.Error("releasing import claim failed", "batch", batch.ID, "err", release)
		}
		s.fail(w, r, err)
		return
	}

	if _, err := s.store.Imports.Commit(ctx, sc, actor, batch.ID, skipDuplicates, clientIP(r)); err != nil {
		if release := s.store.Imports.Release(ctx, batch.ID); release != nil {
			slog.Error("releasing import claim failed", "batch", batch.ID, "err", release)
		}
		s.fail(w, r, err)
		return
	}

	setFlash(w, s.secure(), result.kind(), result.message())
	http.Redirect(w, r, importPath(batch.ID), http.StatusSeeOther)
}

func (s *Server) importDiscard(w http.ResponseWriter, r *http.Request) {
	batch, ok := s.batchOr404(w, r)
	if !ok {
		return
	}
	_, err := s.store.Imports.Discard(r.Context(), auth.ScopeFrom(r.Context()),
		auth.MustUser(r.Context()), batch.ID, clientIP(r))
	if errors.Is(err, domain.ErrConflict) {
		setFlash(w, s.secure(), "warn", "That upload has already been decided.")
		http.Redirect(w, r, importPath(batch.ID), http.StatusSeeOther)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}

	setFlash(w, s.secure(), "ok",
		"The upload has been discarded. Its rows are kept, so what was refused and why is still on the record.")
	http.Redirect(w, r, importPath(batch.ID), http.StatusSeeOther)
}

// commitResult is what a run came to, for the message afterwards.
type commitResult struct {
	imported int
	skipped  int
	failed   int
	refused  int
}

func (c commitResult) kind() string {
	if c.failed > 0 {
		return "warn"
	}
	return "ok"
}

func (c commitResult) message() string {
	parts := []string{fmt.Sprintf("%d %s added to the register", c.imported, plural(c.imported, "CHW", "CHWs"))}
	if c.skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped as possible duplicates", c.skipped))
	}
	if c.failed > 0 {
		parts = append(parts, fmt.Sprintf("%d refused at the last moment — they are in the error file", c.failed))
	}
	if c.refused > 0 {
		parts = append(parts, fmt.Sprintf("%d had already been refused", c.refused))
	}
	return strings.Join(parts, ", ") + "."
}

// runCommit walks the staged rows and writes the ones that are still good.
//
// One transaction per row, carrying the CHW, its audit row and the staged row's
// mark together — see store.CHWs.CreateTx. A row that fails here does not abort
// the batch: it is marked, quarantined, and the rest continue. An
// all-or-nothing transaction over ten thousand inserts was rejected, because
// one lost race would discard a correct nine-thousand-row import and the
// operator's next move would be to upload the identical file again.
func (s *Server) runCommit(ctx context.Context, sc auth.Scope, actor domain.User,
	batch domain.Batch, skipDuplicates bool, ip netip.Addr) (commitResult, error) {

	var result commitResult

	after := 0
	for {
		rows, err := s.store.Imports.Rows(ctx, sc, batch.ID,
			[]domain.RowStatus{domain.RowReady, domain.RowWarning}, after, 500)
		if err != nil {
			return result, err
		}
		if len(rows) == 0 {
			break
		}

		for _, row := range rows {
			after = row.Number

			if skipDuplicates && warnedDuplicate(row) {
				if err := s.store.Imports.MarkRow(ctx, batch.ID, row.Number, domain.RowSkipped, nil); err != nil {
					return result, err
				}
				result.skipped++
				continue
			}

			problem, err := s.commitRow(ctx, sc, actor, batch, row, ip)
			if err != nil {
				return result, err
			}
			if problem == nil {
				result.imported++
				continue
			}

			row.Problems = append(row.Problems, *problem)
			if err := s.store.Imports.MarkRow(ctx, batch.ID, row.Number, domain.RowFailed, row.Problems); err != nil {
				return result, err
			}
			if err := s.store.Imports.Quarantine(ctx, batch.ID, row); err != nil {
				return result, err
			}
			result.failed++
		}
	}

	// Nothing is dropped silently: the rows validation already refused reach
	// import_quarantine now, where they outlive the batch's own working rows.
	refused, err := s.quarantineRefused(ctx, sc, batch)
	if err != nil {
		return result, err
	}
	result.refused = refused
	return result, nil
}

// commitRow writes one row, and returns the problem that stopped it. An error
// is returned only for a failure that is not the row's fault, which stops the
// run rather than blaming the CHW for it.
func (s *Server) commitRow(ctx context.Context, sc auth.Scope, actor domain.User,
	batch domain.Batch, row domain.ImportRow, ip netip.Addr) (*domain.Problem, error) {

	if row.LocationID == nil || len(row.Record) == 0 {
		return &domain.Problem{Code: domain.ProblemLostRace,
			Message: "This row reached the commit without the record it was reviewed as."}, nil
	}

	// The record is read back, not re-derived: what is committed is what the
	// report was drawn from, down to which facility a name resolved to.
	record, err := importer.DecodeRecord(row.Record)
	if err != nil {
		return &domain.Problem{Code: domain.ProblemLostRace,
			Message: "This row could not be read back the way it was reviewed."}, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	chw, err := s.store.CHWs.CreateTx(ctx, tx, sc, actor, store.CHWInput{
		NIN:        record.NIN,
		FirstName:  record.FirstName,
		LastName:   record.LastName,
		Sex:        record.Sex,
		Cadre:      record.Cadre,
		AgeYears:   record.AgeYears,
		LocationID: record.LocationID,
	}, ip)
	if err != nil {
		return lostRace(err), nil
	}
	// The optional attributes, in the same transaction as the CHW they hang
	// off. A file carrying only the core columns writes no profile row at all,
	// rather than a row of nulls: "nothing recorded" and "recorded as nothing"
	// are different answers here too.
	if record.Profile.Answered() {
		if _, err := s.store.Profiles.SaveTx(ctx, tx, sc, actor, chw.ID,
			profileInput(record.Profile), ip); err != nil {
			return lostRace(err), nil
		}
	}

	if err := s.store.Imports.MarkImportedTx(ctx, tx, batch.ID, row.Number, chw.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return lostRace(err), nil
	}
	return nil, nil
}

// profileInput maps the importer's record onto the store's input. The two are
// separate types on purpose: internal/importer does not depend on
// internal/store, so that its whole validation pass can be tested without a
// database.
func profileInput(p importer.ProfileRecord) store.ProfileInput {
	in := store.ProfileInput{
		OwnsPhone:          p.OwnsPhone,
		PhonePrimary:       p.PhonePrimary,
		PhoneForReporting:  p.PhoneForReporting,
		PhoneAlternate:     p.PhoneAlternate,
		FacilityID:         p.FacilityID,
		ServiceStartYear:   p.ServiceStartYear,
		HouseholdsServed:   p.HouseholdsServed,
		Education:          p.Education,
		EnglishSpeak:       p.EnglishSpeak,
		EnglishRead:        p.EnglishRead,
		EnglishWrite:       p.EnglishWrite,
		OtherLanguagesRaw:  p.OtherLanguagesRaw,
		ReceivesIncentive:  p.ReceivesIncentive,
		IncentiveFrequency: p.IncentiveFrequency,
		IncentiveAmountUGX: p.IncentiveAmountUGX,
		// Supervision is absent by decision: the source form records it per
		// service domain and carries no date, so last_supervised_on fills only
		// through the UI. See docs/odk-mapping.md.
	}
	for _, t := range p.Tools {
		in.Tools = append(in.Tools, store.ToolInput{ToolID: t.ToolID, Functional: t.Functional})
	}
	for _, d := range p.Domains {
		in.Domains = append(in.Domains, store.DomainInput{
			DomainID: d.DomainID, Provides: d.Provides, Trained: d.Trained})
	}
	return in
}

// lostRace names what the register did between the report and the commit. Every
// one of these passed validation; the answer changed underneath it.
func lostRace(err error) *domain.Problem {
	switch {
	case errors.Is(err, domain.ErrConflict):
		return &domain.Problem{Field: importer.ColNIN, Code: domain.ProblemDuplicateNIN,
			Message: "That NIN was claimed by another record between the report and this commit."}
	case errors.Is(err, domain.ErrForbidden):
		// Unreachable through the UI — the placement was resolved inside the
		// scope. It is still checked, because CreateTx's post-insert scope
		// check is the guarantee and this is what it looks like when it fires.
		return &domain.Problem{Field: importer.ColDistrict, Code: domain.ProblemOutsideScope,
			Message: "That location is not in your district."}
	case errors.Is(err, domain.ErrNotFound):
		return &domain.Problem{Code: domain.ProblemLostRace,
			Message: "The placement no longer exists."}
	}
	slog.Error("import row failed", "err", err)
	return &domain.Problem{Code: domain.ProblemLostRace,
		Message: "This row could not be written. The error has been logged."}
}

// quarantineRefused copies validation's refusals into import_quarantine, which
// is where they stay once the batch's own rows are anybody's to prune.
func (s *Server) quarantineRefused(ctx context.Context, sc auth.Scope, batch domain.Batch) (int, error) {
	count, after := 0, 0
	for {
		rows, err := s.store.Imports.Rows(ctx, sc, batch.ID,
			[]domain.RowStatus{domain.RowRejected}, after, 500)
		if err != nil {
			return count, err
		}
		if len(rows) == 0 {
			return count, nil
		}
		for _, row := range rows {
			after = row.Number
			if err := s.store.Imports.Quarantine(ctx, batch.ID, row); err != nil {
				return count, err
			}
			count++
		}
	}
}

// warnedDuplicate reports whether the only thing against a row is that someone
// of that name is already recorded at that location.
func warnedDuplicate(row domain.ImportRow) bool {
	for _, p := range row.Problems {
		if p.Code == domain.ProblemPossibleDuplicate {
			return true
		}
	}
	return false
}

// batchOr404 reads the batch in the path, inside the caller's scope. Another
// district's batch is a 404, not a 403: existence is scoped information here as
// much as it is on the register.
func (s *Server) batchOr404(w http.ResponseWriter, r *http.Request) (domain.Batch, bool) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return domain.Batch{}, false
	}
	batch, err := s.store.Imports.Get(r.Context(), auth.ScopeFrom(r.Context()), id)
	if err != nil {
		s.notFoundOrFail(w, r, err)
		return domain.Batch{}, false
	}
	return batch, true
}

func importPath(id int64) string { return "/imports/" + strconv.FormatInt(id, 10) }

func megabytes(n int64) string {
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// csvSafe reduces an uploaded filename to something a Content-Disposition can
// carry without quoting trouble.
func csvSafe(name string) string {
	name = strings.TrimSuffix(strings.TrimSuffix(name, ".csv"), ".xlsx")
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-") + ".csv"
}
