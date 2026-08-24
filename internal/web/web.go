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

var funcs = template.FuncMap{
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
