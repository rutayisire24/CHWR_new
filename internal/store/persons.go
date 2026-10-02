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

// Persons reads and writes what is recorded about a person beyond the persons
// row: contacts, next of kin, documents, education, courses, training, work
// history and languages (0003).
//
// None of those tables has a district. Every method is reached through a
// health worker id, scoped like any worker read, and the person is the one
// that worker is; a district user can touch the details of their own
// district's workers and nobody else's. Every write runs in a transaction with
// its audit row, entity health_worker, so the worker's history shows it.
type Persons struct {
	pool *pgxpool.Pool
}

// Detail names one satellite table. It is the only way a table name reaches
// SQL in this file, and every value is a constant below.
type Detail string

const (
	DetailContact     Detail = "contact"
	DetailKin         Detail = "kin"
	DetailDocument    Detail = "document"
	DetailEducation   Detail = "education"
	DetailCourse      Detail = "course"
	DetailTraining    Detail = "training"
	DetailWorkHistory Detail = "work_history"
	DetailLanguage    Detail = "language"
)

var detailTables = map[Detail]string{
	DetailContact:     "person_contacts",
	DetailKin:         "person_kins",
	DetailDocument:    "person_ids",
	DetailEducation:   "person_education",
	DetailCourse:      "person_courses",
	DetailTraining:    "person_training",
	DetailWorkHistory: "person_workhistory",
	DetailLanguage:    "person_languages",
}

// Valid reports whether d names a satellite table.
func (d Detail) Valid() bool { _, ok := detailTables[d]; return ok }

// workerPerson resolves a worker inside the scope to the person they are and
// the district that owns them.
func workerPerson(ctx context.Context, q querier, sc auth.Scope, workerID int64) (personID, districtID int64, err error) {
	query := `SELECT w.person_id, w.district_id FROM health_workers w WHERE w.id = $1`
	args := []any{workerID}
	if frag, extra := sc.Filter("w.district_id", len(args)+1); frag != "" {
		query += frag
		args = append(args, extra...)
	}
	var district *int64
	if err := q.QueryRow(ctx, query, args...).Scan(&personID, &district); err != nil {
		return 0, 0, fmt.Errorf("worker %d: %w", workerID, translate(err))
	}
	if district == nil {
		return 0, 0, fmt.Errorf("worker %d has no deployment yet: %w", workerID, domain.ErrNotFound)
	}
	return personID, *district, nil
}

// Details reads everything recorded about the person a worker is.
func (s *Persons) Details(ctx context.Context, sc auth.Scope, workerID int64) (domain.PersonDetails, error) {
	personID, _, err := workerPerson(ctx, s.pool, sc, workerID)
	if err != nil {
		return domain.PersonDetails{}, err
	}
	return detailsOn(ctx, s.pool, personID)
}

