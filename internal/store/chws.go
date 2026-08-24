package store

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
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

// ByNIN returns the CHW carrying a NIN, ErrNotFound when none does.
//
// The Scope here is the caller's, and a NIN held in another district comes back
// as ErrNotFound — which is exactly right for the register's own reads, and
// exactly wrong for the importer's duplicate check, since the unique index is
// national. The bulk importer therefore asks nationally and reports the
// collision without naming where it is; see docs/import.md.
func (s *CHWs) ByNIN(ctx context.Context, sc auth.Scope, nin string) (domain.CHW, error) {
	q := `SELECT ` + chwColumns + chwFrom + ` WHERE c.nin = $1`
	args := []any{nin}

	if frag, extra := sc.Filter("c.district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}

	c, err := scanCHW(s.pool.QueryRow(ctx, q, args...))
	if err != nil {
		return domain.CHW{}, fmt.Errorf("get chw by nin: %w", translate(err))
	}
	return c, nil
}

// Filter narrows a listing. The zero Filter is "everything in the scope",
// which is what the register shows when nobody has typed anything.
type Filter struct {
	// Query matches a name or a NIN. Which of the two is decided by the shape
	// of the input, not by a radio button the user has to get right.
	Query string
	Cadre domain.Cadre
	// Status empty means both. A register that hid inactive CHWs by default
	// would quietly answer a different question than the one asked.
	Status domain.CHWStatus
	// LocationID narrows to a subtree at any level — a district, a subcounty,
	// a parish or a single village.
	LocationID int64

	Limit int
	// After and Before are keyset cursors. Offsets were rejected: paging deep
	// into 71,000 villages' worth of register would make every page slower
	// than the last, and a record inserted mid-browse would shift every
	// subsequent page by one.
	After  *Cursor
	Before *Cursor
}

// Cursor is a position in the (last name, first name, id) ordering. It is the
// sort key itself rather than an opaque offset, so it stays valid when rows are
// inserted or removed around it.
type Cursor struct {
	LastName  string
	FirstName string
	ID        int64
}

// Page is one screenful of the register, with the cursors needed to step either
// way. The register is browsed in both directions — a clerk who pages past a
// name goes back for it — so a forward-only cursor would not do.
type Page struct {
	CHWs    []domain.CHW
	First   *Cursor // cursor for the page before this one
	Last    *Cursor // cursor for the page after this one
	HasPrev bool
	HasNext bool
}

// ninish reports whether a query looks like someone reaching for a NIN rather
// than a name: NINs start with two letters and carry digits, and no Ugandan
// surname does.
func ninish(q string) bool {
	if len(q) < 3 {
		return false
	}
	hasDigit := false
	for _, r := range q {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		default:
			return false // a space, a hyphen: that is a name
		}
	}
	return hasDigit
}

// where builds the predicate the listing and the count share. Keeping it in one
// place is not tidiness: a count that filtered differently from the page it
// counts would be a bug nobody notices until the numbers disagree.
func (f Filter) where(sc auth.Scope) (string, []any) {
	where := ` WHERE true`
	var args []any

	if frag, extra := sc.Filter("c.district_id", len(args)+1); frag != "" {
		where += frag
		args = append(args, extra...)
	}
	if f.Cadre != "" {
		args = append(args, string(f.Cadre))
		where += fmt.Sprintf(" AND c.cadre = $%d::cadre", len(args))
	}
	if f.Status != "" {
		args = append(args, string(f.Status))
		where += fmt.Sprintf(" AND c.status = $%d::chw_status", len(args))
	}
	if f.LocationID != 0 {
		// A prefix scan on the materialized path: one comparison covers a
		// district or a single village, and locations_path_idx serves both.
		args = append(args, f.LocationID)
		where += fmt.Sprintf(
			" AND l.path LIKE (SELECT path FROM locations WHERE id = $%d) || '%%'", len(args))
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		if ninish(q) {
			args = append(args, strings.ToUpper(q)+"%")
			where += fmt.Sprintf(" AND c.nin LIKE $%d", len(args))
		} else {
			// The trigram index is built on this exact expression, so the
			// search has to be written against it rather than against the two
			// columns separately.
			args = append(args, "%"+q+"%")
			where += fmt.Sprintf(" AND (c.first_name || ' ' || c.last_name) ILIKE $%d", len(args))
		}
	}
	return where, args
}

