package store

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"

	"hwr/internal/auth"
	"hwr/internal/domain"
)

// Stats reads the aggregate shapes the dashboard draws.
//
// Every query here is a grouped count over the same scoped register, so a
// district manager's dashboard is their district's dashboard and nothing
// wider: the Scope argument reaches the WHERE clause exactly as it does on the
// listing. A chart that quietly summed the country would leak the shape of
// data its reader cannot open.
//
// Workers are counted through the posting the listing sees them through — the
// active deployment, else the most recent — so a chart and the register
// beneath it never disagree about where someone serves.
type Stats struct {
	pool *pgxpool.Pool
}

// segment is the position of a level's id inside `locations.path`, which is
// '/region/district/county/subcounty/parish/village/'. split_part on a leading
// '/' yields an empty first field, so the region sits at 2.
//
// Reading the ancestor out of the path is what lets one query group the
// register at any tier: the alternative is a prefix join against all 84,635
// locations, which is the same answer for considerably more work.
func segment(l domain.Level) int {
	switch l {
	case domain.LevelRegion:
		return 2
	case domain.LevelDistrict:
		return 3
	case domain.LevelCounty:
		return 4
	case domain.LevelSubcounty:
		return 5
	case domain.LevelParish:
		return 6
	default:
		return 7
	}
}

// seenThrough is the lateral join picking the posting a worker is counted
// through: the active one, else the most recent. It is the register listing's
// own rule (workerFrom), so the dashboard and the listing cannot disagree.
const seenThrough = `
    JOIN LATERAL (
        SELECT x.cadre_id, x.location_id, x.facility_id, x.ended_on FROM deployments x
         WHERE x.health_worker_id = w.id
         ORDER BY (x.ended_on IS NULL) DESC, x.started_on DESC, x.id DESC
         LIMIT 1
    ) dep ON true`

// counted is the register the dashboard draws, narrowed by the listing's own
// Filter: every worker through the posting they are seen through, with its
// cadre (cd), category (cat) and location (l) joined under the aliases
// Filter.where expects. It returns the FROM and the WHERE apart, so a caller
// can join more between them, and the arguments bound so far.
//
// Sharing Filter.where is what lets a dashboard link carry its filter into the
// register and land on the same number: the two are one predicate. The search
// box and the cursors are the listing's alone and are dropped here.
func counted(sc auth.Scope, f Filter, args []any) (from, where string, _ []any) {
	f.Query, f.Limit, f.After, f.Before = "", 0, nil, nil
	where, args = f.where(sc, args)
	return `
	  FROM health_workers w` + seenThrough + `
	  JOIN cadres cd            ON cd.id  = dep.cadre_id
	  JOIN cadre_categories cat ON cat.id = cd.category_id
	  JOIN locations l          ON l.id   = dep.location_id`, where, args
}

// under narrows an area list to the subtree below one location — the
// district a scope is pinned to, or the location a filter names. 0 means the
// whole country.
func under(q string, args []any, id int64) (string, []any) {
	if id == 0 {
		return q, args
	}
	args = append(args, id)
	return q + fmt.Sprintf(
		" AND a.path LIKE (SELECT path FROM locations WHERE id = $%d) || '%%'", len(args)), args
}

// Totals are the headline counts and the splits the tiles read off them.
type Totals struct {
	Total     int64
	Active    int64
	Inactive  int64
	Female    int64
	Male      int64
	MedianAge float64
}

