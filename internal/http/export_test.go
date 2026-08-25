package http

import (
	"strings"
	"testing"

	"chwr/internal/domain"
	"chwr/internal/store"
)

func ptr[T any](v T) *T { return &v }

// The register keeps "no" and "not asked" apart in every nullable column. A
// file that spelled both as empty would throw that away on the way out, and an
// import of it would come back as a record that answered no.
func TestExportKeepsNoApartFromNotAsked(t *testing.T) {
	cases := map[string]*bool{
		"":    nil,
		"yes": ptr(true),
		"no":  ptr(false),
	}
	for want, value := range cases {
		if got := boolString(value); got != want {
			t.Errorf("boolString(%v) = %q, want %q", value, got, want)
		}
	}
}

// English is a proficiency multi-select on the way in and has to be one on the
// way out, in the spelling the importer reads back.
func TestExportEnglishRoundTripsIntoTheImportersSpelling(t *testing.T) {
	cases := []struct {
		name               string
		speak, read, write *bool
		want               string
	}{
		{"nothing asked", nil, nil, nil, ""},
		{"speaks and reads", ptr(true), ptr(true), ptr(false), "speak;read"},
		{"all three", ptr(true), ptr(true), ptr(true), "speak;read;write"},
		{"writes only", ptr(false), ptr(false), ptr(true), "write"},
		// Three recorded noes are the form's own `none`, and are not the same
		// answer as an empty cell above.
		{"recorded none", ptr(false), ptr(false), ptr(false), "none"},
	}
	for _, c := range cases {
		if got := englishString(c.speak, c.read, c.write); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
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
			[]string{"chw-register"}, []string{"filtered"}},
		{"one district", district, store.Filter{},
			[]string{"chw-register", "abim"}, []string{"filtered"}},
		{"a search", national, store.Filter{Query: "okello"},
			[]string{"filtered"}, nil},
		{"a cadre", national, store.Filter{Cadre: domain.CadreCHEW},
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
	for _, column := range []string{"nin", "first_name", "last_name", "sex", "cadre",
		"district", "subcounty", "parish", "village", "location_code",
		"phone_owner", "facility", "education", "english", "tools", "services", "trained"} {
		if !seen[column] {
			t.Errorf("the export does not carry %q, so a file cannot round-trip", column)
		}
	}
}
