package store

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"hwr/internal/auth"
	"hwr/internal/domain"
)

// Cadres is the cadre taxonomy as an administrator edits it: categories, and
// the cadres within them. Deployments.Cadres is the same vocabulary as the
// worker form and the importer read it — active rows only.
//
// Reads take no Scope: the vocabulary is the same for every caller. Writes take
// one and refuse a district scope outright, because a cadre is national — a
// district manager adding one would be adding it for the whole country. The
// capability check in the router is the first layer; this is the one that
// holds if a route is ever mis-wired.
type Cadres struct {
	pool *pgxpool.Pool
}

// CadreRow is a cadre with how many postings name it. The count is what the
// admin screen needs to say whether the shape can still change —
// cadres_freeze_trg refuses it once any deployment exists — and how many
// workers a retirement would touch.
type CadreRow struct {
	domain.Cadre
	Deployments int64 // every posting ever, open or ended
	Serving     int64 // open postings only
}

// InUse reports whether any posting names this cadre, which is exactly when
// its code, category and placement level are fixed.
func (r CadreRow) InUse() bool { return r.Deployments > 0 }

// CadreInput is the admin form. Code, category and level are ignored by
// Update once the cadre is in use; the trigger would refuse them anyway, and
// the form does not offer them.
type CadreInput struct {
	CategoryID     int16
	Code           string
	Label          string
	PlacementLevel domain.Level
	ImportAliases  []string
	// SortOrder nil puts a new cadre last in its category, and leaves an
	// existing one where it is.
	SortOrder *int16
	Active    bool
}

// CategoryInput is the form for a new category.
type CategoryInput struct {
	Code  string
	Label string
	// SortOrder nil puts the new category last.
	SortOrder *int16
}