// Totals counts the selection, with every split the dashboard needs taken in
// one pass. FILTER is used rather than a query per split so the figures are
// guaranteed to describe the same instant. The cadre split is not here: it
// comes from Cadres(), because cadres are data, not columns.
func (s *Stats) Totals(ctx context.Context, sc auth.Scope, f Filter) (Totals, error) {
	from, where, args := counted(sc, f, nil)
	q := `
	    SELECT count(*),
	           count(*) FILTER (WHERE w.status = 'active'),
	           count(*) FILTER (WHERE w.status = 'inactive'),
	           count(*) FILTER (WHERE w.sex    = 'female'),
	           count(*) FILTER (WHERE w.sex    = 'male'),
	           coalesce(percentile_cont(0.5) WITHIN GROUP (ORDER BY w.age_years), 0)` + from + where

	var t Totals
	err := s.pool.QueryRow(ctx, q, args...).Scan(&t.Total, &t.Active, &t.Inactive,
		&t.Female, &t.Male, &t.MedianAge)
	if err != nil {
		return Totals{}, fmt.Errorf("count register: %w", translate(err))
	}
	return t, nil
}

// CadreSplit is one cadre's slice of the selection, cut both ways. The two
// cuts come from one grouped query because they are two readings of the same
// rows, and two queries could disagree.
type CadreSplit struct {
	Slug     string
	Label    string
	Category string
	Active   int64
	Inactive int64
	Female   int64
	Male     int64
}

// Cadres returns one row per active cadre the filter admits, in the
// vocabulary's own order, so a cadre with no workers still appears rather than
// silently dropping off the axis.
func (s *Stats) Cadres(ctx context.Context, sc auth.Scope, f Filter) ([]CadreSplit, error) {
	cadres, err := s.activeCadres(ctx, f)
	if err != nil {
		return nil, err
	}

	out := make([]CadreSplit, len(cadres))
	at := make(map[string]int, len(cadres))
	for i, c := range cadres {
		out[i] = CadreSplit{Slug: c.Slug, Label: c.Label, Category: c.CategoryLabel}
		at[c.Slug] = i
	}

	from, where, args := counted(sc, f, nil)
	q := `SELECT cd.slug, w.sex::text, w.status::text, count(*)` + from + where + ` GROUP BY 1, 2, 3`

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("count cadres: %w", translate(err))
	}
	defer rows.Close()

	for rows.Next() {
		var slug, sex, status string
		var n int64
		if err := rows.Scan(&slug, &sex, &status, &n); err != nil {
			return nil, fmt.Errorf("scan cadre split: %w", err)
		}
		i, ok := at[slug]
		if !ok {
			continue
		}
		if domain.WorkerStatus(status) == domain.WorkerActive {
			out[i].Active += n
		} else {
			out[i].Inactive += n
		}
		if domain.Sex(sex) == domain.SexFemale {
			out[i].Female += n
		} else {
			out[i].Male += n
		}
	}
	return out, rows.Err()
}

// activeCadres is the cadre vocabulary the splits are zero-filled against,
// narrowed to the category and cadre the filter names: a dashboard filtered to
// clinicians has no business drawing an empty VHT row.
func (s *Stats) activeCadres(ctx context.Context, f Filter) ([]domain.Cadre, error) {
	where := ` WHERE c.active AND cat.active`
	var args []any
	if f.Category != "" {
		args = append(args, f.Category)
		where += fmt.Sprintf(" AND cat.slug = $%d", len(args))
	}
	if f.Cadre != "" {
		args = append(args, f.Cadre)
		where += fmt.Sprintf(" AND c.slug = $%d", len(args))
	}
	return queryCadres(ctx, s.pool, where, args...)
}

// AreaRow is one administrative area with its slice of the selection. Parent
// is the tier above, carried because district and subcounty names repeat
// across the country and a bare name is not an identity here. Cadres is a
// count per cadre, parallel to the cadre list Areas returns alongside.
type AreaRow struct {
	ID     int64
	Name   string
	Parent string
	Total  int64
	Active int64
	Cadres []int64
}

