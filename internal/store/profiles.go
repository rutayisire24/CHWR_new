package store

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"hwr/internal/auth"
	"hwr/internal/domain"
)

// Profiles reads questionnaires and writes health workers' answers to them.
//
// A profile's definition is vocabulary — the same for every caller — so the
// reads of it take no Scope. A worker's submissions are register data and are
// read and written through the worker, scoped like every other worker read.
// Submissions are history: saving writes a new one, and the latest is what the
// register shows.
type Profiles struct {
	pool *pgxpool.Pool
}

// CHWBaseline is the code of the CHW category's survey: the profile the
// importer's survey columns and the export's are spelled against.
const CHWBaseline = "chw_baseline"

const profileColumns = `p.id, p.code, p.profile_name, coalesce(p.profile_description,''), p.active`

const questionColumns = `
    pq.id, q.id, pq.code, q.question_prompt, coalesce(q.help,''),
    rt.code = 'multi_value', vt.code = 'closed', dt.code, q.response_options,
    q.min_value::float8, q.max_value::float8, coalesce(q.pattern,''),
    pq.required, pq.sort_order, coalesce(d.code,''), coalesce(pq.depends_on_option,''),
    coalesce(sb.code,''), q.active`

const questionFrom = `
    FROM profile_questions pq
    JOIN question_pool q        ON q.id  = pq.question_id
    JOIN response_types rt      ON rt.id = q.response_type_id
    JOIN value_types vt         ON vt.id = q.value_type_id
    JOIN value_data_types dt    ON dt.id = q.value_data_type_id
    LEFT JOIN profile_questions d  ON d.id  = pq.depends_on_id
    LEFT JOIN profile_questions sb ON sb.id = pq.subset_of_id`

// ByCode loads a profile and every question it asks, retired ones included:
// an old submission's answers still have to read back.
func (s *Profiles) ByCode(ctx context.Context, code string) (domain.Profile, error) {
	return s.load(ctx, s.pool, `p.code = $1`, code)
}

// ByID loads a profile by id.
func (s *Profiles) ByID(ctx context.Context, id int16) (domain.Profile, error) {
	return s.load(ctx, s.pool, `p.id = $1`, id)
}

