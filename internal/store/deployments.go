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

// Deployments is the postings register: where each health worker serves, in
// which cadre, over what period. Its methods are transaction-bound (lowercase
// or Tx-suffixed) because a posting never changes alone — a transfer is an end
// plus a start beside the worker's own update, and every change writes its
// audit row into the same transaction.
type Deployments struct {
	pool *pgxpool.Pool
}

// deploymentColumns is the projection every deployment read shares.
const deploymentColumns = `
    dep.id, dep.health_worker_id, dep.cadre_id,
    cd.slug, cd.label, cd.placement_level::text,
    dep.location_id, dep.district_id, l.name, dl.name,
    dep.facility_id, coalesce(f.name,''),
    dep.started_on, dep.ended_on, coalesce(dep.end_reason,''),
    dep.created_by, dep.updated_by, dep.created_at, dep.updated_at`

const deploymentFrom = `
    FROM deployments dep
    JOIN cadres cd       ON cd.id = dep.cadre_id
    JOIN locations l     ON l.id  = dep.location_id
    JOIN locations dl    ON dl.id = dep.district_id
    LEFT JOIN facilities f ON f.id = dep.facility_id`

func scanDeployment(row pgx.Row) (domain.Deployment, error) {
	var d domain.Deployment
	var slug, label, level string
	err := row.Scan(&d.ID, &d.HealthWorkerID, &d.CadreID,
		&slug, &label, &level,
		&d.LocationID, &d.DistrictID, &d.LocationName, &d.DistrictName,
		&d.FacilityID, &d.FacilityName,
		&d.StartedOn, &d.EndedOn, &d.EndReason,
		&d.CreatedBy, &d.UpdatedBy, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return domain.Deployment{}, err
	}
	d.Cadre = domain.Cadre{
		ID:             d.CadreID,
		Slug:           slug,
		Label:          label,
		PlacementLevel: domain.Level(level),
	}
	return d, nil
}

// Cadres lists the active cadre types, for the form's radio group and for
// matching an import's cadre column. It is a vocabulary read, like Tools and
// ServiceDomains: the same for every caller, so it takes no Scope.
func (s *Deployments) Cadres(ctx context.Context) ([]domain.Cadre, error) {
	const q = `
	    SELECT id, category_id, slug, label, placement_level::text, import_aliases, active
	      FROM cadres
	     WHERE active
	     ORDER BY sort_order`

	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list cadres: %w", translate(err))
	}
	defer rows.Close()

	var out []domain.Cadre
	for rows.Next() {
		var c domain.Cadre
		var level string
		if err := rows.Scan(&c.ID, &c.CategoryID, &c.Slug, &c.Label, &level,
			&c.ImportAliases, &c.Active); err != nil {
			return nil, fmt.Errorf("scan cadre: %w", err)
		}
		c.PlacementLevel = domain.Level(level)
		out = append(out, c)
	}
	return out, rows.Err()
}

// Facility is one row of the supervising-facility picker.
type Facility struct {
	ID        int64
	Name      string
	Level     string
	Ownership string
}