func detailsOn(ctx context.Context, q querier, personID int64) (domain.PersonDetails, error) {
	var d domain.PersonDetails
	var err error
	collect := func(sql string, scan func(pgx.Rows) error) {
		if err != nil {
			return
		}
		var rows pgx.Rows
		if rows, err = q.Query(ctx, sql, personID); err != nil {
			err = fmt.Errorf("person %d details: %w", personID, translate(err))
			return
		}
		defer rows.Close()
		for rows.Next() {
			if err = scan(rows); err != nil {
				return
			}
		}
		err = rows.Err()
	}

	collect(`SELECT id, kind::text, value, owned, for_reporting, is_primary FROM person_contacts
	          WHERE person_id = $1 ORDER BY kind, is_primary DESC, id`, func(r pgx.Rows) error {
		var c domain.Contact
		var kind string
		err := r.Scan(&c.ID, &kind, &c.Value, &c.Owned, &c.ForReporting, &c.IsPrimary)
		c.Kind = domain.ContactKind(kind)
		d.Contacts = append(d.Contacts, c)
		return err
	})
	collect(`SELECT id, name, relationship, coalesce(phone,''), is_emergency FROM person_kins
	          WHERE person_id = $1 ORDER BY is_emergency DESC, id`, func(r pgx.Rows) error {
		var k domain.Kin
		err := r.Scan(&k.ID, &k.Name, &k.Relationship, &k.Phone, &k.IsEmergency)
		d.Kin = append(d.Kin, k)
		return err
	})
	collect(`SELECT i.id, t.id, t.code, t.label, i.number FROM person_ids i
	           JOIN identifier_types t ON t.id = i.identifier_type_id
	          WHERE i.person_id = $1 ORDER BY t.sort_order, i.id`, func(r pgx.Rows) error {
		var doc domain.Document
		err := r.Scan(&doc.ID, &doc.Type.ID, &doc.Type.Code, &doc.Type.Label, &doc.Number)
		d.Documents = append(d.Documents, doc)
		return err
	})
	collect(`SELECT id, level::text, coalesce(institution,''), coalesce(qualification,''), year_completed
	           FROM person_education WHERE person_id = $1 ORDER BY level DESC, id`, func(r pgx.Rows) error {
		var e domain.Education
		var level string
		err := r.Scan(&e.ID, &level, &e.Institution, &e.Qualification, &e.YearCompleted)
		e.Level = domain.EducationLevel(level)
		d.Education = append(d.Education, e)
		return err
	})
	collect(`SELECT id, course, coalesce(institution,''), coalesce(qualification,''), started_on, completed_on
	           FROM person_courses WHERE person_id = $1 ORDER BY started_on DESC NULLS LAST, id`, func(r pgx.Rows) error {
		var c domain.Course
		err := r.Scan(&c.ID, &c.Course, &c.Institution, &c.Qualification, &c.StartedOn, &c.CompletedOn)
		d.Courses = append(d.Courses, c)
		return err
	})
	collect(`SELECT id, title, coalesce(provider,''), started_on, ended_on, certified
	           FROM person_training WHERE person_id = $1 ORDER BY started_on DESC NULLS LAST, id`, func(r pgx.Rows) error {
		var t domain.Training
		err := r.Scan(&t.ID, &t.Title, &t.Provider, &t.StartedOn, &t.EndedOn, &t.Certified)
		d.Training = append(d.Training, t)
		return err
	})
	collect(`SELECT id, employer, coalesce(position,''), started_on, ended_on
	           FROM person_workhistory WHERE person_id = $1 ORDER BY started_on DESC NULLS LAST, id`, func(r pgx.Rows) error {
		var w domain.WorkHistory
		err := r.Scan(&w.ID, &w.Employer, &w.Position, &w.StartedOn, &w.EndedOn)
		d.WorkHistory = append(d.WorkHistory, w)
		return err
	})
	collect(`SELECT pl.id, l.id, l.code, l.label, coalesce(pl.understanding_grade::text,''),
	                coalesce(pl.reading_grade::text,''), coalesce(pl.writing_grade::text,'')
	           FROM person_languages pl JOIN languages l ON l.id = pl.language_id
	          WHERE pl.person_id = $1 ORDER BY l.sort_order, l.id`, func(r pgx.Rows) error {
		var l domain.LanguageSkill
		var u, rd, w string
		err := r.Scan(&l.ID, &l.Language.ID, &l.Language.Code, &l.Language.Label, &u, &rd, &w)
		l.Understanding, l.Reading, l.Writing = domain.Proficiency(u), domain.Proficiency(rd), domain.Proficiency(w)
		d.Languages = append(d.Languages, l)
		return err
	})
	return d, err
}

// write runs one detail change in a transaction with its audit row. insert
// does the change and returns the row as it now stands, for the audit.
func (s *Persons) write(ctx context.Context, sc auth.Scope, actor domain.User, workerID int64,
	action string, ip netip.Addr, change func(tx pgx.Tx, personID int64) (before, after any, err error)) error {

	tx, err := begin(ctx, s.pool, actor)
	if err != nil {
		return fmt.Errorf("%s for worker %d: %w", action, workerID, err)
	}
	defer tx.Rollback(ctx)

	personID, districtID, err := workerPerson(ctx, tx, sc, workerID)
	if err != nil {
		return err
	}
	before, after, err := change(tx, personID)
	if err != nil {
		return fmt.Errorf("%s for worker %d: %w", action, workerID, translate(err))
	}

	e := ActorFrom(actor)
	e.Action = action
	e.Entity = "health_worker"
	e.EntityID = &workerID
	e.DistrictID = &districtID
	e.Before = before
	e.After = after
	e.IP = ip
	if err := recordOn(ctx, tx, e); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("%s for worker %d: %w", action, workerID, translate(err))
	}
	return nil
}