// Areas counts the selection by administrative area at one level, below the
// location `within` (0 for the whole country), densest first. Areas with no
// workers are included — an empty district is the finding, not a row to
// omit — which is why the join runs outward from `locations` rather than
// inward from the register.
//
// A worker placed above `level` — a CHEW at a parish when the tier is the
// village, a district-level cadre when it is the subcounty — has no ancestor
// there and is in no row; the path simply ends before that segment. Callers
// that want the whole selection accounted for compare against Totals.
//
// The cadre list is returned beside the rows: the per-area cadre counts are
// parallel to it. limit of 0 means every area at that level.
func (s *Stats) Areas(ctx context.Context, sc auth.Scope, f Filter, level domain.Level, within int64, limit int) ([]AreaRow, []domain.Cadre, error) {
	cadres, err := s.activeCadres(ctx, f)
	if err != nil {
		return nil, nil, err
	}
	at := make(map[string]int, len(cadres))
	for i, c := range cadres {
		at[c.Slug] = i
	}

	// The scope lands twice: once inside the derived table, so a district user
	// sums only their own workers, and once on the area list itself, so the
	// chart does not carry 145 empty districts.
	from, where, args := counted(sc, f, []any{segment(level), string(level)})
	inner := `SELECT w.status::text AS status, cd.slug AS cadre_slug,
	                 nullif(split_part(l.path, '/', $1::int), '')::bigint AS area_id,
	                 count(*) AS n` + from + where + ` GROUP BY 1, 2, 3`

	q := `
	    SELECT a.id, a.name, coalesce(p.name, ''), c.status, c.cadre_slug, c.n
	      FROM locations a
	      LEFT JOIN locations p ON p.id = a.parent_id
	      LEFT JOIN (` + inner + `) c ON c.area_id = a.id
	     WHERE a.level = $2::location_level`
	if id, ok := sc.DistrictID(); ok {
		q, args = under(q, args, id)
	}
	q, args = under(q, args, within)

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("count %s areas: %w", level, translate(err))
	}
	defer rows.Close()

	byID := map[int64]*AreaRow{}
	var order []int64
	for rows.Next() {
		var id int64
		var name, parent string
		var status, cadreSlug *string
		var n *int64
		if err := rows.Scan(&id, &name, &parent, &status, &cadreSlug, &n); err != nil {
			return nil, nil, fmt.Errorf("scan area: %w", err)
		}
		a, ok := byID[id]
		if !ok {
			a = &AreaRow{ID: id, Name: name, Parent: parent, Cadres: make([]int64, len(cadres))}
			byID[id] = a
			order = append(order, id)
		}
		if n == nil {
			continue // an area with nobody: the row itself is the finding
		}
		a.Total += *n
		if *status == string(domain.WorkerActive) {
			a.Active += *n
		}
		if i, ok := at[*cadreSlug]; ok {
			a.Cadres[i] += *n
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	out := make([]AreaRow, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Total != out[j].Total {
			return out[i].Total > out[j].Total
		}
		return out[i].Name < out[j].Name
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, cadres, nil
}

// AgeBands are five-year bands over the recorded age. Age is a snapshot rather
// than a fact — `age_captured_on` says when it was true — so this is the shape
// of the register as captured, not a birth-date distribution.
//
// The first band is open at the bottom (the column check floors it at 18) and
// the last is open at the top.
type AgeBand struct {
	Label string
	Count int64
}

var ageBandLabels = []string{
	"18–24", "25–29", "30–34", "35–39", "40–44",
	"45–49", "50–54", "55–59", "60–64", "65+",
}

// Ages returns the ten bands, zero-filled. A band nobody falls into is a gap in
// the distribution and has to keep its slot on the axis.
func (s *Stats) Ages(ctx context.Context, sc auth.Scope, f Filter) ([]AgeBand, int64, error) {
	from, where, args := counted(sc, f, nil)
	q := `SELECT least(9, greatest(0, (w.age_years - 20) / 5)) AS band, count(*)` + from + where +
		` AND w.age_years IS NOT NULL GROUP BY band ORDER BY band`

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("count age bands: %w", translate(err))
	}
	defer rows.Close()

	bands := make([]AgeBand, len(ageBandLabels))
	for i, l := range ageBandLabels {
		bands[i] = AgeBand{Label: l}
	}
	var known int64
	for rows.Next() {
		var band int
		var n int64
		if err := rows.Scan(&band, &n); err != nil {
			return nil, 0, fmt.Errorf("scan age band: %w", err)
		}
		if band >= 0 && band < len(bands) {
			bands[band].Count = n
		}
		known += n
	}
	return bands, known, rows.Err()
}

// FieldFill is how much of one field the register actually holds. Every profile
// column is nullable and "no" and "not asked" are different answers, so this
// counts rows where an answer of any kind was recorded — not rows that answered
// yes.
type FieldFill struct {
	Label string
	Have  int64
}

// Completeness is the register's own data-quality view, in two parts with two
// denominators. Register fields belong to every health worker whatever their
// cadre, and are measured against the whole selection. Profile fields belong
// to the Community Health Workers category alone — chw_profiles is its profile
// surface and no other category has one — so they are measured against the
// CHWs in the selection, never against a clinician who was never asked.
type Completeness struct {
	Register []FieldFill
	Profile  []FieldFill
	// CHWs is the Profile denominator: workers in the selection whose posting
	// is in the CHW category.
	CHWs int64
}

// Completeness reports, per field, how many workers carry a recorded answer.
// An import that dropped a column shows up here as a bar that never rises.
func (s *Stats) Completeness(ctx context.Context, sc auth.Scope, f Filter) (Completeness, error) {
	from, where, args := counted(sc, f, nil)
	args = append(args, domain.CategoryCHW)
	chw := fmt.Sprintf("cat.slug = $%d", len(args))

	q := `
	    SELECT count(*) FILTER (WHERE w.nin IS NOT NULL),
	           count(*) FILTER (WHERE w.age_years IS NOT NULL),
	           -- The supervising facility is a fact of the open posting.
	           count(*) FILTER (WHERE dep.ended_on IS NULL AND dep.facility_id IS NOT NULL),
	           count(*) FILTER (WHERE ` + chw + `),
	           count(p.health_worker_id) FILTER (WHERE ` + chw + `),
	           count(*) FILTER (WHERE ` + chw + ` AND p.owns_phone IS NOT NULL),
	           count(*) FILTER (WHERE ` + chw + ` AND p.education IS NOT NULL),
	           count(*) FILTER (WHERE ` + chw + ` AND p.service_start_year IS NOT NULL),
	           count(*) FILTER (WHERE ` + chw + ` AND p.households_served IS NOT NULL),
	           count(*) FILTER (WHERE ` + chw + ` AND p.receives_incentive IS NOT NULL),
	           count(*) FILTER (WHERE ` + chw + ` AND p.received_supervision IS NOT NULL),
	           count(*) FILTER (WHERE ` + chw + ` AND d.health_worker_id IS NOT NULL),
	           count(*) FILTER (WHERE ` + chw + ` AND t.health_worker_id IS NOT NULL)` + from + `
	      LEFT JOIN chw_profiles p ON p.health_worker_id = w.id
	      -- The two junctions are folded to one row per worker and joined,
	      -- rather than probed with EXISTS per row: same answer, ~40x less
	      -- work over a register this size.
	      LEFT JOIN (SELECT DISTINCT health_worker_id FROM chw_service_domains) d ON d.health_worker_id = w.id
	      LEFT JOIN (SELECT DISTINCT health_worker_id FROM chw_tools)           t ON t.health_worker_id = w.id` + where

	register := []string{"National ID (NIN)", "Age", "Health facility"}
	profile := []string{
		"Profile started", "Phone ownership", "Education", "Year started service",
		"Households served", "Incentive", "Supervision", "Service domains", "Tools held",
	}
	counts := make([]int64, len(register)+1+len(profile))
	dest := make([]any, len(counts))
	for i := range counts {
		dest[i] = &counts[i]
	}
	if err := s.pool.QueryRow(ctx, q, args...).Scan(dest...); err != nil {
		return Completeness{}, fmt.Errorf("measure completeness: %w", translate(err))
	}

	var c Completeness
	for i, l := range register {
		c.Register = append(c.Register, FieldFill{Label: l, Have: counts[i]})
	}
	c.CHWs = counts[len(register)]
	for i, l := range profile {
		c.Profile = append(c.Profile, FieldFill{Label: l, Have: counts[len(register)+1+i]})
	}
	return c, nil
}

// ServiceCount is one service domain and how much of the selection offers it.
// Trained is a subset of Provides by constraint (trained_implies_provides), so
// the two are read as a whole and its part, never as two independent series.
type ServiceCount struct {
	Label    string
	Provides int64
	Trained  int64
}

// Services counts the CHWs in the selection per service domain, in the
// vocabulary's own order, and returns how many workers the counts are drawn
// from. Service domains are a CHW profile question, so only that category is
// counted. Domains nobody offers keep their row: a service with no providers is
// the point of the chart.
//
// The denominator is the workers with any service answer at all, not the whole
// selection — a domain offered by every one of ten answered records is not a
// domain offered by the country.
func (s *Stats) Services(ctx context.Context, sc auth.Scope, f Filter) ([]ServiceCount, int64, error) {
	from, where, args := counted(sc, f, nil)
	args = append(args, domain.CategoryCHW)
	// The selection is a derived table the answers are joined through, so the
	// outer join from service_domains stays outer: as a WHERE predicate it
	// would turn inner and drop every domain nobody offers.
	sel := `SELECT w.id` + from + where + fmt.Sprintf(" AND cat.slug = $%d", len(args))
	answers := `SELECT x.* FROM chw_service_domains x JOIN (` + sel + `) sel ON sel.id = x.health_worker_id`

	q := `
	    SELECT d.label,
	           count(*) FILTER (WHERE x.provides),
	           count(*) FILTER (WHERE x.trained),
	           (SELECT count(DISTINCT health_worker_id) FROM (` + answers + `) y)
	      FROM service_domains d
	      LEFT JOIN (` + answers + `) x ON x.domain_id = d.id
	     WHERE d.active
	     GROUP BY d.id, d.label, d.sort_order
	     ORDER BY d.sort_order`

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("count service domains: %w", translate(err))
	}
	defer rows.Close()

	var out []ServiceCount
	var respondents int64
	for rows.Next() {
		var sd ServiceCount
		if err := rows.Scan(&sd.Label, &sd.Provides, &sd.Trained, &respondents); err != nil {
			return nil, 0, fmt.Errorf("scan service domain: %w", err)
		}
		out = append(out, sd)
	}
	return out, respondents, rows.Err()
}

