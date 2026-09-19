-- +goose Up
-- 0004: the CHW category's profile surface
--
-- `chw_profiles` holds the optional survey attributes of a worker deployed in
-- the Community Health Workers category — one row per worker, never more.
-- It hangs off the person, not the deployment: the survey answers (phones,
-- education, incentive) stay true across a transfer. What was location-bound —
-- the supervising facility — lives on deployments now.
--
-- "no" and "not asked" are different answers: every column is nullable, and an
-- imported record that answered nothing must not come back as a record that
-- answered no.

CREATE TYPE education_level     AS ENUM ('none','ple','uce','uace','tertiary');
CREATE TYPE incentive_frequency AS ENUM ('monthly','quarterly','annually','one_off');

CREATE TABLE chw_profiles (
    health_worker_id bigint PRIMARY KEY REFERENCES health_workers(id) ON DELETE CASCADE,

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
    health_worker_id bigint   NOT NULL REFERENCES health_workers(id) ON DELETE CASCADE,
    tool_id          smallint NOT NULL REFERENCES tools(id),
    functional       boolean,
    PRIMARY KEY (health_worker_id, tool_id)
);

-- The Training group is three nested multi-selects over ONE vocabulary:
--   Service_domains (offers) >= training (trained, 2y) >= support_supervision.
-- Collapsed to flags on a single row. Supervision is excluded by decision
-- (captured as a date on chw_profiles instead).
CREATE TABLE chw_service_domains (
    health_worker_id bigint   NOT NULL REFERENCES health_workers(id) ON DELETE CASCADE,
    domain_id        smallint NOT NULL REFERENCES service_domains(id),
    provides         boolean  NOT NULL DEFAULT false,
    trained          boolean  NOT NULL DEFAULT false,   -- in the last 2 years
    PRIMARY KEY (health_worker_id, domain_id),
    -- mirrors the form's choice_filter: cannot be trained on an unoffered service
    CONSTRAINT trained_implies_provides CHECK (provides OR NOT trained)
);

CREATE TABLE chw_languages (
    health_worker_id bigint   NOT NULL REFERENCES health_workers(id) ON DELETE CASCADE,
    language_id      smallint NOT NULL REFERENCES languages(id),
    PRIMARY KEY (health_worker_id, language_id)
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
