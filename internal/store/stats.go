package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"chwr/internal/auth"
	"chwr/internal/domain"
)

// Stats reads the aggregate shapes the dashboard draws.
//
// Every query here is a grouped count over the same scoped register, so a
// district manager's dashboard is their district's dashboard and nothing
// wider: the Scope argument reaches the WHERE clause exactly as it does on the
// listing. A chart that quietly summed the country would leak the shape of
// data its reader cannot open.
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

// Totals are the headline counts and the splits the tiles read off them.
type Totals struct {
	Total     int64
	Active    int64
	Inactive  int64
	VHT       int64
	CHEW      int64
	Female    int64
	Male      int64
	WithNIN   int64
	MedianAge float64
}

// Totals counts the register inside the scope, with every split the dashboard
// needs taken in one pass. FILTER is used rather than a query per split so the
// figures are guaranteed to describe the same instant.
func (s *Stats) Totals(ctx context.Context, sc auth.Scope) (Totals, error) {
	q := `
	    SELECT count(*),
	           count(*) FILTER (WHERE status = 'active'),
	           count(*) FILTER (WHERE status = 'inactive'),
	           count(*) FILTER (WHERE cadre  = 'vht'),
	           count(*) FILTER (WHERE cadre  = 'chew'),
	           count(*) FILTER (WHERE sex    = 'female'),
	           count(*) FILTER (WHERE sex    = 'male'),
	           count(*) FILTER (WHERE nin IS NOT NULL),
	           coalesce(percentile_cont(0.5) WITHIN GROUP (ORDER BY age_years), 0)
	      FROM chws
	     WHERE true`
	var args []any
	if frag, extra := sc.Filter("district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}

	var t Totals
	err := s.pool.QueryRow(ctx, q, args...).Scan(&t.Total, &t.Active, &t.Inactive,
		&t.VHT, &t.CHEW, &t.Female, &t.Male, &t.WithNIN, &t.MedianAge)
	if err != nil {
		return Totals{}, fmt.Errorf("count register: %w", err)
	}
	return t, nil
}

// AreaRow is one administrative area with its slice of the register. Parent is
// the tier above, carried because district and subcounty names repeat across
// the country and a bare name is not an identity here.
type AreaRow struct {
	ID     int64
	Name   string
	Parent string
	Total  int64
	Active int64
	VHT    int64
	CHEW   int64
}

// Areas counts the register by administrative area at one level, densest
// first. Areas with no CHWs are included — an empty district is the finding,
// not a row to omit — which is why the join runs outward from `locations`
// rather than inward from `chws`.
//
// limit of 0 means every area at that level inside the scope.
func (s *Stats) Areas(ctx context.Context, sc auth.Scope, level domain.Level, limit int) ([]AreaRow, error) {
	args := []any{segment(level), string(level)}

	// The scope lands twice: once inside the derived table, so a district user
	// sums only their own CHWs, and once on the area list itself, so the chart
	// does not carry 145 empty districts.
	inner := `SELECT c.status, c.cadre,
	                 split_part(l.path, '/', $1::int)::bigint AS area_id
	            FROM chws c
	            JOIN locations l ON l.id = c.location_id
	           WHERE true`
	if frag, extra := sc.Filter("c.district_id", len(args)+1); frag != "" {
		inner += frag
		args = append(args, extra...)
	}

	q := `
	    SELECT a.id, a.name, coalesce(p.name, ''),
	           count(c.area_id),
	           count(c.area_id) FILTER (WHERE c.status = 'active'),
	           count(c.area_id) FILTER (WHERE c.cadre  = 'vht'),
	           count(c.area_id) FILTER (WHERE c.cadre  = 'chew')
	      FROM locations a
	      LEFT JOIN locations p ON p.id = a.parent_id
	      LEFT JOIN (` + inner + `) c ON c.area_id = a.id
	     WHERE a.level = $2::location_level`

	if id, ok := sc.DistrictID(); ok {
		args = append(args, id)
		q += fmt.Sprintf(
			" AND a.path LIKE (SELECT path FROM locations WHERE id = $%d) || '%%'", len(args))
	}
	q += ` GROUP BY a.id, a.name, p.name ORDER BY count(c.area_id) DESC, a.name`
	if limit > 0 {
		args = append(args, limit)
		q += fmt.Sprintf(" LIMIT $%d", len(args))
	}

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("count %s areas: %w", level, translate(err))
	}
	defer rows.Close()

	var out []AreaRow
	for rows.Next() {
		var a AreaRow
		if err := rows.Scan(&a.ID, &a.Name, &a.Parent,
			&a.Total, &a.Active, &a.VHT, &a.CHEW); err != nil {
			return nil, fmt.Errorf("scan area: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
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
func (s *Stats) Ages(ctx context.Context, sc auth.Scope) ([]AgeBand, int64, error) {
	q := `
	    SELECT least(9, greatest(0, (age_years - 20) / 5)) AS band, count(*)
	      FROM chws
	     WHERE age_years IS NOT NULL`
	var args []any
	if frag, extra := sc.Filter("district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}
	q += ` GROUP BY band ORDER BY band`

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

// CadreSplit is one cadre's slice of the register, cut both ways. The two cuts
// come from one grouped query because they are two readings of the same rows,
// and two queries could disagree.
type CadreSplit struct {
	Cadre    domain.Cadre
	Active   int64
	Inactive int64
	Female   int64
	Male     int64
}

// Cadres returns one row per cadre, in the domain's own order, so a cadre with
// no CHWs still appears rather than silently dropping off the axis.
func (s *Stats) Cadres(ctx context.Context, sc auth.Scope) ([]CadreSplit, error) {
	q := `
	    SELECT cadre::text, sex::text, status::text, count(*)
	      FROM chws
	     WHERE true`
	var args []any
	if frag, extra := sc.Filter("district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}
	q += ` GROUP BY 1, 2, 3`

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("count cadres: %w", translate(err))
	}
	defer rows.Close()

	out := make([]CadreSplit, len(domain.Cadres))
	at := make(map[domain.Cadre]int, len(domain.Cadres))
	for i, c := range domain.Cadres {
		out[i] = CadreSplit{Cadre: c}
		at[c] = i
	}

	for rows.Next() {
		var cadre, sex, status string
		var n int64
		if err := rows.Scan(&cadre, &sex, &status, &n); err != nil {
			return nil, fmt.Errorf("scan cadre split: %w", err)
		}
		i, ok := at[domain.Cadre(cadre)]
		if !ok {
			continue
		}
		if domain.CHWStatus(status) == domain.CHWActive {
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

// FieldFill is how much of one field the register actually holds. Every profile
// column is nullable and "no" and "not asked" are different answers, so this
// counts rows where an answer of any kind was recorded — not rows that answered
// yes.
type FieldFill struct {
	Label string
	Have  int64
}

// Completeness reports, per field, how many CHWs in the scope carry a recorded
// answer. It is the register's own data-quality view: an import that dropped a
// column shows up here as a bar that never rises.
func (s *Stats) Completeness(ctx context.Context, sc auth.Scope) ([]FieldFill, error) {
	q := `
	    SELECT count(*) FILTER (WHERE c.nin IS NOT NULL),
	           count(*) FILTER (WHERE c.age_years IS NOT NULL),
	           count(p.chw_id),
	           count(*) FILTER (WHERE p.owns_phone IS NOT NULL),
	           count(*) FILTER (WHERE p.education IS NOT NULL),
	           count(*) FILTER (WHERE p.service_start_year IS NOT NULL),
	           count(*) FILTER (WHERE p.households_served IS NOT NULL),
	           count(*) FILTER (WHERE p.facility_id IS NOT NULL),
	           count(*) FILTER (WHERE p.receives_incentive IS NOT NULL),
	           count(*) FILTER (WHERE p.received_supervision IS NOT NULL),
	           count(*) FILTER (WHERE d.chw_id IS NOT NULL),
	           count(*) FILTER (WHERE t.chw_id IS NOT NULL)
	      FROM chws c
	      LEFT JOIN chw_profiles p ON p.chw_id = c.id
	      -- The two junctions are folded to one row per CHW and joined, rather
	      -- than probed with EXISTS per row: same answer, ~40x less work over
	      -- a register this size.
	      LEFT JOIN (SELECT DISTINCT chw_id FROM chw_service_domains) d ON d.chw_id = c.id
	      LEFT JOIN (SELECT DISTINCT chw_id FROM chw_tools)           t ON t.chw_id = c.id
	     WHERE true`
	var args []any
	if frag, extra := sc.Filter("c.district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}

	labels := []string{
		"National ID (NIN)", "Age", "Profile started", "Phone ownership",
		"Education", "Year started service", "Households served",
		"Health facility", "Incentive", "Supervision",
		"Service domains", "Tools held",
	}
	counts := make([]int64, len(labels))
	dest := make([]any, len(labels))
	for i := range counts {
		dest[i] = &counts[i]
	}
	if err := s.pool.QueryRow(ctx, q, args...).Scan(dest...); err != nil {
		return nil, fmt.Errorf("measure completeness: %w", err)
	}

	out := make([]FieldFill, len(labels))
	for i, l := range labels {
		out[i] = FieldFill{Label: l, Have: counts[i]}
	}
	return out, nil
}

// ServiceCount is one service domain and how much of the register offers it.
// Trained is a subset of Provides by constraint (trained_implies_provides), so
// the two are read as a whole and its part, never as two independent series.
type ServiceCount struct {
	Label    string
	Provides int64
	Trained  int64
}

// Services counts the register per service domain, in the vocabulary's own
// order, and returns how many CHWs the counts are drawn from. Domains nobody
// offers keep their row: a service with no providers is the point of the chart.
//
// The denominator is the CHWs with any service answer at all, not the whole
// register — a domain offered by every one of ten answered records is not a
// domain offered by the country.
func (s *Stats) Services(ctx context.Context, sc auth.Scope) ([]ServiceCount, int64, error) {
	q := `
	    SELECT d.label,
	           count(*) FILTER (WHERE x.provides),
	           count(*) FILTER (WHERE x.trained)
	      FROM service_domains d
	      LEFT JOIN chw_service_domains x ON x.domain_id = d.id
	      LEFT JOIN chws c ON c.id = x.chw_id`
	var args []any
	where := ` WHERE d.active`
	if frag, extra := sc.Filter("c.district_id", len(args)+1); frag != "" {
		// The scope extends the join condition rather than the WHERE clause:
		// as a predicate it would turn the outer join inner and drop every
		// domain nobody offers, which is the row worth seeing.
		q += frag
		args = append(args, extra...)
	}
	q += where + ` GROUP BY d.id, d.label, d.sort_order ORDER BY d.sort_order`

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("count service domains: %w", translate(err))
	}
	defer rows.Close()

	var out []ServiceCount
	for rows.Next() {
		var sd ServiceCount
		if err := rows.Scan(&sd.Label, &sd.Provides, &sd.Trained); err != nil {
			return nil, 0, fmt.Errorf("scan service domain: %w", err)
		}
		out = append(out, sd)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	dq := `SELECT count(DISTINCT x.chw_id)
	         FROM chw_service_domains x
	         JOIN chws c ON c.id = x.chw_id
	        WHERE true`
	var dargs []any
	if frag, extra := sc.Filter("c.district_id", len(dargs)+1); frag != "" {
		dq += frag
		dargs = append(dargs, extra...)
	}
	var from int64
	if err := s.pool.QueryRow(ctx, dq, dargs...).Scan(&from); err != nil {
		return nil, 0, fmt.Errorf("count answered service domains: %w", err)
	}
	return out, from, nil
}

// Coverage is how much of the administrative hierarchy the register reaches:
// districts nationally, subcounties inside a district. The denominator is the
// hierarchy, not the register, so an untouched area counts against it.
type Coverage struct {
	Level   domain.Level
	Covered int64
	Total   int64
}

// Reach measures coverage at the tier the reader can act on. A national user
// asks which districts have nobody; a district manager asks the same of their
// subcounties.
func (s *Stats) Reach(ctx context.Context, sc auth.Scope) (Coverage, error) {
	level := domain.LevelDistrict
	if _, ok := sc.DistrictID(); ok {
		level = domain.LevelSubcounty
	}

	args := []any{segment(level), string(level)}
	inner := `SELECT DISTINCT split_part(l.path, '/', $1::int)::bigint AS area_id
	            FROM chws c
	            JOIN locations l ON l.id = c.location_id
	           WHERE true`
	if frag, extra := sc.Filter("c.district_id", len(args)+1); frag != "" {
		inner += frag
		args = append(args, extra...)
	}

	q := `SELECT count(*) FILTER (WHERE c.area_id IS NOT NULL), count(*)
	        FROM locations a
	        LEFT JOIN (` + inner + `) c ON c.area_id = a.id
	       WHERE a.level = $2::location_level AND a.active`
	if id, ok := sc.DistrictID(); ok {
		args = append(args, id)
		q += fmt.Sprintf(
			" AND a.path LIKE (SELECT path FROM locations WHERE id = $%d) || '%%'", len(args))
	}

	c := Coverage{Level: level}
	if err := s.pool.QueryRow(ctx, q, args...).Scan(&c.Covered, &c.Total); err != nil {
		return Coverage{}, fmt.Errorf("measure reach: %w", err)
	}
	return c, nil
}