// Coverage is how much of the administrative hierarchy the selection reaches
// at one tier. The denominator is the hierarchy, not the register, so an
// untouched area counts against it.
type Coverage struct {
	Level   domain.Level
	Covered int64
	Total   int64
}

// Reach measures coverage at `level` below the location `within` (0 for the
// whole country): a national reader asks which districts have nobody, a
// district manager the same of their subcounties, and a filter to one
// subcounty the same of its parishes.
func (s *Stats) Reach(ctx context.Context, sc auth.Scope, f Filter, level domain.Level, within int64) (Coverage, error) {
	from, where, args := counted(sc, f, []any{segment(level), string(level)})
	inner := `SELECT DISTINCT nullif(split_part(l.path, '/', $1::int), '')::bigint AS area_id` + from + where

	q := `SELECT count(*) FILTER (WHERE c.area_id IS NOT NULL), count(*)
	        FROM locations a
	        LEFT JOIN (` + inner + `) c ON c.area_id = a.id
	       WHERE a.level = $2::location_level AND a.active`
	if id, ok := sc.DistrictID(); ok {
		q, args = under(q, args, id)
	}
	q, args = under(q, args, within)

	c := Coverage{Level: level}
	if err := s.pool.QueryRow(ctx, q, args...).Scan(&c.Covered, &c.Total); err != nil {
		return Coverage{}, fmt.Errorf("measure reach: %w", translate(err))
	}
	return c, nil
}
