-- +goose Up
-- 0002: system users, sessions, audit trail

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
    created_by     bigint REFERENCES users(id),
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
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

CREATE TRIGGER users_district_level_trg BEFORE INSERT OR UPDATE ON users
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

-- Every mutation lands here. Doubles as the change history for CHW records,
-- which is why a separate versioning table is not needed in v1.
CREATE TABLE audit_log (
    id          bigserial PRIMARY KEY,
    actor_id    bigint REFERENCES users(id),
    actor_email citext,                       -- denormalized: survives user deletion
    action      text NOT NULL,                -- 'chw.create' | 'chw.update' | 'chw.deactivate' | 'user.create' | 'auth.login' ...
    entity      text NOT NULL,                -- 'chw' | 'user' | 'location'
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