// ForCadre lists the active profiles a worker in this cadre answers.
func (s *Profiles) ForCadre(ctx context.Context, cadreID int16) ([]domain.Profile, error) {
	rows, err := s.pool.Query(ctx, `
	    SELECT p.id FROM profiles p
	      JOIN profile_applicable_cadre a ON a.profile_id = p.id
	     WHERE a.cadre_id = $1 AND p.active
	     ORDER BY p.sort_order, p.id`, cadreID)
	if err != nil {
		return nil, fmt.Errorf("profiles for cadre %d: %w", cadreID, translate(err))
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int16])
	if err != nil {
		return nil, fmt.Errorf("profiles for cadre %d: %w", cadreID, err)
	}
	out := make([]domain.Profile, 0, len(ids))
	for _, id := range ids {
		p, err := s.ByID(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func (s *Profiles) load(ctx context.Context, q querier, where string, arg any) (domain.Profile, error) {
	var p domain.Profile
	if err := q.QueryRow(ctx, `SELECT `+profileColumns+` FROM profiles p WHERE `+where, arg).
		Scan(&p.ID, &p.Code, &p.Name, &p.Description, &p.Active); err != nil {
		return domain.Profile{}, fmt.Errorf("load profile %v: %w", arg, translate(err))
	}

	rows, err := q.Query(ctx, `SELECT `+questionColumns+questionFrom+`
	     WHERE pq.profile_id = $1 ORDER BY pq.sort_order, pq.id`, p.ID)
	if err != nil {
		return domain.Profile{}, fmt.Errorf("load questions of %s: %w", p.Code, translate(err))
	}
	defer rows.Close()
	for rows.Next() {
		var qn domain.Question
		var dataType string
		var options []byte
		if err := rows.Scan(&qn.ID, &qn.PoolID, &qn.Code, &qn.Prompt, &qn.Help,
			&qn.Multi, &qn.Closed, &dataType, &options,
			&qn.Min, &qn.Max, &qn.Pattern,
			&qn.Required, &qn.SortOrder, &qn.DependsOn, &qn.DependsOnOption,
			&qn.SubsetOf, &qn.Active); err != nil {
			return domain.Profile{}, fmt.Errorf("scan question of %s: %w", p.Code, err)
		}
		qn.DataType = domain.DataType(dataType)
		if err := json.Unmarshal(options, &qn.Options); err != nil {
			return domain.Profile{}, fmt.Errorf("question %s options: %w", qn.Code, err)
		}
		p.Questions = append(p.Questions, qn)
	}
	return p, rows.Err()
}

// Latest returns a worker's most recent submission to a profile. A worker who
// has never answered it gets an empty submission, not an error — "nothing
// recorded" is the normal state of an imported record. ErrNotFound means the
// worker is unknown or outside the scope.
func (s *Profiles) Latest(ctx context.Context, sc auth.Scope, workerID int64, p domain.Profile) (domain.Submission, error) {
	if _, err := workerDistrict(ctx, s.pool, sc, workerID); err != nil {
		return domain.Submission{}, err
	}
	return latestOn(ctx, s.pool, workerID, p)
}

// History lists a worker's submissions to a profile, newest first, answers
// included — the survey's own change history.
func (s *Profiles) History(ctx context.Context, sc auth.Scope, workerID int64, p domain.Profile) ([]domain.Submission, error) {
	if _, err := workerDistrict(ctx, s.pool, sc, workerID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
	    SELECT id, captured_on, source, created_by, created_on FROM health_worker_profiles
	     WHERE health_worker_id = $1 AND profile_id = $2
	     ORDER BY captured_on DESC, id DESC`, workerID, p.ID)
	if err != nil {
		return nil, fmt.Errorf("submissions of worker %d: %w", workerID, translate(err))
	}
	var out []domain.Submission
	for rows.Next() {
		sub := domain.Submission{HealthWorkerID: workerID, ProfileID: p.ID}
		if err := rows.Scan(&sub.ID, &sub.CapturedOn, &sub.Source, &sub.CreatedBy, &sub.CreatedOn); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, sub)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Answers, err = answersOn(ctx, s.pool, out[i].ID, p); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func latestOn(ctx context.Context, q querier, workerID int64, p domain.Profile) (domain.Submission, error) {
	sub := domain.Submission{HealthWorkerID: workerID, ProfileID: p.ID}
	err := q.QueryRow(ctx, `
	    SELECT id, captured_on, source, created_by, created_on FROM health_worker_profiles
	     WHERE health_worker_id = $1 AND profile_id = $2
	     ORDER BY captured_on DESC, id DESC LIMIT 1`, workerID, p.ID).
		Scan(&sub.ID, &sub.CapturedOn, &sub.Source, &sub.CreatedBy, &sub.CreatedOn)
	if err != nil {
		if translate(err) == domain.ErrNotFound {
			return domain.Submission{HealthWorkerID: workerID, ProfileID: p.ID, Answers: domain.Answers{}}, nil
		}
		return domain.Submission{}, fmt.Errorf("latest %s of worker %d: %w", p.Code, workerID, translate(err))
	}
	sub.Answers, err = answersOn(ctx, q, sub.ID, p)
	return sub, err
}

// answersOn reads a submission's responses back into answers by code. A
// closed answer is stored as its option id and read back as its code.
func answersOn(ctx context.Context, q querier, submissionID int64, p domain.Profile) (domain.Answers, error) {
	rows, err := q.Query(ctx, `
	    SELECT r.profile_question_id, r.response_option_id, r.response
	      FROM health_worker_profile_responses r
	     WHERE r.health_worker_profile_id = $1 ORDER BY r.id`, submissionID)
	if err != nil {
		return nil, fmt.Errorf("answers of submission %d: %w", submissionID, translate(err))
	}
	defer rows.Close()

	byID := make(map[int32]domain.Question, len(p.Questions))
	for _, qn := range p.Questions {
		byID[qn.ID] = qn
	}
	out := domain.Answers{}
	for rows.Next() {
		var questionID int32
		var optionID *int32
		var text *string
		if err := rows.Scan(&questionID, &optionID, &text); err != nil {
			return nil, err
		}
		qn := byID[questionID]
		value := ""
		switch {
		case optionID != nil:
			o, ok := qn.OptionByID(*optionID)
			if !ok {
				return nil, fmt.Errorf("submission %d: option %d is not a choice of %s", submissionID, *optionID, qn.Code)
			}
			value = o.Code
		case text != nil:
			value = *text
		}
		out[qn.Code] = append(out[qn.Code], value)
	}
	return out, rows.Err()
}

// Submit records a new submission with its audit row, in one transaction.
func (s *Profiles) Submit(ctx context.Context, sc auth.Scope, actor domain.User, workerID int64,
	p domain.Profile, answers domain.Answers, source string, ip netip.Addr) (domain.Submission, error) {

	tx, err := begin(ctx, s.pool, actor)
	if err != nil {
		return domain.Submission{}, fmt.Errorf("submit %s for worker %d: %w", p.Code, workerID, err)
	}
	defer tx.Rollback(ctx)

	sub, err := s.SubmitTx(ctx, tx, sc, actor, workerID, p, answers, source, ip)
	if err != nil {
		return domain.Submission{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Submission{}, fmt.Errorf("submit %s for worker %d: %w", p.Code, workerID, translate(err))
	}
	return sub, nil
}

// SubmitTx writes a submission into a caller's transaction, the Tx half of the
// pair Workers.CreateTx and Audit.RecordTx already establish: the importer
// writes a worker and their first answers in one transaction, so a crash
// cannot leave one without the other.
//
// Answers are checked against the profile first, so a bad one comes back as a
// *domain.ValidationError keyed by question code rather than a constraint
// violation. A save that changes nothing writes nothing: history records
// answers that changed, not the times a form was opened. Neither does a first
// save with nothing in it — "nothing recorded" needs no row to say so.
func (s *Profiles) SubmitTx(ctx context.Context, tx pgx.Tx, sc auth.Scope, actor domain.User, workerID int64,
	p domain.Profile, answers domain.Answers, source string, ip netip.Addr) (domain.Submission, error) {

	if problems := p.Check(answers); len(problems) > 0 {
		v := domain.NewValidationError()
		for _, pr := range problems {
			v.Add(pr.Question, pr.Message)
		}
		return domain.Submission{}, v
	}
	if err := ActAs(ctx, tx, actor); err != nil {
		return domain.Submission{}, err
	}
	districtID, err := workerDistrict(ctx, tx, sc, workerID)
	if err != nil {
		return domain.Submission{}, err
	}
	before, err := latestOn(ctx, tx, workerID, p)
	if err != nil {
		return domain.Submission{}, err
	}
	if sameAnswers(before.Answers, answers) {
		return before, nil
	}

	sub := domain.Submission{HealthWorkerID: workerID, ProfileID: p.ID, Source: source, Answers: answers}
	if err := tx.QueryRow(ctx, `
	    INSERT INTO health_worker_profiles (health_worker_id, profile_id, source)
	    VALUES ($1, $2, $3)
	    RETURNING id, captured_on, created_by, created_on`, workerID, p.ID, source).
		Scan(&sub.ID, &sub.CapturedOn, &sub.CreatedBy, &sub.CreatedOn); err != nil {
		return domain.Submission{}, fmt.Errorf("submit %s for worker %d: %w", p.Code, workerID, translate(err))
	}

	var questionIDs []int32
	var optionIDs []*int32
	var texts []*string
	for _, qn := range p.Questions {
		for _, value := range answers[qn.Code] {
			questionIDs = append(questionIDs, qn.ID)
			if qn.Closed {
				o, _ := qn.Option(value) // Check has vouched for it
				id := o.ID
				optionIDs, texts = append(optionIDs, &id), append(texts, nil)
			} else {
				v := value
				optionIDs, texts = append(optionIDs, nil), append(texts, &v)
			}
		}
	}
	if len(questionIDs) > 0 {
		if _, err := tx.Exec(ctx, `
		    INSERT INTO health_worker_profile_responses
		           (health_worker_profile_id, profile_question_id, response_option_id, response)
		    SELECT $1, q, o, t FROM unnest($2::int[], $3::int[], $4::text[]) AS x(q, o, t)`,
			sub.ID, questionIDs, optionIDs, texts); err != nil {
			return domain.Submission{}, fmt.Errorf("answers for worker %d: %w", workerID, translate(err))
		}
	}

	e := ActorFrom(actor)
	e.Action = ActionProfileSubmit
	e.Entity = "health_worker"
	e.EntityID = &workerID
	e.DistrictID = &districtID
	if before.Exists() {
		e.Before = submissionSnapshot(p, before)
	}
	e.After = submissionSnapshot(p, sub)
	e.IP = ip
	if err := recordOn(ctx, tx, e); err != nil {
		return domain.Submission{}, err
	}
	return sub, nil
}

// submissionSnapshot is the JSONB written to audit_log: the profile, the
// submission and every answer, so the history is reconstructable from the log
// alone.
func submissionSnapshot(p domain.Profile, s domain.Submission) map[string]any {
	return map[string]any{
		"profile":     p.Code,
		"submission":  s.ID,
		"captured_on": s.CapturedOn.Format(time.DateOnly),
		"source":      s.Source,
		"answers":     s.Answers,
	}
}

func sameAnswers(a, b domain.Answers) bool {
	clean := func(x domain.Answers) map[string][]string {
		out := map[string][]string{}
		for k, v := range x {
			if len(v) > 0 {
				sorted := slices.Clone(v)
				slices.Sort(sorted)
				out[k] = sorted
			}
		}
		return out
	}
	return maps.EqualFunc(clean(a), clean(b), slices.Equal[[]string])
}

// workerDistrict confirms the worker exists inside the scope and returns their
// district anchor, for the audit row: the tables that hang off a worker have
// no district of their own to filter on.
func workerDistrict(ctx context.Context, q querier, sc auth.Scope, workerID int64) (int64, error) {
	query := `SELECT w.district_id FROM health_workers w WHERE w.id = $1`
	args := []any{workerID}
	if frag, extra := sc.Filter("w.district_id", len(args)+1); frag != "" {
		query += frag
		args = append(args, extra...)
	}

	var districtID *int64
	if err := q.QueryRow(ctx, query, args...).Scan(&districtID); err != nil {
		return 0, fmt.Errorf("worker %d: %w", workerID, translate(err))
	}
	if districtID == nil {
		return 0, fmt.Errorf("worker %d has no deployment yet: %w", workerID, domain.ErrNotFound)
	}
	return *districtID, nil
}

// Answered lists the profiles a worker has ever submitted to, so a survey
// answered under a cadre the worker has since left still reads back.
func (s *Profiles) Answered(ctx context.Context, sc auth.Scope, workerID int64) ([]domain.Profile, error) {
	if _, err := workerDistrict(ctx, s.pool, sc, workerID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
	    SELECT DISTINCT p.id, p.sort_order FROM profiles p
	      JOIN health_worker_profiles s ON s.profile_id = p.id
	     WHERE s.health_worker_id = $1 ORDER BY p.sort_order, p.id`, workerID)
	if err != nil {
		return nil, fmt.Errorf("profiles answered by worker %d: %w", workerID, translate(err))
	}
	ids, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (int16, error) {
		var id, order int16
		err := r.Scan(&id, &order)
		return id, err
	})
	if err != nil {
		return nil, err
	}
	out := make([]domain.Profile, 0, len(ids))
	for _, id := range ids {
		p, err := s.ByID(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// ---------------------------------------------------------------- administration

// ProfileRow is a profile as the admin screen lists it: the definition, the
// cadres that answer it, and how many submissions it holds.
type ProfileRow struct {
	domain.Profile
	CadreIDs    []int16
	Submissions int64
}

// List returns every profile for the admin screen.
func (s *Profiles) List(ctx context.Context) ([]ProfileRow, error) {
	rows, err := s.pool.Query(ctx, `
	    SELECT p.id,
	           coalesce((SELECT array_agg(a.cadre_id ORDER BY a.cadre_id) FROM profile_applicable_cadre a WHERE a.profile_id = p.id), '{}'),
	           (SELECT count(*) FROM health_worker_profiles s WHERE s.profile_id = p.id)
	      FROM profiles p ORDER BY p.sort_order, p.id`)
	if err != nil {
		return nil, fmt.Errorf("list profiles: %w", translate(err))
	}
	type head struct {
		id     int16
		cadres []int16
		n      int64
	}
	heads, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (head, error) {
		var h head
		err := r.Scan(&h.id, &h.cadres, &h.n)
		return h, err
	})
	if err != nil {
		return nil, err
	}
	out := make([]ProfileRow, 0, len(heads))
	for _, h := range heads {
		p, err := s.ByID(ctx, h.id)
		if err != nil {
			return nil, err
		}
		out = append(out, ProfileRow{Profile: p, CadreIDs: h.cadres, Submissions: h.n})
	}
	return out, nil
}

// Row returns one profile for the admin screen.
func (s *Profiles) Row(ctx context.Context, id int16) (ProfileRow, error) {
	all, err := s.List(ctx)
	if err != nil {
		return ProfileRow{}, err
	}
	for _, r := range all {
		if r.ID == id {
			return r, nil
		}
	}
	return ProfileRow{}, fmt.Errorf("profile %d: %w", id, domain.ErrNotFound)
}

// AnsweredQuestions is the set of a profile's question codes that hold
// answers, which the schema freezes: the admin screen offers only what it
// would accept.
func (s *Profiles) AnsweredQuestions(ctx context.Context, id int16) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `
	    SELECT DISTINCT pq.code FROM profile_questions pq
	      JOIN health_worker_profile_responses r ON r.profile_question_id = pq.id
	     WHERE pq.profile_id = $1`, id)
	if err != nil {
		return nil, fmt.Errorf("answered questions of profile %d: %w", id, translate(err))
	}
	codes, err := pgx.CollectRows(rows, pgx.RowTo[string])
	out := make(map[string]bool, len(codes))
	for _, c := range codes {
		out[c] = true
	}
	return out, err
}

// ProfileInput is the profile form.
type ProfileInput struct {
	Code        string
	Name        string
	Description string
	Active      bool
	CadreIDs    []int16
}

// SaveProfile creates a profile (id 0) or updates one — its name, description,
// whether it is offered, and the cadres that answer it — with the audit row.
// The code is fixed once the profile exists: imports and links name it.
func (s *Profiles) SaveProfile(ctx context.Context, sc auth.Scope, actor domain.User, id int16, in ProfileInput, ip netip.Addr) (int16, error) {
	if !sc.IsNational() {
		return 0, fmt.Errorf("save profile: %w", domain.ErrForbidden)
	}
	tx, err := begin(ctx, s.pool, actor)
	if err != nil {
		return 0, fmt.Errorf("save profile: %w", err)
	}
	defer tx.Rollback(ctx)

	var before any
	if id == 0 {
		err = tx.QueryRow(ctx, `
		    INSERT INTO profiles (code, profile_name, profile_description, active, sort_order)
		    VALUES ($1, $2, nullif($3,''), $4, (SELECT coalesce(max(sort_order), 0) + 1 FROM profiles))
		    RETURNING id`, in.Code, in.Name, in.Description, in.Active).Scan(&id)
	} else {
		var prev map[string]any
		if err = tx.QueryRow(ctx, `SELECT to_jsonb(p) FROM profiles p WHERE id = $1`, id).Scan(&prev); err == nil {
			before = prev
			_, err = tx.Exec(ctx, `
			    UPDATE profiles SET profile_name = $2, profile_description = nullif($3,''), active = $4
			     WHERE id = $1`, id, in.Name, in.Description, in.Active)
		}
	}
	if err != nil {
		return 0, fmt.Errorf("save profile: %w", translate(err))
	}
	if _, err := tx.Exec(ctx, `DELETE FROM profile_applicable_cadre WHERE profile_id = $1 AND NOT cadre_id = ANY($2)`,
		id, in.CadreIDs); err != nil {
		return 0, fmt.Errorf("save profile cadres: %w", translate(err))
	}
	if _, err := tx.Exec(ctx, `
	    INSERT INTO profile_applicable_cadre (profile_id, cadre_id)
	    SELECT $1, unnest($2::smallint[]) ON CONFLICT DO NOTHING`, id, in.CadreIDs); err != nil {
		return 0, fmt.Errorf("save profile cadres: %w", translate(err))
	}

	var after map[string]any
	if err := tx.QueryRow(ctx, `
	    SELECT to_jsonb(p) || jsonb_build_object('cadre_ids',
	           (SELECT coalesce(jsonb_agg(cadre_id ORDER BY cadre_id), '[]') FROM profile_applicable_cadre WHERE profile_id = p.id))
	      FROM profiles p WHERE id = $1`, id).Scan(&after); err != nil {
		return 0, fmt.Errorf("save profile: %w", translate(err))
	}
	action := ActionProfileUpdate
	if before == nil {
		action = ActionProfileCreate
	}
	if err := auditProfile(ctx, tx, actor, action, int64(id), before, after, ip); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("save profile: %w", translate(err))
	}
	return id, nil
}

