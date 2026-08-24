-- +goose Up
-- 0001: administrative hierarchy + facilities
--
-- Hierarchy source: "Village-Admin Units 06-08-2026.xlsm" (71,207 village rows),
-- authoritative from DISTRICT downward and carrying official numeric codes.
-- Verified clean: every code maps to exactly one name, no duplicate villages,
-- no sibling name collisions, and village_id equals the concatenated code path
-- in all 71,207 rows.
--
-- Regions are not in that file; the 15 regions and their district mapping come
-- from the ODK workbook (144/146 districts match by name; LUWEERO and SSEMBABULE
-- need aliases for ODK's "luwero" / "sembabule").
--
--   region 15 > district 146 > county 353 > subcounty 2198 > parish 10716 > village 71207
--
-- COUNTY IS NOT OPTIONAL: subcounty codes are unique only within a county.
-- Collapsing that tier merges 2198 subcounties into 1466 and 10716 parishes
-- into 9807. The ODK form omitted it, which is why it carried only 1990
-- subcounties and 173 unresolvable references.

CREATE EXTENSION IF NOT EXISTS citext;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TYPE location_level AS ENUM
    ('region','district','county','subcounty','parish','village');

CREATE TABLE locations (
    id         bigserial PRIMARY KEY,
    parent_id  bigint REFERENCES locations(id),
    level      location_level NOT NULL,
    name       text NOT NULL,
    code       text,                     -- official segment code; NULL for regions
    code_path  text,                     -- full concatenation, e.g. '00100301003001'
    path       text NOT NULL,            -- '/1/14/233/' surrogate-id ancestors
    active     boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (parent_id, code),
    -- deliberately NOT unique on (parent_id, name): the source contains one
    -- genuine case of two villages sharing a name in one parish
    -- (parish 095/235/04/038, "BUHOBA A", codes 002 and 003). Code is the
    -- identity; name is a label.
    CHECK ((level = 'region') = (parent_id IS NULL))
);

CREATE UNIQUE INDEX locations_code_path_uniq ON locations (code_path) WHERE code_path IS NOT NULL;
CREATE INDEX locations_parent_idx ON locations (parent_id);
CREATE INDEX locations_level_idx  ON locations (level);
CREATE INDEX locations_path_idx   ON locations (path text_pattern_ops);
CREATE INDEX locations_name_trgm  ON locations USING gin (name gin_trgm_ops);

-- Enforces the ladder and materializes `path`. bigserial defaults are applied
-- before BEFORE-triggers fire, so NEW.id is available here.
-- +goose StatementBegin
CREATE FUNCTION locations_before_insert() RETURNS trigger AS $$
DECLARE
    expected     location_level;
    parent_level location_level;
    parent_path  text;
BEGIN
    expected := CASE NEW.level
        WHEN 'district'  THEN 'region'
        WHEN 'county'    THEN 'district'
        WHEN 'subcounty' THEN 'county'
        WHEN 'parish'    THEN 'subcounty'
        WHEN 'village'   THEN 'parish'
    END::location_level;

    IF NEW.parent_id IS NULL THEN
        NEW.path := '/' || NEW.id || '/';
    ELSE
        SELECT level, path INTO parent_level, parent_path
          FROM locations WHERE id = NEW.parent_id;
        IF parent_path IS NULL THEN
            RAISE EXCEPTION 'parent location % not found', NEW.parent_id;
        END IF;
        IF parent_level IS DISTINCT FROM expected THEN
            RAISE EXCEPTION '% must hang off a %, got a %',
                NEW.level, expected, parent_level;
        END IF;
        NEW.path := parent_path || NEW.id || '/';
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER locations_before_insert_trg BEFORE INSERT ON locations
    FOR EACH ROW EXECUTE FUNCTION locations_before_insert();

-- Facilities come from the ODK workbook (7,896 rows). Their parent column is
-- named `subcountyfilter` but 7,846 of its values are district slugs; the 21
-- that look like subcounties are quarantined rather than guessed at.
CREATE TABLE facilities (
    id          bigserial PRIMARY KEY,
    district_id bigint NOT NULL REFERENCES locations(id),
    name        text NOT NULL,
    slug        text NOT NULL,
    active      boolean NOT NULL DEFAULT true,
    UNIQUE (district_id, name)
);
CREATE INDEX facilities_district_idx ON facilities (district_id);
CREATE INDEX facilities_name_trgm    ON facilities USING gin (name gin_trgm_ops);

-- +goose StatementBegin
CREATE FUNCTION facilities_check_district_level() RETURNS trigger AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM locations WHERE id = NEW.district_id AND level = 'district') THEN
        RAISE EXCEPTION 'facility district_id % is not a district', NEW.district_id;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER facilities_district_level_trg BEFORE INSERT OR UPDATE ON facilities
    FOR EACH ROW EXECUTE FUNCTION facilities_check_district_level();

-- Rows the seeders could not resolve. Nothing is dropped silently.
-- The admin-units hierarchy loads clean; expected occupants are the ~21
-- facilities whose parent slug matches a subcounty rather than a district,
-- plus rejected rows from CHW imports.
CREATE TABLE import_quarantine (
    id          bigserial PRIMARY KEY,
    source      text NOT NULL,          -- 'admin_units' | 'facilities' | 'chw_csv'
    row_ref     text,                   -- source row number or natural key
    payload     jsonb NOT NULL,
    reason      text NOT NULL,          -- 'parent_missing' | 'parent_ambiguous' | 'parent_level_mismatch' | 'validation_failed'
    detail      text,
    candidates  jsonb,
    resolved_at timestamptz,
    resolved_by bigint,
    imported_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX quarantine_unresolved_idx ON import_quarantine (source, reason) WHERE resolved_at IS NULL;
