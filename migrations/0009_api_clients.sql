-- +goose Up
-- 0009: read-only interoperability API — machine clients, bearer tokens, access log.
--
-- Additive and independent of the browser session model in 0002. A consumer
-- (the eCHIS user-management tool, the National Data Warehouse) presents a
-- client_id and secret to mint a short-lived bearer token; only the token's
-- SHA-256 is stored, exactly as `sessions` stores the hash of a cookie and
-- never the cookie itself. The clients read the register; they never write it.

CREATE TYPE api_client_scope AS ENUM ('national','district');

CREATE TABLE api_clients (
    id           bigserial PRIMARY KEY,
    name         text NOT NULL,                     -- human label, e.g. "eCHIS UMT" or "National Data Warehouse"
    client_id    citext NOT NULL UNIQUE,            -- the credential the consumer sends (the config's "email")
    secret_hash  text NOT NULL,                     -- argon2id, the same scheme as users.password_hash
    scope        api_client_scope NOT NULL DEFAULT 'national',
    district_id  bigint REFERENCES locations(id),   -- present iff scope = 'district'
    status       user_status NOT NULL DEFAULT 'active',
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    -- Scope integrity, mirroring users_scope_matches_role: a district client
    -- REQUIRES a district; a national client forbids one.
    CONSTRAINT api_clients_scope_matches CHECK (
        (scope = 'district') = (district_id IS NOT NULL)
    )
);

-- district_id, when present, must actually BE a district (mirrors users).
-- +goose StatementBegin
CREATE FUNCTION api_clients_check_district_level() RETURNS trigger AS $$
BEGIN
    IF NEW.district_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM locations WHERE id = NEW.district_id AND level = 'district'
    ) THEN
        RAISE EXCEPTION 'api client district_id % is not a district', NEW.district_id;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER api_clients_district_level_trg BEFORE INSERT OR UPDATE ON api_clients
    FOR EACH ROW EXECUTE FUNCTION api_clients_check_district_level();

-- Bearer tokens. Rows rather than JWTs, for the same reason sessions are rows:
-- a client can be cut off the instant its access is revoked (DELETE), without a
-- denylist. The raw token lives only in the consumer's memory; the digest is
-- all that is stored.
CREATE TABLE api_tokens (
    token_hash   bytea PRIMARY KEY,                 -- sha256 of the bearer token
    client_id    bigint NOT NULL REFERENCES api_clients(id) ON DELETE CASCADE,
    issued_at    timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    last_used_at timestamptz NOT NULL DEFAULT now(),
    ip           inet
);
CREATE INDEX api_tokens_client_idx ON api_tokens (client_id);
CREATE INDEX api_tokens_expiry_idx ON api_tokens (expires_at);

-- Read-access log. CHWR audits mutations in audit_log; the register carried no
-- record of who READ it, which a machine API that exposes personal data needs.
-- This is that record: one row per API request that reached a handler.
CREATE TABLE api_access_log (
    id          bigserial PRIMARY KEY,
    client_id   bigint REFERENCES api_clients(id),  -- null for an unauthenticated attempt
    client_name text,                               -- denormalized: survives client deletion
    method      text NOT NULL,
    path        text NOT NULL,
    query       text,
    status      smallint NOT NULL,
    row_count   integer,                            -- CHWs returned, where the handler counts them
    ip          inet,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX api_access_client_idx ON api_access_log (client_id, created_at DESC);
CREATE INDEX api_access_time_idx   ON api_access_log (created_at DESC);
