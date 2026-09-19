package store

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"chwr/internal/auth"
	"chwr/internal/domain"
)

// Profiles reads and writes the Community Health Workers category's survey
// attributes: `chw_profiles` and the two junctions. The profile and its
// junctions are one editable thing, so they are written in one transaction
// along with their audit row.
type Profiles struct {
	pool *pgxpool.Pool
}

const profileColumns = `
    p.health_worker_id IS NOT NULL, p.owns_phone, coalesce(p.phone_primary,''), p.phone_for_reporting,
    coalesce(p.phone_alternate,''),
    p.service_start_year, p.households_served, coalesce(p.education::text,''),
    p.english_speak, p.english_read, p.english_write, coalesce(p.other_languages_raw,''),
    p.receives_incentive, coalesce(p.incentive_frequency::text,''), p.incentive_amount_ugx,
    p.received_supervision, p.last_supervised_on, p.updated_by, p.updated_at`

// scanProfile reads a projection that begins with the worker id and a flag for
// whether a profile row exists at all. The flag is projected rather than
// inferred from a NULL scan: a LEFT JOIN that found nothing and a column that
// is genuinely empty must not be told apart by parsing an error message.
func scanProfile(row pgx.Row) (domain.Profile, error) {
	var p domain.Profile
	var education, frequency string
	// updated_at is NOT NULL on the table, but the LEFT JOIN for a worker with
	// no profile yields NULL for it like every other projected column.
	var updatedAt *time.Time
	err := row.Scan(&p.HealthWorkerID, &p.Exists, &p.OwnsPhone, &p.PhonePrimary, &p.PhoneForReporting,
		&p.PhoneAlternate,
		&p.ServiceStartYear, &p.HouseholdsServed, &education,
		&p.EnglishSpeak, &p.EnglishRead, &p.EnglishWrite, &p.OtherLanguagesRaw,
		&p.ReceivesIncentive, &frequency, &p.IncentiveAmountUGX,
		&p.ReceivedSupervision, &p.LastSupervisedOn, &p.UpdatedBy, &updatedAt)
	if err != nil {
		return domain.Profile{}, err
	}
	p.Education = domain.EducationLevel(education)
	p.IncentiveFrequency = domain.IncentiveFrequency(frequency)
	if updatedAt != nil {
		p.UpdatedAt = *updatedAt
	}
	return p, nil
}