// List returns one page of the register, ordered by name.
//
// Ordering is (lower(last_name), lower(first_name), id): surname first because
// that is how a register is read, id last because two people in one village
// genuinely share a name and the sort still has to be total.
func (s *CHWs) List(ctx context.Context, sc auth.Scope, f Filter) (Page, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}

	where, args := f.where(sc)

	// Keyset. Row-wise comparison is what lets one predicate use the whole
	// three-column index; comparing the columns with AND/OR by hand does not.
	order := "ASC"
	if cursor := f.After; cursor != nil {
		args = append(args, strings.ToLower(cursor.LastName), strings.ToLower(cursor.FirstName), cursor.ID)
		where += fmt.Sprintf(
			" AND (lower(c.last_name), lower(c.first_name), c.id) > ($%d, $%d, $%d)",
			len(args)-2, len(args)-1, len(args))
	} else if cursor := f.Before; cursor != nil {
		args = append(args, strings.ToLower(cursor.LastName), strings.ToLower(cursor.FirstName), cursor.ID)
		where += fmt.Sprintf(
			" AND (lower(c.last_name), lower(c.first_name), c.id) < ($%d, $%d, $%d)",
			len(args)-2, len(args)-1, len(args))
		// Walking backwards means reading the rows nearest the cursor, which
		// is the far end of the page; the slice is flipped below.
		order = "DESC"
	}

	// One row more than asked for, to learn whether another page exists
	// without counting the whole set.
	args = append(args, f.Limit+1)
	q := `SELECT ` + chwColumns + chwFrom + where +
		fmt.Sprintf(" ORDER BY lower(c.last_name) %s, lower(c.first_name) %s, c.id %s LIMIT $%d",
			order, order, order, len(args))

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return Page{}, fmt.Errorf("list chws: %w", translate(err))
	}
	defer rows.Close()

	var out []domain.CHW
	for rows.Next() {
		c, err := scanCHW(rows)
		if err != nil {
			return Page{}, fmt.Errorf("scan chw: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}

	more := len(out) > f.Limit
	if more {
		out = out[:f.Limit]
	}
	if order == "DESC" {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}

	page := Page{CHWs: out}
	if len(out) > 0 {
		first := cursorFor(out[0])
		last := cursorFor(out[len(out)-1])
		page.First, page.Last = &first, &last
	}
	switch {
	case f.Before != nil:
		// Walking backwards: the extra row proves there is more behind us, and
		// there is always a page ahead, because we came from it.
		page.HasPrev, page.HasNext = more, true
	case f.After != nil:
		page.HasPrev, page.HasNext = true, more
	default:
		page.HasPrev, page.HasNext = false, more
	}
	return page, nil
}

func cursorFor(c domain.CHW) Cursor {
	return Cursor{LastName: c.LastName, FirstName: c.FirstName, ID: c.ID}
}

// Matching counts the CHWs a filter selects, for the "N found" line. It is a
// separate query from List by design: the page itself is a keyset scan that
// never counts, and a count that ran on every page would undo that.
func (s *CHWs) Matching(ctx context.Context, sc auth.Scope, f Filter) (int64, error) {
	f.Limit, f.After, f.Before = 0, nil, nil
	where, args := f.where(sc)

	var n int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*)`+chwFrom+where, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count matching chws: %w", translate(err))
	}
	return n, nil
}

// Create inserts a CHW and its audit row in one transaction.
func (s *CHWs) Create(ctx context.Context, sc auth.Scope, actor domain.User, in CHWInput, ip netip.Addr) (domain.CHW, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.CHW{}, fmt.Errorf("create chw: %w", err)
	}
	defer tx.Rollback(ctx)

	c, err := s.CreateTx(ctx, tx, sc, actor, in, ip)
	if err != nil {
		return domain.CHW{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.CHW{}, fmt.Errorf("create chw: %w", err)
	}
	return c, nil
}

// CreateTx inserts a CHW and its audit row into a caller's transaction, the
// same arrangement Audit.RecordTx offers for the same reason: the bulk importer
// marks its staged row in the same transaction that creates the CHW, so a
// process that dies mid-commit cannot leave a register record whose import row
// still reads "ready" and would be created a second time on the next attempt.
//
// The scope is enforced after the insert rather than before it: district_id is
// derived by trigger from the path, so the authoritative value does not exist
// until the row does. An out-of-scope placement is rolled back — which is what
// stops a district user importing into another district even if every check
// above this one is wrong.
func (s *CHWs) CreateTx(ctx context.Context, tx pgx.Tx, sc auth.Scope, actor domain.User, in CHWInput, ip netip.Addr) (domain.CHW, error) {
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
