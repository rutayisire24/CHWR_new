package store

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"hwr/internal/auth"
	"hwr/internal/domain"
)

// Activities records the dated events against a posting (0005): the services
// a worker reports having given, and the tools handed out in a district. The
// schema decides what applies to a cadre on a date and refuses the rest; the
// reads here offer only what would be accepted, so a form never presents a
// choice that will be refused.
type Activities struct {
	pool *pgxpool.Pool
}

// Tools is the tool vocabulary. Vocabulary reads take no Scope.
func (s *Activities) Tools(ctx context.Context) ([]domain.Tool, error) {
	return vocab[domain.Tool](ctx, s.pool, `SELECT id, code, label FROM tools WHERE active ORDER BY sort_order, id`)
}

// Services is the service vocabulary.
func (s *Activities) Services(ctx context.Context) ([]domain.Service, error) {
	return vocab[domain.Service](ctx, s.pool, `SELECT id, code, label FROM services WHERE active ORDER BY sort_order, id`)
}

// ServicesFor is the services a cadre may report.
func (s *Activities) ServicesFor(ctx context.Context, cadreID int16) ([]domain.Service, error) {
	return vocab[domain.Service](ctx, s.pool, `
	    SELECT s.id, s.code, s.label FROM services s
	      JOIN service_applicable_cadre a ON a.service_id = s.id
	     WHERE a.cadre_id = $1 AND s.active ORDER BY s.sort_order, s.id`, cadreID)
}

// ToolsFor is the tools a cadre may be given.
func (s *Activities) ToolsFor(ctx context.Context, cadreID int16) ([]domain.Tool, error) {
	return vocab[domain.Tool](ctx, s.pool, `
	    SELECT t.id, t.code, t.label FROM tools t
	      JOIN tool_applicable_cadre a ON a.tool_id = t.id
	     WHERE a.cadre_id = $1 AND t.active ORDER BY t.sort_order, t.id`, cadreID)
}

// vocab reads an (id, code, label) list into Tool or Service.
func vocab[T domain.Tool | domain.Service](ctx context.Context, q querier, sql string, args ...any) ([]T, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("read vocabulary: %w", translate(err))
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (T, error) {
		var id int16
		var code, label string
		err := r.Scan(&id, &code, &label)
		return T{ID: id, Code: code, Label: label}, err
	})
}

// ------------------------------------------------------------ service updates

