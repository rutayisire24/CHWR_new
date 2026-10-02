package domain

import "testing"

// A cadre is matched against an import cell by its slug or one of its aliases,
// with case and separators folded away. The ODK export alone spells one cadre
// four ways, and normalising them is what lets an import of the existing
// register land at all.
func TestCadreMatchesImport(t *testing.T) {
	vht := Cadre{Code: "vht", ImportAliases: []string{"village health team"}}
	chew := Cadre{Code: "chew", ImportAliases: []string{"chw", "community health extension worker"}}

	for _, in := range []string{"vht", "VHT", " Vht ", "Village Health Team", "village-health-team"} {
		if !vht.MatchesImport(in) {
			t.Errorf("vht.MatchesImport(%q) = false, want true", in)
		}
	}
	for _, in := range []string{"chew", "CHEW", "CHW", "chw", "Community Health Extension Worker"} {
		if !chew.MatchesImport(in) {
			t.Errorf("chew.MatchesImport(%q) = false, want true", in)
		}
	}
	for _, bad := range []string{"", "other", "vht chew", "nurse", "midwife"} {
		if vht.MatchesImport(bad) || chew.MatchesImport(bad) {
			t.Errorf("MatchesImport(%q) accepted", bad)
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

func TestEducationEnum(t *testing.T) {
	for _, e := range EducationLevels {
		if !e.Valid() || e.Label() == string(e) {
			t.Errorf("%s is in EducationLevels but invalid or unlabelled", e)
		}
	}
	if EducationLevel("degree").Valid() {
		t.Error("an unknown education level reports valid")
	}
}