// All lists every cadre, retired ones included, with its posting counts.
func (s *Cadres) All(ctx context.Context) ([]CadreRow, error) {
	q := `SELECT ` + cadreColumns + `,
	             (SELECT count(*) FROM deployments d WHERE d.cadre_id = c.id),
	             (SELECT count(*) FROM deployments d WHERE d.cadre_id = c.id AND d.ended_on IS NULL)` +
		cadreFrom + cadreOrder

	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list cadres: %w", translate(err))
	}
	defer rows.Close()

	var out []CadreRow
	for rows.Next() {
		var r CadreRow
		var level string
		if err := rows.Scan(&r.ID, &r.CategoryID, &r.CategoryCode, &r.CategoryLabel,
			&r.Code, &r.Label, &level, &r.ImportAliases, &r.SortOrder, &r.Active,
			&r.Deployments, &r.Serving); err != nil {
			return nil, fmt.Errorf("scan cadre: %w", err)
		}
		r.PlacementLevel = domain.Level(level)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Get returns one cadre with its counts.
func (s *Cadres) Get(ctx context.Context, id int16) (CadreRow, error) {
	return s.getOn(ctx, s.pool, id)
}

func (s *Cadres) getOn(ctx context.Context, q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, id int16) (CadreRow, error) {
	row := q.QueryRow(ctx, `SELECT `+cadreColumns+`,
	             (SELECT count(*) FROM deployments d WHERE d.cadre_id = c.id),
	             (SELECT count(*) FROM deployments d WHERE d.cadre_id = c.id AND d.ended_on IS NULL)`+
		cadreFrom+` WHERE c.id = $1`, id)

	var r CadreRow
	var level string
	if err := row.Scan(&r.ID, &r.CategoryID, &r.CategoryCode, &r.CategoryLabel,
		&r.Code, &r.Label, &level, &r.ImportAliases, &r.SortOrder, &r.Active,
		&r.Deployments, &r.Serving); err != nil {
		return CadreRow{}, fmt.Errorf("get cadre %d: %w", id, translate(err))
	}
	r.PlacementLevel = domain.Level(level)
	return r, nil
}

// Categories lists every category, retired ones included.
func (s *Cadres) Categories(ctx context.Context) ([]domain.CadreCategory, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, code, label, sort_order, active FROM cadre_categories ORDER BY sort_order, id`)
	if err != nil {
		return nil, fmt.Errorf("list categories: %w", translate(err))
	}
	defer rows.Close()

	var out []domain.CadreCategory
	for rows.Next() {
		var c domain.CadreCategory
		if err := rows.Scan(&c.ID, &c.Code, &c.Label, &c.SortOrder, &c.Active); err != nil {
			return nil, fmt.Errorf("scan category: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Create adds a cadre and its audit row in one transaction.
func (s *Cadres) Create(ctx context.Context, sc auth.Scope, actor domain.User, in CadreInput, ip netip.Addr) (CadreRow, error) {
	if !sc.IsNational() {
		return CadreRow{}, fmt.Errorf("create cadre: %w", domain.ErrForbidden)
	}
	tx, err := begin(ctx, s.pool, actor)
	if err != nil {
		return CadreRow{}, fmt.Errorf("create cadre: %w", err)
	}
	defer tx.Rollback(ctx)

	var id int16
	err = tx.QueryRow(ctx, `
	    INSERT INTO cadres (cadre_category_id, code, label, placement_level, import_aliases, sort_order, active)
	    VALUES ($1, $2, $3, $4::location_level, $5,
	            coalesce($6, (SELECT coalesce(max(sort_order), 0) + 1 FROM cadres WHERE cadre_category_id = $1)),
	            $7)
	    RETURNING id`,
		in.CategoryID, in.Code, in.Label, string(in.PlacementLevel), aliases(in.ImportAliases),
		in.SortOrder, in.Active).Scan(&id)
	if err != nil {
		return CadreRow{}, fmt.Errorf("create cadre: %w", translate(err))
	}

	created, err := s.getOn(ctx, tx, id)
	if err != nil {
		return CadreRow{}, err
	}
	if err := auditCadre(ctx, tx, actor, ActionCadreCreate, int64(id), nil, cadreSnapshot(created), ip); err != nil {
		return CadreRow{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CadreRow{}, fmt.Errorf("create cadre: %w", err)
	}
	return created, nil
}

// Update edits a cadre and writes its audit row in one transaction. Once the
// cadre is in use, only the label, aliases, sort order and active flag change:
// the rest is carried over from the row itself rather than the input, so the
// form cannot even ask cadres_freeze_trg to refuse something.
func (s *Cadres) Update(ctx context.Context, sc auth.Scope, actor domain.User, id int16, in CadreInput, ip netip.Addr) (CadreRow, error) {
	if !sc.IsNational() {
		return CadreRow{}, fmt.Errorf("update cadre %d: %w", id, domain.ErrForbidden)
	}
	tx, err := begin(ctx, s.pool, actor)
	if err != nil {
		return CadreRow{}, fmt.Errorf("update cadre %d: %w", id, err)
	}
	defer tx.Rollback(ctx)

	// Lock the row: two admins editing one cadre must not interleave the
	// in-use read with the write.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM cadres WHERE id = $1 FOR UPDATE`, id); err != nil {
		return CadreRow{}, fmt.Errorf("update cadre %d: %w", id, translate(err))
	}
	before, err := s.getOn(ctx, tx, id)
	if err != nil {
		return CadreRow{}, err
	}
	if before.InUse() {
		in.CategoryID = before.CategoryID
		in.Code = before.Code
		in.PlacementLevel = before.PlacementLevel
	}

	tag, err := tx.Exec(ctx, `
	    UPDATE cadres
	       SET cadre_category_id = $2, code = $3, label = $4, placement_level = $5::location_level,
	           import_aliases = $6, sort_order = coalesce($7, sort_order), active = $8
	     WHERE id = $1`,
		id, in.CategoryID, in.Code, in.Label, string(in.PlacementLevel), aliases(in.ImportAliases),
		in.SortOrder, in.Active)
	if err != nil {
		return CadreRow{}, fmt.Errorf("update cadre %d: %w", id, translate(err))
	}
	if tag.RowsAffected() == 0 {
		return CadreRow{}, fmt.Errorf("update cadre %d: %w", id, domain.ErrNotFound)
	}

	after, err := s.getOn(ctx, tx, id)
	if err != nil {
		return CadreRow{}, err
	}
	if err := auditCadre(ctx, tx, actor, ActionCadreUpdate, int64(id),
		cadreSnapshot(before), cadreSnapshot(after), ip); err != nil {
		return CadreRow{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CadreRow{}, fmt.Errorf("update cadre %d: %w", id, err)
	}
	return after, nil
}

// CreateCategory adds a category and its audit row in one transaction. A new
// category has no profile surface: its workers are registered with the core
// record and a posting, and a profile of its own is a migration and a form.
func (s *Cadres) CreateCategory(ctx context.Context, sc auth.Scope, actor domain.User, in CategoryInput, ip netip.Addr) (domain.CadreCategory, error) {
	if !sc.IsNational() {
		return domain.CadreCategory{}, fmt.Errorf("create category: %w", domain.ErrForbidden)
	}
	tx, err := begin(ctx, s.pool, actor)
	if err != nil {
		return domain.CadreCategory{}, fmt.Errorf("create category: %w", err)
	}
	defer tx.Rollback(ctx)

	var c domain.CadreCategory
	err = tx.QueryRow(ctx, `
	    INSERT INTO cadre_categories (code, label, sort_order)
	    VALUES ($1, $2, coalesce($3, (SELECT coalesce(max(sort_order), 0) + 1 FROM cadre_categories)))
	    RETURNING id, code, label, sort_order, active`,
		in.Code, in.Label, in.SortOrder).Scan(&c.ID, &c.Code, &c.Label, &c.SortOrder, &c.Active)
	if err != nil {
		return domain.CadreCategory{}, fmt.Errorf("create category: %w", translate(err))
	}

	e := ActorFrom(actor)
	e.Action = ActionCategoryCreate
	e.Entity = "cadre_category"
	id := int64(c.ID)
	e.EntityID = &id
	e.DistrictID = nil // national vocabulary: no district owns it
	e.After = map[string]any{"id": c.ID, "code": c.Code, "label": c.Label,
		"sort_order": c.SortOrder, "active": c.Active}
	e.IP = ip
	if err := recordOn(ctx, tx, e); err != nil {
		return domain.CadreCategory{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.CadreCategory{}, fmt.Errorf("create category: %w", err)
	}
	return c, nil
}

func auditCadre(ctx context.Context, tx pgx.Tx, actor domain.User, action string, id int64, before, after any, ip netip.Addr) error {
	e := ActorFrom(actor)
	e.Action = action
	e.Entity = "cadre"
	e.EntityID = &id
	e.DistrictID = nil // national vocabulary: no district owns it
	e.Before = before
	e.After = after
	e.IP = ip
	return recordOn(ctx, tx, e)
}

// cadreSnapshot is the JSONB shape written to audit_log for a cadre.
func cadreSnapshot(c CadreRow) map[string]any {
	return map[string]any{
		"id":              c.ID,
		"category_id":     c.CategoryID,
		"code":            c.Code,
		"label":           c.Label,
		"placement_level": string(c.PlacementLevel),
		"import_aliases":  c.ImportAliases,
		"sort_order":      c.SortOrder,
		"active":          c.Active,
	}
}

// aliases never hands pgx a nil slice: import_aliases is NOT NULL, and a nil
// []string encodes as NULL rather than '{}'.
func aliases(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}
