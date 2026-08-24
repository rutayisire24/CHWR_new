package domain

import "testing"

// The NIN rule is the source form's own constraint, repeated in Go so a typo is
// a field message rather than a CHECK violation. It lives here because the CHW
// form and the bulk importer must apply the same one.
func TestValidNIN(t *testing.T) {
	valid := []string{"CM90210987654X", "CF12345678901A"}
	invalid := []string{
		"",                // absent is handled before the pattern
		"CM9021098765X",   // thirteen
		"CM902109876543X", // fifteen
		"C190210987654X",  // digit in the first two
		"CM902109876543",  // no trailing letter
		"cm90210987654x",  // lower case: callers uppercase first
		"CM 0210987654X",  // a space is not a NIN character
	}
	for _, nin := range valid {
		if !ValidNIN(nin) {
			t.Errorf("ValidNIN(%q) = false, want true", nin)
		}
	}
	for _, nin := range invalid {
		if ValidNIN(nin) {
			t.Errorf("ValidNIN(%q) = true, want false", nin)
		}
	}
}

func TestValidAge(t *testing.T) {
	for _, ok := range []int{18, 42, 99} {
		if !ValidAge(ok) {
			t.Errorf("ValidAge(%d) = false, want true", ok)
		}
	}
	// 17 is the source form's own boundary: `> 17 and <= 99`.
	for _, bad := range []int{-1, 0, 17, 100, 1990} {
		if ValidAge(bad) {
			t.Errorf("ValidAge(%d) = true, want false", bad)
		}
	}
}

// The ODK export spells one cadre four ways. Normalising them is what lets an
// import of the existing register land at all; it is not leniency about which
// cadres exist, of which there are exactly two.
func TestParseCadre(t *testing.T) {
	cases := map[string]Cadre{
		"vht":                 CadreVHT,
		"VHT":                 CadreVHT,
		" Vht ":               CadreVHT,
		"Village Health Team": CadreVHT,
		"chew":                CadreCHEW,
		"CHEW":                CadreCHEW,
		"CHW":                 CadreCHEW,
	}
	for in, want := range cases {
		got, ok := ParseCadre(in)
		if !ok || got != want {
			t.Errorf("ParseCadre(%q) = (%q, %v), want (%q, true)", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "other", "vht chew", "nurse", "midwife"} {
		if got, ok := ParseCadre(bad); ok {
			t.Errorf("ParseCadre(%q) = (%q, true), want false", bad, got)
		}
	}
}

func TestParseSex(t *testing.T) {
	cases := map[string]Sex{
		"male": SexMale, "Male": SexMale, "M": SexMale,
		"female": SexFemale, "FEMALE": SexFemale, "f": SexFemale,
	}
	for in, want := range cases {
		got, ok := ParseSex(in)
		if !ok || got != want {
			t.Errorf("ParseSex(%q) = (%q, %v), want (%q, true)", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "x", "other", "unknown"} {
		if _, ok := ParseSex(bad); ok {
			t.Errorf("ParseSex(%q) accepted", bad)
		}
	}
}