// row returns a satellite row as JSON for the audit, with the table it is in.
func row(ctx context.Context, tx pgx.Tx, d Detail, id int64) (map[string]any, error) {
	var out map[string]any
	err := tx.QueryRow(ctx, `SELECT to_jsonb(t) - 'uuid' - 'created_on' - 'created_by' - 'last_updated_on' - 'last_updated_by'
	                           FROM `+detailTables[d]+` t WHERE id = $1`, id).Scan(&out)
	if err != nil {
		return nil, err
	}
	out["detail"] = string(d)
	return out, nil
}

// AddContact records a way to reach the person. Making it primary demotes the
// previous primary of its kind in the same transaction, since the schema
// allows one.
func (s *Persons) AddContact(ctx context.Context, sc auth.Scope, actor domain.User, workerID int64, c domain.Contact, ip netip.Addr) error {
	return s.write(ctx, sc, actor, workerID, ActionPersonDetailAdd, ip, func(tx pgx.Tx, personID int64) (any, any, error) {
		if c.IsPrimary {
			if _, err := tx.Exec(ctx, `UPDATE person_contacts SET is_primary = false
			                            WHERE person_id = $1 AND kind = $2::contact_kind AND is_primary`,
				personID, string(c.Kind)); err != nil {
				return nil, nil, err
			}
		}
		id, err := insertContact(ctx, tx, personID, c)
		if err != nil {
			return nil, nil, err
		}
		after, err := row(ctx, tx, DetailContact, id)
		return nil, after, err
	})
}

func insertContact(ctx context.Context, tx pgx.Tx, personID int64, c domain.Contact) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `
	    INSERT INTO person_contacts (person_id, kind, value, owned, for_reporting, is_primary)
	    VALUES ($1, $2::contact_kind, $3, $4, $5, $6) RETURNING id`,
		personID, string(c.Kind), c.Value, c.Owned, c.ForReporting, c.IsPrimary).Scan(&id)
	return id, err
}

// AddKin records a next of kin.
func (s *Persons) AddKin(ctx context.Context, sc auth.Scope, actor domain.User, workerID int64, k domain.Kin, ip netip.Addr) error {
	return s.insert(ctx, sc, actor, workerID, DetailKin, ip, `
	    INSERT INTO person_kins (person_id, name, relationship, phone, is_emergency)
	    VALUES ($1, $2, $3, nullif($4,''), $5) RETURNING id`,
		k.Name, k.Relationship, k.Phone, k.IsEmergency)
}

// AddDocument records an identity document.
func (s *Persons) AddDocument(ctx context.Context, sc auth.Scope, actor domain.User, workerID int64, doc domain.Document, ip netip.Addr) error {
	return s.insert(ctx, sc, actor, workerID, DetailDocument, ip, `
	    INSERT INTO person_ids (person_id, identifier_type_id, number)
	    VALUES ($1, $2, $3) RETURNING id`,
		doc.Type.ID, doc.Number)
}

// AddEducation records a level of schooling.
func (s *Persons) AddEducation(ctx context.Context, sc auth.Scope, actor domain.User, workerID int64, e domain.Education, ip netip.Addr) error {
	return s.insert(ctx, sc, actor, workerID, DetailEducation, ip, `
	    INSERT INTO person_education (person_id, level, institution, qualification, year_completed)
	    VALUES ($1, $2::education_level, nullif($3,''), nullif($4,''), $5) RETURNING id`,
		string(e.Level), e.Institution, e.Qualification, e.YearCompleted)
}

