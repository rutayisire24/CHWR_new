package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"chwr/internal/auth"
	"chwr/internal/domain"
)

// Export is the register as a file. It is its own file rather than a method on
// Workers because it deliberately crosses every aggregate — the person, their
// posting, their profile and both junctions — to produce one row per worker,
// which is the one shape none of them owns.
type Export struct {
	pool *pgxpool.Pool
}

// ExportRow is one worker, flattened. Pointers where the register distinguishes
// "no" from "not asked", because a file that spelled both as empty would throw
// away the distinction the whole schema is built around.
type ExportRow struct {
	ID  int64
	NIN string

	FirstName     string
	LastName      string
	Sex           domain.Sex
	Cadre         string // slug of the posting's cadre
	AgeYears      *int16
	AgeCapturedOn time.Time

	// The placement, spelled out. County is derived and not shown, exactly as
	// the UI leaves it out: it is mandatory in the data and meaningless to a
	// reader who did not choose it.
	District  string
	Subcounty string
	Parish    string
	Village   string
	// LocationCode is the official code path, which is what makes an exported
	// row re-importable without any name being ambiguous.
	LocationCode string

	Status             domain.WorkerStatus
	DeactivatedAt      *time.Time
	DeactivationReason string

	OwnsPhone         *bool
	PhonePrimary      string
	PhoneForReporting *bool
	PhoneAlternate    string

	Facility         string
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

	// The junction sets, already folded to `;`-separated slugs — the same
	// spelling the importer reads, so a file that comes out can go back in.
	Tools           string
	ToolsFunctional string
	Services        string
	Trained         string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Rows streams the register through yield, one row at a time, inside the scope
// and narrowed by the same Filter the listing uses. A filter that selects 20
// records on screen exports those 20: the file and the page cannot disagree,
// because they are built from the same predicate.
//
// It streams rather than returning a slice because the national register has
// no reason to exist in memory at once. There is no keyset here and no LIMIT:
// paging is for a reader who moves through a page at a time, and an export is
// the whole selection by definition.
func (e *Export) Rows(ctx context.Context, sc auth.Scope, f Filter, yield func(ExportRow) error) error {
	// Paging fields are ignored on purpose: an export of "page three" would be
	// a file nobody asked for.
	f.Limit, f.After, f.Before = 0, nil, nil
	where, args := f.where(sc)

	// The junction sets are folded to one row per worker and joined, rather
	// than probed per row: the dashboard learned the same lesson at this size,
	// where the per-row form cost 430ms and the join 9ms.
	//
	// The placement comes from the posting the listing sees the worker
	// through (the active deployment, else the most recent), and ancestors
	// come out of locations.path with split_part joined by primary key, which
	// is what keeps a national export off a prefix scan against all 84,635
	// locations.
	q := `
	    WITH tool_sets AS (
	        SELECT ct.health_worker_id,
	               string_agg(t.slug, ';' ORDER BY t.sort_order) AS held,
	               string_agg(t.slug, ';' ORDER BY t.sort_order)
	                   FILTER (WHERE ct.functional) AS working
	          FROM chw_tools ct JOIN tools t ON t.id = ct.tool_id
	         GROUP BY ct.health_worker_id
	    ), domain_sets AS (
	        SELECT csd.health_worker_id,
	               string_agg(sd.slug, ';' ORDER BY sd.sort_order)
	                   FILTER (WHERE csd.provides) AS provides,
	               string_agg(sd.slug, ';' ORDER BY sd.sort_order)
	                   FILTER (WHERE csd.trained) AS trained
	          FROM chw_service_domains csd JOIN service_domains sd ON sd.id = csd.domain_id
	         GROUP BY csd.health_worker_id
	    )
	    SELECT w.id, coalesce(w.nin,''), w.first_name, w.last_name,
	           w.sex::text, coalesce(cd.slug,''), w.age_years, w.age_captured_on,
	           coalesce(dl.name,''), coalesce(sub.name,''), coalesce(par.name,''), coalesce(vil.name,''),
	           coalesce(l.code_path,''),
	           w.status::text, w.deactivated_at, coalesce(w.deactivation_reason,''),
	           p.owns_phone, coalesce(p.phone_primary,''), p.phone_for_reporting,
	           coalesce(p.phone_alternate,''),
	           coalesce(fac.name,''), p.service_start_year, p.households_served,
	           coalesce(p.education::text,''),
	           p.english_speak, p.english_read, p.english_write,
	           coalesce(p.other_languages_raw,''),
	           p.receives_incentive, coalesce(p.incentive_frequency::text,''),
	           p.incentive_amount_ugx,
	           p.received_supervision, p.last_supervised_on,
	           coalesce(ts.held,''), coalesce(ts.working,''),
	           coalesce(ds.provides,''), coalesce(ds.trained,''),
	           w.created_at, w.updated_at
	      FROM health_workers w
	      LEFT JOIN LATERAL (
	          SELECT x.* FROM deployments x
	           WHERE x.health_worker_id = w.id
	           ORDER BY (x.ended_on IS NULL) DESC, x.started_on DESC, x.id DESC
	           LIMIT 1
	      ) dep ON true
	      LEFT JOIN cadres cd ON cd.id = dep.cadre_id
	      LEFT JOIN locations l ON l.id = dep.location_id
	      LEFT JOIN locations dl ON dl.id = dep.district_id
	      LEFT JOIN locations sub ON sub.id = nullif(split_part(l.path,'/',5),'')::bigint
	      LEFT JOIN locations par ON par.id = nullif(split_part(l.path,'/',6),'')::bigint
	      LEFT JOIN locations vil ON vil.id = nullif(split_part(l.path,'/',7),'')::bigint
	      LEFT JOIN chw_profiles p ON p.health_worker_id = w.id
	      LEFT JOIN facilities fac ON fac.id = dep.facility_id
	      LEFT JOIN tool_sets ts ON ts.health_worker_id = w.id
	      LEFT JOIN domain_sets ds ON ds.health_worker_id = w.id` + where + `
	     ORDER BY lower(w.last_name), lower(w.first_name), w.id`

	rows, err := e.pool.Query(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("export register: %w", translate(err))
	}
	defer rows.Close()

	for rows.Next() {
		var r ExportRow
		var sex, cadre, status, education, frequency string
		if err := rows.Scan(&r.ID, &r.NIN, &r.FirstName, &r.LastName,
			&sex, &cadre, &r.AgeYears, &r.AgeCapturedOn,
			&r.District, &r.Subcounty, &r.Parish, &r.Village, &r.LocationCode,
			&status, &r.DeactivatedAt, &r.DeactivationReason,
			&r.OwnsPhone, &r.PhonePrimary, &r.PhoneForReporting, &r.PhoneAlternate,
			&r.Facility, &r.ServiceStartYear, &r.HouseholdsServed, &education,
			&r.EnglishSpeak, &r.EnglishRead, &r.EnglishWrite, &r.OtherLanguagesRaw,
			&r.ReceivesIncentive, &frequency, &r.IncentiveAmountUGX,
			&r.ReceivedSupervision, &r.LastSupervisedOn,
			&r.Tools, &r.ToolsFunctional, &r.Services, &r.Trained,
			&r.CreatedAt, &r.UpdatedAt); err != nil {
			return fmt.Errorf("scan export row: %w", err)
		}
		r.Sex = domain.Sex(sex)
		r.Cadre = cadre
		r.Status = domain.WorkerStatus(status)
		r.Education = domain.EducationLevel(education)
		r.IncentiveFrequency = domain.IncentiveFrequency(frequency)

		if err := yield(r); err != nil {
			return err
		}
	}
	return rows.Err()
}
