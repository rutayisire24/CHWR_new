package store

import "testing"

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
