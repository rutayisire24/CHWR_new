-- +goose Up
-- 0003: the CHW register
-- Core identity on `chws`; optional survey attributes on `chw_profiles`;
-- multi-valued answers in junction tables.

CREATE TYPE cadre               AS ENUM ('vht','chew');
CREATE TYPE sex                 AS ENUM ('male','female');
CREATE TYPE chw_status          AS ENUM ('active','inactive');
CREATE TYPE education_level     AS ENUM ('none','ple','uce','uace','tertiary');
CREATE TYPE incentive_frequency AS ENUM ('monthly','quarterly','annually','one_off');

------------------------------------------------------------------ core record
CREATE TABLE chws (
    id          bigserial PRIMARY KEY,
    -- Optional in the source form and labelled "NIN / Alternative No", so it is
    -- nullable; unique only where present. Regex is the form's own constraint.
    nin         text CHECK (nin ~ '^[A-Z]{2}[A-Z0-9]{11}[A-Z]$'),
    first_name  text NOT NULL,
    last_name   text NOT NULL,
    sex         sex   NOT NULL,
    cadre       cadre NOT NULL,               -- single-valued by decision; form allowed multi + other
    age_years   smallint CHECK (age_years BETWEEN 18 AND 99),
    -- age is a snapshot, not a fact: this records when it was true.
    -- ODK provenance is stripped, so imports stamp this with the import date.
    age_captured_on date NOT NULL DEFAULT current_date,
    -- Placement level is determined by cadre: CHEWs serve a parish, VHTs a
    -- village. One column, with the level enforced by trigger, rather than two
    -- nullable columns that could both be set or both be empty.
    location_id bigint NOT NULL REFERENCES locations(id),
    district_id bigint NOT NULL REFERENCES locations(id),  -- derived; denormalized for RBAC scoping
    status      chw_status NOT NULL DEFAULT 'active',
    deactivated_at     timestamptz,
    deactivation_reason text,
    created_by  bigint REFERENCES users(id),
    updated_by  bigint REFERENCES users(id),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chws_deactivation_complete CHECK (
        (status = 'inactive') = (deactivated_at IS NOT NULL)
    )
);

CREATE UNIQUE INDEX chws_nin_uniq   ON chws (nin) WHERE nin IS NOT NULL;
CREATE INDEX chws_district_idx      ON chws (district_id, status);
CREATE INDEX chws_location_idx      ON chws (location_id);
CREATE INDEX chws_cadre_idx         ON chws (cadre);
CREATE INDEX chws_name_trgm         ON chws USING gin ((first_name || ' ' || last_name) gin_trgm_ops);
-- soft duplicate detection for CHWs with no NIN
CREATE INDEX chws_dup_probe_idx     ON chws (location_id, lower(last_name), lower(first_name));

-- Placement rule + district derivation. district_id is never supplied by the
-- caller. Fires on cadre changes too: re-cadring a CHW without moving them
-- would otherwise leave them at the wrong level.
-- +goose StatementBegin
CREATE FUNCTION chws_set_placement() RETURNS trigger AS $$
DECLARE
    lvl      location_level;
    required location_level;
BEGIN
    required := CASE NEW.cadre WHEN 'chew' THEN 'parish' WHEN 'vht' THEN 'village' END::location_level;

    SELECT level INTO lvl FROM locations WHERE id = NEW.location_id;
    IF lvl IS NULL THEN
        RAISE EXCEPTION 'location % not found', NEW.location_id;
    END IF;
    IF lvl <> required THEN
        RAISE EXCEPTION 'a % must be placed at % level, got %', NEW.cadre, required, lvl;
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

CREATE TRIGGER chws_placement_trg BEFORE INSERT OR UPDATE OF location_id, cadre ON chws
    FOR EACH ROW EXECUTE FUNCTION chws_set_placement();

