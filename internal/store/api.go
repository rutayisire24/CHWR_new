package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"chwr/internal/auth"
	"chwr/internal/domain"
)

// API is the read-only projection the interoperability layer serves to eCHIS
// (through the user-management tool) and to the National Data Warehouse. It
// crosses the register, its profile, the hierarchy and the facility list to
// produce one enriched row per CHW — the shape the consumers are configured to
// read — and it is read-only by construction: there are no mutating methods.
//
// Every method takes a Scope, exactly like every other read in this package, so
// a national client reads the country and a district-scoped client reads its
// district and nothing else.
type API struct {
	pool *pgxpool.Pool
}

// APICHW is one CHW as the API exposes it. The identifiers and placement the
// eCHIS configuration maps, plus the supervisory relationship (decision D13)
// and the villages a CHW covers (decision D5).
type APICHW struct {
	ID        int64 // the HW-ID (decision D2)
	NIN       string
	FirstName string
	LastName  string
	Sex       string
	Cadre     string
	Status    string

	AgeYears      *int16
	AgeCapturedOn time.Time
	DateOfBirth   *time.Time // a real captured date of birth, when present

	District  string
	Subcounty string
	Parish    string
	Village   string // the CHW's own village (VHTs); empty for a CHEW
	Facility  string
	Phone     string

	ReceivedSupervision *bool
	LastSupervisedOn    *time.Time
	UpdatedAt           time.Time

	// Supervisor is the CHEW who oversees this VHT's parish (decision D5/D13);
	// empty for a CHEW or where no CHEW is placed at the parish.
	SupervisorID   *int64
	SupervisorName string

	// Villages a CHW covers: a VHT's single village, or every village under a
	// CHEW's parish (decision D5).
	Villages []string
}

// APIFilter narrows the projection. The name filters (district, facility,
// village) are the ones the eCHIS user-management tool sends; UpdatedSince and
// AfterID serve the warehouse's full and incremental extraction.
type APIFilter struct {
	Active       *bool  // nil = both statuses
	Cadre        string // "vht" | "chew" | ""
	DistrictID   *int64
	DistrictName string
	FacilityName string
	VillageName  string
	ParishID     *int64 // internal: the parish a CHEW's supervisees sit under
	HWID         *int64
	UpdatedSince *time.Time

	// Supervision is a targeted-supervision filter: "overdue" (past the
	// interval) or "never" (no supervision recorded). Overdue needs a positive
	// SupervisionIntervalDays, or it is ignored.
	Supervision             string
	SupervisionIntervalDays int

	Limit   int
	AfterID int64 // keyset cursor: return CHWs with a greater id
}

const apiSelect = `
    SELECT
        c.id, coalesce(c.nin,''), c.first_name, c.last_name,
        c.sex::text, c.cadre::text, c.status::text,
        c.age_years, c.age_captured_on, c.date_of_birth,
        d.name,
        coalesce(sub.name,''), coalesce(par.name,''), coalesce(vil.name,''),
        coalesce(fac.name,''),
        coalesce(p.phone_primary, p.phone_alternate, ''),
        p.received_supervision, p.last_supervised_on,
        greatest(c.updated_at, coalesce(p.updated_at, c.updated_at)),
        sup.id, coalesce(sup.first_name || ' ' || sup.last_name, ''),
        CASE WHEN c.cadre = 'vht'
             THEN array_remove(ARRAY[vil.name], NULL)
             ELSE coalesce((
                 SELECT array_agg(v.name ORDER BY v.name)
                   FROM locations v
                  WHERE v.level = 'village' AND v.active
                    AND v.path LIKE l.path || '%'
             ), ARRAY[]::text[])
        END`

// apiFrom resolves the placement's ancestors out of locations.path with
// split_part and joins them by primary key — the same technique the export
// uses, which keeps a national read off a prefix scan against all 84,635
// locations. The lateral join finds a VHT's supervising CHEW: the active CHEW
// placed at the VHT's parish (path position 6).
const apiFrom = `
    FROM chws c
    JOIN locations l ON l.id = c.location_id
    JOIN locations d ON d.id = c.district_id
    LEFT JOIN locations sub ON sub.id = nullif(split_part(l.path,'/',5),'')::bigint
    LEFT JOIN locations par ON par.id = nullif(split_part(l.path,'/',6),'')::bigint
    LEFT JOIN locations vil ON vil.id = nullif(split_part(l.path,'/',7),'')::bigint
    LEFT JOIN chw_profiles p ON p.chw_id = c.id
    LEFT JOIN facilities fac ON fac.id = p.facility_id
    LEFT JOIN LATERAL (
        SELECT s.id, s.first_name, s.last_name
          FROM chws s
         WHERE c.cadre = 'vht' AND s.cadre = 'chew' AND s.status = 'active'
           AND s.location_id = nullif(split_part(l.path,'/',6),'')::bigint
         LIMIT 1
    ) sup ON true`

