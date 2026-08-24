-- +goose Up
-- 0006: bulk import staging.
--
-- An upload validates every row and writes nothing to `chws`. It stages the
-- rows here, the operator reads the report, and only then does a commit turn
-- ready rows into register records. See docs/import.md.
--
-- The staged rows are a table rather than session state or a re-read of the
-- file, because the register moves between the two requests — a NIN gets
-- claimed, a location is deactivated — and what is committed has to be the
-- thing that was reviewed.
--
-- Nothing here is swept on a timer. A pending batch's rows are the only copy
-- there is: rejects reach import_quarantine at commit, so discarding one before
-- then would destroy the only record that an upload was attempted and refused.

CREATE TYPE import_batch_status AS ENUM ('pending','committed','discarded');

-- ready | warning | rejected are the verdicts an upload produces; imported |
-- skipped | failed are what a commit turns the first two into. A warned row is
-- imported unless the operator asked to skip its kind; a failed row passed
-- validation and then lost a race at commit.
CREATE TYPE import_row_status AS ENUM
    ('ready','warning','rejected','imported','skipped','failed');

CREATE TABLE import_batches (
    id          bigserial PRIMARY KEY,
    filename    text NOT NULL,
    format      text NOT NULL CHECK (format IN ('csv','xlsx')),
    uploaded_by bigint NOT NULL REFERENCES users(id),
    -- The uploader's scope at upload time, NULL for a national one. Batches are
    -- read through this, so a district cannot open another's report or its
    -- error file. It is recorded rather than re-derived because a user's role
    -- can change afterwards and the batch's reach cannot.
    district_id bigint REFERENCES locations(id),
    status      import_batch_status NOT NULL DEFAULT 'pending',

    -- The header exactly as the file spelled it, in order. errors.csv is
    -- rebuilt from this, so a district gets their own columns back rather than
    -- ours.
    columns     jsonb NOT NULL DEFAULT '[]'::jsonb,

    total_rows    integer NOT NULL DEFAULT 0,
    ready_rows    integer NOT NULL DEFAULT 0,
    warning_rows  integer NOT NULL DEFAULT 0,
    rejected_rows integer NOT NULL DEFAULT 0,
    imported_rows integer NOT NULL DEFAULT 0,
    -- The choice made at commit, kept because it explains the difference
    -- between warning_rows and imported_rows a month later.
    skip_duplicates boolean NOT NULL DEFAULT false,

    created_at   timestamptz NOT NULL DEFAULT now(),
    committed_at timestamptz,
    -- Same shape as chws_deactivation_complete: a status and its timestamp
    -- cannot disagree.
    CONSTRAINT import_batches_commit_complete CHECK (
        (status = 'committed') = (committed_at IS NOT NULL)
    )
);

-- The scoped listing, newest first.
CREATE INDEX import_batches_district_idx ON import_batches (district_id, created_at DESC);
-- Pending batches sort first on that listing, so an abandoned upload nags
-- rather than disappears.
CREATE INDEX import_batches_pending_idx ON import_batches (created_at DESC)
    WHERE status = 'pending';

-- the district_id above must actually BE a district
-- +goose StatementBegin
CREATE FUNCTION import_batches_check_district_level() RETURNS trigger AS $$
BEGIN
    IF NEW.district_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM locations WHERE id = NEW.district_id AND level = 'district'
    ) THEN
        RAISE EXCEPTION 'import batch district_id % is not a district', NEW.district_id;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER import_batches_district_level_trg BEFORE INSERT OR UPDATE ON import_batches
    FOR EACH ROW EXECUTE FUNCTION import_batches_check_district_level();

CREATE TABLE import_rows (
    batch_id   bigint  NOT NULL REFERENCES import_batches(id) ON DELETE CASCADE,
    -- The line number in the source file, counting the header, so a message
    -- points at what the operator sees in Excel.
    row_number integer NOT NULL,

    -- Exactly as it arrived: keys as the file spelled them, values untouched.
    -- A rejected row has to be explainable a month later, and "what did the
    -- file actually say" is the first question.
    raw    jsonb NOT NULL,
    status import_row_status NOT NULL,

    -- The resolved placement. Its level is the cadre's business, checked by the
    -- importer and then by chws_set_placement on the way in.
    location_id bigint REFERENCES locations(id),
    -- [{"field":…,"code":…,"message":…,"candidates":…}]
    problems    jsonb NOT NULL DEFAULT '[]'::jsonb,
    -- Set once the row becomes a register record.
    chw_id      bigint REFERENCES chws(id),

    PRIMARY KEY (batch_id, row_number),
    CONSTRAINT import_rows_imported_has_chw CHECK (
        (status = 'imported') = (chw_id IS NOT NULL)
    ),
    -- A refusal without a stated reason is the silent drop invariant 7 exists
    -- to forbid.
    CONSTRAINT import_rows_refusal_explained CHECK (
        status NOT IN ('rejected','failed') OR jsonb_array_length(problems) > 0
    )
);

-- The report groups by verdict; the commit walks the ready and warned rows.
CREATE INDEX import_rows_status_idx ON import_rows (batch_id, status);

-- import_quarantine has been waiting for this since 0001. Linking it to the
-- batch is what makes the permanent record traceable back to the upload that
-- produced it; it stays nullable, because the hierarchy and facility seeders
-- write here too and have no batch.
ALTER TABLE import_quarantine
    ADD COLUMN batch_id bigint REFERENCES import_batches(id);

CREATE INDEX quarantine_batch_idx ON import_quarantine (batch_id) WHERE batch_id IS NOT NULL;

