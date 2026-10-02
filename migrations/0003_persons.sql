-- +goose Up
-- 0003: the person
--
-- `persons` is who someone is — their names, identity number, birth and sex —
-- independent of any work they do. `health_workers` (0004) is their place in
-- the workforce and points at a person. The two were one table while the
-- register held only CHWs; they are split so that what is true of a human
-- being (a phone number, a next of kin, a language, an education) is recorded
-- once, against the person, and survives whatever happens to their posting.
--
-- The satellite tables below each hang off the person with a person_id. None
-- of them has a district of its own: they are scoped through the health worker
-- who is that person, like the questionnaire responses in 0005.

CREATE TYPE sex AS ENUM ('male','female');

-- The record's own state, not the workforce's: a health worker leaves the
-- workforce (health_workers.status), a person dies, or turns out to be a
-- duplicate of another and is merged into them.
CREATE TYPE person_status AS ENUM ('active','deceased','merged');

CREATE TABLE persons (
    id          bigserial PRIMARY KEY,
    -- Optional in the source forms and labelled "NIN / Alternative No", so it
    -- is nullable; unique only where present. Other identity documents live
    -- in person_ids; the NIN is the one the register keys duplicates on.
    nin         text CHECK (nin ~ '^[A-Z]{2}[A-Z0-9]{11}[A-Z]$'),
    first_name  text NOT NULL CHECK (btrim(first_name) <> ''),
    last_name   text NOT NULL CHECK (btrim(last_name) <> ''),
    other_name  text CHECK (btrim(other_name) <> ''),
    -- Birth date, or an estimate of it. The field forms collected an age, not a
    -- date: an age is a snapshot, so it is stored as the birth date it implies
    -- and flagged as estimated, and the age is always computed, never stale.
    dob           date CHECK (dob >= '1900-01-01'),
    dob_estimated boolean NOT NULL DEFAULT false,
    sex         sex NOT NULL,
    status      person_status NOT NULL DEFAULT 'active',
    CONSTRAINT persons_estimate_needs_a_date CHECK (dob IS NOT NULL OR NOT dob_estimated)
);
SELECT record_columns('persons');

CREATE UNIQUE INDEX persons_nin_uniq ON persons (nin) WHERE nin IS NOT NULL;
CREATE INDEX persons_name_trgm      ON persons USING gin ((first_name || ' ' || last_name) gin_trgm_ops);
-- The national listing sorts case-insensitively and pages with a keyset on
-- this expression; the register holds "Okello", "OKELLO" and "okello" for the
-- same surname convention.
CREATE INDEX persons_name_sort_idx  ON persons (lower(last_name), lower(first_name), id);

------------------------------------------------------------------ contacts
CREATE TYPE contact_kind AS ENUM ('phone','email','address');

CREATE TABLE person_contacts (
    id          bigserial PRIMARY KEY,
    person_id   bigint NOT NULL REFERENCES persons(id),
    kind        contact_kind NOT NULL,
    value       text NOT NULL CHECK (btrim(value) <> ''),
    -- A phone the person owns, or one they can be reached on that belongs to
    -- someone else — the CHW survey asks exactly this ("do you own a phone?",
    -- then the number, or an alternative number). NULL for anything else.
    owned         boolean,
    for_reporting boolean,
    is_primary    boolean NOT NULL DEFAULT false,
    CONSTRAINT person_contacts_phone_shape CHECK (kind <> 'phone' OR value ~ '^[0-9]{9}$'),
    CONSTRAINT person_contacts_email_shape CHECK (kind <> 'email' OR value ~ '^[^@\s]+@[^@\s]+$'),
    CONSTRAINT person_contacts_phone_only  CHECK (kind = 'phone' OR (owned IS NULL AND for_reporting IS NULL)),
    UNIQUE (person_id, kind, value)
);
SELECT record_columns('person_contacts');
CREATE INDEX person_contacts_person_idx ON person_contacts (person_id);
CREATE UNIQUE INDEX person_contacts_one_primary ON person_contacts (person_id, kind) WHERE is_primary;

-------------------------------------------------------------- next of kin
CREATE TABLE person_kins (
    id           bigserial PRIMARY KEY,
    person_id    bigint NOT NULL REFERENCES persons(id),
    name         text NOT NULL CHECK (btrim(name) <> ''),
    relationship text NOT NULL CHECK (btrim(relationship) <> ''),
    phone        text CHECK (phone ~ '^[0-9]{9}$'),
    is_emergency boolean NOT NULL DEFAULT false
);
SELECT record_columns('person_kins');
CREATE INDEX person_kins_person_idx ON person_kins (person_id);

-------------------------------------------------------- identity documents
-- An extensible vocabulary, so a table rather than an enum.
CREATE TABLE identifier_types (
    id         smallserial PRIMARY KEY,
    code       text NOT NULL UNIQUE CHECK (code ~ '^[a-z][a-z0-9_]{1,31}$'),
    label      text NOT NULL CHECK (btrim(label) <> ''),
    sort_order smallint NOT NULL DEFAULT 0,
    active     boolean NOT NULL DEFAULT true
);
SELECT record_columns('identifier_types');