func (f APIFilter) where(sc auth.Scope) (string, []any) {
	where := ` WHERE true`
	var args []any

	if frag, extra := sc.Filter("c.district_id", len(args)+1); frag != "" {
		where += frag
		args = append(args, extra...)
	}
	if f.HWID != nil {
		args = append(args, *f.HWID)
		where += fmt.Sprintf(" AND c.id = $%d", len(args))
	}
	if f.Active != nil {
		args = append(args, statusText(*f.Active))
		where += fmt.Sprintf(" AND c.status = $%d::chw_status", len(args))
	}
	if f.Cadre != "" {
		args = append(args, f.Cadre)
		where += fmt.Sprintf(" AND c.cadre = $%d::cadre", len(args))
	}
	if f.DistrictID != nil {
		args = append(args, *f.DistrictID)
		where += fmt.Sprintf(" AND c.district_id = $%d", len(args))
	}
	if f.DistrictName != "" {
		args = append(args, f.DistrictName)
		where += fmt.Sprintf(" AND d.name ILIKE $%d", len(args))
	}
	if f.FacilityName != "" {
		args = append(args, f.FacilityName)
		where += fmt.Sprintf(" AND fac.name ILIKE $%d", len(args))
	}
	if f.VillageName != "" {
		args = append(args, f.VillageName)
		where += fmt.Sprintf(" AND vil.name ILIKE $%d", len(args))
	}
	if f.ParishID != nil {
		args = append(args, *f.ParishID)
		where += fmt.Sprintf(" AND nullif(split_part(l.path,'/',6),'')::bigint = $%d", len(args))
	}
	if f.UpdatedSince != nil {
		args = append(args, *f.UpdatedSince)
		where += fmt.Sprintf(" AND greatest(c.updated_at, coalesce(p.updated_at, c.updated_at)) > $%d", len(args))
	}
	switch f.Supervision {
	case "never":
		where += ` AND (p.received_supervision IS NOT TRUE OR p.last_supervised_on IS NULL)`
	case "overdue":
		if f.SupervisionIntervalDays > 0 {
			args = append(args, f.SupervisionIntervalDays)
			where += fmt.Sprintf(
				" AND (p.last_supervised_on IS NULL OR p.last_supervised_on < current_date - $%d::int)", len(args))
		}
	}
	if f.AfterID > 0 {
		args = append(args, f.AfterID)
		where += fmt.Sprintf(" AND c.id > $%d", len(args))
	}
	return where, args
}

// List returns one page of the projection ordered by HW-ID, and whether a
// further page exists. Ordering by id gives the warehouse a stable full walk
// with a keyset cursor: no offset, so a record inserted mid-extraction never
// shifts a later page.
func (a *API) List(ctx context.Context, sc auth.Scope, f APIFilter) ([]APICHW, bool, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 50
	}
	where, args := f.where(sc)
	args = append(args, f.Limit+1) // one extra to learn whether another page exists
	q := apiSelect + apiFrom + where + fmt.Sprintf(" ORDER BY c.id LIMIT $%d", len(args))

	rows, err := a.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, false, fmt.Errorf("api list chws: %w", translate(err))
	}
	defer rows.Close()

	var out []APICHW
	for rows.Next() {
		r, err := scanAPICHW(rows)
		if err != nil {
			return nil, false, fmt.Errorf("api scan chw: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}

	more := len(out) > f.Limit
	if more {
		out = out[:f.Limit]
	}
	return out, more, nil
}

// Get returns one CHW by HW-ID inside the scope, or ErrNotFound.
func (a *API) Get(ctx context.Context, sc auth.Scope, hwid int64) (APICHW, error) {
	list, _, err := a.List(ctx, sc, APIFilter{HWID: &hwid, Limit: 1})
	if err != nil {
		return APICHW{}, err
	}
	if len(list) == 0 {
		return APICHW{}, domain.ErrNotFound
	}
	return list[0], nil
}

// Supervisees returns the VHTs a CHEW oversees: the VHTs whose village sits
// under the CHEW's parish (decision D5). A CHW that is not a CHEW oversees
// nobody, and an out-of-scope or unknown id is ErrNotFound.
func (a *API) Supervisees(ctx context.Context, sc auth.Scope, chewID int64) ([]APICHW, error) {
	var parishID int64
	var cadre string
	q := `SELECT c.location_id, c.cadre::text FROM chws c WHERE c.id = $1`
	args := []any{chewID}
	if frag, extra := sc.Filter("c.district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}
	if err := a.pool.QueryRow(ctx, q, args...).Scan(&parishID, &cadre); err != nil {
		return nil, fmt.Errorf("supervisees of %d: %w", chewID, translate(err))
	}
	if cadre != "chew" {
		return []APICHW{}, nil // a VHT has no supervisees
	}
	list, _, err := a.List(ctx, sc, APIFilter{ParishID: &parishID, Cadre: "vht", Limit: 1000})
	return list, err
}

func statusText(active bool) string {
	if active {
		return "active"
	}
	return "inactive"
}

func scanAPICHW(rows interface {
	Scan(dest ...any) error
}) (APICHW, error) {
	var r APICHW
	if err := rows.Scan(
		&r.ID, &r.NIN, &r.FirstName, &r.LastName,
		&r.Sex, &r.Cadre, &r.Status,
		&r.AgeYears, &r.AgeCapturedOn, &r.DateOfBirth,
		&r.District, &r.Subcounty, &r.Parish, &r.Village, &r.Facility, &r.Phone,
		&r.ReceivedSupervision, &r.LastSupervisedOn, &r.UpdatedAt,
		&r.SupervisorID, &r.SupervisorName, &r.Villages,
	); err != nil {
		return APICHW{}, err
	}
	if r.Villages == nil {
		r.Villages = []string{}
	}
	return r, nil
}
