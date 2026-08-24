package domain

import "testing"

// Cadre determines placement: CHEWs sit at parish level, VHTs at village
// level. chws_set_placement is the enforcement; this is the copy the form asks
// with, so the two must not drift.
func TestCadrePlacementLevel(t *testing.T) {
	if got := CadreCHEW.PlacementLevel(); got != LevelParish {
		t.Errorf("CHEW placement = %s, want parish", got)
	}
	if got := CadreVHT.PlacementLevel(); got != LevelVillage {
		t.Errorf("VHT placement = %s, want village", got)
	}
}

func TestCadreValid(t *testing.T) {
	for _, c := range Cadres {
		if !c.Valid() {
			t.Errorf("%s is in Cadres but reports invalid", c)
		}
	}
	// The source form offered a multi-select with "other"; the decision was to
	// drop it, so anything outside the two enum members is rejected.
	for _, c := range []Cadre{"", "other", "CHEW", "vht "} {
		if Cadre(c).Valid() {
			t.Errorf("Cadre(%q) reports valid", c)
		}
	}
}

func TestSexValid(t *testing.T) {
	for _, s := range Sexes {
		if !s.Valid() {
			t.Errorf("%s is in Sexes but reports invalid", s)
		}
	}
	for _, s := range []Sex{"", "Male", "unknown"} {
		if Sex(s).Valid() {
			t.Errorf("Sex(%q) reports valid", s)
		}
	}
}

func TestRoleDistrict(t *testing.T) {
	// The half of users_scope_matches_role the application reads.
	if !RoleDistrictManager.District() || !RoleDistrictViewer.District() {
		t.Error("district roles must report District() true")
	}
	if RoleNationalAdmin.District() || RoleNationalViewer.District() {
		t.Error("national roles must report District() false")
	}
}

func TestValidationError(t *testing.T) {
	v := NewValidationError()
	if v.Any() {
		t.Error("a fresh ValidationError reports failures")
	}
	if err := v.OrNil(); err != nil {
		t.Errorf("OrNil on an empty error = %v, want nil", err)
	}

	v.Add("nin", "malformed")
	if !v.Any() {
		t.Error("Any() false after Add")
	}
	if err := v.OrNil(); err == nil {
		t.Error("OrNil on a populated error returned nil")
	}
}

// The profile columns are all nullable, so "no" and "not asked" are different
// answers and the accessors have to keep them apart.
func TestProfileNullableAnswers(t *testing.T) {
	no := false
	yes := true

	var unanswered Profile
	if unanswered.SpeaksEnglish() {
		t.Error("an unanswered profile reports English proficiency")
	}
	if unanswered.Answered() {
		t.Error("an unanswered profile reports Answered()")
	}

	// A profile row that exists but says no to everything has still been
	// answered — the answers are just negative.
	answered := Profile{Exists: true, OwnsPhone: &no, ReceivesIncentive: &no}
	if !answered.Answered() {
		t.Error("a profile of noes reports nothing answered")
	}

	partial := Profile{EnglishSpeak: &yes, EnglishRead: &yes, EnglishWrite: &no}
	if !partial.SpeaksEnglish() {
		t.Error("speaks and reads should count as English proficiency")
	}
	if got := partial.EnglishSummary(); got != "speaks, reads" {
		t.Errorf("EnglishSummary = %q, want %q", got, "speaks, reads")
	}
	if got := (Profile{EnglishWrite: &no}).EnglishSummary(); got != "" {
		t.Errorf("EnglishSummary with nothing true = %q, want empty", got)
	}
}

// The two phone columns are alternatives, not two lines for one person.
func TestProfilePhone(t *testing.T) {
	owner := Profile{PhonePrimary: "772123456"}
	if got := owner.Phone(); got != "772123456" {
		t.Errorf("Phone = %q for an owner", got)
	}
	reachable := Profile{PhoneAlternate: "700111222"}
	if got := reachable.Phone(); got != "700111222" {
		t.Errorf("Phone = %q for a non-owner with an alternate", got)
	}
	if got := (Profile{}).Phone(); got != "" {
		t.Errorf("Phone = %q with neither recorded", got)
	}
}

func TestEducationAndIncentiveEnums(t *testing.T) {
	for _, e := range EducationLevels {
		if !e.Valid() || e.Label() == string(e) {
			t.Errorf("%s is in EducationLevels but invalid or unlabelled", e)
		}
	}
	for _, f := range IncentiveFrequencies {
		if !f.Valid() || f.Label() == string(f) {
			t.Errorf("%s is in IncentiveFrequencies but invalid or unlabelled", f)
		}
	}
	if EducationLevel("degree").Valid() || IncentiveFrequency("weekly").Valid() {
		t.Error("a value outside the enum reported valid")
	}
}
