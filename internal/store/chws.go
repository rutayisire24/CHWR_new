package store

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"chwr/internal/auth"
	"chwr/internal/domain"
)

// CHWs is the register itself. Every mutation here runs in a transaction that
// also writes its audit row, so invariant 6 holds by construction rather than
// by the handler remembering.
type CHWs struct {
	pool *pgxpool.Pool
}

// chwColumns is the projection every CHW query shares. The enums are cast to
// text so pgx needs no type registration, and nin is coalesced because "not
// recorded" is the common case, not an error.
const chwColumns = `
    c.id, coalesce(c.nin,''), c.first_name, c.last_name, c.sex::text, c.cadre::text,
    c.age_years, c.age_captured_on, c.location_id, c.district_id,
    l.name, d.name, c.status::text, c.deactivated_at, coalesce(c.deactivation_reason,''),
    c.created_by, c.updated_by, c.created_at, c.updated_at`

const chwFrom = `
    FROM chws c
    JOIN locations l ON l.id = c.location_id
    JOIN locations d ON d.id = c.district_id`

func scanCHW(row pgx.Row) (domain.CHW, error) {
	var c domain.CHW
	var sex, cadre, status string
	err := row.Scan(&c.ID, &c.NIN, &c.FirstName, &c.LastName, &sex, &cadre,
		&c.AgeYears, &c.AgeCapturedOn, &c.LocationID, &c.DistrictID,
		&c.LocationName, &c.DistrictName, &status, &c.DeactivatedAt, &c.DeactivationReason,
		&c.CreatedBy, &c.UpdatedBy, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return domain.CHW{}, err
	}
	c.Sex = domain.Sex(sex)
	c.Cadre = domain.Cadre(cadre)
	c.Status = domain.CHWStatus(status)
	return c, nil
}

// CHWInput is the core record as a form supplies it. district_id is absent by
// design: a trigger derives it from location_id, and nothing outside the
// database is allowed to set it.
type CHWInput struct {
	NIN        string // empty means not recorded
	FirstName  string
	LastName   string
	Sex        domain.Sex
	Cadre      domain.Cadre
	AgeYears   *int16
	LocationID int64
}

