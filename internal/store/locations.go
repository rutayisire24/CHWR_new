package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"chwr/internal/auth"
	"chwr/internal/domain"
)

// Locations reads the administrative hierarchy:
// region > district > county > subcounty > parish > village.
//
// Identity at every level is (parent_id, code), not name — one parish genuinely
// holds two villages sharing a name — so lookups here are always by id.
type Locations struct {
	pool *pgxpool.Pool
}

// District is a district for a select element.
type District struct {
	ID   int64
	Name string
}

// Districts lists every district, alphabetically. Scoped: a district user
// building a form sees only their own, so a district role can never be
// assigned outward from a district account.
func (l *Locations) Districts(ctx context.Context, sc auth.Scope) ([]District, error) {
	q := `SELECT id, name FROM locations WHERE level = 'district'`
	var args []any

	if frag, extra := sc.Filter("id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}
	q += ` ORDER BY name`

	rows, err := l.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list districts: %w", translate(err))
	}
	defer rows.Close()

	var out []District
	for rows.Next() {
		var d District
		if err := rows.Scan(&d.ID, &d.Name); err != nil {
			return nil, fmt.Errorf("scan district: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Descendants lists the locations of one level beneath an ancestor, ordered by
// name — the subcounties of a district, the parishes of a subcounty, the
// villages of a parish. It is a prefix scan on the materialized path, which is
// what lets the UI cascade district > subcounty > parish > village while
// skipping the county tier the form never shows.
//
// County is still mandatory in the data: subcounty codes are unique only
// within a county, and collapsing the tier lost 732 subcounties to collisions.
// It is derived from the path for display rather than selected.
func (l *Locations) Descendants(ctx context.Context, sc auth.Scope, ancestorID int64, level domain.Level) ([]District, error) {
	// The scope check is on the ancestor, not on each row: a district user may
	// walk anything inside their own district and nothing outside it.
	if ok, err := l.insideScope(ctx, sc, ancestorID); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("descendants of %d: %w", ancestorID, domain.ErrNotFound)
	}

	const q = `
	    SELECT c.id, c.name
	      FROM locations c
	      JOIN locations a ON c.path LIKE a.path || '%'
	     WHERE a.id = $1 AND c.level = $2::location_level AND c.active
	     ORDER BY c.name`

	rows, err := l.pool.Query(ctx, q, ancestorID, string(level))
	if err != nil {
		return nil, fmt.Errorf("list %s under %d: %w", level, ancestorID, translate(err))
	}
	defer rows.Close()

	var out []District
	for rows.Next() {
		var d District
		if err := rows.Scan(&d.ID, &d.Name); err != nil {
			return nil, fmt.Errorf("scan location: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Ancestors returns a location's chain from region down to the location
// itself. The CHW form uses it to prefill the cascading selects on edit, and
// the detail page to show where a CHW actually sits.
func (l *Locations) Ancestors(ctx context.Context, sc auth.Scope, id int64) ([]domain.Place, error) {
	const q = `
	    SELECT a.id, a.level::text, a.name, coalesce(a.code,'')
	      FROM locations c
	      JOIN locations a ON c.path LIKE a.path || '%'
	     WHERE c.id = $1
	     ORDER BY length(a.path)`

	rows, err := l.pool.Query(ctx, q, id)
	if err != nil {
		return nil, fmt.Errorf("ancestors of %d: %w", id, translate(err))
	}
	defer rows.Close()

	var out []domain.Place
	for rows.Next() {
		var p domain.Place
		var level string
		if err := rows.Scan(&p.ID, &level, &p.Name, &p.Code); err != nil {
			return nil, fmt.Errorf("scan ancestor: %w", err)
		}
		p.Level = domain.Level(level)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("ancestors of %d: %w", id, domain.ErrNotFound)
	}
	return out, nil
}

// LevelOf reports a location's level, and the district it belongs to. The CHW
// handlers use it to reject a placement whose level does not match the cadre
// before the trigger has to, and to keep a district user from placing a CHW
// outside their scope.
func (l *Locations) LevelOf(ctx context.Context, id int64) (domain.Level, int64, error) {
	const q = `
	    SELECT c.level::text, coalesce(d.id, 0)
	      FROM locations c
	      LEFT JOIN locations d ON c.path LIKE d.path || '%' AND d.level = 'district'
	     WHERE c.id = $1`

	var level string
	var districtID int64
	if err := l.pool.QueryRow(ctx, q, id).Scan(&level, &districtID); err != nil {
		return "", 0, fmt.Errorf("level of location %d: %w", id, translate(err))
	}
	return domain.Level(level), districtID, nil
}

// ChildrenAt lists the locations of one level beneath an ancestor, with their
// codes, for the bulk importer to match a spreadsheet's names against.
//
// It is Descendants' shape without the ordering or the display type: the
// importer wants every sibling of that level so it can tell a match from an
// ambiguity, and it wants the code, because the code is the identity a
// quarantined row is resolved by.
func (l *Locations) ChildrenAt(ctx context.Context, sc auth.Scope, ancestorID int64, level domain.Level) ([]domain.Place, error) {
	if ok, err := l.insideScope(ctx, sc, ancestorID); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("children of %d: %w", ancestorID, domain.ErrNotFound)
	}

	const q = `
	    SELECT c.id, c.level::text, c.name, coalesce(c.code,'')
	      FROM locations c
	      JOIN locations a ON c.path LIKE a.path || '%'
	     WHERE a.id = $1 AND c.level = $2::location_level AND c.active`

	rows, err := l.pool.Query(ctx, q, ancestorID, string(level))
	if err != nil {
		return nil, fmt.Errorf("list %s under %d: %w", level, ancestorID, translate(err))
	}
	defer rows.Close()

	var out []domain.Place
	for rows.Next() {
		var p domain.Place
		var lvl string
		if err := rows.Scan(&p.ID, &lvl, &p.Name, &p.Code); err != nil {
			return nil, fmt.Errorf("scan location: %w", err)
		}
		p.Level = domain.Level(lvl)
		out = append(out, p)
	}
	return out, rows.Err()
}

// ByCode resolves the official concatenated code path — the identity the
// admin-units workbook carries and the escape hatch from a name two villages
// share. It takes no Scope on purpose: the caller checks the resolved
// location's district itself, so that a code outside the caller's district is
// answered as out of scope rather than as a code that does not exist. Which of
// those two the report may say is the caller's decision, not this method's.
func (l *Locations) ByCode(ctx context.Context, code string) (int64, error) {
	var id int64
	err := l.pool.QueryRow(ctx,
		`SELECT id FROM locations WHERE code_path = $1`, code).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("location with code %q: %w", code, translate(err))
	}
	return id, nil
}

// insideScope reports whether a location is the user's district or sits under
// it. National scopes are inside everything.
func (l *Locations) insideScope(ctx context.Context, sc auth.Scope, id int64) (bool, error) {
	districtID, pinned := sc.DistrictID()
	if !pinned {
		return true, nil
	}

	const q = `
	    SELECT EXISTS (
	        SELECT 1 FROM locations c, locations d
	         WHERE c.id = $1 AND d.id = $2 AND c.path LIKE d.path || '%'
	    )`

	var ok bool
	if err := l.pool.QueryRow(ctx, q, id, districtID).Scan(&ok); err != nil {
		return false, fmt.Errorf("scope check on location %d: %w", id, translate(err))
	}
	return ok, nil
}

// Counts are the headline figures on the dashboard.
type Counts struct {
	Locations  int64
	Facilities int64
	CHWs       int64
}

// Counts reports hierarchy and facility totals, plus the CHW total inside the
// scope. The first two are national by nature — the hierarchy is the same
// country for everyone — so only the CHW count is filtered.
func (l *Locations) Counts(ctx context.Context, sc auth.Scope) (Counts, error) {
	var c Counts
	if err := l.pool.QueryRow(ctx, `SELECT count(*) FROM locations`).Scan(&c.Locations); err != nil {
		return Counts{}, fmt.Errorf("count locations: %w", err)
	}
	if err := l.pool.QueryRow(ctx, `SELECT count(*) FROM facilities`).Scan(&c.Facilities); err != nil {
		return Counts{}, fmt.Errorf("count facilities: %w", err)
	}

	q := `SELECT count(*) FROM chws WHERE true`
	var args []any
	if frag, extra := sc.Filter("district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}
	if err := l.pool.QueryRow(ctx, q, args...).Scan(&c.CHWs); err != nil {
		return Counts{}, fmt.Errorf("count chws: %w", err)
	}
	return c, nil
}