// ReportServices records the services a worker gave on a date. A second report
// for the same date replaces the first, inside the same audited transaction:
// a report is the answer for that date, not an addition to it.
func (s *Activities) ReportServices(ctx context.Context, sc auth.Scope, actor domain.User, workerID int64,
	on time.Time, serviceIDs []int16, ip netip.Addr) (domain.ServiceUpdate, error) {

	tx, err := begin(ctx, s.pool, actor)
	if err != nil {
		return domain.ServiceUpdate{}, fmt.Errorf("report services for worker %d: %w", workerID, err)
	}
	defer tx.Rollback(ctx)

	if _, err := workerDistrict(ctx, tx, sc, workerID); err != nil {
		return domain.ServiceUpdate{}, err
	}

	var before any
	var updateID int64
	err = tx.QueryRow(ctx, `SELECT id FROM health_worker_service_updates
	                         WHERE health_worker_id = $1 AND reporting_date = $2`, workerID, on).Scan(&updateID)
	switch translate(err) {
	case nil:
		prev, err := serviceUpdateOn(ctx, tx, updateID)
		if err != nil {
			return domain.ServiceUpdate{}, err
		}
		before = serviceUpdateSnapshot(prev)
		if _, err := tx.Exec(ctx, `DELETE FROM health_worker_service_update_details
		                            WHERE health_worker_service_update_id = $1`, updateID); err != nil {
			return domain.ServiceUpdate{}, fmt.Errorf("replace service update %d: %w", updateID, translate(err))
		}
	case domain.ErrNotFound:
		// The district and the posting are derived by trigger from the date.
		if err := tx.QueryRow(ctx, `
		    INSERT INTO health_worker_service_updates (health_worker_id, reporting_date, deployment_id, district_id)
		    VALUES ($1, $2, 0, 0) RETURNING id`, workerID, on).Scan(&updateID); err != nil {
			return domain.ServiceUpdate{}, fmt.Errorf("report services for worker %d: %w", workerID, translate(err))
		}
	default:
		return domain.ServiceUpdate{}, fmt.Errorf("report services for worker %d: %w", workerID, translate(err))
	}

	if len(serviceIDs) > 0 {
		if _, err := tx.Exec(ctx, `
		    INSERT INTO health_worker_service_update_details (health_worker_service_update_id, service_id)
		    SELECT $1, unnest($2::smallint[])`, updateID, serviceIDs); err != nil {
			return domain.ServiceUpdate{}, fmt.Errorf("report services for worker %d: %w", workerID, translate(err))
		}
	}

	after, err := serviceUpdateOn(ctx, tx, updateID)
	if err != nil {
		return domain.ServiceUpdate{}, err
	}
	e := ActorFrom(actor)
	e.Action = ActionServiceReport
	e.Entity = "health_worker"
	e.EntityID = &workerID
	e.DistrictID = &after.DistrictID
	e.Before = before
	e.After = serviceUpdateSnapshot(after)
	e.IP = ip
	if err := recordOn(ctx, tx, e); err != nil {
		return domain.ServiceUpdate{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ServiceUpdate{}, fmt.Errorf("report services for worker %d: %w", workerID, translate(err))
	}
	return after, nil
}

// ServiceUpdates lists a worker's reports, newest first.
func (s *Activities) ServiceUpdates(ctx context.Context, sc auth.Scope, workerID int64) ([]domain.ServiceUpdate, error) {
	if _, err := workerDistrict(ctx, s.pool, sc, workerID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT id FROM health_worker_service_updates
	                                 WHERE health_worker_id = $1 ORDER BY reporting_date DESC`, workerID)
	if err != nil {
		return nil, fmt.Errorf("service updates of worker %d: %w", workerID, translate(err))
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	out := make([]domain.ServiceUpdate, 0, len(ids))
	for _, id := range ids {
		u, err := serviceUpdateOn(ctx, s.pool, id)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}

func serviceUpdateOn(ctx context.Context, q querier, id int64) (domain.ServiceUpdate, error) {
	var u domain.ServiceUpdate
	if err := q.QueryRow(ctx, `
	    SELECT u.id, u.health_worker_id, u.reporting_date, u.district_id, cd.label, u.created_by, u.created_on
	      FROM health_worker_service_updates u
	      JOIN deployments d ON d.id = u.deployment_id
	      JOIN cadres cd     ON cd.id = d.cadre_id
	     WHERE u.id = $1`, id).
		Scan(&u.ID, &u.HealthWorkerID, &u.ReportingDate, &u.DistrictID, &u.CadreLabel, &u.CreatedBy, &u.CreatedOn); err != nil {
		return domain.ServiceUpdate{}, fmt.Errorf("service update %d: %w", id, translate(err))
	}
	var err error
	u.Services, err = vocab[domain.Service](ctx, q, `
	    SELECT s.id, s.code, s.label FROM health_worker_service_update_details x
	      JOIN services s ON s.id = x.service_id
	     WHERE x.health_worker_service_update_id = $1 ORDER BY s.sort_order, s.id`, id)
	return u, err
}

func serviceUpdateSnapshot(u domain.ServiceUpdate) map[string]any {
	codes := make([]string, len(u.Services))
	for i, sv := range u.Services {
		codes[i] = sv.Code
	}
	return map[string]any{
		"service_update": u.ID,
		"reporting_date": u.ReportingDate.Format(time.DateOnly),
		"services":       codes,
	}
}

// --------------------------------------------------------- tool distributions

// DistributionItem is one line of a hand-out as a form supplies it.
type DistributionItem struct {
	WorkerID int64
	ToolID   int16
	Quantity int16
}

// Distribute records a hand-out in a district and every tool given in it, with
// its audit row, in one transaction. A recipient posted elsewhere on that
// date, or a tool their cadre does not carry, refuses the whole hand-out: a
// distribution is recorded as it happened or not at all.
func (s *Activities) Distribute(ctx context.Context, sc auth.Scope, actor domain.User, districtID int64,
	on time.Time, note string, items []DistributionItem, ip netip.Addr) (domain.ToolDistribution, error) {

	if !sc.Allows(districtID) {
		return domain.ToolDistribution{}, fmt.Errorf("distribute in district %d: %w", districtID, domain.ErrForbidden)
	}
	tx, err := begin(ctx, s.pool, actor)
	if err != nil {
		return domain.ToolDistribution{}, fmt.Errorf("distribute in district %d: %w", districtID, err)
	}
	defer tx.Rollback(ctx)

	var id int64
	if err := tx.QueryRow(ctx, `
	    INSERT INTO health_worker_tool_distributions (district_id, reporting_date, note)
	    VALUES ($1, $2, nullif($3,'')) RETURNING id`, districtID, on, note).Scan(&id); err != nil {
		return domain.ToolDistribution{}, fmt.Errorf("distribute in district %d: %w", districtID, translate(err))
	}
	workers := make([]int64, len(items))
	tools := make([]int16, len(items))
	quantities := make([]int16, len(items))
	for i, it := range items {
		workers[i], tools[i], quantities[i] = it.WorkerID, it.ToolID, max(it.Quantity, 1)
	}
	if _, err := tx.Exec(ctx, `
	    INSERT INTO health_worker_tool_distribution_details
	           (health_worker_tool_distribution_id, health_worker_id, tool_id, quantity)
	    SELECT $1, w, t, q FROM unnest($2::bigint[], $3::smallint[], $4::smallint[]) AS x(w, t, q)`,
		id, workers, tools, quantities); err != nil {
		return domain.ToolDistribution{}, fmt.Errorf("distribute in district %d: %w", districtID, translate(err))
	}

	d, err := distributionOn(ctx, tx, id)
	if err != nil {
		return domain.ToolDistribution{}, err
	}
	e := ActorFrom(actor)
	e.Action = ActionToolDistribution
	e.Entity = "tool_distribution"
	e.EntityID = &d.ID
	e.DistrictID = &d.DistrictID
	e.After = distributionSnapshot(d)
	e.IP = ip
	if err := recordOn(ctx, tx, e); err != nil {
		return domain.ToolDistribution{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ToolDistribution{}, fmt.Errorf("distribute in district %d: %w", districtID, translate(err))
	}
	return d, nil
}

// Distributions lists hand-outs inside the scope, newest first, summarised.
func (s *Activities) Distributions(ctx context.Context, sc auth.Scope, limit int) ([]domain.ToolDistribution, error) {
	q := `
	    SELECT d.id, d.district_id, l.name, d.reporting_date, coalesce(d.note,''),
	           count(DISTINCT x.health_worker_id), coalesce(sum(x.quantity), 0), d.created_by, d.created_on
	      FROM health_worker_tool_distributions d
	      JOIN locations l ON l.id = d.district_id
	      LEFT JOIN health_worker_tool_distribution_details x ON x.health_worker_tool_distribution_id = d.id
	     WHERE true`
	var args []any
	if frag, extra := sc.Filter("d.district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}
	args = append(args, limit)
	q += fmt.Sprintf(` GROUP BY d.id, l.name ORDER BY d.reporting_date DESC, d.id DESC LIMIT $%d`, len(args))

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list distributions: %w", translate(err))
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.ToolDistribution, error) {
		var d domain.ToolDistribution
		err := r.Scan(&d.ID, &d.DistrictID, &d.DistrictName, &d.ReportingDate, &d.Note,
			&d.Recipients, &d.Units, &d.CreatedBy, &d.CreatedOn)
		return d, err
	})
}

// Distribution reads one hand-out inside the scope, with every item.
func (s *Activities) Distribution(ctx context.Context, sc auth.Scope, id int64) (domain.ToolDistribution, error) {
	d, err := distributionOn(ctx, s.pool, id)
	if err != nil {
		return domain.ToolDistribution{}, err
	}
	if !sc.Allows(d.DistrictID) {
		return domain.ToolDistribution{}, fmt.Errorf("distribution %d: %w", id, domain.ErrNotFound)
	}
	return d, nil
}

func distributionOn(ctx context.Context, q querier, id int64) (domain.ToolDistribution, error) {
	var d domain.ToolDistribution
	if err := q.QueryRow(ctx, `
	    SELECT d.id, d.district_id, l.name, d.reporting_date, coalesce(d.note,''), d.created_by, d.created_on
	      FROM health_worker_tool_distributions d JOIN locations l ON l.id = d.district_id
	     WHERE d.id = $1`, id).
		Scan(&d.ID, &d.DistrictID, &d.DistrictName, &d.ReportingDate, &d.Note, &d.CreatedBy, &d.CreatedOn); err != nil {
		return domain.ToolDistribution{}, fmt.Errorf("distribution %d: %w", id, translate(err))
	}
	rows, err := q.Query(ctx, `
	    SELECT x.health_worker_id, coalesce(w.worker_code,''), p.first_name || ' ' || p.last_name,
	           t.id, t.code, t.label, x.quantity
	      FROM health_worker_tool_distribution_details x
	      JOIN health_workers w ON w.id = x.health_worker_id
	      JOIN persons p        ON p.id = w.person_id
	      JOIN tools t          ON t.id = x.tool_id
	     WHERE x.health_worker_tool_distribution_id = $1
	     ORDER BY lower(p.last_name), lower(p.first_name), w.id, t.sort_order`, id)
	if err != nil {
		return domain.ToolDistribution{}, fmt.Errorf("distribution %d items: %w", id, translate(err))
	}
	d.Items, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.DistributionItem, error) {
		var it domain.DistributionItem
		err := r.Scan(&it.HealthWorkerID, &it.WorkerCode, &it.WorkerName,
			&it.Tool.ID, &it.Tool.Code, &it.Tool.Label, &it.Quantity)
		return it, err
	})
	seen := map[int64]bool{}
	for _, it := range d.Items {
		seen[it.HealthWorkerID] = true
		d.Units += int64(it.Quantity)
	}
	d.Recipients = int64(len(seen))
	return d, err
}

func distributionSnapshot(d domain.ToolDistribution) map[string]any {
	items := make([]map[string]any, len(d.Items))
	for i, it := range d.Items {
		items[i] = map[string]any{"health_worker_id": it.HealthWorkerID, "tool": it.Tool.Code, "quantity": it.Quantity}
	}
	return map[string]any{
		"distribution":   d.ID,
		"district_id":    d.DistrictID,
		"reporting_date": d.ReportingDate.Format(time.DateOnly),
		"note":           d.Note,
		"items":          items,
	}
}

// ToolsReceived lists what a worker has been given, newest first.
func (s *Activities) ToolsReceived(ctx context.Context, sc auth.Scope, workerID int64) ([]domain.ToolReceipt, error) {
	if _, err := workerDistrict(ctx, s.pool, sc, workerID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
	    SELECT d.id, d.reporting_date, t.id, t.code, t.label, x.quantity
	      FROM health_worker_tool_distribution_details x
	      JOIN health_worker_tool_distributions d ON d.id = x.health_worker_tool_distribution_id
	      JOIN tools t ON t.id = x.tool_id
	     WHERE x.health_worker_id = $1
	     ORDER BY d.reporting_date DESC, t.sort_order`, workerID)
	if err != nil {
		return nil, fmt.Errorf("tools received by worker %d: %w", workerID, translate(err))
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.ToolReceipt, error) {
		var t domain.ToolReceipt
		err := r.Scan(&t.DistributionID, &t.ReportingDate, &t.Tool.ID, &t.Tool.Code, &t.Tool.Label, &t.Quantity)
		return t, err
	})
}
