package web

import "testing"

// A district manager's dashboard names its own tier. Pinning an "s" on the end
// of Level.Label() puts "Subcountys reached" on the page.
func TestPlural(t *testing.T) {
	fn := funcs["plural"].(func(string) string)
	cases := map[string]string{
		"Region": "Regions", "District": "Districts", "County": "Counties",
		"Subcounty": "Subcounties", "Parish": "Parishes", "Village": "Villages",
		"": "",
	}
	for in, want := range cases {
		if got := fn(in); got != want {
			t.Errorf("plural(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGroup(t *testing.T) {
	cases := map[int64]string{
		0: "0", 7: "7", 999: "999", 1000: "1,000",
		24573: "24,573", 84635: "84,635", -1279: "-1,279",
	}
	for in, want := range cases {
		if got := group(in); got != want {
			t.Errorf("group(%d) = %q, want %q", in, got, want)
		}
	}
}

// "Nothing measured" and "nothing found" are different answers, and the
// register's own rule about nullable columns applies to its dashboard too.
func TestShare(t *testing.T) {
	fn := funcs["share"].(func(int64, int64) string)
	cases := []struct {
		n, total int64
		want     string
	}{
		{4881, 24573, "20%"},
		{347, 24573, "1.4%"},
		{1, 24573, "<0.1%"},
		{0, 24573, "0.0%"},
		{5, 0, "—"},
	}
	for _, c := range cases {
		if got := fn(c.n, c.total); got != c.want {
			t.Errorf("share(%d, %d) = %q, want %q", c.n, c.total, got, c.want)
		}
	}
}

// The meters and the inline bars are SVG because the width is data and the
// content security policy allows no inline style attribute to carry it.
func TestPctWidth(t *testing.T) {
	fn := funcs["pctwidth"].(func(int64, int64) string)
	cases := []struct {
		n, total int64
		want     string
	}{
		{23294, 24573, "94.80%"},
		{146, 146, "100.00%"},
		{0, 24573, "0.00%"},
		{5, 0, "0%"},
		{200, 100, "100.00%"}, // clamped: a bar may not leave its track
	}
	for _, c := range cases {
		if got := fn(c.n, c.total); got != c.want {
			t.Errorf("pctwidth(%d, %d) = %q, want %q", c.n, c.total, got, c.want)
		}
	}
}
