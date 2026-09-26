package http

import (
	"net/http"
	"testing"
	"time"

	"chwr/internal/config"
	"chwr/internal/store"
)

// The birth date the API exposes is a real captured date_of_birth when there is
// one, and otherwise an approximation from age (decision D4): 1 January of the
// estimated birth year, flagged as approximate. An unknown age yields no birth
// date rather than a fabricated one.
func TestApproxBirthDate(t *testing.T) {
	age := int16(41)
	captured := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if d, approx := approxBirthDate(store.APICHW{AgeYears: &age, AgeCapturedOn: captured}); d != "1985-01-01" || !approx {
		t.Errorf("age-derived birth date = (%q, %v), want (1985-01-01, true)", d, approx)
	}

	dob := time.Date(1990, 5, 4, 0, 0, 0, 0, time.UTC)
	if d, approx := approxBirthDate(store.APICHW{DateOfBirth: &dob, AgeYears: &age, AgeCapturedOn: captured}); d != "1990-05-04" || approx {
		t.Errorf("real birth date = (%q, %v), want (1990-05-04, false)", d, approx)
	}

	if d, approx := approxBirthDate(store.APICHW{}); d != "" || approx {
		t.Errorf("unknown age birth date = (%q, %v), want (\"\", false)", d, approx)
	}
}

// The consumer's field mapping is a contract: the eCHIS user-management tool
// reads fullName, nationalId, id, phone, birthDate, gender, parish,
// district.name, subcounty.name, position.facility.name and villages[0]. A
// rename on our side silently breaks provisioning, so the shape is pinned here.
func TestAPICHWJSONMatchesEchisMapping(t *testing.T) {
	s := &Server{cfg: config.Config{SupervisionIntervalDays: 30}}
	age := int16(41)
	captured := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sup := int64(1)
	c := store.APICHW{
		ID: 7, NIN: "CM90210987654X", FirstName: "Grace", LastName: "Nakato",
		Sex: "female", Cadre: "vht", Status: "active",
		AgeYears: &age, AgeCapturedOn: captured,
		District: "HOIMA", Subcounty: "BUHANIKA", Parish: "BUTEMA", Village: "KIFURANSA",
		Facility: "Bombo HC II", Phone: "772100200",
		Villages: []string{"KIFURANSA"}, SupervisorID: &sup, SupervisorName: "John Chew",
		UpdatedAt: captured,
	}
	m := s.apiCHWJSON(c)

	if m["id"] != int64(7) {
		t.Errorf("id = %v, want 7 (the HW-ID)", m["id"])
	}
	if m["fullName"] != "Grace Nakato" {
		t.Errorf("fullName = %v", m["fullName"])
	}
	if m["nationalId"] != "CM90210987654X" {
		t.Errorf("nationalId = %v", m["nationalId"])
	}
	if m["gender"] != "female" {
		t.Errorf("gender = %v, want female", m["gender"])
	}
	if m["phone"] != "772100200" {
		t.Errorf("phone = %v", m["phone"])
	}
	if m["parish"] != "BUTEMA" {
		t.Errorf("parish = %v", m["parish"])
	}
	if m["birthDate"] != "1985-01-01" || m["birthDateApproximate"] != true {
		t.Errorf("birthDate = %v (approx %v)", m["birthDate"], m["birthDateApproximate"])
	}
	if got := nested(t, m, "district", "name"); got != "HOIMA" {
		t.Errorf("district.name = %v, want HOIMA", got)
	}
	if got := nested(t, m, "subcounty", "name"); got != "BUHANIKA" {
		t.Errorf("subcounty.name = %v, want BUHANIKA", got)
	}
	if got := nested(t, m, "position", "facility", "name"); got != "Bombo HC II" {
		t.Errorf("position.facility.name = %v", got)
	}
	villages, ok := m["villages"].([]string)
	if !ok || len(villages) != 1 || villages[0] != "KIFURANSA" {
		t.Errorf("villages = %v, want [KIFURANSA]", m["villages"])
	}
	sv, ok := m["supervisor"].(map[string]any)
	if !ok || sv["hwId"] != int64(1) {
		t.Errorf("supervisor = %v, want {hwId:1,...}", m["supervisor"])
	}
}

// nested walks a chain of string keys through nested map[string]any and returns
// the leaf, failing the test if any hop is not a map.
func nested(t *testing.T, m map[string]any, keys ...string) any {
	t.Helper()
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("expected a map at key %q, got %T", k, cur)
		}
		cur = mm[k]
	}
	return cur
}

func TestBearerToken(t *testing.T) {
	cases := map[string]struct {
		header string
		want   string
		ok     bool
	}{
		"plain":       {"Bearer abc.def", "abc.def", true},
		"lowercase":   {"bearer abc", "abc", true},
		"no scheme":   {"abc", "", false},
		"empty token": {"Bearer ", "", false},
		"basic":       {"Basic abc", "", false},
		"missing":     {"", "", false},
	}
	for name, c := range cases {
		r := httptest_newRequest(c.header)
		got, ok := bearerToken(r)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: bearerToken = (%q,%v), want (%q,%v)", name, got, ok, c.want, c.ok)
		}
	}
}

func httptest_newRequest(authHeader string) *http.Request {
	r, _ := http.NewRequest(http.MethodGet, "/api/v1/chws", nil)
	if authHeader != "" {
		r.Header.Set("Authorization", authHeader)
	}
	return r
}

func TestEffectiveLimitAndFirstNonEmpty(t *testing.T) {
	for _, c := range []struct{ in, want int }{{0, 50}, {-5, 50}, {2000, 50}, {20, 20}, {1000, 1000}} {
		if got := effectiveLimit(c.in); got != c.want {
			t.Errorf("effectiveLimit(%d) = %d, want %d", c.in, got, c.want)
		}
	}
	if firstNonEmpty("", "b") != "b" || firstNonEmpty("a", "b") != "a" || firstNonEmpty("", "") != "" {
		t.Error("firstNonEmpty picked the wrong value")
	}
}
