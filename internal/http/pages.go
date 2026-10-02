package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"

	"hwr/internal/auth"
	"hwr/internal/domain"
	"hwr/internal/store"
)

type dashboardPage struct {
	Locations  int64
	Facilities int64

	// Filter narrows every figure on the page; it is the listing's own filter,
	// decoded by the same decodeFilter, so a link out of the dashboard lands on
	// a register that agrees with it. Where names the area the page is about.
	Filter     filterView
	Categories []categoryOption
	CadreOpts  []cadreGroup
	Sexes      []sexOption
	Districts  []districtOption
	Prefill    map[string]int64
	Places     []domain.Place
	Where      string
	// RegisterURL opens the listing on exactly this selection.
	RegisterURL string

	Totals store.Totals
	Reach  store.Coverage

	// Areas is the chart tier — regions nationally, the tier below whatever
	// district or area the page is anchored to otherwise. League is the tier
	// below that, as a table; it is absent when the chart is already at the
	// village. AreaCadres is the cadre vocabulary the per-area counts line up
	// with.
	Areas      []areaRow
	AreaLevel  domain.Level
	AreaCadres []domain.Cadre
	// AboveTier counts workers placed above AreaLevel — a district-level cadre
	// has no subcounty — and so in none of the areas.
	AboveTier    int64
	League       []areaRow
	LeagueLevel  domain.Level
	LeagueParent domain.Level
	// LeagueTop is the leader's headcount, which the inline bars are drawn
	// against: the table ranks areas among themselves, not against the country.
	LeagueTop int64

	Ages      []store.AgeBand
	AgesKnown int64
	Cadres    []cadreTile
	Complete  store.Completeness

	// ShowCHW says the selection can hold Community Health Workers, whose
	// profile — service domains, tools, phone, incentive — no other category
	// is asked. Filtered to another category, that section has nothing to say.
	ShowCHW  bool
	Services []store.ServiceCount
	// ServicesFrom is how many CHWs answered the service question at all.
	// The bars are a share of that, not of the register.
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

// cadreTile is a cadre's split plus the register link its figure carries.
type cadreTile struct {
	store.CadreSplit
	Href string
}

// Total is the cadre's headcount in the selection, whatever the status: the
// number the tile's link opens.
func (c cadreTile) Total() int64 { return c.Active + c.Inactive }

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
	Areas      series    `json:"areas"`
	AreaHrefs  []string  `json:"areaHrefs"`
	Ages       series    `json:"ages"`
	Cadres     []string  `json:"cadres"`
	Active     []float64 `json:"active"`
	Inactive   []float64 `json:"inactive"`
	Female     []float64 `json:"female"`
	Male       []float64 `json:"male"`
	Fields     series    `json:"fields"`
	FieldPct   []float64 `json:"fieldPct"`
	Profile    series    `json:"profile"`
	ProfilePct []float64 `json:"profilePct"`
	Services   series    `json:"services"`
	Trained    []float64 `json:"trained"`
	Total      int64     `json:"total"`
	CHWs       int64     `json:"chws"`
}

// dashboardTiers picks the tiers the page draws from the area it is anchored
// to: the chart ranks the tier below the anchor, the league table the tier
// below that, and reach is measured at the chart's tier. Nationally the chart
// is regions but reach is districts — fifteen regions all reached says nothing.
// County is skipped throughout, as it is in every cascade.
func dashboardTiers(anchor domain.Level) (chart, league, reach domain.Level) {
	switch anchor {
	case "":
		return domain.LevelRegion, domain.LevelDistrict, domain.LevelDistrict
	case domain.LevelDistrict:
		return domain.LevelSubcounty, domain.LevelParish, domain.LevelSubcounty
	case domain.LevelSubcounty:
		return domain.LevelParish, domain.LevelVillage, domain.LevelParish
	default:
		return domain.LevelVillage, "", domain.LevelVillage
	}
}

