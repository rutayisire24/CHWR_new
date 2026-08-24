package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"chwr/internal/auth"
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
