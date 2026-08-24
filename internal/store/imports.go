package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"chwr/internal/auth"
	"chwr/internal/domain"
)

// Imports is the staging area behind bulk upload: a batch per file, a row per
// line, and the tallies the report is drawn from. It writes nothing to `chws` —
// that is CHWs.CreateTx's job, joined to this package's row marking inside one
// transaction so the two cannot disagree.
//
// docs/import.md is the design.
type Imports struct {
	pool *pgxpool.Pool
}

// batchColumns is the projection every batch query shares. The enum is cast to
// text so pgx needs no type registration, as everywhere else.
const batchColumns = `
    b.id, b.filename, b.format, b.uploaded_by, u.full_name,
    b.district_id, coalesce(d.name,''), b.status::text, b.columns,
    b.total_rows, b.ready_rows, b.warning_rows, b.rejected_rows, b.imported_rows,
    b.skip_duplicates, b.created_at, b.committed_at`

const batchFrom = `
    FROM import_batches b
    JOIN users u ON u.id = b.uploaded_by
    LEFT JOIN locations d ON d.id = b.district_id`

func scanBatch(row pgx.Row) (domain.Batch, error) {
	var b domain.Batch
	var status string
	var columns []byte
	err := row.Scan(&b.ID, &b.Filename, &b.Format, &b.UploadedBy, &b.UploaderName,
		&b.DistrictID, &b.DistrictName, &status, &columns,
		&b.Total, &b.Ready, &b.Warning, &b.Rejected, &b.Imported,
		&b.SkipDuplicates, &b.CreatedAt, &b.CommittedAt)
	if err != nil {
		return domain.Batch{}, err
	}
	b.Status = domain.BatchStatus(status)
	if len(columns) > 0 {
		if err := json.Unmarshal(columns, &b.Columns); err != nil {
			return domain.Batch{}, fmt.Errorf("batch %d columns: %w", b.ID, err)
		}
	}
	return b, nil
}