// parentTier is the tier a league row's Parent column names, county skipped.
func parentTier(l domain.Level) domain.Level {
	switch l {
	case domain.LevelDistrict:
		return domain.LevelRegion
	case domain.LevelParish:
		return domain.LevelSubcounty
	case domain.LevelVillage:
		return domain.LevelParish
	}
	return ""
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

	cadres, err := s.store.Deployments.Cadres(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// The listing's filter, less what only a listing has: the search box and
	// the cursors.
	f, view := decodeFilter(r, cadres)
	f.Query, f.After, f.Before = "", nil, nil
	view.Query = ""

	// The page is anchored to an area: the district a scope is pinned to, or
	// the location the filter names. A location outside the scope is dropped
	// rather than honoured — the counts would be scoped regardless, but the
	// area list beneath it would name another district's subcounties.
	var anchor int64
	var anchorLevel domain.Level
	if id, ok := sc.DistrictID(); ok {
		anchor, anchorLevel = id, domain.LevelDistrict
	}
	var places []domain.Place
	prefill := map[string]int64{}
	if f.LocationID != 0 {
		level, districtID, err := s.store.Locations.LevelOf(ctx, f.LocationID)
		switch {
		case errors.Is(err, domain.ErrNotFound):
			f.LocationID = 0
		case err != nil:
			s.fail(w, r, err)
			return
		case districtID == 0 || !sc.Allows(districtID):
			f.LocationID = 0
		default:
			anchor, anchorLevel = f.LocationID, level
			if places, err = s.store.Locations.Ancestors(ctx, sc, f.LocationID); err != nil {
				s.fail(w, r, err)
				return
			}
			for _, place := range places {
				prefill[string(place.Level)] = place.ID
			}
		}
		view.LocationID = f.LocationID
	}
	view.Active = f.Category != "" || f.Cadre != "" || f.Status != "" || f.Sex != "" || f.LocationID != 0

	areaLevel, leagueLevel, reachLevel := dashboardTiers(anchorLevel)
	areaLimit := 15
	if anchorLevel == "" {
		areaLimit = 0 // all fifteen regions
	}

	districts, err := s.store.Locations.Districts(ctx, sc)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	options := make([]districtOption, 0, len(districts))
	for _, d := range districts {
		options = append(options, districtOption{
			ID: d.ID, Name: d.Name,
			Selected: prefill[string(domain.LevelDistrict)] == d.ID,
		})
	}

	carry := filterQuery(f)
	page := dashboardPage{
		Filter:       view,
		Categories:   categoryOptions(cadres, view.Category),
		CadreOpts:    cadreOptions(cadres, view.Cadre),
		Sexes:        sexOptions(view.Sex),
		Districts:    options,
		Prefill:      prefill,
		Places:       places,
		Where:        "the whole country",
		RegisterURL:  registerURL(carry),
		AreaLevel:    areaLevel,
		LeagueLevel:  leagueLevel,
		LeagueParent: parentTier(leagueLevel),
		CanAudit:     auth.Can(user.Role, auth.CapAuditView),
		ShowCHW:      admitsCHW(f, cadres),
	}
	if len(places) > 0 {
		page.Where = places[len(places)-1].Name
	} else if user.DistrictName != "" {
		page.Where = user.DistrictName
	}

	counts, err := s.store.Locations.Counts(ctx, sc)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page.Locations, page.Facilities = counts.Locations, counts.Facilities

	if page.Totals, err = s.store.Stats.Totals(ctx, sc, f); err != nil {
		s.fail(w, r, err)
		return
	}
	if page.Reach, err = s.store.Stats.Reach(ctx, sc, f, reachLevel, anchor); err != nil {
		s.fail(w, r, err)
		return
	}
	// Every area is fetched and the chart cut afterwards, so the rows can be
	// summed against the total: whoever is placed above this tier is in none
	// of them, and the page says so rather than letting the chart undercount.
	areas, areaCadres, err := s.store.Stats.Areas(ctx, sc, f, areaLevel, anchor, 0)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var inAreas int64
	for _, a := range areas {
		inAreas += a.Total
	}
	page.AboveTier = page.Totals.Total - inAreas
	if areaLimit > 0 && len(areas) > areaLimit {
		areas = areas[:areaLimit]
	}
	page.Areas = withLinks(areas, areaLevel, carry)
	page.AreaCadres = areaCadres

	if leagueLevel != "" {
		league, _, err := s.store.Stats.Areas(ctx, sc, f, leagueLevel, anchor, 12)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		page.League = withLinks(league, leagueLevel, carry)
		if len(page.League) > 0 {
			page.LeagueTop = page.League[0].Total // Areas comes back densest first
		}
	}
	if page.Ages, page.AgesKnown, err = s.store.Stats.Ages(ctx, sc, f); err != nil {
		s.fail(w, r, err)
		return
	}
	split, err := s.store.Stats.Cadres(ctx, sc, f)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, c := range split {
		q := cloneQuery(carry)
		q.Set("cadre", c.Slug)
		page.Cadres = append(page.Cadres, cadreTile{CadreSplit: c, Href: registerURL(q)})
	}
	if page.Complete, err = s.store.Stats.Completeness(ctx, sc, f); err != nil {
		s.fail(w, r, err)
		return
	}
	if page.ShowCHW {
		if page.Services, page.ServicesFrom, err = s.store.Stats.Services(ctx, sc, f); err != nil {
			s.fail(w, r, err)
			return
		}
		for _, sd := range page.Services {
			if sd.Provides > 0 {
				page.AnyService = true
				break
			}
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

// admitsCHW reports whether the filter can select a Community Health Worker:
// no category or cadre named, or one in the CHW category.
func admitsCHW(f store.Filter, cadres []domain.Cadre) bool {
	if f.Category != "" && f.Category != domain.CategoryCHW {
		return false
	}
	if f.Cadre != "" {
		for _, c := range cadres {
			if c.Slug == f.Cadre {
				return c.CarriesCHWProfile()
			}
		}
	}
	return true
}

// filterQuery is the filter as the listing's query string, so a link out of
// the dashboard carries it. The location is added per link, not here: an area
// link narrows to the area, and the deepest location field wins.
func filterQuery(f store.Filter) url.Values {
	q := url.Values{}
	if f.Category != "" {
		q.Set("category", f.Category)
	}
	if f.Cadre != "" {
		q.Set("cadre", f.Cadre)
	}
	if f.Status != "" {
		q.Set("status", string(f.Status))
	}
	if f.Sex != "" {
		q.Set("sex", string(f.Sex))
	}
	if f.LocationID != 0 {
		// Any location field will do: the listing takes the deepest one set,
		// and an area link below adds a deeper one.
		q.Set("district_id", strconv.FormatInt(f.LocationID, 10))
	}
	return q
}

func cloneQuery(q url.Values) url.Values {
	out := make(url.Values, len(q))
	for k, v := range q {
		out[k] = append([]string(nil), v...)
	}
	return out
}

func registerURL(q url.Values) string {
	if len(q) == 0 {
		return "/health-workers"
	}
	return "/health-workers?" + q.Encode()
}

// marshalCharts flattens the page into the numbers the canvases plot.
//
// json.Marshal escapes '<', '>' and '&' to their \u form, so the result cannot
// close the script element it is written into; template.JS then carries it
// past the escaper unchanged. Only figures already on the page go in.
func marshalCharts(p dashboardPage) (template.JS, error) {
	d := chartData{Total: p.Totals.Total, CHWs: p.Complete.CHWs}

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
		d.Cadres = append(d.Cadres, c.Label)
		d.Active = append(d.Active, float64(c.Active))
		d.Inactive = append(d.Inactive, float64(c.Inactive))
		d.Female = append(d.Female, float64(c.Female))
		d.Male = append(d.Male, float64(c.Male))
	}
	for _, f := range p.Complete.Register {
		d.Fields.Labels = append(d.Fields.Labels, f.Label)
		d.Fields.Values = append(d.Fields.Values, float64(f.Have))
		d.FieldPct = append(d.FieldPct, pct(f.Have, p.Totals.Total))
	}
	if p.ShowCHW {
		for _, f := range p.Complete.Profile {
			d.Profile.Labels = append(d.Profile.Labels, f.Label)
			d.Profile.Values = append(d.Profile.Values, float64(f.Have))
			d.ProfilePct = append(d.ProfilePct, pct(f.Have, p.Complete.CHWs))
		}
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

// withLinks attaches each area's register link, carrying the filter.
func withLinks(areas []store.AreaRow, level domain.Level, carry url.Values) []areaRow {
	out := make([]areaRow, len(areas))
	for i, a := range areas {
		out[i] = areaRow{AreaRow: a, Href: areaHref(level, a.ID, carry)}
	}
	return out
}

// areaHref links an area back to the register filtered to it. The listing takes
// one location field per tier and uses the deepest one filled in.
func areaHref(level domain.Level, id int64, carry url.Values) string {
	field := map[domain.Level]string{
		domain.LevelDistrict:  "district_id",
		domain.LevelSubcounty: "subcounty_id",
		domain.LevelParish:    "parish_id",
		domain.LevelVillage:   "village_id",
	}[level]
	if field == "" {
		return "" // a region is not a filter the listing takes
	}
	q := cloneQuery(carry)
	q.Del("district_id")
	q.Set(field, strconv.FormatInt(id, 10))
	return registerURL(q)
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
