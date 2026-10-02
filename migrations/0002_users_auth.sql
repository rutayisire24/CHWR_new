-- +goose Up
-- 0002: system users, sessions, audit trail — and the record columns
--
-- Every record table carries five columns, the same five, at the end:
--
--     uuid             a stable identity to exchange with other systems; the
--                      bigint id stays the key everything here joins on
--     created_on       when the row was written
--     created_by       who wrote it (users.id; NULL for a seed or the system)
--     last_updated_on  when it last changed
--     last_updated_by  who changed it (NULL for the system)
--
-- They are filled by trigger, never by hand. The store names the acting user
-- once per transaction (`SET LOCAL hwr.actor_id`), and stamp_row() copies it
-- onto every row the transaction writes, so a statement cannot forget to.
-- That makes the columns structural the way audit_log is (invariant 6), and it
-- is why no SQL in internal/store spells `updated_at = now()` any more.
--
-- Logs are not records and carry none of this: sessions, audit_log, the
-- worker-code counters and the staged import rows (whose batch carries it).

CREATE TYPE user_role AS ENUM
    ('national_admin','national_viewer','district_manager','district_viewer');

CREATE TYPE user_status AS ENUM ('active','disabled');

CREATE TABLE users (
    id             bigserial PRIMARY KEY,
    email          citext NOT NULL UNIQUE,
    full_name      text NOT NULL,
    password_hash  text NOT NULL,              -- argon2id
    must_reset     boolean NOT NULL DEFAULT true,
    role           user_role NOT NULL,
    district_id    bigint REFERENCES locations(id),
    status         user_status NOT NULL DEFAULT 'active',
    last_login_at  timestamptz,
    -- scope integrity: district roles REQUIRE a district, national roles forbid one.
    -- Enforced here so no application bug can produce an unscoped district user.
    CONSTRAINT users_scope_matches_role CHECK (
        (role IN ('district_manager','district_viewer')) = (district_id IS NOT NULL)
    )
);
CREATE INDEX users_district_idx ON users (district_id);

-- the district_id above must actually BE a district
-- +goose StatementBegin
CREATE FUNCTION users_check_district_level() RETURNS trigger AS $$
BEGIN
    IF NEW.district_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM locations WHERE id = NEW.district_id AND level = 'district'
    ) THEN
        RAISE EXCEPTION 'user district_id % is not a district', NEW.district_id;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER users_district_level_trg BEFORE INSERT OR UPDATE OF district_id ON users
    FOR EACH ROW EXECUTE FUNCTION users_check_district_level();

-- Server-side sessions: revocation must be instant when staff leave.
CREATE TABLE sessions (
    token_hash   bytea PRIMARY KEY,          -- sha256 of the cookie value; raw token never stored
    user_id      bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    issued_at    timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    ip           inet,
    user_agent   text
);
CREATE INDEX sessions_user_idx    ON sessions (user_id);
CREATE INDEX sessions_expiry_idx  ON sessions (expires_at);

-- Every mutation lands here. Doubles as the change history for register
-- records, which is why a separate versioning table is not needed.
CREATE TABLE audit_log (
    id          bigserial PRIMARY KEY,
    actor_id    bigint REFERENCES users(id),
    actor_email citext,                       -- denormalized: survives user deletion
    action      text NOT NULL,                -- 'health_worker.create' | 'profile.submit' | 'user.create' | 'auth.login' ...
    entity      text NOT NULL,                -- 'health_worker' | 'deployment' | 'user' | 'cadre' ...
    entity_id   bigint,
    district_id bigint REFERENCES locations(id),  -- lets district admins read their own slice
    before      jsonb,
    after       jsonb,
    ip          inet,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_entity_idx   ON audit_log (entity, entity_id, created_at DESC);
CREATE INDEX audit_actor_idx    ON audit_log (actor_id, created_at DESC);
CREATE INDEX audit_district_idx ON audit_log (district_id, created_at DESC);

------------------------------------------------------------ record columns
-- The actor is read with missing_ok, so a psql session or a seed that never
-- named one writes NULL — "the system" — rather than failing. When no actor is
-- named, whatever the statement itself left in created_by / last_updated_by
-- stands; the store always names one, so that path is for psql and seeds.
-- +goose StatementBegin
CREATE FUNCTION stamp_row() RETURNS trigger AS $$
DECLARE
    actor bigint := nullif(current_setting('hwr.actor_id', true), '')::bigint;
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.created_on      := now();
        NEW.last_updated_on := NEW.created_on;
        NEW.created_by      := coalesce(actor, NEW.created_by);
        NEW.last_updated_by := NEW.created_by;
        RETURN NEW;
    END IF;

    IF NEW.uuid IS DISTINCT FROM OLD.uuid
       OR NEW.created_on IS DISTINCT FROM OLD.created_on
       OR NEW.created_by IS DISTINCT FROM OLD.created_by THEN
        RAISE EXCEPTION '%: uuid, created_on and created_by are fixed when the row is written',
            TG_TABLE_NAME;
    END IF;
    NEW.last_updated_on := now();
    NEW.last_updated_by := coalesce(actor, NEW.last_updated_by);
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- record_columns gives a table the five columns and the two triggers that fill
-- them. One function rather than five lines per table, because "every record
-- table carries the same columns" is one rule and belongs in one place.
--
-- The update trigger fires only when the row actually changed, so a trigger
-- re-asserting a value it already holds (the district sync) does not count as
-- an edit.
-- +goose StatementBegin
CREATE FUNCTION record_columns(tbl regclass) RETURNS void AS $$
DECLARE
    name text := (SELECT relname FROM pg_class WHERE oid = tbl);
BEGIN
    EXECUTE format($f$
        ALTER TABLE %1$s
            ADD COLUMN uuid            uuid NOT NULL DEFAULT gen_random_uuid(),
            ADD COLUMN created_on      timestamptz NOT NULL DEFAULT now(),
            ADD COLUMN created_by      bigint REFERENCES users(id),
            ADD COLUMN last_updated_on timestamptz NOT NULL DEFAULT now(),
            ADD COLUMN last_updated_by bigint REFERENCES users(id),
            ADD CONSTRAINT %2$I UNIQUE (uuid)$f$, tbl, name || '_uuid_key');
    EXECUTE format('CREATE TRIGGER %I BEFORE INSERT ON %s FOR EACH ROW EXECUTE FUNCTION stamp_row()',
        name || '_stamp_insert_trg', tbl);
    EXECUTE format('CREATE TRIGGER %I BEFORE UPDATE ON %s FOR EACH ROW WHEN (OLD.* IS DISTINCT FROM NEW.*) EXECUTE FUNCTION stamp_row()',
        name || '_stamp_update_trg', tbl);
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

SELECT record_columns('locations');
SELECT record_columns('facilities');
SELECT record_columns('users');

-- District is a level of `locations`, not a table of its own: every derived
-- district_id, every user's scope and every facility's parent point at a
-- locations row, and the path walk that derives them cannot cross into a
-- second table. This view is the district as an entity, for anything that
-- wants one by name.
CREATE VIEW districts AS
SELECT d.id, d.uuid, d.code, d.name, d.parent_id AS region_id, d.active,
       d.created_on, d.created_by, d.last_updated_on, d.last_updated_by
  FROM locations d
 WHERE d.level = 'district';