// QuestionInput is the question form. A new question goes into the pool and
// onto the profile together; an existing one changes only what the schema
// lets an answered question change.
type QuestionInput struct {
	Code            string
	Prompt          string
	Help            string
	Multi           bool
	DataType        domain.DataType
	Options         []domain.Option // a closed question's choices; none for an open one
	Min, Max        *float64
	Pattern         string
	Required        bool
	SortOrder       int16
	Active          bool
	DependsOn       string // a question code in the same profile
	DependsOnOption string
	SubsetOf        string
}

// AddQuestion puts a new question into the pool and onto the profile.
func (s *Profiles) AddQuestion(ctx context.Context, sc auth.Scope, actor domain.User, profileID int16, in QuestionInput, ip netip.Addr) error {
	return s.questionWrite(ctx, sc, actor, profileID, ip, func(tx pgx.Tx) (any, error) {
		options, err := json.Marshal(optionsOrEmpty(in.Options))
		if err != nil {
			return nil, err
		}
		var poolID int32
		if err := tx.QueryRow(ctx, `
		    INSERT INTO question_pool (code, question_prompt, help, response_type_id, value_type_id,
		                               value_data_type_id, response_options, min_value, max_value, pattern, active)
		    VALUES ($1, $2, nullif($3,''),
		            (SELECT id FROM response_types WHERE code = CASE WHEN $4 THEN 'multi_value' ELSE 'single_value' END),
		            (SELECT id FROM value_types WHERE code = CASE WHEN jsonb_array_length($6::jsonb) > 0 THEN 'closed' ELSE 'open' END),
		            (SELECT id FROM value_data_types WHERE code = $5),
		            $6::jsonb, $7, $8, nullif($9,''), true)
		    RETURNING id`,
			in.Code, in.Prompt, in.Help, in.Multi, string(in.DataType), options, in.Min, in.Max, in.Pattern).Scan(&poolID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `
		    INSERT INTO profile_questions (profile_id, question_id, code, sort_order, required,
		                                   depends_on_id, depends_on_option, subset_of_id)
		    VALUES ($1, $2, $3, $4, $5,
		            (SELECT id FROM profile_questions WHERE profile_id = $1 AND code = nullif($6,'')), nullif($7,''),
		            (SELECT id FROM profile_questions WHERE profile_id = $1 AND code = nullif($8,'')))`,
			profileID, poolID, in.Code, in.SortOrder, in.Required, in.DependsOn, in.DependsOnOption, in.SubsetOf); err != nil {
			return nil, err
		}
		return map[string]any{"question": in.Code, "added": in}, nil
	})
}

