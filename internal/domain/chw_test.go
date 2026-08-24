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