// AddCourse records a formal course.
func (s *Persons) AddCourse(ctx context.Context, sc auth.Scope, actor domain.User, workerID int64, c domain.Course, ip netip.Addr) error {
	return s.insert(ctx, sc, actor, workerID, DetailCourse, ip, `
	    INSERT INTO person_courses (person_id, course, institution, qualification, started_on, completed_on)
	    VALUES ($1, $2, nullif($3,''), nullif($4,''), $5, $6) RETURNING id`,
		c.Course, c.Institution, c.Qualification, c.StartedOn, c.CompletedOn)
}

// AddTraining records in-service training.
func (s *Persons) AddTraining(ctx context.Context, sc auth.Scope, actor domain.User, workerID int64, t domain.Training, ip netip.Addr) error {
	return s.insert(ctx, sc, actor, workerID, DetailTraining, ip, `
	    INSERT INTO person_training (person_id, title, provider, started_on, ended_on, certified)
	    VALUES ($1, $2, nullif($3,''), $4, $5, $6) RETURNING id`,
		t.Title, t.Provider, t.StartedOn, t.EndedOn, t.Certified)
}

// AddWorkHistory records work done outside the register.
func (s *Persons) AddWorkHistory(ctx context.Context, sc auth.Scope, actor domain.User, workerID int64, w domain.WorkHistory, ip netip.Addr) error {
	return s.insert(ctx, sc, actor, workerID, DetailWorkHistory, ip, `
	    INSERT INTO person_workhistory (person_id, employer, position, started_on, ended_on)
	    VALUES ($1, $2, nullif($3,''), $4, $5) RETURNING id`,
		w.Employer, w.Position, w.StartedOn, w.EndedOn)
}

// SetLanguage records how well the person uses a language, replacing what was
// recorded for it: one row per language, graded per skill.
func (s *Persons) SetLanguage(ctx context.Context, sc auth.Scope, actor domain.User, workerID int64, l domain.LanguageSkill, ip netip.Addr) error {
	return s.write(ctx, sc, actor, workerID, ActionPersonDetailAdd, ip, func(tx pgx.Tx, personID int64) (any, any, error) {
		var existing int64
		var before any
		err := tx.QueryRow(ctx, `SELECT id FROM person_languages WHERE person_id = $1 AND language_id = $2`,
			personID, l.Language.ID).Scan(&existing)
		if err == nil {
			if before, err = row(ctx, tx, DetailLanguage, existing); err != nil {
				return nil, nil, err
			}
		} else if translate(err) != domain.ErrNotFound {
			return nil, nil, err
		}
		id, err := upsertLanguage(ctx, tx, personID, l)
		if err != nil {
			return nil, nil, err
		}
		after, err := row(ctx, tx, DetailLanguage, id)
		return before, after, err
	})
}

func upsertLanguage(ctx context.Context, tx pgx.Tx, personID int64, l domain.LanguageSkill) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `
	    INSERT INTO person_languages (person_id, language_id, understanding_grade, reading_grade, writing_grade)
	    VALUES ($1, $2, nullif($3,'')::proficiency, nullif($4,'')::proficiency, nullif($5,'')::proficiency)
	    ON CONFLICT (person_id, language_id) DO UPDATE
	       SET understanding_grade = excluded.understanding_grade,
	           reading_grade = excluded.reading_grade,
	           writing_grade = excluded.writing_grade
	    RETURNING id`,
		personID, l.Language.ID, string(l.Understanding), string(l.Reading), string(l.Writing)).Scan(&id)
	return id, err
}

// insert runs a single-row INSERT whose first parameter is the person id.
func (s *Persons) insert(ctx context.Context, sc auth.Scope, actor domain.User, workerID int64, d Detail,
	ip netip.Addr, sql string, args ...any) error {

	return s.write(ctx, sc, actor, workerID, ActionPersonDetailAdd, ip, func(tx pgx.Tx, personID int64) (any, any, error) {
		var id int64
		if err := tx.QueryRow(ctx, sql, append([]any{personID}, args...)...).Scan(&id); err != nil {
			return nil, nil, err
		}
		after, err := row(ctx, tx, d, id)
		return nil, after, err
	})
}