INSERT INTO identifier_types (code, label, sort_order) VALUES
    ('passport',        'Passport',              1),
    ('refugee_id',      'Refugee ID',            2),
    ('driving_permit',  'Driving permit',        3),
    ('employee_number', 'Employee number',       4),
    ('alternative_no',  'Alternative number',    5);

CREATE TABLE person_ids (
    id                 bigserial PRIMARY KEY,
    person_id          bigint   NOT NULL REFERENCES persons(id),
    identifier_type_id smallint NOT NULL REFERENCES identifier_types(id),
    number             text     NOT NULL CHECK (btrim(number) <> ''),
    -- One document of a kind per person, and one person per document.
    UNIQUE (person_id, identifier_type_id),
    UNIQUE (identifier_type_id, number)
);
SELECT record_columns('person_ids');

--------------------------------------------------------- education, training
CREATE TYPE education_level AS ENUM ('none','ple','uce','uace','tertiary');

CREATE TABLE person_education (
    id             bigserial PRIMARY KEY,
    person_id      bigint NOT NULL REFERENCES persons(id),
    level          education_level NOT NULL,
    institution    text,
    qualification  text,
    year_completed smallint CHECK (year_completed BETWEEN 1940 AND 2100)
);
SELECT record_columns('person_education');
CREATE INDEX person_education_person_idx ON person_education (person_id);

-- Formal courses: a programme of study leading to a qualification.
CREATE TABLE person_courses (
    id            bigserial PRIMARY KEY,
    person_id     bigint NOT NULL REFERENCES persons(id),
    course        text NOT NULL CHECK (btrim(course) <> ''),
    institution   text,
    qualification text,
    started_on    date,
    completed_on  date,
    CHECK (completed_on IS NULL OR started_on IS NULL OR completed_on >= started_on)
);
SELECT record_columns('person_courses');
CREATE INDEX person_courses_person_idx ON person_courses (person_id);

-- In-service training: short, often repeated, given to people already working.
CREATE TABLE person_training (
    id         bigserial PRIMARY KEY,
    person_id  bigint NOT NULL REFERENCES persons(id),
    title      text NOT NULL CHECK (btrim(title) <> ''),
    provider   text,
    started_on date,
    ended_on   date,
    certified  boolean,
    CHECK (ended_on IS NULL OR started_on IS NULL OR ended_on >= started_on)
);
SELECT record_columns('person_training');
CREATE INDEX person_training_person_idx ON person_training (person_id);

-- Work done before or outside this register. Postings inside it are
-- `deployments`, which already answer "who served where, when".
CREATE TABLE person_workhistory (
    id         bigserial PRIMARY KEY,
    person_id  bigint NOT NULL REFERENCES persons(id),
    employer   text NOT NULL CHECK (btrim(employer) <> ''),
    position   text,
    started_on date,
    ended_on   date,
    CHECK (ended_on IS NULL OR started_on IS NULL OR ended_on >= started_on)
);
SELECT record_columns('person_workhistory');
CREATE INDEX person_workhistory_person_idx ON person_workhistory (person_id);

---------------------------------------------------------------- languages
CREATE TABLE languages (
    id         smallserial PRIMARY KEY,
    code       text NOT NULL UNIQUE CHECK (code ~ '^[a-z][a-z0-9_]{1,31}$'),
    label      text NOT NULL CHECK (btrim(label) <> ''),
    sort_order smallint NOT NULL DEFAULT 0,
    active     boolean NOT NULL DEFAULT true
);
SELECT record_columns('languages');

INSERT INTO languages (code, label, sort_order) VALUES
    ('english', 'English', 1),
    ('luganda', 'Luganda', 2),
    ('swahili', 'Kiswahili', 3),
    ('runyankore_rukiga', 'Runyankore-Rukiga', 4),
    ('runyoro_rutooro', 'Runyoro-Rutooro', 5),
    ('lusoga', 'Lusoga', 6),
    ('acholi', 'Acholi', 7),
    ('lango', 'Lango', 8),
    ('ateso', 'Ateso', 9),
    ('lugbara', 'Lugbara', 10),
    ('lumasaba', 'Lumasaba', 11),
    ('alur', 'Alur', 12),
    ('karamojong', 'Ngakarimojong', 13),
    ('lukonzo', 'Lukonzo', 14);

-- A grade per skill. NULL is "not asked", 'none' is "asked, cannot": the
-- distinction every survey answer in this register keeps.
CREATE TYPE proficiency AS ENUM ('none','basic','good','fluent');

CREATE TABLE person_languages (
    id                  bigserial PRIMARY KEY,
    person_id           bigint   NOT NULL REFERENCES persons(id),
    language_id         smallint NOT NULL REFERENCES languages(id),
    understanding_grade proficiency,
    reading_grade       proficiency,
    writing_grade       proficiency,
    UNIQUE (person_id, language_id)
);
SELECT record_columns('person_languages');
