package http

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"

	"chwr/internal/auth"
	"chwr/internal/domain"
	"chwr/internal/store"
)

type dashboardPage struct {
	Locations  int64
	Facilities int64

	Totals store.Totals
	Reach  store.Coverage

	// Areas is the chart tier — regions nationally, subcounties inside a
	// district. League is the tier below it, as a table: a national reader
	// wants the district league, a district manager the parish one.
	Areas       []areaRow
	AreaLevel   domain.Level
	League      []areaRow
	LeagueLevel domain.Level
	// LeagueTop is the leader's headcount, which the inline bars are drawn
	// against: the table ranks areas among themselves, not against the country.
	LeagueTop int64

	Ages      []store.AgeBand
	AgesKnown int64
	Cadres    []store.CadreSplit
	Fields    []store.FieldFill
	Services  []store.ServiceCount
	// ServicesFrom is how many CHWs answered the service question at all. The
	// bars are a share of that, not of the register.
	ServicesFrom int64

	// AnyService is false before the first import lands. The card then shows
	// an empty state rather than twelve bars pinned at zero, which reads as a
	// finding when it is only an absence.
	AnyService bool

	Recent   []store.LogEntry
	CanAudit bool

	// Charts is the same figures again, as JSON, for the canvases. It is
	// marshalled server-side and read out of a `type="application/json"`
	// block: the CSP allows no inline script, and nothing here is executable.
	Charts template.JS
}

// areaRow is an area plus the register link its name carries. The link is
// built here rather than in the template, because it is routing.
type areaRow struct {
	store.AreaRow
	Href string
}

// series is one labelled set of numbers, in the order the chart draws them.
type series struct {
	Labels []string  `json:"labels"`
	Values []float64 `json:"values"`
}

// chartData is the whole dashboard as numbers. Every figure in it also appears
// in the page as text or in a table, so no value is reachable only by hovering
// a canvas.
type chartData struct {
	Areas     series    `json:"areas"`
	AreaHrefs []string  `json:"areaHrefs"`
	Ages      series    `json:"ages"`
	Cadres    []string  `json:"cadres"`
	Active    []float64 `json:"active"`
	Inactive  []float64 `json:"inactive"`
	Female    []float64 `json:"female"`
	Male      []float64 `json:"male"`
	Fields    series    `json:"fields"`
	FieldPct  []float64 `json:"fieldPct"`
	Services  series    `json:"services"`
	Trained   []float64 `json:"trained"`
	Total     int64     `json:"total"`
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	// The stdlib mux matches "/" as a prefix for anything unrouted, so the
	// catch-all lands here and has to be turned away explicitly.
	if r.URL.Path != "/" {
		s.notFound(w, r)
		return
	}

	ctx := r.Context()
	sc := auth.ScopeFrom(ctx)
	user := auth.MustUser(ctx)

	// A district reader gets the same dashboard one tier down: the country's
	// regions mean nothing inside one district, and the register's own scope
	// already answers the question they can act on.
	areaLevel, leagueLevel := domain.LevelRegion, domain.LevelDistrict
	areaLimit := 0
	if _, ok := sc.DistrictID(); ok {
		areaLevel, leagueLevel = domain.LevelSubcounty, domain.LevelParish
		areaLimit = 15
	}

	page := dashboardPage{
		AreaLevel:   areaLevel,
		LeagueLevel: leagueLevel,
		CanAudit:    auth.Can(user.Role, auth.CapAuditView),
	}

	counts, err := s.store.Locations.Counts(ctx, sc)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page.Locations, page.Facilities = counts.Locations, counts.Facilities

	if page.Totals, err = s.store.Stats.Totals(ctx, sc); err != nil {
		s.fail(w, r, err)
		return
	}
	if page.Reach, err = s.store.Stats.Reach(ctx, sc); err != nil {
		s.fail(w, r, err)
		return
	}
	areas, err := s.store.Stats.Areas(ctx, sc, areaLevel, areaLimit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page.Areas = withLinks(areas, areaLevel)

	league, err := s.store.Stats.Areas(ctx, sc, leagueLevel, 12)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page.League = withLinks(league, leagueLevel)
	if len(page.League) > 0 {
		page.LeagueTop = page.League[0].Total // Areas comes back densest first
	}
	if page.Ages, page.AgesKnown, err = s.store.Stats.Ages(ctx, sc); err != nil {
		s.fail(w, r, err)
		return
	}
	if page.Cadres, err = s.store.Stats.Cadres(ctx, sc); err != nil {
		s.fail(w, r, err)
		return
	}
	if page.Fields, err = s.store.Stats.Completeness(ctx, sc); err != nil {
		s.fail(w, r, err)
		return
	}
	if page.Services, page.ServicesFrom, err = s.store.Stats.Services(ctx, sc); err != nil {
		s.fail(w, r, err)
		return
	}
	for _, sd := range page.Services {
		if sd.Provides > 0 {
			page.AnyService = true
			break
		}
	}
	if page.CanAudit {
		if page.Recent, err = s.store.Audit.List(ctx, sc, 8); err != nil {
			s.fail(w, r, err)
			return
		}
	}

	if page.Charts, err = marshalCharts(page); err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "dashboard", page)
}

