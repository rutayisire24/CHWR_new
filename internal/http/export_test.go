package http

import (
	"strings"
	"testing"
	"time"

	"hwr/internal/domain"
	"hwr/internal/store"
)

// English is a proficiency multi-select on the way in and has to be one on the
// way out, in the spelling the importer reads back.
func TestExportEnglishRoundTripsIntoTheImportersSpelling(t *testing.T) {
	none, basic, good := domain.ProficiencyNone, domain.ProficiencyBasic, domain.ProficiencyGood
	cases := []struct {
		name string
		e    domain.LanguageSkill
		want string
	}{
		{"nothing asked", domain.LanguageSkill{}, ""},
		{"speaks and reads", domain.LanguageSkill{Understanding: basic, Reading: good, Writing: none}, "speak;read"},
		{"all three", domain.LanguageSkill{Understanding: good, Reading: good, Writing: basic}, "speak;read;write"},
		{"writes only, the rest unasked", domain.LanguageSkill{Writing: basic}, "write"},
		// Three recorded nones are the form's own `none`, and are not the same
		// answer as an empty cell above.
		{"recorded none", domain.LanguageSkill{Understanding: none, Reading: none, Writing: none}, "none"},
	}
	for _, c := range cases {
		if got := englishString(c.e); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// An estimated birth date goes out as the age it came from, an exact one as
// a date, and a survey answer as the codes the importer reads.
func TestExportRowsSpellAnswersTheImportersWay(t *testing.T) {
	exact := time.Date(1990, 3, 4, 0, 0, 0, 0, time.UTC)
	row := exportRecord(store.ExportRow{DOB: &exact, Answers: domain.Answers{
		"owns_phone": {"no"}, "tools_held": {"bicycle", "torch"}, "last_supervised_on": {"2026-03-01"},
	}})
	at := func(column string) string {
		for i, c := range exportColumns {
			if c == column {
				return row[i]
			}
		}
		t.Fatalf("no column %q", column)
		return ""
	}
	if at("dob") != "1990-03-04" || at("age_years") != "" {
		t.Errorf("an exact birth date went out as dob %q, age %q", at("dob"), at("age_years"))
	}
	if at("phone_owner") != "no" || at("tools") != "bicycle;torch" || at("last_supervised_on") != "2026-03-01" {
		t.Errorf("answers went out as %q / %q / %q", at("phone_owner"), at("tools"), at("last_supervised_on"))
	}
	if at("receives_incentive") != "" {
		t.Error("an unasked question went out answered")
	}

	estimated := domain.EstimateDOB(40, time.Now())
	row = exportRecord(store.ExportRow{DOB: &estimated, DOBEstimated: true})
	if at("dob") != "" || at("age_years") != "40" {
		t.Errorf("an estimate went out as dob %q, age %q", at("dob"), at("age_years"))
	}
}

// A partial export that looks like a full one is a file someone will later
// mistake for the register.
func TestExportFilenameSaysWhatItHolds(t *testing.T) {
	national := domain.User{}
	district := domain.User{DistrictName: "ABIM"}

	cases := []struct {
		name   string
		user   domain.User
		filter store.Filter
		want   []string
		absent []string
	}{
		{"whole register", national, store.Filter{},
			[]string{"health-worker-register"}, []string{"filtered"}},
		{"one district", district, store.Filter{},
			[]string{"health-worker-register", "abim"}, []string{"filtered"}},
		{"a search", national, store.Filter{Query: "okello"},
			[]string{"filtered"}, nil},
		{"a cadre", national, store.Filter{Cadre: "chew"},
			[]string{"filtered"}, nil},
		{"a location", district, store.Filter{LocationID: 42},
			[]string{"abim", "filtered"}, nil},
	}
	for _, c := range cases {
		got := exportFilename(c.user, c.filter)
		for _, want := range c.want {
			if !strings.Contains(got, want) {
				t.Errorf("%s: %q does not carry %q", c.name, got, want)
			}
		}
		for _, absent := range c.absent {
			if strings.Contains(got, absent) {
				t.Errorf("%s: %q claims to be %q", c.name, got, absent)
			}
		}
		if !strings.HasSuffix(got, ".csv") {
			t.Errorf("%s: %q is not a .csv", c.name, got)
		}
	}
}

// The columns the importer owns must keep the importer's own names, or a file
// that comes out of the register cannot go back into it.
func TestExportColumnsMatchTheImportVocabulary(t *testing.T) {
	if len(exportColumns) != len(exportRecord(store.ExportRow{})) {
		t.Fatalf("the header has %d columns and a row has %d",
			len(exportColumns), len(exportRecord(store.ExportRow{})))
	}

	seen := map[string]bool{}
	for _, column := range exportColumns {
		if seen[column] {
			t.Errorf("column %q appears twice", column)
		}
		seen[column] = true
	}

	// Every column the importer reads, spelled the way it reads it. The
	// register-only columns beside them are named as unknown on an import,
	// which is right for values an upload must not be able to set.
	for _, column := range []string{"nin", "first_name", "last_name", "other_name", "sex", "cadre",
		"dob", "age_years", "district", "subcounty", "parish", "village", "location_code",
		"phone_owner", "facility", "education", "english", "tools", "services", "trained",
		"received_supervision", "last_supervised_on"} {
		if !seen[column] {
			t.Errorf("the export does not carry %q, so a file cannot round-trip", column)
		}
	}
}