// Create opens a pending batch.
//
// The batch's district is taken from the Scope, never from an argument — the
// same rule chws.district_id follows for the same reason. A handler cannot
// upload on behalf of a district it does not hold, because there is nowhere to
// say which district it meant.
func (s *Imports) Create(ctx context.Context, sc auth.Scope, actor domain.User,
	filename, format string, columns []string, ip netip.Addr) (domain.Batch, error) {

	if format != domain.FormatCSV && format != domain.FormatXLSX {
		// A guard, not a validation path: the handler decides the format from
		// the uploaded file, so reaching this means a caller invented one.
		return domain.Batch{}, fmt.Errorf("create import batch: unknown format %q", format)
	}

	encoded, err := json.Marshal(columns)
	if err != nil {
		return domain.Batch{}, fmt.Errorf("create import batch: columns: %w", err)
	}

	var districtID *int64
	if id, pinned := sc.DistrictID(); pinned {
		districtID = &id
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Batch{}, fmt.Errorf("create import batch: %w", err)
	}
	defer tx.Rollback(ctx)

	const q = `
	    WITH inserted AS (
	        INSERT INTO import_batches (filename, format, uploaded_by, district_id, columns)
	        VALUES ($1, $2, $3, $4, $5::jsonb)
	        RETURNING *
	    )
	    SELECT ` + batchColumns + `
	    FROM inserted b
	    JOIN users u ON u.id = b.uploaded_by
	    LEFT JOIN locations d ON d.id = b.district_id`

	b, err := scanBatch(tx.QueryRow(ctx, q, filename, format, actor.ID, districtID, encoded))
	if err != nil {
		return domain.Batch{}, fmt.Errorf("create import batch: %w", translate(err))
	}

	if err := s.auditTx(ctx, tx, actor, ActionImportUpload, b, nil, auditBatch(b), ip); err != nil {
		return domain.Batch{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Batch{}, fmt.Errorf("create import batch: %w", err)
	}
	return b, nil
}

// Get returns one batch inside the scope. A batch belonging to another district
// is ErrNotFound, not ErrForbidden — existence is scoped information here as
// much as it is on the register.
func (s *Imports) Get(ctx context.Context, sc auth.Scope, id int64) (domain.Batch, error) {
	q := `SELECT ` + batchColumns + batchFrom + ` WHERE b.id = $1`
	args := []any{id}

	if frag, extra := sc.Filter("b.district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}

	b, err := scanBatch(s.pool.QueryRow(ctx, q, args...))
	if err != nil {
		return domain.Batch{}, fmt.Errorf("get import batch %d: %w", id, translate(err))
	}
	return b, nil
}

// List returns recent batches inside the scope, pending ones first. An
// abandoned upload should nag from the top of the page rather than sink out of
// sight, because nothing sweeps it away.
func (s *Imports) List(ctx context.Context, sc auth.Scope, limit int) ([]domain.Batch, error) {
	if limit <= 0 || limit > 200 {
		limit = 25
	}

	q := `SELECT ` + batchColumns + batchFrom + ` WHERE true`
	var args []any

	if frag, extra := sc.Filter("b.district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}
	args = append(args, limit)
	q += fmt.Sprintf(`
	     ORDER BY (b.status = 'pending') DESC, b.created_at DESC
	     LIMIT $%d`, len(args))

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list import batches: %w", translate(err))
	}
	defer rows.Close()

	var out []domain.Batch
	for rows.Next() {
		b, err := scanBatch(rows)
		if err != nil {
			return nil, fmt.Errorf("scan import batch: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// stageChunk bounds one insert. The whole file travels as six array parameters
// regardless of its length, so this is about the size of a single message
// rather than about round trips.
const stageChunk = 2000

// Stage writes the validated rows and retallies the batch. It is the only way
// rows enter a batch: an upload validates the whole file, stages it, and the
// operator reads what was staged.
//
// Rows travel as arrays through unnest rather than through CopyFrom, because
// COPY cannot cast and the enum column would otherwise need its OID registered
// in the type map — the convention here is to cast in SQL and register nothing.
func (s *Imports) Stage(ctx context.Context, sc auth.Scope, batchID int64, rows []domain.ImportRow) error {
	if _, err := s.Get(ctx, sc, batchID); err != nil {
		return err
	}

	for start := 0; start < len(rows); start += stageChunk {
		end := min(start+stageChunk, len(rows))

		chunk := rows[start:end]
		numbers := make([]int32, len(chunk))
		raws := make([]string, len(chunk))
		statuses := make([]string, len(chunk))
		locations := make([]*int64, len(chunk))
		problems := make([]string, len(chunk))

		for i, r := range chunk {
			raw, err := json.Marshal(r.Raw)
			if err != nil {
				return fmt.Errorf("stage row %d: raw: %w", r.Number, err)
			}
			problem, err := json.Marshal(problemsOrEmpty(r.Problems))
			if err != nil {
				return fmt.Errorf("stage row %d: problems: %w", r.Number, err)
			}
			numbers[i] = int32(r.Number)
			raws[i] = string(raw)
			statuses[i] = string(r.Status)
			locations[i] = r.LocationID
			problems[i] = string(problem)
		}

		const q = `
		    INSERT INTO import_rows (batch_id, row_number, raw, status, location_id, problems)
		    SELECT $1, r.number, r.raw::jsonb, r.status::import_row_status,
		           r.location_id, r.problems::jsonb
		      FROM unnest($2::int[], $3::text[], $4::text[], $5::bigint[], $6::text[])
		           AS r(number, raw, status, location_id, problems)`

		if _, err := s.pool.Exec(ctx, q, batchID, numbers, raws, statuses, locations, problems); err != nil {
			return fmt.Errorf("stage rows %d-%d of batch %d: %w", start, end, batchID, translate(err))
		}
	}

	return s.retally(ctx, s.pool, batchID)
}

// Rows reads staged rows, optionally narrowed to a set of verdicts — the report
// asks for one group at a time, and errors.csv asks for the refused ones.
//
// Paging is by row number, which is already the natural order and is unique
// within a batch, so it needs no cursor of its own.
func (s *Imports) Rows(ctx context.Context, sc auth.Scope, batchID int64,
	statuses []domain.RowStatus, afterRow, limit int) ([]domain.ImportRow, error) {

	if _, err := s.Get(ctx, sc, batchID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 5000 {
		limit = 200
	}

	q := `
	    SELECT batch_id, row_number, raw, status::text, location_id, problems, chw_id
	      FROM import_rows
	     WHERE batch_id = $1 AND row_number > $2`
	args := []any{batchID, afterRow}

	if len(statuses) > 0 {
		wanted := make([]string, len(statuses))
		for i, st := range statuses {
			wanted[i] = string(st)
		}
		args = append(args, wanted)
		q += fmt.Sprintf(" AND status = ANY($%d::import_row_status[])", len(args))
	}
	args = append(args, limit)
	q += fmt.Sprintf(" ORDER BY row_number LIMIT $%d", len(args))

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("read rows of batch %d: %w", batchID, translate(err))
	}
	defer rows.Close()

	var out []domain.ImportRow
	for rows.Next() {
		var r domain.ImportRow
		var status string
		var raw, problems []byte
		var number int32
		if err := rows.Scan(&r.BatchID, &number, &raw, &status, &r.LocationID, &problems, &r.CHWID); err != nil {
			return nil, fmt.Errorf("scan import row: %w", err)
		}
		r.Number = int(number)
		r.Status = domain.RowStatus(status)
		if err := json.Unmarshal(raw, &r.Raw); err != nil {
			return nil, fmt.Errorf("import row %d raw: %w", r.Number, err)
		}
		if err := json.Unmarshal(problems, &r.Problems); err != nil {
			return nil, fmt.Errorf("import row %d problems: %w", r.Number, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarkImportedTx records that a staged row became a register record, inside the
// transaction that created it. That is the whole point of the Tx pair: a
// process killed mid-commit cannot leave a CHW on the register whose import row
// still reads "ready" and would be created again on the next attempt.
func (s *Imports) MarkImportedTx(ctx context.Context, tx pgx.Tx, batchID int64, rowNumber int, chwID int64) error {
	_, err := tx.Exec(ctx, `
	    UPDATE import_rows SET status = 'imported', chw_id = $3
	     WHERE batch_id = $1 AND row_number = $2`, batchID, rowNumber, chwID)
	if err != nil {
		return fmt.Errorf("mark row %d of batch %d imported: %w", rowNumber, batchID, translate(err))
	}
	return nil
}

// MarkRow records any other outcome a commit reaches: a warned row the operator
// chose to skip, or one that passed validation and then lost a race.
//
// A refusal must carry its problems — import_rows_refusal_explained is the
// schema saying the same thing invariant 7 does.
func (s *Imports) MarkRow(ctx context.Context, batchID int64, rowNumber int,
	status domain.RowStatus, problems []domain.Problem) error {

	encoded, err := json.Marshal(problemsOrEmpty(problems))
	if err != nil {
		return fmt.Errorf("mark row %d: problems: %w", rowNumber, err)
	}

	_, err = s.pool.Exec(ctx, `
	    UPDATE import_rows
	       SET status = $3::import_row_status,
	           problems = CASE WHEN jsonb_array_length($4::jsonb) > 0
	                           THEN $4::jsonb ELSE problems END
	     WHERE batch_id = $1 AND row_number = $2`,
		batchID, rowNumber, string(status), encoded)
	if err != nil {
		return fmt.Errorf("mark row %d of batch %d %s: %w", rowNumber, batchID, status, translate(err))
	}
	return nil
}

// Quarantine copies a refused row into import_quarantine, where it stays after
// the batch's own rows are anybody's to prune. import_rows answers "what
// happened in batch 46"; this answers "which rows never made it in, and why",
// which is the question invariant 7 exists for.
func (s *Imports) Quarantine(ctx context.Context, batchID int64, r domain.ImportRow) error {
	raw, err := json.Marshal(r.Raw)
	if err != nil {
		return fmt.Errorf("quarantine row %d: raw: %w", r.Number, err)
	}

	blocking := r.Blocking()
	if len(blocking) == 0 {
		return fmt.Errorf("quarantine row %d: nothing to explain the refusal", r.Number)
	}
	candidates, err := json.Marshal(blocking[0].Candidates)
	if err != nil {
		return fmt.Errorf("quarantine row %d: candidates: %w", r.Number, err)
	}

	_, err = s.pool.Exec(ctx, `
	    INSERT INTO import_quarantine (source, row_ref, payload, reason, detail, candidates, batch_id)
	    VALUES ('chw_csv', $1, $2::jsonb, $3, $4,
	            nullif($5,'null')::jsonb, $6)`,
		fmt.Sprint(r.Number), raw, string(blocking[0].Code), blocking[0].Message,
		string(candidates), batchID)
	if err != nil {
		return fmt.Errorf("quarantine row %d of batch %d: %w", r.Number, batchID, translate(err))
	}
	return nil
}

// Commit closes a batch once its rows have been written. The rows themselves
// are created by the caller through CHWs.CreateTx — this records the decision
// and the final tally, and writes the batch's audit row.
func (s *Imports) Commit(ctx context.Context, sc auth.Scope, actor domain.User,
	id int64, skipDuplicates bool, ip netip.Addr) (domain.Batch, error) {

	return s.finish(ctx, sc, actor, id, domain.BatchCommitted, skipDuplicates, ActionImportCommit, ip)
}

// Discard closes a batch nobody will import. The staged rows stay: discarding
// is a decision, and the record that a file was uploaded and turned down is
// worth as much as the record of one that landed.
func (s *Imports) Discard(ctx context.Context, sc auth.Scope, actor domain.User,
	id int64, ip netip.Addr) (domain.Batch, error) {

	return s.finish(ctx, sc, actor, id, domain.BatchDiscarded, false, ActionImportDiscard, ip)
}

func (s *Imports) finish(ctx context.Context, sc auth.Scope, actor domain.User, id int64,
	status domain.BatchStatus, skipDuplicates bool, action string, ip netip.Addr) (domain.Batch, error) {

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Batch{}, fmt.Errorf("%s batch %d: %w", action, id, err)
	}
	defer tx.Rollback(ctx)

	before, err := s.getTx(ctx, tx, sc, id)
	if err != nil {
		return domain.Batch{}, err
	}
	if !before.Pending() {
		// A double submit lands here, and the second one is not an error; but
		// a batch already committed must not be re-decided as discarded.
		if before.Status == status {
			return before, nil
		}
		return domain.Batch{}, fmt.Errorf("%s batch %d already %s: %w",
			action, id, before.Status, domain.ErrConflict)
	}

	if err := s.retally(ctx, tx, id); err != nil {
		return domain.Batch{}, err
	}

	q := `
	    WITH updated AS (
	        UPDATE import_batches b SET
	            status = $2::import_batch_status,
	            skip_duplicates = $3,
	            committed_at = CASE WHEN $2 = 'committed' THEN now() ELSE NULL END
	        WHERE b.id = $1`
	args := []any{id, string(status), skipDuplicates}

	if frag, extra := sc.Filter("b.district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}
	q += `
	        RETURNING *
	    )
	    SELECT ` + batchColumns + `
	    FROM updated b
	    JOIN users u ON u.id = b.uploaded_by
	    LEFT JOIN locations d ON d.id = b.district_id`

	after, err := scanBatch(tx.QueryRow(ctx, q, args...))
	if err != nil {
		return domain.Batch{}, fmt.Errorf("%s batch %d: %w", action, id, translate(err))
	}

	if err := s.auditTx(ctx, tx, actor, action, after, auditBatch(before), auditBatch(after), ip); err != nil {
		return domain.Batch{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Batch{}, fmt.Errorf("%s batch %d: %w", action, id, err)
	}
	return after, nil
}

// retally recomputes the batch's counts from its rows. They are stored rather
// than counted on every read because the listing shows them for every batch,
// and derived from the rows rather than accumulated by the caller because a
// counter the caller maintains is a counter that drifts.
func (s *Imports) retally(ctx context.Context, q execer, batchID int64) error {
	_, err := q.Exec(ctx, `
	    UPDATE import_batches b SET
	        total_rows    = t.total,
	        ready_rows    = t.ready,
	        warning_rows  = t.warning,
	        rejected_rows = t.rejected,
	        imported_rows = t.imported
	    FROM (
	        SELECT count(*)                                      AS total,
	               count(*) FILTER (WHERE status = 'ready')      AS ready,
	               count(*) FILTER (WHERE status = 'warning')    AS warning,
	               count(*) FILTER (WHERE status IN ('rejected','failed')) AS rejected,
	               count(*) FILTER (WHERE status = 'imported')   AS imported
	          FROM import_rows WHERE batch_id = $1
	    ) t
	    WHERE b.id = $1`, batchID)
	if err != nil {
		return fmt.Errorf("retally batch %d: %w", batchID, translate(err))
	}
	return nil
}

// getTx reads a batch inside a transaction, applying the scope, so a decision
// can compare before and after against one snapshot.
func (s *Imports) getTx(ctx context.Context, tx pgx.Tx, sc auth.Scope, id int64) (domain.Batch, error) {
	q := `SELECT ` + batchColumns + batchFrom + ` WHERE b.id = $1`
	args := []any{id}

	if frag, extra := sc.Filter("b.district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}

	b, err := scanBatch(tx.QueryRow(ctx, q, args...))
	if err != nil {
		return domain.Batch{}, fmt.Errorf("get import batch %d: %w", id, translate(err))
	}
	return b, nil
}

func (s *Imports) auditTx(ctx context.Context, tx pgx.Tx, actor domain.User, action string,
	b domain.Batch, before, after any, ip netip.Addr) error {

	e := ActorFrom(actor)
	e.Action = action
	e.Entity = "import_batch"
	e.EntityID = &b.ID
	e.DistrictID = b.DistrictID // the batch's district, so a district admin reads its own uploads
	e.Before = before
	e.After = after
	e.IP = ip
	return recordOn(ctx, tx, e)
}

// auditBatch is the JSONB shape written to audit_log: the decision and the
// tally, which is what a reader of the history wants to know about an upload.
// The rows themselves are not copied — each imported CHW writes its own
// chw.create row, and that is where the register's change history lives.
func auditBatch(b domain.Batch) map[string]any {
	return map[string]any{
		"id":              b.ID,
		"filename":        b.Filename,
		"format":          b.Format,
		"status":          string(b.Status),
		"district_id":     b.DistrictID,
		"total_rows":      b.Total,
		"ready_rows":      b.Ready,
		"warning_rows":    b.Warning,
		"rejected_rows":   b.Rejected,
		"imported_rows":   b.Imported,
		"skip_duplicates": b.SkipDuplicates,
	}
}

// problemsOrEmpty keeps a nil slice out of the JSONB column: the schema counts
// the array to insist a refusal is explained, and `null` has no length.
func problemsOrEmpty(p []domain.Problem) []domain.Problem {
	if p == nil {
		return []domain.Problem{}
	}
	return p
}