// Remove deletes one detail row of the person a worker is. A row of another
// person — another worker's id in a forged form — matches nothing and is
// ErrNotFound, like any row outside the scope.
func (s *Persons) Remove(ctx context.Context, sc auth.Scope, actor domain.User, workerID int64, d Detail, id int64, ip netip.Addr) error {
	table, ok := detailTables[d]
	if !ok {
		return fmt.Errorf("remove %q: %w", d, domain.ErrNotFound)
	}
	return s.write(ctx, sc, actor, workerID, ActionPersonDetailRemove, ip, func(tx pgx.Tx, personID int64) (any, any, error) {
		before, err := row(ctx, tx, d, id)
		if err != nil {
			return nil, nil, err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE id = $1 AND person_id = $2`, id, personID)
		if err != nil {
			return nil, nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, nil, domain.ErrNotFound
		}
		return before, nil, nil
	})
}

// SurveyDetails is what the CHW survey asks that belongs to the person: the
// phone numbers, the highest education, and English. The importer writes them
// beside the worker, in the worker's transaction.
type SurveyDetails struct {
	Phones    []domain.Contact
	Education domain.EducationLevel
	English   *domain.LanguageSkill
}

// Empty reports whether there is nothing to write.
func (d SurveyDetails) Empty() bool {
	return len(d.Phones) == 0 && d.Education == "" && d.English == nil
}

// WriteSurveyDetailsTx records a new worker's survey details in a caller's
// transaction, with one audit row for the lot.
func (s *Persons) WriteSurveyDetailsTx(ctx context.Context, tx pgx.Tx, sc auth.Scope, actor domain.User,
	workerID int64, d SurveyDetails, ip netip.Addr) error {

	if d.Empty() {
		return nil
	}
	if err := ActAs(ctx, tx, actor); err != nil {
		return err
	}
	personID, districtID, err := workerPerson(ctx, tx, sc, workerID)
	if err != nil {
		return err
	}
	for _, c := range d.Phones {
		if _, err := insertContact(ctx, tx, personID, c); err != nil {
			return fmt.Errorf("phone for worker %d: %w", workerID, translate(err))
		}
	}
	if d.Education != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO person_education (person_id, level) VALUES ($1, $2::education_level)`,
			personID, string(d.Education)); err != nil {
			return fmt.Errorf("education for worker %d: %w", workerID, translate(err))
		}
	}
	if d.English != nil {
		l := *d.English
		if err := tx.QueryRow(ctx, `SELECT id FROM languages WHERE code = $1`, domain.LanguageEnglish).Scan(&l.Language.ID); err != nil {
			return fmt.Errorf("english for worker %d: %w", workerID, translate(err))
		}
		if _, err := upsertLanguage(ctx, tx, personID, l); err != nil {
			return fmt.Errorf("english for worker %d: %w", workerID, translate(err))
		}
	}

	after, err := detailsOn(ctx, tx, personID)
	if err != nil {
		return err
	}
	e := ActorFrom(actor)
	e.Action = ActionPersonDetailAdd
	e.Entity = "health_worker"
	e.EntityID = &workerID
	e.DistrictID = &districtID
	e.After = after
	e.IP = ip
	return recordOn(ctx, tx, e)
}

// Languages is the language vocabulary. Like the cadre list it is the same for
// every caller and takes no Scope.
func (s *Persons) Languages(ctx context.Context) ([]domain.Language, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, code, label FROM languages WHERE active ORDER BY sort_order, id`)
	if err != nil {
		return nil, fmt.Errorf("list languages: %w", translate(err))
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.Language, error) {
		var l domain.Language
		err := r.Scan(&l.ID, &l.Code, &l.Label)
		return l, err
	})
}

// IdentifierTypes is the identity-document vocabulary.
func (s *Persons) IdentifierTypes(ctx context.Context) ([]domain.IdentifierType, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, code, label FROM identifier_types WHERE active ORDER BY sort_order, id`)
	if err != nil {
		return nil, fmt.Errorf("list identifier types: %w", translate(err))
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.IdentifierType, error) {
		var t domain.IdentifierType
		err := r.Scan(&t.ID, &t.Code, &t.Label)
		return t, err
	})
}
