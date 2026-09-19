-- +goose Up
-- 0003: health workers, cadres, deployments
--
-- The register is of PEOPLE, not postings. `health_workers` carries only who
-- the person is. What they do and where they do it is a `deployments` row:
-- a posting in a cadre at a location, over a period. A transfer or a promotion
-- ends one row and opens another, so the register itself answers "who was
-- deployed at X on date D" — previously knowable only from audit JSONB.
--
-- Cadres are DATA, not an enum: a two-level taxonomy of category (Community
-- Health Workers) and type (VHT, CHEW). The placement rule — which level of
-- the hierarchy a type serves — is a column on the type, so adding a cadre is
-- an INSERT, not a migration. Each category owns its profile surface
-- (0004_chw_profile); a future category gets its own.

CREATE TYPE sex           AS ENUM ('male','female');
CREATE TYPE worker_status AS ENUM ('active','inactive');

------------------------------------------------------------------ the person
CREATE TABLE health_workers (
    id          bigserial PRIMARY KEY,
    -- Optional in the source form and labelled "NIN / Alternative No", so it is
    -- nullable; unique only where present. Regex is the form's own constraint.
    nin         text CHECK (nin ~ '^[A-Z]{2}[A-Z0-9]{11}[A-Z]$'),
    first_name  text NOT NULL,
    last_name   text NOT NULL,
    sex         sex  NOT NULL,
    -- age is a snapshot, not a fact: this records when it was true.
    -- ODK provenance is stripped, so imports stamp this with the import date.
    age_years   smallint CHECK (age_years BETWEEN 18 AND 99),
    age_captured_on date NOT NULL DEFAULT current_date,
    -- The district that owns this record for scoping. Derived by trigger from
    -- the worker's deployments and never supplied: it tracks the latest
    -- posting, and it survives deactivation, which a join against the active
    -- deployment could not — a district must still see the workers who left.
    -- NULL only inside the transaction that creates the worker, before their
    -- first deployment lands.
    district_id bigint REFERENCES locations(id),
    -- Workers are never deleted. Deactivation means "left the workforce"; it
    -- requires every deployment to have ended first (trigger below).
    status      worker_status NOT NULL DEFAULT 'active',
    deactivated_at     timestamptz,
    deactivation_reason text,
    created_by  bigint REFERENCES users(id),
    updated_by  bigint REFERENCES users(id),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT health_workers_deactivation_complete CHECK (
        (status = 'inactive') = (deactivated_at IS NOT NULL)
    )
);

CREATE UNIQUE INDEX health_workers_nin_uniq   ON health_workers (nin) WHERE nin IS NOT NULL;
CREATE INDEX health_workers_district_idx      ON health_workers (district_id);
CREATE INDEX health_workers_name_trgm         ON health_workers USING gin ((first_name || ' ' || last_name) gin_trgm_ops);
-- The national listing sorts case-insensitively and pages with a keyset on
-- this expression; the register holds "Okello", "OKELLO" and "okello" for the
-- same surname convention.
CREATE INDEX health_workers_name_sort_idx     ON health_workers (lower(last_name), lower(first_name), id);
-- NIN lookup by prefix: a clerk types the first characters and expects the
-- match to narrow; text_pattern_ops makes LIKE 'CM90%' an index scan.
CREATE INDEX health_workers_nin_prefix_idx    ON health_workers (nin text_pattern_ops) WHERE nin IS NOT NULL;

---------------------------------------------------- cadre taxonomy, as data
CREATE TABLE cadre_categories (
    id         smallserial PRIMARY KEY,
    slug       text NOT NULL UNIQUE,
    label      text NOT NULL,
    sort_order smallint NOT NULL DEFAULT 0,
    active     boolean NOT NULL DEFAULT true
);

CREATE TABLE cadres (
    id              smallserial PRIMARY KEY,
    category_id     smallint NOT NULL REFERENCES cadre_categories(id),
    slug            text NOT NULL,
    label           text NOT NULL,
    -- Which hierarchy level a deployment in this cadre serves. The placement
    -- trigger reads it from here; there is no CASE left to edit.
    placement_level location_level NOT NULL,
    -- Spellings an import may use for this cadre, matched case-folded with
    -- separators stripped, beside the slug itself.
    import_aliases  text[] NOT NULL DEFAULT '{}',
    sort_order      smallint NOT NULL DEFAULT 0,
    active          boolean NOT NULL DEFAULT true,
    UNIQUE (category_id, slug)
);

INSERT INTO cadre_categories (slug, label, sort_order)
VALUES ('chw', 'Community Health Workers', 1);

INSERT INTO cadres (category_id, slug, label, placement_level, import_aliases, sort_order)
SELECT c.id, v.slug, v.label, v.placement_level::location_level, v.aliases, v.ord
  FROM cadre_categories c
  JOIN (VALUES
        ('vht',  'Village Health Team member',       'village', ARRAY['village health team'],                          1::smallint),
        ('chew', 'Community Health Extension Worker', 'parish',  ARRAY['chw', 'community health extension worker'],     2::smallint)
       ) AS v(slug, label, placement_level, aliases, ord) ON c.slug = 'chw';

----------------------------------------------------------------- the posting
CREATE TABLE deployments (
    id               bigserial PRIMARY KEY,
    health_worker_id bigint  NOT NULL REFERENCES health_workers(id),
    cadre_id         smallint NOT NULL REFERENCES cadres(id),
    location_id      bigint  NOT NULL REFERENCES locations(id),
    district_id      bigint  NOT NULL REFERENCES locations(id),  -- derived; denormalized for RBAC scoping
    -- The supervising facility: an optional attachment, not a placement.
    -- Must sit in the deployment's own district (trigger below).
    facility_id      bigint  REFERENCES facilities(id),
    started_on       date    NOT NULL DEFAULT current_date,
    ended_on         date,
    end_reason       text,
    created_by  bigint REFERENCES users(id),
    updated_by  bigint REFERENCES users(id),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    -- A deployment ends with a reason; an open deployment carries none.
    CONSTRAINT deployments_end_complete CHECK (
        (ended_on IS NULL) = (end_reason IS NULL)
    ),
    CONSTRAINT deployments_dates_ordered CHECK (
        ended_on IS NULL OR ended_on >= started_on
    )
);

