package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"hwr/internal/auth"
	"hwr/internal/domain"
)

// Export is the register as a file. It is its own file rather than a method on
// Workers because it deliberately crosses every aggregate — the person, their
// posting, their details and their latest survey answers — to produce one row
// per worker, which is the one shape none of them owns.
type Export struct {
	pool *pgxpool.Pool
}

// ExportRow is one worker, flattened. "No" and "not asked" stay apart: an
// unanswered question is absent from Answers, an unrecorded grade is empty,
// because a file that spelled both the same would throw away the distinction
// the whole schema is built around.
type ExportRow struct {
	ID   int64
	Code string
	NIN  string

	FirstName    string
	LastName     string
	OtherName    string
	Sex          domain.Sex
	Cadre        string // code of the posting's cadre
	DOB          *time.Time
	DOBEstimated bool

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

	Facility string

	// The person's details the CHW survey asks for: their own phone and one
	// they can be reached on, the highest schooling, and English by skill.
	PhoneOwn       string
	PhoneAlternate string
	Education      domain.EducationLevel
	English        domain.LanguageSkill

	// Answers is the latest submission to the CHW baseline survey.
	Answers domain.Answers

	CreatedOn     time.Time
	LastUpdatedOn time.Time
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
	where, args := f.where(sc, nil)
	args = append(args, CHWBaseline)
	profile := fmt.Sprintf("(SELECT id FROM profiles WHERE code = $%d)", len(args))

	// The per-person sets are folded to one row each and joined, rather than
	// probed per row: the dashboard learned the same lesson at this size,
	// where the per-row form cost 430ms and the join 9ms.
	//
	// The placement comes from the posting the listing sees the worker
	// through (the active deployment, else the most recent), and ancestors
	// come out of locations.path with split_part joined by primary key, which
	// is what keeps a national export off a prefix scan against all 84,635
	// locations.
	q := `
	    WITH latest AS (
	        SELECT DISTINCT ON (s.health_worker_id) s.health_worker_id, s.id
	          FROM health_worker_profiles s
	         WHERE s.profile_id = ` + profile + `
	         ORDER BY s.health_worker_id, s.captured_on DESC, s.id DESC
	    ), answers AS (
	        SELECT a.health_worker_id, jsonb_object_agg(a.code, a.vals) AS answers
	          FROM (SELECT l.health_worker_id, pq.code,
	                       jsonb_agg(coalesce(o.code, r.response) ORDER BY r.id) AS vals
	                  FROM latest l
	                  JOIN health_worker_profile_responses r ON r.health_worker_profile_id = l.id
	                  JOIN profile_questions pq ON pq.id = r.profile_question_id
	                  JOIN question_pool q      ON q.id  = pq.question_id
	                  LEFT JOIN LATERAL (
	                      SELECT x->>'code' AS code FROM jsonb_array_elements(q.response_options) x
	                       WHERE (x->>'id')::int = r.response_option_id
	                  ) o ON true
	                 GROUP BY l.health_worker_id, pq.code) a
	         GROUP BY a.health_worker_id
	    ), phones AS (
	        SELECT person_id,
	               (array_agg(value ORDER BY is_primary DESC, id) FILTER (WHERE owned))[1] AS own,
	               (array_agg(value ORDER BY is_primary DESC, id) FILTER (WHERE owned IS NOT TRUE))[1] AS alternate
	          FROM person_contacts WHERE kind = 'phone' GROUP BY person_id
	    ), schooling AS (
	        SELECT person_id, max(level)::text AS level FROM person_education GROUP BY person_id
	    )
	    SELECT w.id, coalesce(w.worker_code,''), coalesce(p.nin,''), p.first_name, p.last_name,
	           coalesce(p.other_name,''), p.sex::text, coalesce(cd.code,''), p.dob, p.dob_estimated,
	           coalesce(dl.name,''), coalesce(sub.name,''), coalesce(par.name,''), coalesce(vil.name,''),
	           coalesce(l.code_path,''),
	           w.status::text, w.deactivated_at, coalesce(w.deactivation_reason,''),
	           coalesce(fac.name,''),
	           coalesce(ph.own,''), coalesce(ph.alternate,''), coalesce(sch.level,''),
	           coalesce(en.understanding_grade::text,''), coalesce(en.reading_grade::text,''),
	           coalesce(en.writing_grade::text,''),
	           coalesce(ans.answers, '{}'),
	           w.created_on, w.last_updated_on
	      FROM health_workers w
	      JOIN persons p ON p.id = w.person_id
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
	      LEFT JOIN facilities fac ON fac.id = dep.facility_id
	      LEFT JOIN phones ph ON ph.person_id = p.id
	      LEFT JOIN schooling sch ON sch.person_id = p.id
	      LEFT JOIN person_languages en ON en.person_id = p.id
	                                   AND en.language_id = (SELECT id FROM languages WHERE code = 'english')
	      LEFT JOIN answers ans ON ans.health_worker_id = w.id` + where + `
	     ORDER BY lower(p.last_name), lower(p.first_name), w.id`

	rows, err := e.pool.Query(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("export register: %w", translate(err))
	}
	defer rows.Close()

	for rows.Next() {
		var r ExportRow
		var sex, status, education, understanding, reading, writing string
		var answers map[string][]string
		if err := rows.Scan(&r.ID, &r.Code, &r.NIN, &r.FirstName, &r.LastName,
			&r.OtherName, &sex, &r.Cadre, &r.DOB, &r.DOBEstimated,
			&r.District, &r.Subcounty, &r.Parish, &r.Village, &r.LocationCode,
			&status, &r.DeactivatedAt, &r.DeactivationReason,
			&r.Facility,
			&r.PhoneOwn, &r.PhoneAlternate, &education,
			&understanding, &reading, &writing,
			&answers,
			&r.CreatedOn, &r.LastUpdatedOn); err != nil {
			return fmt.Errorf("scan export row: %w", err)
		}
		r.Sex = domain.Sex(sex)
		r.Status = domain.WorkerStatus(status)
		r.Education = domain.EducationLevel(education)
		r.English = domain.LanguageSkill{Understanding: domain.Proficiency(understanding),
			Reading: domain.Proficiency(reading), Writing: domain.Proficiency(writing)}
		r.Answers = answers

		if err := yield(r); err != nil {
			return err
		}
	}
	return rows.Err()
}
