package http

import (
	"encoding/csv"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"chwr/internal/auth"
	"chwr/internal/domain"
	"chwr/internal/importer"
	"chwr/internal/store"
)

// exportColumns is the header, and the order every row follows.
//
// The columns the importer reads keep the importer's own names, so a file that
// comes out of the register can go back into it. The rest are what the register
// knows and an upload cannot supply — the id, the derived placement, the status
// and the timestamps — and the importer names them as unknown and ignores them,
// which is the right answer for a column it must not let anyone set.
var exportColumns = []string{
	"id",
	importer.ColNIN, importer.ColFirstName, importer.ColLastName,
	importer.ColSex, importer.ColCadre, importer.ColAge, "age_captured_on",
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
	"received_supervision", "last_supervised_on",
	"created_at", "updated_at",
}

// chwsExport streams the register as CSV, through the same Scope and the same
// Filter as the listing behind it. A district user exports their district; a
// filtered listing exports what it shows.
func (s *Server) chwsExport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sc := auth.ScopeFrom(ctx)
	user := auth.MustUser(ctx)
	filter, _ := decodeFilter(r)

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

	err := s.store.Export.Rows(ctx, sc, filter, func(row store.ExportRow) error {
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
// *bool, not a fact about the register.
func exportRecord(r store.ExportRow) []string {
	return []string{
		strconv.FormatInt(r.ID, 10),
		r.NIN, r.FirstName, r.LastName,
		string(r.Sex), string(r.Cadre), intPtrString(r.AgeYears), dateString(&r.AgeCapturedOn),
		r.District, r.Subcounty, r.Parish, r.Village,
		r.LocationCode,
		string(r.Status), timeDateString(r.DeactivatedAt), r.DeactivationReason,
		boolString(r.OwnsPhone), r.PhonePrimary, boolString(r.PhoneForReporting),
		r.PhoneAlternate,
		r.Facility, intPtrString(r.ServiceStartYear), int32PtrString(r.HouseholdsServed),
		string(r.Education),
		englishString(r.EnglishSpeak, r.EnglishRead, r.EnglishWrite), r.OtherLanguagesRaw,
		boolString(r.ReceivesIncentive), string(r.IncentiveFrequency),
		int32PtrString(r.IncentiveAmountUGX),
		r.Tools, r.ToolsFunctional,
		r.Services, r.Trained,
		boolString(r.ReceivedSupervision), dateString(r.LastSupervisedOn),
		r.CreatedAt.Format(time.RFC3339), r.UpdatedAt.Format(time.RFC3339),
	}
}

// boolString writes the three states the register keeps apart. An empty cell is
// "not asked" and reads back as NULL; only "no" is a recorded no.
func boolString(b *bool) string {
	if b == nil {
		return ""
	}
	if *b {
		return "yes"
	}
	return "no"
}

// englishString folds the three proficiency flags back into the multi-select
// the importer reads. All three recorded false is `none`, which is what the
// source form's own choice list calls it — and is not the same as an empty cell.
func englishString(speak, read, write *bool) string {
	if speak == nil && read == nil && write == nil {
		return ""
	}
	var parts []string
	for _, p := range []struct {
		flag *bool
		name string
	}{{speak, "speak"}, {read, "read"}, {write, "write"}} {
		if p.flag != nil && *p.flag {
			parts = append(parts, p.name)
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ";")
}

func intPtrString(n *int16) string {
	if n == nil {
		return ""
	}
	return strconv.Itoa(int(*n))
}

func int32PtrString(n *int32) string {
	if n == nil {
		return ""
	}
	return strconv.Itoa(int(*n))
}

func dateString(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.DateOnly)
}

func timeDateString(t *time.Time) string { return dateString(t) }

// exportFilename names the file for the folder it lands in, beside a dozen
// others. It carries the scope and the date, and says when a filter was applied
// — a partial export that looks like a full one is a file someone will later
// mistake for the register.
func exportFilename(user domain.User, f store.Filter) string {
	parts := []string{"chw-register"}
	if user.DistrictName != "" {
		parts = append(parts, strings.ToLower(strings.ReplaceAll(user.DistrictName, " ", "-")))
	}
	if f.Query != "" || f.Cadre != "" || f.Status != "" || f.LocationID != 0 {
		parts = append(parts, "filtered")
	}
	parts = append(parts, time.Now().Format("20060102"))
	return fmt.Sprintf("%s.csv", strings.Join(parts, "-"))
}