// Get returns the profile for a worker inside the scope. A worker with no
// profile row yet is not an error: the answer is an empty profile, because
// "nothing recorded" is the normal state of an imported record.
//
// The scope is applied to the worker, not to the profile: chw_profiles has no
// district column of its own.
func (s *Profiles) Get(ctx context.Context, sc auth.Scope, workerID int64) (domain.Profile, error) {
	q := `SELECT w.id, ` + profileColumns + `
	      FROM health_workers w
	      LEFT JOIN chw_profiles p ON p.health_worker_id = w.id
	      WHERE w.id = $1`
	args := []any{workerID}

	if frag, extra := sc.Filter("w.district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}

	// ErrNotFound here means the worker is unknown or out of scope, never that
	// the profile is empty.
	p, err := scanProfile(s.pool.QueryRow(ctx, q, args...))
	if err != nil {
		return domain.Profile{}, fmt.Errorf("get profile for worker %d: %w", workerID, translate(err))
	}
	return p, nil
}

// Tools returns the tool vocabulary with each tool's state for one worker.
// Tools the worker does not hold come back with Held false rather than being
// absent, so the form is a checklist of the whole vocabulary.
func (s *Profiles) Tools(ctx context.Context, workerID int64) ([]domain.CHWTool, error) {
	const q = `
	    SELECT t.id, t.slug, t.label, ct.health_worker_id IS NOT NULL, ct.functional
	      FROM tools t
	      LEFT JOIN chw_tools ct ON ct.tool_id = t.id AND ct.health_worker_id = $1
	     WHERE t.active
	     ORDER BY t.sort_order`

	rows, err := s.pool.Query(ctx, q, workerID)
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", translate(err))
	}
	defer rows.Close()

	var out []domain.CHWTool
	for rows.Next() {
		var t domain.CHWTool
		if err := rows.Scan(&t.ID, &t.Slug, &t.Label, &t.Held, &t.Functional); err != nil {
			return nil, fmt.Errorf("scan tool: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ServiceDomains returns the twelve-value vocabulary with each domain's state
// for one worker.
func (s *Profiles) ServiceDomains(ctx context.Context, workerID int64) ([]domain.CHWServiceDomain, error) {
	const q = `
	    SELECT d.id, d.slug, d.label,
	           coalesce(cd.provides, false), coalesce(cd.trained, false)
	      FROM service_domains d
	      LEFT JOIN chw_service_domains cd ON cd.domain_id = d.id AND cd.health_worker_id = $1
	     WHERE d.active
	     ORDER BY d.sort_order`

	rows, err := s.pool.Query(ctx, q, workerID)
	if err != nil {
		return nil, fmt.Errorf("list service domains: %w", translate(err))
	}
	defer rows.Close()

	var out []domain.CHWServiceDomain
	for rows.Next() {
		var d domain.CHWServiceDomain
		if err := rows.Scan(&d.ID, &d.Slug, &d.Label, &d.Provides, &d.Trained); err != nil {
			return nil, fmt.Errorf("scan service domain: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ProfileInput is the profile as a form supplies it, plus the two junction
// selections. They travel together because they are one screen and one save.
type ProfileInput struct {
	OwnsPhone         *bool
	PhonePrimary      string
	PhoneForReporting *bool
	PhoneAlternate    string

	ServiceStartYear *int16
	HouseholdsServed *int32
	Education        domain.EducationLevel

	EnglishSpeak      *bool
	EnglishRead       *bool
	EnglishWrite      *bool
	OtherLanguagesRaw string

	ReceivesIncentive  *bool
	IncentiveFrequency domain.IncentiveFrequency
	IncentiveAmountUGX *int32

	ReceivedSupervision *bool
	LastSupervisedOn    *time.Time

	// Tools held, with their condition where it was asked.
	Tools []ToolInput
	// Service domains provided, with training where it applies.
	Domains []DomainInput
}

// ToolInput is one held tool. Tools the worker does not hold are simply absent.
type ToolInput struct {
	ToolID     int16
	Functional *bool
}

// DomainInput is one provided service domain. Domains that are neither
// provided nor trained are absent.
type DomainInput struct {
	DomainID int16
	Provides bool
	Trained  bool
}

// Save upserts the profile and replaces both junction sets, with the audit row,
// in one transaction. The junctions are replaced rather than diffed: they are
// the answer to a multi-select, so the submitted set *is* the new state, and a
// diff would only add a way for the two to disagree.
func (s *Profiles) Save(ctx context.Context, sc auth.Scope, actor domain.User, workerID int64,
	in ProfileInput, ip netip.Addr) (domain.Profile, error) {

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Profile{}, fmt.Errorf("save profile for worker %d: %w", workerID, err)
	}
	defer tx.Rollback(ctx)

	profile, err := s.SaveTx(ctx, tx, sc, actor, workerID, in, ip)
	if err != nil {
		return domain.Profile{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Profile{}, fmt.Errorf("save profile for worker %d: %w", workerID, err)
	}
	return profile, nil
}

// SaveTx writes the profile into a caller's transaction, the Tx half of the
// pair Workers.CreateTx and Audit.RecordTx already establish.
//
// The bulk importer uses it so that a worker, their deployment, their profile,
// every audit row and the staged row's mark commit together. A profile written
// in a second transaction could be lost while the worker it describes survived,
// which is the half-loaded record the quarantine invariant exists to prevent —
// one row at a time rather than one file at a time, but the same failure.
func (s *Profiles) SaveTx(ctx context.Context, tx pgx.Tx, sc auth.Scope, actor domain.User,
	workerID int64, in ProfileInput, ip netip.Addr) (domain.Profile, error) {

	// Confirm the worker is inside the scope before writing anything hanging
	// off them: chw_profiles has no district of its own to filter on.
	districtID, err := s.workerDistrictTx(ctx, tx, sc, workerID)
	if err != nil {
		return domain.Profile{}, err
	}

	before, err := s.snapshot(ctx, tx, workerID)
	if err != nil {
		return domain.Profile{}, err
	}

	const upsert = `
	    INSERT INTO chw_profiles (health_worker_id, owns_phone, phone_primary, phone_for_reporting,
	        phone_alternate, service_start_year, households_served, education,
	        english_speak, english_read, english_write, other_languages_raw,
	        receives_incentive, incentive_frequency, incentive_amount_ugx,
	        received_supervision, last_supervised_on, updated_by, updated_at)
	    VALUES ($1, $2, nullif($3,''), $4, nullif($5,''), $6, $7, nullif($8,'')::education_level,
	            $9, $10, $11, nullif($12,''), $13, nullif($14,'')::incentive_frequency, $15,
	            $16, $17, $18, now())
	    ON CONFLICT (health_worker_id) DO UPDATE SET
	        owns_phone = excluded.owns_phone,
	        phone_primary = excluded.phone_primary,
	        phone_for_reporting = excluded.phone_for_reporting,
	        phone_alternate = excluded.phone_alternate,
	        service_start_year = excluded.service_start_year,
	        households_served = excluded.households_served,
	        education = excluded.education,
	        english_speak = excluded.english_speak,
	        english_read = excluded.english_read,
	        english_write = excluded.english_write,
	        other_languages_raw = excluded.other_languages_raw,
	        receives_incentive = excluded.receives_incentive,
	        incentive_frequency = excluded.incentive_frequency,
	        incentive_amount_ugx = excluded.incentive_amount_ugx,
	        received_supervision = excluded.received_supervision,
	        last_supervised_on = excluded.last_supervised_on,
	        updated_by = excluded.updated_by,
	        updated_at = now()`

	if _, err := tx.Exec(ctx, upsert, workerID, in.OwnsPhone, in.PhonePrimary, in.PhoneForReporting,
		in.PhoneAlternate, in.ServiceStartYear, in.HouseholdsServed, string(in.Education),
		in.EnglishSpeak, in.EnglishRead, in.EnglishWrite, in.OtherLanguagesRaw,
		in.ReceivesIncentive, string(in.IncentiveFrequency), in.IncentiveAmountUGX,
		in.ReceivedSupervision, in.LastSupervisedOn, actor.ID); err != nil {
		return domain.Profile{}, fmt.Errorf("save profile for worker %d: %w", workerID, translate(err))
	}

	if _, err := tx.Exec(ctx, `DELETE FROM chw_tools WHERE health_worker_id = $1`, workerID); err != nil {
		return domain.Profile{}, fmt.Errorf("clear tools for worker %d: %w", workerID, err)
	}
	for _, t := range in.Tools {
		if _, err := tx.Exec(ctx,
			`INSERT INTO chw_tools (health_worker_id, tool_id, functional) VALUES ($1,$2,$3)`,
			workerID, t.ToolID, t.Functional); err != nil {
			return domain.Profile{}, fmt.Errorf("save tool %d for worker %d: %w", t.ToolID, workerID, translate(err))
		}
	}

	if _, err := tx.Exec(ctx, `DELETE FROM chw_service_domains WHERE health_worker_id = $1`, workerID); err != nil {
		return domain.Profile{}, fmt.Errorf("clear service domains for worker %d: %w", workerID, err)
	}
	for _, d := range in.Domains {
		if _, err := tx.Exec(ctx,
			`INSERT INTO chw_service_domains (health_worker_id, domain_id, provides, trained) VALUES ($1,$2,$3,$4)`,
			workerID, d.DomainID, d.Provides, d.Trained); err != nil {
			return domain.Profile{}, fmt.Errorf("save service domain %d for worker %d: %w", d.DomainID, workerID, translate(err))
		}
	}

	after, err := s.snapshot(ctx, tx, workerID)
	if err != nil {
		return domain.Profile{}, err
	}

	e := ActorFrom(actor)
	e.Action = ActionProfileUpdate
	e.Entity = "health_worker"
	e.EntityID = &workerID
	e.DistrictID = &districtID
	e.Before = before
	e.After = after
	e.IP = ip
	if err := recordOn(ctx, tx, e); err != nil {
		return domain.Profile{}, err
	}

	return s.getTx(ctx, tx, workerID)
}

// workerDistrictTx confirms the worker exists inside the scope and returns
// their district anchor, for the audit row.
func (s *Profiles) workerDistrictTx(ctx context.Context, tx pgx.Tx, sc auth.Scope, workerID int64) (int64, error) {
	q := `SELECT w.district_id FROM health_workers w WHERE w.id = $1`
	args := []any{workerID}
	if frag, extra := sc.Filter("w.district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}

	var districtID *int64
	if err := tx.QueryRow(ctx, q, args...).Scan(&districtID); err != nil {
		return 0, fmt.Errorf("worker %d: %w", workerID, translate(err))
	}
	if districtID == nil {
		return 0, fmt.Errorf("worker %d has no deployment yet: %w", workerID, domain.ErrNotFound)
	}
	return *districtID, nil
}

func (s *Profiles) getTx(ctx context.Context, tx pgx.Tx, workerID int64) (domain.Profile, error) {
	const q = `SELECT p.health_worker_id, ` + profileColumns + `
	           FROM chw_profiles p
	           WHERE p.health_worker_id = $1`
	p, err := scanProfile(tx.QueryRow(ctx, q, workerID))
	if err != nil {
		return domain.Profile{}, fmt.Errorf("read back profile for worker %d: %w", workerID, translate(err))
	}
	return p, nil
}

// snapshot is the JSONB written to audit_log. audit_log doubles as the
// register's change history, so the whole profile and both junction sets go
// in: a partial snapshot would make the history unreconstructable.
func (s *Profiles) snapshot(ctx context.Context, tx pgx.Tx, workerID int64) (map[string]any, error) {
	const q = `
	    SELECT to_jsonb(p) - 'updated_at' - 'updated_by'
	      FROM chw_profiles p WHERE p.health_worker_id = $1`

	out := map[string]any{}
	var profile map[string]any
	if err := tx.QueryRow(ctx, q, workerID).Scan(&profile); err != nil && !errors.Is(translate(err), domain.ErrNotFound) {
		return nil, fmt.Errorf("snapshot profile for worker %d: %w", workerID, err)
	}
	out["profile"] = profile

	tools := map[string]any{}
	rows, err := tx.Query(ctx, `
	    SELECT t.slug, ct.functional FROM chw_tools ct
	      JOIN tools t ON t.id = ct.tool_id WHERE ct.health_worker_id = $1 ORDER BY t.sort_order`, workerID)
	if err != nil {
		return nil, fmt.Errorf("snapshot tools for worker %d: %w", workerID, err)
	}
	for rows.Next() {
		var slug string
		var functional *bool
		if err := rows.Scan(&slug, &functional); err != nil {
			rows.Close()
			return nil, err
		}
		tools[slug] = functional
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out["tools"] = tools

	domains := map[string]any{}
	rows, err = tx.Query(ctx, `
	    SELECT d.slug, cd.provides, cd.trained FROM chw_service_domains cd
	      JOIN service_domains d ON d.id = cd.domain_id WHERE cd.health_worker_id = $1 ORDER BY d.sort_order`, workerID)
	if err != nil {
		return nil, fmt.Errorf("snapshot service domains for worker %d: %w", workerID, err)
	}
	for rows.Next() {
		var slug string
		var provides, trained bool
		if err := rows.Scan(&slug, &provides, &trained); err != nil {
			rows.Close()
			return nil, err
		}
		domains[slug] = map[string]bool{"provides": provides, "trained": trained}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out["service_domains"] = domains

	return out, nil
}