// FacilitiesIn lists the facilities of a district for the picker. Government
// facilities come first: community health workers report to those, and the
// private clinics and drug shops are loaded for completeness rather than for
// selection.
func (s *Deployments) FacilitiesIn(ctx context.Context, sc auth.Scope, districtID int64) ([]Facility, error) {
	if !sc.Allows(districtID) {
		return nil, fmt.Errorf("facilities in district %d: %w", districtID, domain.ErrNotFound)
	}

	const q = `
	    SELECT id, name, coalesce(level,''), coalesce(ownership,'')
	      FROM facilities
	     WHERE district_id = $1
	     ORDER BY (ownership = 'GOV') DESC, name`

	rows, err := s.pool.Query(ctx, q, districtID)
	if err != nil {
		return nil, fmt.Errorf("list facilities in district %d: %w", districtID, translate(err))
	}
	defer rows.Close()

	var out []Facility
	for rows.Next() {
		var f Facility
		if err := rows.Scan(&f.ID, &f.Name, &f.Level, &f.Ownership); err != nil {
			return nil, fmt.Errorf("scan facility: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// FacilityDistrict returns the district a facility belongs to, so a handler can
// refuse a cross-district attachment with a field message. The trigger
// deployments_facility_district_trg is still the guarantee.
func (s *Deployments) FacilityDistrict(ctx context.Context, facilityID int64) (int64, error) {
	var districtID int64
	err := s.pool.QueryRow(ctx, `SELECT district_id FROM facilities WHERE id = $1`, facilityID).Scan(&districtID)
	if err != nil {
		return 0, fmt.Errorf("facility %d: %w", facilityID, translate(err))
	}
	return districtID, nil
}

// facilityDistrictTx is the in-transaction half of FacilityDistrict, for
// callers deciding whether an attachment survives a move.
func (s *Deployments) facilityDistrictTx(ctx context.Context, tx pgx.Tx, facilityID int64) (int64, error) {
	var districtID int64
	err := tx.QueryRow(ctx, `SELECT district_id FROM facilities WHERE id = $1`, facilityID).Scan(&districtID)
	if err != nil {
		return 0, fmt.Errorf("facility %d: %w", facilityID, translate(err))
	}
	return districtID, nil
}

// DeploymentInput is a posting as a form or a staged import row supplies it.
// district_id is absent by design: the placement trigger derives it.
type DeploymentInput struct {
	CadreID    int16
	LocationID int64
	FacilityID *int64
}

// startTx opens a posting. The scope is checked after the insert for the same
// reason Workers.CreateTx checks late: the authoritative district exists only
// once the trigger has derived it.
func (s *Deployments) startTx(ctx context.Context, tx pgx.Tx, sc auth.Scope, actor domain.User,
	workerID int64, in DeploymentInput, ip netip.Addr) (domain.Deployment, error) {

	const q = `
	    WITH dep AS (
	        INSERT INTO deployments (health_worker_id, cadre_id, location_id, district_id,
	                                 facility_id, created_by, updated_by)
	        VALUES ($1, $2, $3, 0, $4, $5, $5)
	        RETURNING *
	    )
	    SELECT ` + deploymentColumns + `
	    FROM dep
	    JOIN cadres cd       ON cd.id = dep.cadre_id
	    JOIN locations l     ON l.id  = dep.location_id
	    JOIN locations dl    ON dl.id = dep.district_id
	    LEFT JOIN facilities f ON f.id = dep.facility_id`

	d, err := scanDeployment(tx.QueryRow(ctx, q,
		workerID, in.CadreID, in.LocationID, in.FacilityID, actor.ID))
	if err != nil {
		return domain.Deployment{}, fmt.Errorf("deploy worker %d: %w", workerID, translate(err))
	}
	if !sc.Allows(d.DistrictID) {
		return domain.Deployment{}, fmt.Errorf("deploy worker %d in district %d: %w", workerID, d.DistrictID, domain.ErrForbidden)
	}

	if err := s.auditTx(ctx, tx, actor, ActionDeploymentStart, d, nil, auditDeployment(d), ip); err != nil {
		return domain.Deployment{}, err
	}
	return d, nil
}

// endTx closes the worker's open posting, if there is one. A nil result is not
// an error: a worker between postings has nothing to end.
func (s *Deployments) endTx(ctx context.Context, tx pgx.Tx, sc auth.Scope, actor domain.User,
	workerID int64, reason string, ip netip.Addr) (*domain.Deployment, error) {

	before, err := s.activeForTx(ctx, tx, workerID)
	if err != nil {
		return nil, err
	}
	if before == nil {
		return nil, nil
	}
	if !sc.Allows(before.DistrictID) {
		return nil, fmt.Errorf("end deployment of worker %d in district %d: %w", workerID, before.DistrictID, domain.ErrForbidden)
	}

	if _, err := tx.Exec(ctx, `
	    UPDATE deployments SET
	        ended_on = current_date, end_reason = nullif($2,''),
	        updated_by = $3, updated_at = now()
	    WHERE id = $1 AND ended_on IS NULL`,
		before.ID, reason, actor.ID); err != nil {
		return nil, fmt.Errorf("end deployment %d: %w", before.ID, translate(err))
	}

	after, err := s.byIDTx(ctx, tx, before.ID)
	if err != nil {
		return nil, err
	}
	if err := s.auditTx(ctx, tx, actor, ActionDeploymentEnd, after, auditDeployment(*before), auditDeployment(after), ip); err != nil {
		return nil, err
	}
	return &after, nil
}

// changeTx moves a worker: the open posting ends with the reason given and a
// new one opens, in the caller's transaction beside the worker's own update.
// A worker with no open posting — reactivated and not yet placed — simply
// starts.
func (s *Deployments) changeTx(ctx context.Context, tx pgx.Tx, sc auth.Scope, actor domain.User,
	workerID int64, in DeploymentInput, reason string, ip netip.Addr) (domain.Deployment, error) {

	if _, err := s.endTx(ctx, tx, sc, actor, workerID, reason, ip); err != nil {
		return domain.Deployment{}, err
	}
	return s.startTx(ctx, tx, sc, actor, workerID, in, ip)
}

// ForWorker lists every posting a worker has held, newest first — the
// transfer and promotion history that used to live only in audit JSONB.
// Scoped through the worker's district anchor, like every register read.
func (s *Deployments) ForWorker(ctx context.Context, sc auth.Scope, workerID int64) ([]domain.Deployment, error) {
	q := `SELECT ` + deploymentColumns + deploymentFrom + `
	      JOIN health_workers w ON w.id = dep.health_worker_id
	      WHERE dep.health_worker_id = $1`
	args := []any{workerID}

	if frag, extra := sc.Filter("w.district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}
	q += ` ORDER BY dep.started_on DESC, dep.id DESC`

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("deployments of worker %d: %w", workerID, translate(err))
	}
	defer rows.Close()

	var out []domain.Deployment
	for rows.Next() {
		d, err := scanDeployment(rows)
		if err != nil {
			return nil, fmt.Errorf("scan deployment: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// SetFacility re-points the supervising facility on the worker's open posting,
// as its own audited act. A nil facilityID clears the attachment.
func (s *Deployments) SetFacility(ctx context.Context, sc auth.Scope, actor domain.User,
	workerID int64, facilityID *int64, ip netip.Addr) error {

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("attach facility for worker %d: %w", workerID, err)
	}
	defer tx.Rollback(ctx)

	if err := s.setFacilityTx(ctx, tx, sc, actor, workerID, facilityID, ip); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("attach facility for worker %d: %w", workerID, err)
	}
	return nil
}

// setFacilityTx re-points the supervising facility on the open posting — an
// attachment change, not a redeployment, so the row is edited rather than
// ended. The district agreement is the trigger's to guarantee.
func (s *Deployments) setFacilityTx(ctx context.Context, tx pgx.Tx, sc auth.Scope, actor domain.User,
	workerID int64, facilityID *int64, ip netip.Addr) error {

	before, err := s.activeForTx(ctx, tx, workerID)
	if err != nil {
		return err
	}
	if before == nil {
		return fmt.Errorf("attach facility for worker %d: %w", workerID, domain.ErrNotFound)
	}
	if !sc.Allows(before.DistrictID) {
		return fmt.Errorf("attach facility for worker %d in district %d: %w", workerID, before.DistrictID, domain.ErrForbidden)
	}

	if _, err := tx.Exec(ctx, `
	    UPDATE deployments SET
	        facility_id = $2, updated_by = $3, updated_at = now()
	    WHERE id = $1 AND ended_on IS NULL`,
		before.ID, facilityID, actor.ID); err != nil {
		return fmt.Errorf("attach facility on deployment %d: %w", before.ID, translate(err))
	}

	after, err := s.byIDTx(ctx, tx, before.ID)
	if err != nil {
		return err
	}
	return s.auditTx(ctx, tx, actor, ActionDeploymentUpdate, after, auditDeployment(*before), auditDeployment(after), ip)
}

// activeForTx reads the worker's open posting, nil when there is none.
func (s *Deployments) activeForTx(ctx context.Context, tx pgx.Tx, workerID int64) (*domain.Deployment, error) {
	d, err := scanDeployment(tx.QueryRow(ctx,
		`SELECT `+deploymentColumns+deploymentFrom+`
		 WHERE dep.health_worker_id = $1 AND dep.ended_on IS NULL`, workerID))
	if err != nil {
		if translated := translate(err); translated == domain.ErrNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("active deployment for worker %d: %w", workerID, err)
	}
	return &d, nil
}

func (s *Deployments) byIDTx(ctx context.Context, tx pgx.Tx, id int64) (domain.Deployment, error) {
	d, err := scanDeployment(tx.QueryRow(ctx,
		`SELECT `+deploymentColumns+deploymentFrom+` WHERE dep.id = $1`, id))
	if err != nil {
		return domain.Deployment{}, fmt.Errorf("get deployment %d: %w", id, translate(err))
	}
	return d, nil
}

// auditTx writes the deployment mutation's audit row in the same transaction.
func (s *Deployments) auditTx(ctx context.Context, tx pgx.Tx, actor domain.User, action string,
	d domain.Deployment, before, after any, ip netip.Addr) error {

	e := ActorFrom(actor)
	e.Action = action
	e.Entity = "deployment"
	e.EntityID = &d.ID
	e.DistrictID = &d.DistrictID
	e.Before = before
	e.After = after
	e.IP = ip
	return recordOn(ctx, tx, e)
}

// auditDeployment is the JSONB shape written to audit_log for a posting.
func auditDeployment(d domain.Deployment) map[string]any {
	m := map[string]any{
		"id":               d.ID,
		"health_worker_id": d.HealthWorkerID,
		"cadre":            d.Cadre.Slug,
		"location_id":      d.LocationID,
		"district_id":      d.DistrictID,
		"facility_id":      d.FacilityID,
		"started_on":       d.StartedOn.Format(time.DateOnly),
	}
	if d.EndedOn != nil {
		m["ended_on"] = d.EndedOn.Format(time.DateOnly)
		m["end_reason"] = d.EndReason
	}
	return m
}
