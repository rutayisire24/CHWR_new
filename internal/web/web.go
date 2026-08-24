// Package web holds the templates and static assets, embedded into the binary
// and parsed once at startup.
package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Static is the asset tree served under /static/.
func Static() fs.FS {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(fmt.Sprintf("web: static assets missing: %v", err))
	}
	return sub
}

// Templates is the parsed page set. Every page is layout.html plus its own
// file, so a page cannot render without the chrome and the nav.
type Templates struct {
	pages map[string]*template.Template
}

// Parse builds the page set. It fails loudly at startup rather than on the
// first request that needs a broken template.
func Parse() (*Templates, error) {
	names, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("scan templates: %w", err)
	}

	t := &Templates{pages: make(map[string]*template.Template)}
	for _, name := range names {
		base := pageName(name)
		if base == "layout" {
			continue
		}
		tmpl, err := template.New("layout.html").Funcs(funcs).
			ParseFS(templateFS, "templates/layout.html", name)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", base, err)
		}
		t.pages[base] = tmpl
	}
	if len(t.pages) == 0 {
		return nil, fmt.Errorf("parse templates: no pages found")
	}
	return t, nil
}

// Render writes a page. It buffers first: a template that fails halfway must
// not leave a half-written 200 on the wire.
func (t *Templates) Render(w http.ResponseWriter, status int, page string, data any) error {
	tmpl, ok := t.pages[page]
	if !ok {
		return fmt.Errorf("render: unknown page %q", page)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("render %s: %w", page, err)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, err := io.Copy(w, &buf)
	return err
}

func pageName(path string) string {
	base := path
	if i := len("templates/"); len(path) > i {
		base = path[i:]
	}
	return base[:len(base)-len(".html")]
}

// group inserts thousands separators. Figures on the dashboard run to five
// digits and are read at a glance, where 24573 and 245730 look alike.
//
// It takes any integer because templates call it with both: a store count is
// int64, an import batch's tally is int, and html/template will not convert
// between them on the way in.
func groupAny(v any) string {
	switch n := v.(type) {
	case int:
		return group(int64(n))
	case int32:
		return group(int64(n))
	case int64:
		return group(n)
	}
	return fmt.Sprint(v)
}

func group(n int64) string {
	sign := ""
	if n < 0 {
		sign, n = "-", -n
	}
	digits := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return sign + b.String()
}

var funcs = template.FuncMap{
	// num groups thousands in a plain count.
	"num": groupAny,
	// lower folds a Label() for use mid-sentence.
	"lower": strings.ToLower,
	// plural covers the six level names — Districts, Subcounties, Parishes,
	// Villages, Counties, Regions — rather than pinning an "s" on the end and
	// producing "Subcountys" on a district manager's dashboard.
	"plural": func(word string) string {
		switch {
		case word == "":
			return word
		case strings.HasSuffix(word, "y") && !strings.ContainsRune("aeiouAEIOU", rune(word[len(word)-2])):
			return word[:len(word)-1] + "ies"
		case strings.HasSuffix(word, "s"), strings.HasSuffix(word, "x"), strings.HasSuffix(word, "z"),
			strings.HasSuffix(word, "ch"), strings.HasSuffix(word, "sh"):
			return word + "es"
		default:
			return word + "s"
		}
	},
	// dict builds a map inline, so a sub-template can take more than one
	// argument. Odd arguments are a template bug and fail the render rather
	// than silently dropping a value.
	"dict": func(kv ...any) (map[string]any, error) {
		if len(kv)%2 != 0 {
			return nil, fmt.Errorf("dict: odd argument count %d", len(kv))
		}
		m := make(map[string]any, len(kv)/2)
		for i := 0; i < len(kv); i += 2 {
			k, ok := kv[i].(string)
			if !ok {
				return nil, fmt.Errorf("dict: key %d is not a string", i)
			}
			m[k] = kv[i+1]
		}
		return m, nil
	},
	// share renders n out of total as a percentage. An empty denominator is
	// "—", not 0%: nothing measured is not the same as nothing found.
	"share": func(n, total int64) string {
		if total == 0 {
			return "—"
		}
		p := float64(n) / float64(total) * 100
		switch {
		case p > 0 && p < 0.1:
			return "<0.1%"
		case p >= 10:
			return strconv.FormatFloat(p, 'f', 0, 64) + "%"
		default:
			return strconv.FormatFloat(p, 'f', 1, 64) + "%"
		}
	},
	// pctwidth is the same ratio as an SVG width, for the meters and the inline
	// bars in the league table — the width is data, and the content security
	// policy allows no inline style attribute to carry it. Clamped, so a
	// rounding error cannot overflow its track.
	"pctwidth": func(n, total int64) string {
		if total <= 0 {
			return "0%"
		}
		p := float64(n) / float64(total) * 100
		if p > 100 {
			p = 100
		}
		return strconv.FormatFloat(p, 'f', 2, 64) + "%"
	},
	// deref answers a *bool in a template, where `if p.Flag` would be true for
	// any non-nil pointer — including one pointing at false. The profile
	// columns are all nullable, so "not asked" and "no" are different answers
	// and the distinction has to survive into the markup.
	"deref": func(b *bool) bool { return b != nil && *b },
	// ugx groups thousands. Amounts run to six figures and are read off a
	// screen by someone checking them against a payment list.
	"ugx": func(n *int32) string {
		if n == nil {
			return "not recorded"
		}
		digits := strconv.FormatInt(int64(*n), 10)
		var b strings.Builder
		for i, r := range digits {
			if i > 0 && (len(digits)-i)%3 == 0 {
				b.WriteByte(',')
			}
			b.WriteRune(r)
		}
		return b.String()
	},
	// yesno renders a nullable answer as it was given, including not having
	// been given.
	"yesno": func(b *bool) string {
		switch {
		case b == nil:
			return "not recorded"
		case *b:
			return "yes"
		default:
			return "no"
		}
	},
	"date": func(t time.Time) string {
		if t.IsZero() {
			return "—"
		}
		return t.Format("2 Jan 2006")
	},
	"datetime": func(t time.Time) string {
		if t.IsZero() {
			return "—"
		}
		return t.Format("2 Jan 2006 15:04")
	},
	// month renders a year+month date, which is all supervision captures: the
	// stored day is always the first and means nothing.
	"month": func(t *time.Time) string {
		if t == nil {
			return "—"
		}
		return t.Format("January 2006")
	},
	"optdate": func(t *time.Time) string {
		if t == nil {
			return "never"
		}
		return t.Format("2 Jan 2006 15:04")
	},
}