--------------------------------------------------------- optional attributes
CREATE TABLE chw_profiles (
    chw_id bigint PRIMARY KEY REFERENCES chws(id) ON DELETE CASCADE,

    -- Phone: the form asks "do you own a phone?" then branches. phone_primary and
    -- phone_alternate are mutually exclusive, they are not two lines for one person.
    owns_phone          boolean,
    phone_primary       text CHECK (phone_primary   ~ '^[0-9]{9}$'),
    phone_for_reporting boolean,
    phone_alternate     text CHECK (phone_alternate ~ '^[0-9]{9}$'),
    CONSTRAINT phone_branch_exclusive CHECK (
        CASE owns_phone
            WHEN true  THEN phone_alternate IS NULL
            WHEN false THEN phone_primary IS NULL AND phone_for_reporting IS NULL
            ELSE true
        END
    ),

    facility_id        bigint REFERENCES facilities(id),
    service_start_year smallint CHECK (service_start_year BETWEEN 1960 AND 2100),
    households_served  integer  CHECK (households_served BETWEEN 3 AND 100000),
    education          education_level,

    -- Form collects proficiency as a multi-select (Speak/Read/Write/None), not yes/no.
    -- A plain "speaks English" view is english_speak OR english_read OR english_write.
    english_speak boolean,
    english_read  boolean,
    english_write boolean,
    -- free text in the source ("separate languages with commas"); raw kept verbatim,
    -- parsed values land in chw_languages
    other_languages_raw text,

    receives_incentive   boolean,
    incentive_frequency  incentive_frequency,
    incentive_amount_ugx integer CHECK (incentive_amount_ugx BETWEEN 1000 AND 500000),
    CONSTRAINT incentive_details_require_yes CHECK (
        receives_incentive IS TRUE
        OR (incentive_frequency IS NULL AND incentive_amount_ugx IS NULL)
    ),

    -- Supervision as year+month, by decision. The ODK form records supervision
    -- per service domain and carries no date, so this is NULL on every imported
    -- row and only fills in via the web UI.
    received_supervision boolean,
    last_supervised_on   date CHECK (extract(day from last_supervised_on) = 1),
    CONSTRAINT supervision_date_requires_yes CHECK (
        received_supervision IS TRUE OR last_supervised_on IS NULL
    ),

    updated_by bigint REFERENCES users(id),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX chw_profiles_facility_idx ON chw_profiles (facility_id);

------------------------------------------------------- controlled vocabularies
CREATE TABLE tools (
    id         smallserial PRIMARY KEY,
    slug       text NOT NULL UNIQUE,
    label      text NOT NULL,
    sort_order smallint NOT NULL DEFAULT 0,
    active     boolean NOT NULL DEFAULT true
);

CREATE TABLE service_domains (
    id         smallserial PRIMARY KEY,
    slug       text NOT NULL UNIQUE,
    label      text NOT NULL,
    sort_order smallint NOT NULL DEFAULT 0,
    active     boolean NOT NULL DEFAULT true
);

CREATE TABLE languages (
    id     smallserial PRIMARY KEY,
    slug   text NOT NULL UNIQUE,
    label  text NOT NULL,
    active boolean NOT NULL DEFAULT true
);

--------------------------------------------------------------- multi-selects
-- `tool_functional` is choice-filtered to tools already held, so functionality
-- is per tool rather than one global flag.
CREATE TABLE chw_tools (
    chw_id     bigint  NOT NULL REFERENCES chws(id) ON DELETE CASCADE,
    tool_id    smallint NOT NULL REFERENCES tools(id),
    functional boolean,
    PRIMARY KEY (chw_id, tool_id)
);

-- The Training group is three nested multi-selects over ONE vocabulary:
--   Service_domains (offers) >= training (trained, 2y) >= support_supervision.
-- Collapsed to flags on a single row. Supervision is excluded by decision
-- (captured as a date on chw_profiles instead).
CREATE TABLE chw_service_domains (
    chw_id    bigint   NOT NULL REFERENCES chws(id) ON DELETE CASCADE,
    domain_id smallint NOT NULL REFERENCES service_domains(id),
    provides  boolean  NOT NULL DEFAULT false,
    trained   boolean  NOT NULL DEFAULT false,   -- in the last 2 years
    PRIMARY KEY (chw_id, domain_id),
    -- mirrors the form's choice_filter: cannot be trained on an unoffered service
    CONSTRAINT trained_implies_provides CHECK (provides OR NOT trained)
);

CREATE TABLE chw_languages (
    chw_id      bigint   NOT NULL REFERENCES chws(id) ON DELETE CASCADE,
    language_id smallint NOT NULL REFERENCES languages(id),
    PRIMARY KEY (chw_id, language_id)
);

------------------------------------------------------------- seed vocabularies
INSERT INTO tools (slug, label, sort_order) VALUES
    ('bicycle','Bicycle',1), ('gumboots','Gumboots',2), ('thermometer','Thermometer',3),
    ('medicine_box','Medicine Box',4), ('muac_tape','MUAC Tape',5), ('torch','Torch',6),
    ('register','VHT Reporting Tools',7);
-- NB: the source list's "None" member is deliberately absent. It means
-- "no tools", which is the empty set here, not a tool called None.

INSERT INTO service_domains (slug, label, sort_order) VALUES
    ('iccm','Management of Common Childhood Illnesses (ICCM)',1),
    ('maternal_newborn','Maternal and Newborn Health',2),
    ('hiv','HIV Specific Services',3),
    ('ncd','Prevention and Control of Non-Communicable Diseases',4),
    ('immunization','Immunization Services',5),
    ('srhr','Sexual and Reproductive Health Services and Rights',6),
    ('nutrition','Nutrition Services',7),
    ('essential_clinical','Integrated Essential Clinical Care Services',8),
    ('cbmis','Community Based Management Information System (Reporting)',9),
    ('environmental_health','Environmental Health and Sanitation Services',10),
    ('epidemic_response','Epidemics and Disaster Preparedness and Response',11),
    ('school_health','School Health Services',12);