// UpdateQuestion rewords a question, reorders it, retires or restores it, and
// adds choices. Its code, type and range are not offered for change: the
// schema freezes them once answered, and before then the admin can retire it
// and add the question they meant.
func (s *Profiles) UpdateQuestion(ctx context.Context, sc auth.Scope, actor domain.User, profileID int16, code string,
	in QuestionInput, ip netip.Addr) error {

	return s.questionWrite(ctx, sc, actor, profileID, ip, func(tx pgx.Tx) (any, error) {
		var before map[string]any
		if err := tx.QueryRow(ctx, `
		    SELECT to_jsonb(q) || jsonb_build_object('sort_order', pq.sort_order, 'required', pq.required)
		      FROM profile_questions pq JOIN question_pool q ON q.id = pq.question_id
		     WHERE pq.profile_id = $1 AND pq.code = $2`, profileID, code).Scan(&before); err != nil {
			return nil, err
		}
		options, err := json.Marshal(optionsOrEmpty(in.Options))
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `
		    UPDATE question_pool q SET question_prompt = $3, help = nullif($4,''), active = $5,
		           response_options = CASE WHEN jsonb_array_length($6::jsonb) > 0 THEN $6::jsonb ELSE q.response_options END
		      FROM profile_questions pq
		     WHERE pq.question_id = q.id AND pq.profile_id = $1 AND pq.code = $2`,
			profileID, code, in.Prompt, in.Help, in.Active, options); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE profile_questions SET sort_order = $3, required = $4
		                            WHERE profile_id = $1 AND code = $2`,
			profileID, code, in.SortOrder, in.Required); err != nil {
			return nil, err
		}
		return map[string]any{"question": code, "before": before, "after": in}, nil
	})
}

func (s *Profiles) questionWrite(ctx context.Context, sc auth.Scope, actor domain.User, profileID int16, ip netip.Addr,
	change func(pgx.Tx) (any, error)) error {

	if !sc.IsNational() {
		return fmt.Errorf("edit profile %d: %w", profileID, domain.ErrForbidden)
	}
	tx, err := begin(ctx, s.pool, actor)
	if err != nil {
		return fmt.Errorf("edit profile %d: %w", profileID, err)
	}
	defer tx.Rollback(ctx)
	after, err := change(tx)
	if err != nil {
		return fmt.Errorf("edit profile %d: %w", profileID, translate(err))
	}
	if err := auditProfile(ctx, tx, actor, ActionProfileUpdate, int64(profileID), nil, after, ip); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("edit profile %d: %w", profileID, translate(err))
	}
	return nil
}

func optionsOrEmpty(o []domain.Option) []domain.Option {
	if o == nil {
		return []domain.Option{}
	}
	return o
}

func auditProfile(ctx context.Context, tx pgx.Tx, actor domain.User, action string, id int64, before, after any, ip netip.Addr) error {
	e := ActorFrom(actor)
	e.Action = action
	e.Entity = "profile"
	e.EntityID = &id
	e.DistrictID = nil // national vocabulary: no district owns it
	e.Before = before
	e.After = after
	e.IP = ip
	return recordOn(ctx, tx, e)
}
