package http

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// safeNext guards the login form's ?next= against being used as an open
// redirect onto another host.
func TestSafeNext(t *testing.T) {
	cases := map[string]string{
		"":                        "/",
		"/users":                  "/users",
		"/users?role=admin":       "/users?role=admin",
		"https://evil.example/x":  "/",
		"//evil.example/x":        "/",
		"http://127.0.0.1:8099/x": "/",
		"users":                   "/",
		"/../etc/passwd":          "/../etc/passwd", // same-site; the browser resolves it and the mux 404s
	}
	for in, want := range cases {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

// The cascade posts one field per level and leaves the deeper ones empty until
// they are reachable, so the placement is the deepest field that came back.
func TestDeepestLocation(t *testing.T) {
	cases := []struct {
		name string
		form url.Values
		want int64
	}{
		{"village wins", url.Values{
			"district_id": {"72"}, "subcounty_id": {"2296"},
			"parish_id": {"4160"}, "village_id": {"51696"},
		}, 51696},
		{"parish when the village is blank", url.Values{
			"district_id": {"72"}, "subcounty_id": {"2296"},
			"parish_id": {"4160"}, "village_id": {""},
		}, 4160},
		{"district alone", url.Values{"district_id": {"72"}}, 72},
		{"nothing chosen", url.Values{"district_id": {""}}, 0},
		{"empty form", url.Values{}, 0},
		{"a non-numeric id is not a placement", url.Values{
			"district_id": {"72"}, "village_id": {"'; drop table chws--"},
		}, 72},
	}

	for _, c := range cases {
		r := httptest.NewRequest(http.MethodPost, "/chws/new", strings.NewReader(c.form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if err := r.ParseForm(); err != nil {
			t.Fatalf("%s: parse form: %v", c.name, err)
		}
		if got := deepestLocation(r); got != c.want {
			t.Errorf("%s: deepestLocation = %d, want %d", c.name, got, c.want)
		}
	}
}

// The NIN regex is the source form's own constraint, repeated so a typo is a
// field message rather than a CHECK violation.
func TestNINPattern(t *testing.T) {
	valid := []string{"CM90210987654X", "CF12345678901A"}
	invalid := []string{
		"",                // absent is handled before the pattern
		"CM9021098765X",   // thirteen
		"CM902109876543X", // fifteen
		"C190210987654X",  // digit in the first two
		"CM902109876543",  // no trailing letter
		"cm90210987654x",  // lower case: the handler uppercases first
	}
	for _, nin := range valid {
		if !ninPattern.MatchString(nin) {
			t.Errorf("ninPattern rejected %q", nin)
		}
	}
	for _, nin := range invalid {
		if ninPattern.MatchString(nin) {
			t.Errorf("ninPattern accepted %q", nin)
		}
	}
}

// People type phone numbers the way they say them. The stored form is the nine
// digits the schema's CHECK insists on.
func TestDigitsOnly(t *testing.T) {
	cases := map[string]string{
		"772123456":     "772123456",
		"0772123456":    "772123456",
		"0772 123 456":  "772123456",
		"0772-123-456":  "772123456",
		"+256772123456": "772123456",
		"256772123456":  "772123456",
		"":              "",
		"not a number":  "",
	}
	for in, want := range cases {
		if got := digitsOnly(in); got != want {
			t.Errorf("digitsOnly(%q) = %q, want %q", in, got, want)
		}
	}
}

// triState reads a yes / no / not-asked group. Every profile column is
// nullable, so "not asked" has to survive as nil rather than collapsing to no.
func TestTriState(t *testing.T) {
	cases := map[string]*bool{
		"yes":     boolPtr(true),
		"no":      boolPtr(false),
		"":        nil,
		"unknown": nil,
	}
	for value, want := range cases {
		form := url.Values{"owns_phone": {value}}
		r := httptest.NewRequest(http.MethodPost, "/p", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		got := triState(r, "owns_phone")
		switch {
		case want == nil && got != nil:
			t.Errorf("triState(%q) = %v, want nil", value, *got)
		case want != nil && got == nil:
			t.Errorf("triState(%q) = nil, want %v", value, *want)
		case want != nil && *got != *want:
			t.Errorf("triState(%q) = %v, want %v", value, *got, *want)
		}
	}
}

// trained_implies_provides is the schema's CHECK and the form's choice_filter.
// A post that claims training on an unoffered service has been tampered with;
// it is corrected to the safe reading rather than stored.
func TestDecodeDomainsDropsTrainingWithoutProvision(t *testing.T) {
	form := url.Values{
		"provides": {"1", "7"},
		"trained":  {"1", "4"}, // 4 is trained but not provided
	}
	r := httptest.NewRequest(http.MethodPost, "/p", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatal(err)
	}

	got := decodeDomains(r)
	if len(got) != 2 {
		t.Fatalf("decoded %d domains, want 2", len(got))
	}
	for _, d := range got {
		if !d.Provides {
			t.Errorf("domain %d decoded with provides false", d.DomainID)
		}
		if d.DomainID == 4 {
			t.Error("domain 4 was trained but not provided, and should not appear")
		}
		if d.DomainID == 7 && d.Trained {
			t.Error("domain 7 was provided but not trained, and should not be trained")
		}
		if d.DomainID == 1 && !d.Trained {
			t.Error("domain 1 was provided and trained, and should be trained")
		}
	}
}
