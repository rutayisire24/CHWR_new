package store

import (
	"testing"
	"time"

	"chwr/internal/domain"
)

// A search box that made the user say whether they were typing a name or a NIN
// would be a box they get wrong. The shape of the input decides instead.
func TestNinish(t *testing.T) {
	nins := []string{"CM90210987654X", "CM9021", "cm90210987654x", "CN20000000123K"}
	for _, q := range nins {
		if !ninish(q) {
			t.Errorf("ninish(%q) = false, want true", q)
		}
	}

	names := []string{
		"Okello",         // letters only
		"Grace Okello",   // a space is always a name
		"O'Brien",        // punctuation is never a NIN
		"CM",             // too short to be a partial NIN
		"Kiprotich-Rono", // hyphenated surname
		"",               // nothing typed
	}
	for _, q := range names {
		if ninish(q) {
			t.Errorf("ninish(%q) = true, want false", q)
		}
	}
}

// The dashboard groups the register by pulling an ancestor id out of
// `locations.path`, which is '/region/district/county/subcounty/parish/village/'.
// An off-by-one here would not fail: it would group by the wrong tier and
// still render a chart, which is the worst kind of wrong.
func TestSegmentMatchesTheLadder(t *testing.T) {
	// A leading '/' makes split_part's first field empty, so the ladder starts
	// at 2.
	want := map[domain.Level]int{
		domain.LevelRegion:    2,
		domain.LevelDistrict:  3,
		domain.LevelCounty:    4,
		domain.LevelSubcounty: 5,
		domain.LevelParish:    6,
		domain.LevelVillage:   7,
	}
	for level, n := range want {
		if got := segment(level); got != n {
			t.Errorf("segment(%s) = %d, want %d", level, got, n)
		}
	}

	// Every level the dashboard can be asked for is in the map above; a level
	// added to the enum without one here would silently group by village.
	for i, level := range []domain.Level{
		domain.LevelRegion, domain.LevelDistrict, domain.LevelCounty,
		domain.LevelSubcounty, domain.LevelParish, domain.LevelVillage,
	} {
		if got := segment(level); got != i+2 {
			t.Errorf("segment(%s) = %d, want %d (position %d in the ladder)", level, got, i+2, i)
		}
	}
}

// Age bands zero-fill from a query that only returns bands somebody falls into,
// so the labels and the band arithmetic have to agree on how many there are.
func TestAgeBandLabelsCoverTheBuckets(t *testing.T) {
	// The SQL is least(9, greatest(0, (age - 20) / 5)), so band 9 is the last.
	if len(ageBandLabels) != 10 {
		t.Fatalf("ageBandLabels has %d entries, want 10 (bands 0..9)", len(ageBandLabels))
	}
	if ageBandLabels[0] != "18–24" {
		t.Errorf("first band is %q; the column check floors age at 18, so band 0 is open at the bottom", ageBandLabels[0])
	}
	if ageBandLabels[9] != "65+" {
		t.Errorf("last band is %q; least(9, …) folds every older age into it, so it is open at the top", ageBandLabels[9])
	}
}

// Saving the register form opens a posting only when it has to. Both ways of
// getting this wrong are silent: a missed redeployment leaves a reactivated
// worker active with nowhere to serve, and a spurious one ends a posting on
// every save and fills the history with transfers that never happened.
func TestRedeployment(t *testing.T) {
	ended := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	vht := func(endedOn *time.Time) *domain.Deployment {
		return &domain.Deployment{CadreID: 1, LocationID: 51675, EndedOn: endedOn}
	}
	sameVHT := WorkerInput{CadreID: 1, LocationID: 51675}
	otherVillage := WorkerInput{CadreID: 1, LocationID: 51676}
	chewAtParish := WorkerInput{CadreID: 2, LocationID: 5199}

	cases := []struct {
		name       string
		status     domain.WorkerStatus
		deployment *domain.Deployment
		in         WorkerInput
		want       bool
		reason     string
	}{
		{"placed and unchanged", domain.WorkerActive, vht(nil), sameVHT, false, ""},
		{"moved to another village", domain.WorkerActive, vht(nil), otherVillage, true, "transfer"},
		{"promoted, which moves them too", domain.WorkerActive, vht(nil), chewAtParish, true, "recadre"},
		{"never deployed", domain.WorkerActive, nil, sameVHT, true, "transfer"},
		// The prefilled form posts the placement they left from.
		{"reactivated, same placement", domain.WorkerActive, vht(&ended), sameVHT, true, "transfer"},
		{"reactivated, new placement", domain.WorkerActive, vht(&ended), otherVillage, true, "transfer"},
		// Correcting a retired worker's name must not try to deploy them.
		{"inactive, unchanged", domain.WorkerInactive, vht(&ended), sameVHT, false, ""},
		{"inactive, moved", domain.WorkerInactive, vht(&ended), otherVillage, true, "transfer"},
	}
	for _, c := range cases {
		before := domain.HealthWorker{Status: c.status, Deployment: c.deployment}
		got, reason := redeployment(before, c.in)
		if got != c.want || reason != c.reason {
			t.Errorf("%s: redeployment = (%v, %q), want (%v, %q)", c.name, got, reason, c.want, c.reason)
		}
	}
}
