package http

import (
	"encoding/csv"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"hwr/internal/auth"
	"hwr/internal/domain"
	"hwr/internal/importer"
	"hwr/internal/store"
)

// exportColumns is the header, and the order every row follows.
//
// The columns the importer reads keep the importer's own names, so a file that
// comes out of the register can go back into it. The rest are what the register
// knows and an upload cannot supply — the id, the worker code, the derived
// placement, the status and the timestamps — and the importer names them as
// unknown and ignores them, which is the right answer for a column it must not
// let anyone set.
//
// A birth date goes out as dob when it is a date, and as age_years when it is
// an estimate from an age: an import reads either, and turning an estimate
// into a date would claim a precision nobody recorded.
var exportColumns = []string{
	"id", "worker_code",
	importer.ColNIN, importer.ColFirstName, importer.ColLastName, importer.ColOtherName,
	importer.ColSex, importer.ColCadre, importer.ColDOB, importer.ColAge,
	importer.ColDistrict, importer.ColSubcounty, importer.ColParish, importer.ColVillage,
	importer.ColCode,
	"status", "deactivated_on", "deactivation_reason",
	importer.ColPhoneOwner, importer.ColPhonePrimary, importer.ColPhoneReporting,
	importer.ColPhoneAlternate,
	importer.ColFacility, importer.ColServiceYear, importer.ColHouseholds,
	importer.ColEducation,
	importer.ColEnglish, importer.ColOtherLanguages,
	importer.ColIncentive, importer.ColIncentiveFreq, importer.ColIncentiveAmount,
	importer.ColTools, importer.ColToolsFunctional,
	importer.ColServices, importer.ColTrained,
	importer.ColSupervised, importer.ColLastSupervised,
	"created_on", "last_updated_on",
}

// workersExport streams the register as CSV, through the same Scope and the
// same Filter as the listing behind it. A district user exports their district;
// a filtered listing exports what it shows.
func (s *Server) workersExport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sc := auth.ScopeFrom(ctx)
	user := auth.MustUser(ctx)
	cadres, err := s.store.Deployments.Cadres(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	filter, _ := decodeFilter(r, cadres)

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+exportFilename(user, filter)+`"`)

	out := csv.NewWriter(w)
	defer out.Flush()
	if err := out.Write(exportColumns); err != nil {
		slog.Error("export header failed", "err", err)
		return
	}

	// Flushed in batches so a large export starts arriving immediately rather
	// than sitting in a buffer, and so a client that goes away stops the query
	// rather than filling memory behind it.
	const flushEvery = 500
	written := 0

	err = s.store.Export.Rows(ctx, sc, filter, func(row store.ExportRow) error {
		if err := out.Write(exportRecord(row)); err != nil {
			return err
		}
		if written++; written%flushEvery == 0 {
			out.Flush()
			return out.Error()
		}
		return nil
	})
	if err != nil {
		// The header and some rows are already on the wire, so there is no
		// error page to show: an HTTP status was chosen the moment the first
		// byte went out. Ending the file short is the honest failure, and the
		// log carries why.
		slog.Error("export truncated", "rows", written, "err", err)
	}
}

// exportRecord flattens one row into the file's columns. Formatting lives here
// rather than in the store, because "yes" and an empty cell are a rendering of
// an answer, not a fact about the register.
func exportRecord(r store.ExportRow) []string {
	dob, age := "", ""
	if r.DOB != nil {
		if r.DOBEstimated {
			age = strconv.Itoa(*(domain.Person{DOB: r.DOB}).Age())
		} else {
			dob = r.DOB.Format(time.DateOnly)
		}
	}
	answer := func(column string) string {
		return strings.Join(r.Answers[importer.SurveyQuestion(column)], ";")
	}
	return []string{
		strconv.FormatInt(r.ID, 10), r.Code,
		r.NIN, r.FirstName, r.LastName, r.OtherName,
		string(r.Sex), r.Cadre, dob, age,
		r.District, r.Subcounty, r.Parish, r.Village,
		r.LocationCode,
		string(r.Status), dateString(r.DeactivatedAt), r.DeactivationReason,
		answer(importer.ColPhoneOwner), r.PhoneOwn, answer(importer.ColPhoneReporting),
		r.PhoneAlternate,
		r.Facility, answer(importer.ColServiceYear), answer(importer.ColHouseholds),
		string(r.Education),
		englishString(r.English), answer(importer.ColOtherLanguages),
		answer(importer.ColIncentive), answer(importer.ColIncentiveFreq),
		answer(importer.ColIncentiveAmount),
		answer(importer.ColTools), answer(importer.ColToolsFunctional),
		answer(importer.ColServices), answer(importer.ColTrained),
		answer(importer.ColSupervised), answer(importer.ColLastSupervised),
		r.CreatedOn.Format(time.RFC3339), r.LastUpdatedOn.Format(time.RFC3339),
	}
}

// englishString folds the three grades back into the multi-select the importer
// reads. Three recorded nones are `none`, which is what the source form's own
// choice list calls it — and is not the same as an empty cell, which is
// English never asked about.
func englishString(e domain.LanguageSkill) string {
	if e.Understanding == "" && e.Reading == "" && e.Writing == "" {
		return ""
	}
	var parts []string
	for _, p := range []struct {
		grade domain.Proficiency
		name  string
	}{{e.Understanding, "speak"}, {e.Reading, "read"}, {e.Writing, "write"}} {
		if p.grade.Can() {
			parts = append(parts, p.name)
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ";")
}

func dateString(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.DateOnly)
}

// exportFilename names the file for the folder it lands in, beside a dozen
// others. It carries the scope and the date, and says when a filter was applied
// — a partial export that looks like a full one is a file someone will later
// mistake for the register.
func exportFilename(user domain.User, f store.Filter) string {
	parts := []string{"health-worker-register"}
	if user.DistrictName != "" {
		parts = append(parts, strings.ToLower(strings.ReplaceAll(user.DistrictName, " ", "-")))
	}
	if f.Query != "" || f.Cadre != "" || f.Status != "" || f.LocationID != 0 {
		parts = append(parts, "filtered")
	}
	parts = append(parts, time.Now().Format("20060102"))
	return fmt.Sprintf("%s.csv", strings.Join(parts, "-"))
}
