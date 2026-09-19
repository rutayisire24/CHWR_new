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

// Workers is the register itself — the people. Their postings live in
// Deployments; every mutation here runs in a transaction that also writes its
// audit row, so the audit invariant holds by construction rather than by the
// handler remembering.
type Workers struct {
	pool        *pgxpool.Pool
	deployments *Deployments
}

// workerColumns is the projection every register query shares: the person,
// plus the posting they are seen through. The enums are cast to text so pgx
// needs no type registration, and nin is coalesced because "not recorded" is
// the common case, not an error.
const workerColumns = `
    w.id, coalesce(w.nin,''), w.first_name, w.last_name, w.sex::text,
    w.age_years, w.age_captured_on, w.district_id,
    w.status::text, w.deactivated_at, coalesce(w.deactivation_reason,''),
    w.created_by, w.updated_by, w.created_at, w.updated_at,
    dep.id, dep.cadre_id, coalesce(cd.slug,''), coalesce(cd.label,''),
        coalesce(cd.placement_level::text,''),
    dep.location_id, dep.district_id, coalesce(l.name,''), coalesce(dl.name,''),
    dep.facility_id, coalesce(f.name,''),
    dep.started_on, dep.ended_on, coalesce(dep.end_reason,'')`

// workerFrom joins the posting a worker is seen through: the active one, or
// their most recent when none is active. An inactive worker is still listed
// where they last served — a register that dropped their cadre and location
// the day they left would be unreadable.
const workerFrom = `
    FROM health_workers w
    LEFT JOIN LATERAL (
        SELECT x.* FROM deployments x
         WHERE x.health_worker_id = w.id
         ORDER BY (x.ended_on IS NULL) DESC, x.started_on DESC, x.id DESC
         LIMIT 1
    ) dep ON true
    LEFT JOIN cadres cd    ON cd.id = dep.cadre_id
    LEFT JOIN locations l  ON l.id  = dep.location_id
    LEFT JOIN locations dl ON dl.id = dep.district_id
    LEFT JOIN facilities f ON f.id  = dep.facility_id`

func scanWorker(row pgx.Row) (domain.HealthWorker, error) {
	var w domain.HealthWorker
	var sex, status string
	var depID, depLocationID, depDistrictID, facilityID *int64
	var cadreID *int16
	var cadreSlug, cadreLabel, cadreLevel, locName, distName, facName, endReason string
	var startedOn, endedOn *time.Time

	err := row.Scan(&w.ID, &w.NIN, &w.FirstName, &w.LastName, &sex,
		&w.AgeYears, &w.AgeCapturedOn, &w.DistrictID,
		&status, &w.DeactivatedAt, &w.DeactivationReason,
		&w.CreatedBy, &w.UpdatedBy, &w.CreatedAt, &w.UpdatedAt,
		&depID, &cadreID, &cadreSlug, &cadreLabel, &cadreLevel,
		&depLocationID, &depDistrictID, &locName, &distName,
		&facilityID, &facName, &startedOn, &endedOn, &endReason)
	if err != nil {
		return domain.HealthWorker{}, err
	}
	w.Sex = domain.Sex(sex)
	w.Status = domain.WorkerStatus(status)

	if depID != nil {
		d := domain.Deployment{
			ID:             *depID,
			HealthWorkerID: w.ID,
			Cadre: domain.Cadre{
				Slug:           cadreSlug,
				Label:          cadreLabel,
				PlacementLevel: domain.Level(cadreLevel),
			},
			LocationID:   *depLocationID,
			DistrictID:   *depDistrictID,
			LocationName: locName,
			DistrictName: distName,
			FacilityID:   facilityID,
			FacilityName: facName,
			EndReason:    endReason,
		}
		if cadreID != nil {
			d.CadreID = *cadreID
			d.Cadre.ID = *cadreID
		}
		if startedOn != nil {
			d.StartedOn = *startedOn
		}
		d.EndedOn = endedOn
		w.Deployment = &d
	}
	return w, nil
}

// WorkerInput is the register form: the person, plus the posting they are
// being created or edited with. district_id is absent by design — a trigger
// derives it from location_id, and nothing outside the database may set it.
type WorkerInput struct {
	NIN        string // empty means not recorded
	FirstName  string
	LastName   string
	Sex        domain.Sex
	AgeYears   *int16
	CadreID    int16
	LocationID int64
	FacilityID *int64
}