// marshalCharts flattens the page into the numbers the canvases plot.
//
// json.Marshal escapes '<', '>' and '&' to their \u form, so the result cannot
// close the script element it is written into; template.JS then carries it
// past the escaper unchanged. Only figures already on the page go in.
func marshalCharts(p dashboardPage) (template.JS, error) {
	d := chartData{Total: p.Totals.Total}

	for _, a := range p.Areas {
		d.Areas.Labels = append(d.Areas.Labels, a.Name)
		d.Areas.Values = append(d.Areas.Values, float64(a.Total))
		d.AreaHrefs = append(d.AreaHrefs, a.Href)
	}
	for _, b := range p.Ages {
		d.Ages.Labels = append(d.Ages.Labels, b.Label)
		d.Ages.Values = append(d.Ages.Values, float64(b.Count))
	}
	for _, c := range p.Cadres {
		d.Cadres = append(d.Cadres, c.Cadre.Label())
		d.Active = append(d.Active, float64(c.Active))
		d.Inactive = append(d.Inactive, float64(c.Inactive))
		d.Female = append(d.Female, float64(c.Female))
		d.Male = append(d.Male, float64(c.Male))
	}
	for _, f := range p.Fields {
		d.Fields.Labels = append(d.Fields.Labels, f.Label)
		d.Fields.Values = append(d.Fields.Values, float64(f.Have))
		d.FieldPct = append(d.FieldPct, pct(f.Have, p.Totals.Total))
	}
	for _, sd := range p.Services {
		d.Services.Labels = append(d.Services.Labels, sd.Label)
		// The stack is trained plus the rest of provides: `trained_implies_
		// provides` makes trained a subset, so plotting the two side by side
		// would double-count the trained.
		d.Services.Values = append(d.Services.Values, float64(sd.Provides-sd.Trained))
		d.Trained = append(d.Trained, float64(sd.Trained))
	}

	raw, err := json.Marshal(d)
	if err != nil {
		return "", fmt.Errorf("marshal dashboard charts: %w", err)
	}
	return template.JS(raw), nil
}

// withLinks attaches each area's register link.
func withLinks(areas []store.AreaRow, level domain.Level) []areaRow {
	out := make([]areaRow, len(areas))
	for i, a := range areas {
		out[i] = areaRow{AreaRow: a, Href: areaHref(level, a.ID)}
	}
	return out
}

// areaHref links an area back to the register filtered to it. The listing takes
// one location field per tier and uses the deepest one filled in.
func areaHref(level domain.Level, id int64) string {
	field := map[domain.Level]string{
		domain.LevelDistrict:  "district_id",
		domain.LevelSubcounty: "subcounty_id",
		domain.LevelParish:    "parish_id",
		domain.LevelVillage:   "village_id",
	}[level]
	if field == "" {
		return "" // a region is not a filter the listing takes
	}
	return fmt.Sprintf("/chws?%s=%d", field, id)
}

// pct is a share of the register, to one decimal. It returns 0 for an empty
// register rather than dividing by it.
func pct(n, total int64) float64 {
	if total == 0 {
		return 0
	}
	return float64(int64(float64(n)/float64(total)*1000+0.5)) / 10
}

type auditPage struct {
	Entries []store.LogEntry
}

func (s *Server) auditList(w http.ResponseWriter, r *http.Request) {
	entries, err := s.store.Audit.List(r.Context(), auth.ScopeFrom(r.Context()), 100)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "audit", auditPage{Entries: entries})
}