-- One active deployment per worker. A second concurrent posting is refused
-- here; relaxing that policy later is dropping this index, nothing more.
CREATE UNIQUE INDEX deployments_one_active_idx ON deployments (health_worker_id) WHERE ended_on IS NULL;
-- Every scoped read anchors on the active deployment's district.
CREATE INDEX deployments_district_idx     ON deployments (district_id) WHERE ended_on IS NULL;
CREATE INDEX deployments_worker_idx       ON deployments (health_worker_id);
CREATE INDEX deployments_location_idx     ON deployments (location_id) WHERE ended_on IS NULL;
CREATE INDEX deployments_cadre_idx        ON deployments (cadre_id);
CREATE INDEX deployments_facility_idx     ON deployments (facility_id) WHERE facility_id IS NOT NULL;

-- Placement rule + district derivation. district_id is never supplied by the
-- caller. The level a cadre serves comes from the cadres row: VHTs at village,
-- CHEWs at parish, and the next cadre at whatever its row says. An inactive
-- worker takes no new deployment; reactivate first.
-- +goose StatementBegin
CREATE FUNCTION deployments_set_placement() RETURNS trigger AS $$
DECLARE
    lvl      location_level;
    required location_level;
    w_status worker_status;
BEGIN
    SELECT placement_level INTO required FROM cadres WHERE id = NEW.cadre_id;
    IF required IS NULL THEN
        RAISE EXCEPTION 'cadre % not found', NEW.cadre_id;
    END IF;

    SELECT status INTO w_status FROM health_workers WHERE id = NEW.health_worker_id;
    IF w_status = 'inactive' AND NEW.ended_on IS NULL THEN
        RAISE EXCEPTION 'health worker % is inactive; reactivate before deploying', NEW.health_worker_id;
    END IF;

    SELECT level INTO lvl FROM locations WHERE id = NEW.location_id;
    IF lvl IS NULL THEN
        RAISE EXCEPTION 'location % not found', NEW.location_id;
    END IF;
    IF lvl <> required THEN
        RAISE EXCEPTION 'cadre % must be placed at % level, got %',
            (SELECT slug FROM cadres WHERE id = NEW.cadre_id), required, lvl;
    END IF;

    SELECT a.id INTO NEW.district_id
      FROM locations c
      JOIN locations a ON c.path LIKE a.path || '%'
     WHERE c.id = NEW.location_id AND a.level = 'district';
    IF NEW.district_id IS NULL THEN
        RAISE EXCEPTION 'location % has no district ancestor', NEW.location_id;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER deployments_placement_trg BEFORE INSERT OR UPDATE OF location_id, cadre_id, health_worker_id ON deployments
    FOR EACH ROW EXECUTE FUNCTION deployments_set_placement();

-- The facility must sit in the deployment's own district. Attachment and
-- placement share one row now, so this single AFTER trigger covers both
-- directions the old schema needed two for: attaching across districts, and
-- moving a deployment whose facility would be stranded. AFTER, because it
-- reads the district_id the placement trigger has just derived.
-- +goose StatementBegin
CREATE FUNCTION deployments_check_facility_district() RETURNS trigger AS $$
BEGIN
    IF NEW.facility_id IS NULL THEN
        RETURN NULL;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM facilities f
                    WHERE f.id = NEW.facility_id AND f.district_id = NEW.district_id) THEN
        RAISE EXCEPTION 'facility % is not in district %; attach a facility from the deployment''s own district, or move it in the same transaction',
            NEW.facility_id, NEW.district_id;
    END IF;
    RETURN NULL;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER deployments_facility_district_trg
    AFTER INSERT OR UPDATE OF facility_id, location_id, cadre_id ON deployments
    FOR EACH ROW EXECUTE FUNCTION deployments_check_facility_district();

-- Keeps the worker's district anchor pointing at their latest posting. It
-- deliberately does NOT clear on a deployment ending: the last district still
-- owns the record of a worker between postings or out of the workforce.
-- +goose StatementBegin
CREATE FUNCTION deployments_sync_worker_district() RETURNS trigger AS $$
BEGIN
    UPDATE health_workers SET district_id = NEW.district_id
     WHERE id = NEW.health_worker_id;
    RETURN NULL;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER deployments_worker_district_trg
    AFTER INSERT OR UPDATE ON deployments
    FOR EACH ROW EXECUTE FUNCTION deployments_sync_worker_district();

-- A worker with an active deployment cannot be deactivated: the application
-- ends the deployment in the same transaction first. This is what keeps
-- "inactive worker, open posting" unrepresentable.
-- +goose StatementBegin
CREATE FUNCTION health_workers_check_deactivation() RETURNS trigger AS $$
BEGIN
    IF NEW.status = 'inactive' AND OLD.status = 'active' AND EXISTS (
        SELECT 1 FROM deployments
         WHERE health_worker_id = NEW.id AND ended_on IS NULL
    ) THEN
        RAISE EXCEPTION 'health worker % has an active deployment; end it in the same transaction before deactivating',
            NEW.id;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER health_workers_deactivation_trg
    BEFORE UPDATE OF status ON health_workers
    FOR EACH ROW EXECUTE FUNCTION health_workers_check_deactivation();