// Get returns one worker inside the scope. A worker in another district is
// ErrNotFound, not ErrForbidden: existence is itself scoped information.
func (s *Workers) Get(ctx context.Context, sc auth.Scope, id int64) (domain.HealthWorker, error) {
	q := `SELECT ` + workerColumns + workerFrom + ` WHERE w.id = $1`
	args := []any{id}

	if frag, extra := sc.Filter("w.district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}

	w, err := scanWorker(s.pool.QueryRow(ctx, q, args...))
	if err != nil {
		return domain.HealthWorker{}, fmt.Errorf("get worker %d: %w", id, translate(err))
	}
	return w, nil
}

// ByNIN returns the worker carrying a NIN, ErrNotFound when none does.
//
// The Scope here is the caller's, and a NIN held in another district comes back
// as ErrNotFound — which is exactly right for the register's own reads, and
// exactly wrong for the importer's duplicate check, since the unique index is
// national. The bulk importer therefore asks nationally and reports the
// collision without naming where it is; see docs/import.md.
func (s *Workers) ByNIN(ctx context.Context, sc auth.Scope, nin string) (domain.HealthWorker, error) {
	q := `SELECT ` + workerColumns + workerFrom + ` WHERE w.nin = $1`
	args := []any{nin}

	if frag, extra := sc.Filter("w.district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}

	w, err := scanWorker(s.pool.QueryRow(ctx, q, args...))
	if err != nil {
		return domain.HealthWorker{}, fmt.Errorf("get worker by nin: %w", translate(err))
	}
	return w, nil
}

// Filter narrows a listing. The zero Filter is "everything in the scope",
// which is what the register shows when nobody has typed anything.
type Filter struct {
	// Query matches a name or a NIN. Which of the two is decided by the shape
	// of the input, not by a radio button the user has to get right.
	Query string
	// Cadre is a cadres slug ('vht'), matched against the posting the worker
	// is seen through.
	Cadre string
	// Status empty means both. A register that hid inactive workers by default
	// would quietly answer a different question than the one asked.
	Status domain.WorkerStatus
	// LocationID narrows to a subtree at any level — a district, a subcounty,
	// a parish or a single village.
	LocationID int64

	Limit int
	// After and Before are keyset cursors. Offsets were rejected: paging deep
	// into the register would make every page slower than the last, and a
	// record inserted mid-browse would shift every subsequent page by one.
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
	Workers []domain.HealthWorker
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

	if frag, extra := sc.Filter("w.district_id", len(args)+1); frag != "" {
		where += frag
		args = append(args, extra...)
	}
	if f.Cadre != "" {
		args = append(args, f.Cadre)
		where += fmt.Sprintf(" AND cd.slug = $%d", len(args))
	}
	if f.Status != "" {
		args = append(args, string(f.Status))
		where += fmt.Sprintf(" AND w.status = $%d::worker_status", len(args))
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
			where += fmt.Sprintf(" AND w.nin LIKE $%d", len(args))
		} else {
			// The trigram index is built on this exact expression, so the
			// search has to be written against it rather than against the two
			// columns separately.
			args = append(args, "%"+q+"%")
			where += fmt.Sprintf(" AND (w.first_name || ' ' || w.last_name) ILIKE $%d", len(args))
		}
	}
	return where, args
}

// needsDeployment reports whether the filter touches the posting. A count that
// does not can skip the lateral join and count the people directly.
func (f Filter) needsDeployment() bool { return f.Cadre != "" || f.LocationID != 0 }

// List returns one page of the register, ordered by name.
//
// Ordering is (lower(last_name), lower(first_name), id): surname first because
// that is how a register is read, id last because two people in one village
// genuinely share a name and the sort still has to be total.
func (s *Workers) List(ctx context.Context, sc auth.Scope, f Filter) (Page, error) {
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
			" AND (lower(w.last_name), lower(w.first_name), w.id) > ($%d, $%d, $%d)",
			len(args)-2, len(args)-1, len(args))
	} else if cursor := f.Before; cursor != nil {
		args = append(args, strings.ToLower(cursor.LastName), strings.ToLower(cursor.FirstName), cursor.ID)
		where += fmt.Sprintf(
			" AND (lower(w.last_name), lower(w.first_name), w.id) < ($%d, $%d, $%d)",
			len(args)-2, len(args)-1, len(args))
		// Walking backwards means reading the rows nearest the cursor, which
		// is the far end of the page; the slice is flipped below.
		order = "DESC"
	}

	// One row more than asked for, to learn whether another page exists
	// without counting the whole set.
	args = append(args, f.Limit+1)
	q := `SELECT ` + workerColumns + workerFrom + where +
		fmt.Sprintf(" ORDER BY lower(w.last_name) %s, lower(w.first_name) %s, w.id %s LIMIT $%d",
			order, order, order, len(args))

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return Page{}, fmt.Errorf("list workers: %w", translate(err))
	}
	defer rows.Close()

	var out []domain.HealthWorker
	for rows.Next() {
		w, err := scanWorker(rows)
		if err != nil {
			return Page{}, fmt.Errorf("scan worker: %w", err)
		}
		out = append(out, w)
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

	page := Page{Workers: out}
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

func cursorFor(w domain.HealthWorker) Cursor {
	return Cursor{LastName: w.LastName, FirstName: w.FirstName, ID: w.ID}
}

// Matching counts the workers a filter selects, for the "N found" line. It is
// a separate query from List by design: the page itself is a keyset scan that
// never counts, and a count that ran on every page would undo that.
func (s *Workers) Matching(ctx context.Context, sc auth.Scope, f Filter) (int64, error) {
	f.Limit, f.After, f.Before = 0, nil, nil
	where, args := f.where(sc)

	from := ` FROM health_workers w`
	if f.needsDeployment() {
		from = workerFrom
	}

	var n int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*)`+from+where, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count matching workers: %w", translate(err))
	}
	return n, nil
}

// Create inserts a worker, their first deployment and both audit rows in one
// transaction.
func (s *Workers) Create(ctx context.Context, sc auth.Scope, actor domain.User, in WorkerInput, ip netip.Addr) (domain.HealthWorker, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HealthWorker{}, fmt.Errorf("create worker: %w", err)
	}
	defer tx.Rollback(ctx)

	w, err := s.CreateTx(ctx, tx, sc, actor, in, ip)
	if err != nil {
		return domain.HealthWorker{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.HealthWorker{}, fmt.Errorf("create worker: %w", err)
	}
	return w, nil
}

// CreateTx inserts a worker, their first deployment and both audit rows into a
// caller's transaction, the same arrangement Audit.RecordTx offers for the same
// reason: the bulk importer marks its staged row in the same transaction that
// creates the worker, so a process that dies mid-commit cannot leave a register
// record whose import row still reads "ready" and would be created a second
// time on the next attempt.
//
// The scope is enforced after the insert rather than before it: district_id is
// derived by trigger from the path, so the authoritative value does not exist
// until the row does. An out-of-scope placement is rolled back — which is what
// stops a district user importing into another district even if every check
// above this one is wrong.
func (s *Workers) CreateTx(ctx context.Context, tx pgx.Tx, sc auth.Scope, actor domain.User, in WorkerInput, ip netip.Addr) (domain.HealthWorker, error) {
	// The deployment's district_id is a placeholder the placement trigger
	// overwrites; the worker's own district anchor is filled by the sync
	// trigger as the deployment lands. The read-back takes the district from
	// the deployment because the CTE snapshot predates that sync.
	const q = `
	    WITH w AS (
	        INSERT INTO health_workers (nin, first_name, last_name, sex, age_years,
	                                    created_by, updated_by)
	        VALUES (nullif($1,''), $2, $3, $4::sex, $5, $6, $6)
	        RETURNING *
	    ), dep AS (
	        INSERT INTO deployments (health_worker_id, cadre_id, location_id, district_id,
	                                 facility_id, created_by, updated_by)
	        SELECT w.id, $7, $8, 0, $9, $6, $6 FROM w
	        RETURNING *
	    )
	    SELECT
	        w.id, coalesce(w.nin,''), w.first_name, w.last_name, w.sex::text,
	        w.age_years, w.age_captured_on, dep.district_id,
	        w.status::text, w.deactivated_at, coalesce(w.deactivation_reason,''),
	        w.created_by, w.updated_by, w.created_at, w.updated_at,
	        dep.id, dep.cadre_id, cd.slug, cd.label, cd.placement_level::text,
	        dep.location_id, dep.district_id, l.name, dl.name,
	        dep.facility_id, coalesce(f.name,''),
	        dep.started_on, dep.ended_on, coalesce(dep.end_reason,'')
	    FROM w, dep
	    JOIN cadres cd    ON cd.id = dep.cadre_id
	    JOIN locations l  ON l.id  = dep.location_id
	    JOIN locations dl ON dl.id = dep.district_id
	    LEFT JOIN facilities f ON f.id = dep.facility_id`

	w, err := scanWorker(tx.QueryRow(ctx, q,
		in.NIN, in.FirstName, in.LastName, string(in.Sex), in.AgeYears, actor.ID,
		in.CadreID, in.LocationID, in.FacilityID))
	if err != nil {
		return domain.HealthWorker{}, fmt.Errorf("create worker: %w", translate(err))
	}
	if !sc.Allows(w.Deployment.DistrictID) {
		return domain.HealthWorker{}, fmt.Errorf("create worker in district %d: %w", w.Deployment.DistrictID, domain.ErrForbidden)
	}

	if err := s.auditTx(ctx, tx, actor, ActionWorkerCreate, w, nil, auditWorker(w), ip); err != nil {
		return domain.HealthWorker{}, err
	}
	if err := s.deployments.auditTx(ctx, tx, actor, ActionDeploymentStart, *w.Deployment, nil, auditDeployment(*w.Deployment), ip); err != nil {
		return domain.HealthWorker{}, err
	}
	return w, nil
}

// Update rewrites the register form: the person's fields, and — when the cadre
// or location changed — the posting, as an ended deployment plus a newly opened
// one in the same transaction. age_captured_on is re-stamped only when the age
// actually changes: age is a snapshot, and a save that left it alone must not
// claim the snapshot is fresh.
//
// A district user cannot move a worker out of their district — the pre-update
// scope filter blocks editing someone else's, and the post-move check inside
// the deployment write blocks transferring one away.
func (s *Workers) Update(ctx context.Context, sc auth.Scope, actor domain.User, id int64, in WorkerInput, ip netip.Addr) (domain.HealthWorker, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HealthWorker{}, fmt.Errorf("update worker %d: %w", id, err)
	}
	defer tx.Rollback(ctx)

	before, err := s.getTx(ctx, tx, sc, id)
	if err != nil {
		return domain.HealthWorker{}, err
	}

	q := `
	    WITH updated AS (
	        UPDATE health_workers w SET
	            nin = nullif($2,''), first_name = $3, last_name = $4,
	            sex = $5::sex, age_years = $6,
	            age_captured_on = CASE WHEN w.age_years IS DISTINCT FROM $6
	                                   THEN current_date ELSE w.age_captured_on END,
	            updated_by = $7, updated_at = now()
	        WHERE w.id = $1`
	args := []any{id, in.NIN, in.FirstName, in.LastName, string(in.Sex),
		in.AgeYears, actor.ID}

	if frag, extra := sc.Filter("w.district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}
	q += ` RETURNING id ) SELECT id FROM updated`

	if _, err := tx.Exec(ctx, q, args...); err != nil {
		return domain.HealthWorker{}, fmt.Errorf("update worker %d: %w", id, translate(err))
	}

	// The posting. A change of cadre or location ends the current deployment
	// and opens another; the facility travels with it when the new district is
	// the old one, and is otherwise left behind — a transfer across districts
	// starts unattached rather than stranded on a facility it may not report
	// to. The facility is otherwise edited on its own, through SetFacility.
	if redeploy, reason := redeployment(before, in); redeploy {
		dep, err := s.deployments.changeTx(ctx, tx, sc, actor, id, DeploymentInput{
			CadreID: in.CadreID, LocationID: in.LocationID,
		}, reason, ip)
		if err != nil {
			return domain.HealthWorker{}, fmt.Errorf("redeploy worker %d: %w", id, err)
		}

		carry := in.FacilityID
		if carry == nil && before.Deployment != nil {
			carry = before.Deployment.FacilityID
		}
		if carry != nil {
			facilityDistrict, ferr := s.deployments.facilityDistrictTx(ctx, tx, *carry)
			if ferr != nil {
				return domain.HealthWorker{}, fmt.Errorf("carry facility for worker %d: %w", id, ferr)
			}
			if facilityDistrict == dep.DistrictID {
				if err := s.deployments.setFacilityTx(ctx, tx, sc, actor, id, carry, ip); err != nil {
					return domain.HealthWorker{}, fmt.Errorf("carry facility for worker %d: %w", id, err)
				}
			}
		}
	}

	after, err := s.getTx(ctx, tx, sc, id)
	if err != nil {
		return domain.HealthWorker{}, err
	}

	// The worker audit goes in only when the person themselves changed; a pure
	// transfer is already told by the deployment rows.
	if before.NIN != after.NIN ||
		before.FirstName != after.FirstName || before.LastName != after.LastName ||
		before.Sex != after.Sex || !sameInt16p(before.AgeYears, after.AgeYears) {
		if err := s.auditTx(ctx, tx, actor, ActionWorkerUpdate, after, auditWorker(before), auditWorker(after), ip); err != nil {
			return domain.HealthWorker{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.HealthWorker{}, fmt.Errorf("update worker %d: %w", id, err)
	}
	return after, nil
}

// redeployment decides whether saving the register form opens a new posting,
// and the reason the old one ends with.
//
// An active worker with no open posting — reactivated, and now being placed —
// is deployed even when the form repeats the placement they left from, because
// that is exactly what the prefilled form posts. An inactive worker is only
// redeployed when the placement changed, which the schema then refuses: the
// handler turns that into a field message first.
//
// A change of cadre is a recadre even though it moves the worker too: cadres
// sit at different levels, so a promotion always changes the location as well.
func redeployment(before domain.HealthWorker, in WorkerInput) (bool, string) {
	dep := before.Deployment
	if dep == nil {
		return true, "transfer"
	}
	unplaced := before.Status == domain.WorkerActive && !dep.Active()
	if !unplaced && dep.CadreID == in.CadreID && dep.LocationID == in.LocationID {
		return false, ""
	}
	if dep.CadreID != in.CadreID {
		return true, "recadre"
	}
	return true, "transfer"
}

func sameInt16p(a, b *int16) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// Deactivate retires a worker from the workforce. Workers are never deleted:
// the record stays, with a status, a timestamp and a reason that
// health_workers_deactivation_complete keeps consistent. The active deployment
// ends first, in the same transaction — the schema refuses the status change
// while a posting is open.
func (s *Workers) Deactivate(ctx context.Context, sc auth.Scope, actor domain.User, id int64, reason string, ip netip.Addr) (domain.HealthWorker, error) {
	return s.setStatus(ctx, sc, actor, id, domain.WorkerInactive, reason, ActionWorkerDeactivate, ip)
}

// Reactivate returns a worker to the workforce, clearing the deactivation
// fields the CHECK constraint requires to be absent while active. It opens no
// deployment: where they serve next is a separate decision, made on the edit
// form. The audit row keeps the old reason in `before`, so the history of why
// they left is not lost.
func (s *Workers) Reactivate(ctx context.Context, sc auth.Scope, actor domain.User, id int64, ip netip.Addr) (domain.HealthWorker, error) {
	return s.setStatus(ctx, sc, actor, id, domain.WorkerActive, "", ActionWorkerReactivate, ip)
}

func (s *Workers) setStatus(ctx context.Context, sc auth.Scope, actor domain.User, id int64,
	status domain.WorkerStatus, reason, action string, ip netip.Addr) (domain.HealthWorker, error) {

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HealthWorker{}, fmt.Errorf("%s %d: %w", action, id, err)
	}
	defer tx.Rollback(ctx)

	before, err := s.getTx(ctx, tx, sc, id)
	if err != nil {
		return domain.HealthWorker{}, err
	}
	if before.Status == status {
		return before, nil // idempotent: a double submit is not an error
	}

	// Deactivating ends the open posting first, with the same reason; the
	// trigger on health_workers refuses the status change while one is open.
	if status == domain.WorkerInactive && before.Deployment != nil && before.Deployment.Active() {
		if _, err := s.deployments.endTx(ctx, tx, sc, actor, id, reason, ip); err != nil {
			return domain.HealthWorker{}, fmt.Errorf("end deployment for worker %d: %w", id, err)
		}
	}

	// The inactive flag is its own parameter rather than a second reading of
	// the status text: one placeholder used at two different types is exactly
	// the kind of inference Postgres resolves in whichever way you did not
	// expect.
	q := `
	    UPDATE health_workers w SET
	        status = $2::worker_status,
	        deactivated_at = CASE WHEN $3 THEN now() ELSE NULL END,
	        deactivation_reason = nullif($4,''),
	        updated_by = $5, updated_at = now()
	    WHERE w.id = $1`
	args := []any{id, string(status), status == domain.WorkerInactive, reason, actor.ID}

	if frag, extra := sc.Filter("w.district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}
	if _, err := tx.Exec(ctx, q, args...); err != nil {
		return domain.HealthWorker{}, fmt.Errorf("%s %d: %w", action, id, translate(err))
	}

	after, err := s.getTx(ctx, tx, sc, id)
	if err != nil {
		return domain.HealthWorker{}, err
	}

	if err := s.auditTx(ctx, tx, actor, action, after, auditWorker(before), auditWorker(after), ip); err != nil {
		return domain.HealthWorker{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.HealthWorker{}, fmt.Errorf("%s %d: %w", action, id, err)
	}
	return after, nil
}

// PossibleDuplicates finds actively-deployed workers with the same name at the
// same location. NIN is optional in the source form, so the unique index cannot
// catch a double entry; this is the soft probe the active-deployment location
// index exists for. It warns, it does not block: two people in one village can
// share a name.
func (s *Workers) PossibleDuplicates(ctx context.Context, sc auth.Scope, locationID int64, first, last string, excludeID int64) ([]domain.HealthWorker, error) {
	q := `SELECT ` + workerColumns + workerFrom + `
	      WHERE dep.ended_on IS NULL
	        AND dep.location_id = $1
	        AND lower(w.last_name) = lower($2)
	        AND lower(w.first_name) = lower($3)
	        AND w.id <> $4`
	args := []any{locationID, last, first, excludeID}

	if frag, extra := sc.Filter("w.district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("duplicate probe: %w", translate(err))
	}
	defer rows.Close()

	var out []domain.HealthWorker
	for rows.Next() {
		w, err := scanWorker(rows)
		if err != nil {
			return nil, fmt.Errorf("scan duplicate: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// getTx reads a worker inside a transaction, applying the scope, so a mutation
// can compare before and after without a second connection seeing a different
// snapshot.
func (s *Workers) getTx(ctx context.Context, tx pgx.Tx, sc auth.Scope, id int64) (domain.HealthWorker, error) {
	q := `SELECT ` + workerColumns + workerFrom + ` WHERE w.id = $1`
	args := []any{id}

	if frag, extra := sc.Filter("w.district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}

	w, err := scanWorker(tx.QueryRow(ctx, q, args...))
	if err != nil {
		return domain.HealthWorker{}, fmt.Errorf("get worker %d: %w", id, translate(err))
	}
	return w, nil
}

// auditTx writes the mutation's audit row in the mutation's own transaction.
func (s *Workers) auditTx(ctx context.Context, tx pgx.Tx, actor domain.User, action string,
	w domain.HealthWorker, before, after any, ip netip.Addr) error {

	e := ActorFrom(actor)
	e.Action = action
	e.Entity = "health_worker"
	e.EntityID = &w.ID
	e.DistrictID = w.DistrictID // the worker's district, not the actor's: this is the slice a district admin reads
	e.Before = before
	e.After = after
	e.IP = ip
	return recordOn(ctx, tx, e)
}

// auditWorker is the JSONB shape written to audit_log for the person. audit_log
// doubles as the register's change history, so a partial snapshot would make
// the history unreconstructable.
func auditWorker(w domain.HealthWorker) map[string]any {
	m := map[string]any{
		"id":              w.ID,
		"nin":             w.NIN,
		"first_name":      w.FirstName,
		"last_name":       w.LastName,
		"sex":             string(w.Sex),
		"age_years":       w.AgeYears,
		"age_captured_on": w.AgeCapturedOn.Format(time.DateOnly),
		"district_id":     w.DistrictID,
		"status":          string(w.Status),
	}
	if w.DeactivatedAt != nil {
		m["deactivated_at"] = w.DeactivatedAt.Format(time.RFC3339)
		m["deactivation_reason"] = w.DeactivationReason
	}
	return m
}