// Get returns one CHW inside the scope. A CHW in another district is
// ErrNotFound, not ErrForbidden: existence is itself scoped information.
func (s *CHWs) Get(ctx context.Context, sc auth.Scope, id int64) (domain.CHW, error) {
	q := `SELECT ` + chwColumns + chwFrom + ` WHERE c.id = $1`
	args := []any{id}

	if frag, extra := sc.Filter("c.district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}

	c, err := scanCHW(s.pool.QueryRow(ctx, q, args...))
	if err != nil {
		return domain.CHW{}, fmt.Errorf("get chw %d: %w", id, translate(err))
	}
	return c, nil
}

// Filter narrows a listing. The full search-and-paginate UI is phase 5; this
// is what the register needs to be navigable while phase 3 lands.
type Filter struct {
	Status domain.CHWStatus // empty means both
	Limit  int
}

// List returns CHWs inside the scope, most recently changed first.
func (s *CHWs) List(ctx context.Context, sc auth.Scope, f Filter) ([]domain.CHW, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}

	q := `SELECT ` + chwColumns + chwFrom + ` WHERE true`
	var args []any

	if frag, extra := sc.Filter("c.district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}
	if f.Status != "" {
		args = append(args, string(f.Status))
		q += fmt.Sprintf(" AND c.status = $%d::chw_status", len(args))
	}
	args = append(args, f.Limit)
	q += fmt.Sprintf(" ORDER BY c.updated_at DESC LIMIT $%d", len(args))

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list chws: %w", translate(err))
	}
	defer rows.Close()

	var out []domain.CHW
	for rows.Next() {
		c, err := scanCHW(rows)
		if err != nil {
			return nil, fmt.Errorf("scan chw: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Count reports how many CHWs are in the scope, by status.
func (s *CHWs) Count(ctx context.Context, sc auth.Scope) (active, inactive int64, err error) {
	q := `SELECT count(*) FILTER (WHERE status = 'active'),
	             count(*) FILTER (WHERE status = 'inactive')
	      FROM chws c WHERE true`
	var args []any
	if frag, extra := sc.Filter("c.district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}
	if err := s.pool.QueryRow(ctx, q, args...).Scan(&active, &inactive); err != nil {
		return 0, 0, fmt.Errorf("count chws: %w", translate(err))
	}
	return active, inactive, nil
}

// Create inserts a CHW and its audit row in one transaction.
//
// The scope is enforced after the insert rather than before it: district_id is
// derived by trigger from the path, so the authoritative value does not exist
// until the row does. An out-of-scope placement is rolled back.
func (s *CHWs) Create(ctx context.Context, sc auth.Scope, actor domain.User, in CHWInput, ip netip.Addr) (domain.CHW, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.CHW{}, fmt.Errorf("create chw: %w", err)
	}
	defer tx.Rollback(ctx)

	const q = `
	    WITH inserted AS (
	        INSERT INTO chws (nin, first_name, last_name, sex, cadre, age_years,
	                          location_id, district_id, created_by, updated_by)
	        VALUES (nullif($1,''), $2, $3, $4::sex, $5::cadre, $6, $7, 0, $8, $8)
	        RETURNING *
	    )
	    SELECT ` + chwColumns + `
	    FROM inserted c
	    JOIN locations l ON l.id = c.location_id
	    JOIN locations d ON d.id = c.district_id`

	c, err := scanCHW(tx.QueryRow(ctx, q,
		in.NIN, in.FirstName, in.LastName, string(in.Sex), string(in.Cadre),
		in.AgeYears, in.LocationID, actor.ID))
	if err != nil {
		return domain.CHW{}, fmt.Errorf("create chw: %w", translate(err))
	}
	if !sc.Allows(c.DistrictID) {
		return domain.CHW{}, fmt.Errorf("create chw in district %d: %w", c.DistrictID, domain.ErrForbidden)
	}

	if err := s.auditTx(ctx, tx, actor, ActionCHWCreate, c, nil, auditCHW(c), ip); err != nil {
		return domain.CHW{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.CHW{}, fmt.Errorf("create chw: %w", err)
	}
	return c, nil
}

// Update rewrites the core record. age_captured_on is re-stamped only when the
// age actually changes: age is a snapshot, and a save that left it alone must
// not claim the snapshot is fresh.
//
// A district user cannot move a CHW out of their district — the pre-update
// scope filter blocks editing someone else's, and the post-update check blocks
// transferring one away.
func (s *CHWs) Update(ctx context.Context, sc auth.Scope, actor domain.User, id int64, in CHWInput, ip netip.Addr) (domain.CHW, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.CHW{}, fmt.Errorf("update chw %d: %w", id, err)
	}
	defer tx.Rollback(ctx)

	before, err := s.getTx(ctx, tx, sc, id)
	if err != nil {
		return domain.CHW{}, err
	}

	q := `
	    WITH updated AS (
	        UPDATE chws c SET
	            nin = nullif($2,''), first_name = $3, last_name = $4,
	            sex = $5::sex, cadre = $6::cadre, age_years = $7,
	            age_captured_on = CASE WHEN c.age_years IS DISTINCT FROM $7
	                                   THEN current_date ELSE c.age_captured_on END,
	            location_id = $8, updated_by = $9, updated_at = now()
	        WHERE c.id = $1`
	args := []any{id, in.NIN, in.FirstName, in.LastName, string(in.Sex),
		string(in.Cadre), in.AgeYears, in.LocationID, actor.ID}

	if frag, extra := sc.Filter("c.district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}
	q += `
	        RETURNING *
	    )
	    SELECT ` + chwColumns + `
	    FROM updated c
	    JOIN locations l ON l.id = c.location_id
	    JOIN locations d ON d.id = c.district_id`

	after, err := scanCHW(tx.QueryRow(ctx, q, args...))
	if err != nil {
		return domain.CHW{}, fmt.Errorf("update chw %d: %w", id, translate(err))
	}
	if !sc.Allows(after.DistrictID) {
		return domain.CHW{}, fmt.Errorf("move chw %d to district %d: %w", id, after.DistrictID, domain.ErrForbidden)
	}

	if err := s.auditTx(ctx, tx, actor, ActionCHWUpdate, after, auditCHW(before), auditCHW(after), ip); err != nil {
		return domain.CHW{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.CHW{}, fmt.Errorf("update chw %d: %w", id, err)
	}
	return after, nil
}

// Deactivate retires a CHW. CHWs are never deleted: the record stays, with a
// status, a timestamp and a reason that chws_deactivation_complete keeps
// consistent with each other.
func (s *CHWs) Deactivate(ctx context.Context, sc auth.Scope, actor domain.User, id int64, reason string, ip netip.Addr) (domain.CHW, error) {
	return s.setStatus(ctx, sc, actor, id, domain.CHWInactive, reason, ActionCHWDeactivate, ip)
}

// Reactivate returns a CHW to service, clearing the deactivation fields the
// CHECK constraint requires to be absent while active. The audit row keeps the
// old reason in `before`, so the history of why they left is not lost.
func (s *CHWs) Reactivate(ctx context.Context, sc auth.Scope, actor domain.User, id int64, ip netip.Addr) (domain.CHW, error) {
	return s.setStatus(ctx, sc, actor, id, domain.CHWActive, "", ActionCHWReactivate, ip)
}

func (s *CHWs) setStatus(ctx context.Context, sc auth.Scope, actor domain.User, id int64,
	status domain.CHWStatus, reason, action string, ip netip.Addr) (domain.CHW, error) {

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.CHW{}, fmt.Errorf("%s %d: %w", action, id, err)
	}
	defer tx.Rollback(ctx)

	before, err := s.getTx(ctx, tx, sc, id)
	if err != nil {
		return domain.CHW{}, err
	}
	if before.Status == status {
		return before, nil // idempotent: a double submit is not an error
	}

	// The inactive flag is its own parameter rather than a second reading of
	// the status text: one placeholder used at two different types is exactly
	// the kind of inference Postgres resolves in whichever way you did not
	// expect.
	q := `
	    WITH updated AS (
	        UPDATE chws c SET
	            status = $2::chw_status,
	            deactivated_at = CASE WHEN $3 THEN now() ELSE NULL END,
	            deactivation_reason = nullif($4,''),
	            updated_by = $5, updated_at = now()
	        WHERE c.id = $1`
	args := []any{id, string(status), status == domain.CHWInactive, reason, actor.ID}

	if frag, extra := sc.Filter("c.district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}
	q += `
	        RETURNING *
	    )
	    SELECT ` + chwColumns + `
	    FROM updated c
	    JOIN locations l ON l.id = c.location_id
	    JOIN locations d ON d.id = c.district_id`

	after, err := scanCHW(tx.QueryRow(ctx, q, args...))
	if err != nil {
		return domain.CHW{}, fmt.Errorf("%s %d: %w", action, id, translate(err))
	}

	if err := s.auditTx(ctx, tx, actor, action, after, auditCHW(before), auditCHW(after), ip); err != nil {
		return domain.CHW{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.CHW{}, fmt.Errorf("%s %d: %w", action, id, err)
	}
	return after, nil
}

// PossibleDuplicates finds active CHWs with the same name at the same
// location. NIN is optional in the source form, so the unique index cannot
// catch a double entry; this is the soft probe chws_dup_probe_idx exists for.
// It warns, it does not block: two people in one village can share a name.
func (s *CHWs) PossibleDuplicates(ctx context.Context, sc auth.Scope, locationID int64, first, last string, excludeID int64) ([]domain.CHW, error) {
	q := `SELECT ` + chwColumns + chwFrom + `
	      WHERE c.location_id = $1
	        AND lower(c.last_name) = lower($2)
	        AND lower(c.first_name) = lower($3)
	        AND c.id <> $4`
	args := []any{locationID, last, first, excludeID}

	if frag, extra := sc.Filter("c.district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("duplicate probe: %w", translate(err))
	}
	defer rows.Close()

	var out []domain.CHW
	for rows.Next() {
		c, err := scanCHW(rows)
		if err != nil {
			return nil, fmt.Errorf("scan duplicate: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// getTx reads a CHW inside a transaction, applying the scope, so a mutation
// can compare before and after without a second connection seeing a different
// snapshot.
func (s *CHWs) getTx(ctx context.Context, tx pgx.Tx, sc auth.Scope, id int64) (domain.CHW, error) {
	q := `SELECT ` + chwColumns + chwFrom + ` WHERE c.id = $1`
	args := []any{id}

	if frag, extra := sc.Filter("c.district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}

	c, err := scanCHW(tx.QueryRow(ctx, q, args...))
	if err != nil {
		return domain.CHW{}, fmt.Errorf("get chw %d: %w", id, translate(err))
	}
	return c, nil
}

// auditTx writes the mutation's audit row in the mutation's own transaction.
func (s *CHWs) auditTx(ctx context.Context, tx pgx.Tx, actor domain.User, action string,
	c domain.CHW, before, after any, ip netip.Addr) error {

	e := ActorFrom(actor)
	e.Action = action
	e.Entity = "chw"
	e.EntityID = &c.ID
	e.DistrictID = &c.DistrictID // the CHW's district, not the actor's: this is the slice a district admin reads
	e.Before = before
	e.After = after
	e.IP = ip
	return recordOn(ctx, tx, e)
}

// auditCHW is the JSONB shape written to audit_log. It is the whole core
// record: audit_log doubles as CHW change history, so a partial snapshot would
// make the history unreconstructable.
func auditCHW(c domain.CHW) map[string]any {
	m := map[string]any{
		"id":              c.ID,
		"nin":             c.NIN,
		"first_name":      c.FirstName,
		"last_name":       c.LastName,
		"sex":             string(c.Sex),
		"cadre":           string(c.Cadre),
		"age_years":       c.AgeYears,
		"age_captured_on": c.AgeCapturedOn.Format(time.DateOnly),
		"location_id":     c.LocationID,
		"district_id":     c.DistrictID,
		"status":          string(c.Status),
	}
	if c.DeactivatedAt != nil {
		m["deactivated_at"] = c.DeactivatedAt.Format(time.RFC3339)
		m["deactivation_reason"] = c.DeactivationReason
	}
	return m
}
