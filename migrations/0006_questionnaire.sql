-- +goose Up
-- 0006: the questionnaire — profiles as data
--
-- A profile is a questionnaire a health worker answers: the CHW baseline
-- survey today, another category's tomorrow. Questions live in a shared pool;
-- a profile picks questions from it, in order, with the branching between
-- them; a health worker's answers are one dated submission. Adding a profile
-- or a question is an INSERT, the way adding a cadre is.
--
--   question_pool ── profile_questions ── profiles ── profile_applicable_cadre
--                          │
--   health_worker_profiles (one dated submission)
--     └ health_worker_profile_responses (one row per answer; several for a
--                                        multi-select)
--
-- Submissions are history, never edited: saving a profile writes a new
-- submission, and what the register shows is the latest one. Responses are
-- append-only and so is a submission's identity.
--
-- "No" and "not asked" stay different answers. A question nobody asked has no
-- response row; a "no" is a row holding the option `no`.
--
-- What the typed columns' CHECKs used to guarantee is guaranteed here in two
-- places: each answer is checked against its question as it is written (the
-- option exists, the value parses as the data type and sits in its range, a
-- single-value question gets one answer), and each submission is checked as a
-- whole when it commits (an answer inside a branch needs the answer that opens
-- the branch; a subset question's answers are within its parent's).

------------------------------------------------------------- vocabularies
CREATE TABLE response_types (
    id    smallserial PRIMARY KEY,
    code  text NOT NULL UNIQUE,
    label text NOT NULL
);
SELECT record_columns('response_types');
INSERT INTO response_types (code, label) VALUES
    ('single_value', 'One answer'),
    ('multi_value',  'Any number of answers');

CREATE TABLE value_types (
    id    smallserial PRIMARY KEY,
    code  text NOT NULL UNIQUE,
    label text NOT NULL
);
SELECT record_columns('value_types');
INSERT INTO value_types (code, label) VALUES
    ('open',   'Typed in'),
    ('closed', 'Chosen from a list');

CREATE TABLE value_data_types (
    id    smallserial PRIMARY KEY,
    code  text NOT NULL UNIQUE,
    label text NOT NULL
);
SELECT record_columns('value_data_types');
INSERT INTO value_data_types (code, label) VALUES
    ('text',      'Text'),
    ('boolean',   'True or false'),
    ('yes_no',    'Yes or no'),
    ('integer',   'Whole number'),
    ('numeric',   'Number'),
    ('character', 'Code'),
    ('date',      'Date'),
    ('month',     'Month (stored as its first day)');

------------------------------------------------------------- the pool
CREATE TABLE question_pool (
    id                 serial PRIMARY KEY,
    code               text NOT NULL UNIQUE CHECK (code ~ '^[a-z][a-z0-9_]{1,47}$'),
    question_prompt    text NOT NULL CHECK (btrim(question_prompt) <> ''),
    help               text,
    response_type_id   smallint NOT NULL REFERENCES response_types(id),
    value_type_id      smallint NOT NULL REFERENCES value_types(id),
    value_data_type_id smallint NOT NULL REFERENCES value_data_types(id),
    -- A closed question's choices: [{"id": 1, "code": "yes", "prompt": "Yes"}].
    -- The id is what a response stores, so it is permanent; the code is what an
    -- import or an export spells; the prompt is what a form shows. An optional
    -- "aliases" array lists other spellings an import may use. On a
    -- multi-select, a choice coded `none` is the recorded empty answer and
    -- stands alone.
    response_options   jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(response_options) = 'array'),
    -- Bounds for an open number, and a pattern for open text.
    min_value          numeric,
    max_value          numeric,
    pattern            text,
    active             boolean NOT NULL DEFAULT true,
    CHECK (min_value IS NULL OR max_value IS NULL OR min_value <= max_value)
);
SELECT record_columns('question_pool');

-- The shape a response can only be checked against if the question itself is
-- sound: a closed question has choices and an open one has none, each choice
-- has an integer id and a code, and neither repeats.
-- +goose StatementBegin
CREATE FUNCTION question_pool_check() RETURNS trigger AS $$
DECLARE
    vt text := (SELECT code FROM value_types WHERE id = NEW.value_type_id);
    n  int  := jsonb_array_length(NEW.response_options);
BEGIN
    IF (vt = 'closed') <> (n > 0) THEN
        RAISE EXCEPTION 'question %: a closed question needs choices and an open one takes none', NEW.code;
    END IF;
    IF EXISTS (SELECT 1 FROM jsonb_array_elements(NEW.response_options) o
                WHERE jsonb_typeof(o->'id') <> 'number' OR (o->>'id')::numeric <> (o->>'id')::int
                   OR coalesce(o->>'code', '') !~ '^[a-z0-9][a-z0-9_]*$'
                   OR btrim(coalesce(o->>'prompt', '')) = '') THEN
        RAISE EXCEPTION 'question %: every choice needs an integer id, a lower-case code and a prompt', NEW.code;
    END IF;
    IF (SELECT count(DISTINCT o->>'id') FROM jsonb_array_elements(NEW.response_options) o) <> n
       OR (SELECT count(DISTINCT o->>'code') FROM jsonb_array_elements(NEW.response_options) o) <> n THEN
        RAISE EXCEPTION 'question %: choice ids and codes must not repeat', NEW.code;
    END IF;
    -- The subset rule reads option 0 as the recorded empty answer.
    IF EXISTS (SELECT 1 FROM jsonb_array_elements(NEW.response_options) o
                WHERE (o->>'code' = 'none') <> ((o->>'id')::int = 0)) THEN
        RAISE EXCEPTION 'question %: the choice coded none is option 0, and option 0 is none', NEW.code;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER question_pool_check_trg BEFORE INSERT OR UPDATE ON question_pool
    FOR EACH ROW EXECUTE FUNCTION question_pool_check();

------------------------------------------------------------- profiles
CREATE TABLE profiles (
    id                  smallserial PRIMARY KEY,
    code                text NOT NULL UNIQUE CHECK (code ~ '^[a-z][a-z0-9_]{1,31}$'),
    profile_name        text NOT NULL CHECK (btrim(profile_name) <> ''),
    profile_description text,
    sort_order          smallint NOT NULL DEFAULT 0,
    active              boolean NOT NULL DEFAULT true
);
SELECT record_columns('profiles');

CREATE TABLE profile_applicable_cadre (
    id         serial PRIMARY KEY,
    profile_id smallint NOT NULL REFERENCES profiles(id),
    cadre_id   smallint NOT NULL REFERENCES cadres(id),
    UNIQUE (profile_id, cadre_id)
);
SELECT record_columns('profile_applicable_cadre');

CREATE TABLE profile_questions (
    id            serial PRIMARY KEY,
    profile_id    smallint NOT NULL REFERENCES profiles(id),
    question_id   integer  NOT NULL REFERENCES question_pool(id),
    -- The question's name inside this profile; also the import/export column
    -- when the profile is the one the importer reads.
    code          text NOT NULL CHECK (code ~ '^[a-z][a-z0-9_]{1,47}$'),
    sort_order    smallint NOT NULL DEFAULT 0,
    required      boolean  NOT NULL DEFAULT false,
    -- Branching: asked only when another question in this profile was
    -- answered with this option code.
    depends_on_id     integer REFERENCES profile_questions(id),
    depends_on_option text,
    -- Choice-filtering: the answers must be among the answers given to
    -- another multi-select in this profile (tools functional ⊆ tools held).
    subset_of_id      integer REFERENCES profile_questions(id),
    UNIQUE (profile_id, code),
    UNIQUE (profile_id, question_id),
    CONSTRAINT profile_questions_branch_complete CHECK ((depends_on_id IS NULL) = (depends_on_option IS NULL)),
    CHECK (depends_on_id <> id AND subset_of_id <> id)
);
SELECT record_columns('profile_questions');

-- A branch and a subset each point at a question in the same profile, of a
-- shape the rule can be read against.
-- +goose StatementBegin
CREATE FUNCTION profile_questions_check() RETURNS trigger AS $$
DECLARE
    target record;
BEGIN
    IF NEW.depends_on_id IS NOT NULL THEN
        SELECT pq.profile_id, q.response_options, rt.code AS rtype, vt.code AS vtype INTO target
          FROM profile_questions pq
          JOIN question_pool q   ON q.id = pq.question_id
          JOIN response_types rt ON rt.id = q.response_type_id
          JOIN value_types vt    ON vt.id = q.value_type_id
         WHERE pq.id = NEW.depends_on_id;
        IF target.profile_id IS DISTINCT FROM NEW.profile_id OR target.vtype <> 'closed' OR target.rtype <> 'single_value' THEN
            RAISE EXCEPTION 'question %: a branch depends on a single-choice question in the same profile', NEW.code;
        END IF;
        IF NOT EXISTS (SELECT 1 FROM jsonb_array_elements(target.response_options) o
                        WHERE o->>'code' = NEW.depends_on_option) THEN
            RAISE EXCEPTION 'question %: the branch names option %, which its question does not offer',
                NEW.code, NEW.depends_on_option;
        END IF;
    END IF;
    IF NEW.subset_of_id IS NOT NULL THEN
        SELECT pq.profile_id, q.response_options, rt.code AS rtype, vt.code AS vtype INTO target
          FROM profile_questions pq
          JOIN question_pool q   ON q.id = pq.question_id
          JOIN response_types rt ON rt.id = q.response_type_id
          JOIN value_types vt    ON vt.id = q.value_type_id
         WHERE pq.id = NEW.subset_of_id;
        IF target.profile_id IS DISTINCT FROM NEW.profile_id OR target.vtype <> 'closed' THEN
            RAISE EXCEPTION 'question %: a subset is of a closed question in the same profile', NEW.code;
        END IF;
        IF EXISTS (SELECT 1 FROM question_pool q, jsonb_array_elements(q.response_options) o
                    WHERE q.id = NEW.question_id
                      AND NOT target.response_options @> jsonb_build_array(jsonb_build_object('id', o->'id', 'code', o->'code'))) THEN
            RAISE EXCEPTION 'question %: every choice must also be a choice of the question it is a subset of', NEW.code;
        END IF;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER profile_questions_check_trg BEFORE INSERT OR UPDATE ON profile_questions
    FOR EACH ROW EXECUTE FUNCTION profile_questions_check();

------------------------------------------------------------- submissions
CREATE TABLE health_worker_profiles (
    id               bigserial PRIMARY KEY,
    health_worker_id bigint   NOT NULL REFERENCES health_workers(id),
    profile_id       smallint NOT NULL REFERENCES profiles(id),
    captured_on      date     NOT NULL DEFAULT current_date,
    source           text     NOT NULL CHECK (source IN ('form','import'))
);
SELECT record_columns('health_worker_profiles');
-- The latest submission per worker and profile is the one every read wants.
CREATE INDEX health_worker_profiles_latest_idx
    ON health_worker_profiles (health_worker_id, profile_id, captured_on DESC, id DESC);

-- A worker answers a profile only while it applies to the cadre they are seen
-- through — their open posting, else their last — and a submission, once
-- written, is not moved to another worker or another profile.
-- +goose StatementBegin
CREATE FUNCTION health_worker_profiles_check() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF (NEW.health_worker_id, NEW.profile_id, NEW.captured_on, NEW.source)
           IS DISTINCT FROM (OLD.health_worker_id, OLD.profile_id, OLD.captured_on, OLD.source) THEN
            RAISE EXCEPTION 'submission % is history; write a new one instead', OLD.id;
        END IF;
        RETURN NEW;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM profile_applicable_cadre a
         WHERE a.profile_id = NEW.profile_id
           AND a.cadre_id = (SELECT d.cadre_id FROM deployments d
                              WHERE d.health_worker_id = NEW.health_worker_id
                              ORDER BY (d.ended_on IS NULL) DESC, d.started_on DESC, d.id DESC
                              LIMIT 1)
    ) THEN
        RAISE EXCEPTION 'profile % does not apply to health worker %''s cadre',
            (SELECT code FROM profiles WHERE id = NEW.profile_id), NEW.health_worker_id;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER health_worker_profiles_check_trg BEFORE INSERT OR UPDATE ON health_worker_profiles
    FOR EACH ROW EXECUTE FUNCTION health_worker_profiles_check();

CREATE TABLE health_worker_profile_responses (
    id                       bigserial PRIMARY KEY,
    health_worker_profile_id bigint  NOT NULL REFERENCES health_worker_profiles(id),
    profile_question_id      integer NOT NULL REFERENCES profile_questions(id),
    -- A closed answer is an option id from the question's choices; an open one
    -- is its text. Exactly one of the two.
    response_option_id       integer,
    response                 text,
    CONSTRAINT health_worker_profile_responses_one_value
        CHECK ((response_option_id IS NULL) <> (response IS NULL))
);
SELECT record_columns('health_worker_profile_responses');
CREATE INDEX health_worker_profile_responses_submission_idx
    ON health_worker_profile_responses (health_worker_profile_id, profile_question_id);
CREATE UNIQUE INDEX health_worker_profile_responses_option_uniq
    ON health_worker_profile_responses (health_worker_profile_id, profile_question_id, response_option_id)
    WHERE response_option_id IS NOT NULL;
CREATE UNIQUE INDEX health_worker_profile_responses_text_uniq
    ON health_worker_profile_responses (health_worker_profile_id, profile_question_id, response)
    WHERE response IS NOT NULL;

-- Each answer against its own question, as it is written.
-- +goose StatementBegin
CREATE FUNCTION health_worker_profile_responses_check() RETURNS trigger AS $$
DECLARE
    q   record;
    num numeric;
BEGIN
    IF TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION 'responses are history; write a new submission instead';
    END IF;

    SELECT pq.code, pq.profile_id, qp.response_options, qp.min_value, qp.max_value, qp.pattern,
           rt.code AS rtype, vt.code AS vtype, dt.code AS dtype
      INTO q
      FROM profile_questions pq
      JOIN question_pool qp     ON qp.id = pq.question_id
      JOIN response_types rt    ON rt.id = qp.response_type_id
      JOIN value_types vt       ON vt.id = qp.value_type_id
      JOIN value_data_types dt  ON dt.id = qp.value_data_type_id
     WHERE pq.id = NEW.profile_question_id;

    IF q.profile_id IS DISTINCT FROM (SELECT profile_id FROM health_worker_profiles WHERE id = NEW.health_worker_profile_id) THEN
        RAISE EXCEPTION 'question % is not part of this submission''s profile', q.code;
    END IF;

    IF q.rtype = 'single_value' AND EXISTS (
        SELECT 1 FROM health_worker_profile_responses
         WHERE health_worker_profile_id = NEW.health_worker_profile_id
           AND profile_question_id = NEW.profile_question_id) THEN
        RAISE EXCEPTION 'question % takes one answer', q.code;
    END IF;

    IF q.vtype = 'closed' THEN
        IF NEW.response_option_id IS NULL OR NOT EXISTS (
            SELECT 1 FROM jsonb_array_elements(q.response_options) o
             WHERE (o->>'id')::int = NEW.response_option_id) THEN
            RAISE EXCEPTION 'question %: % is not one of its choices', q.code, coalesce(NEW.response_option_id::text, NEW.response);
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.response IS NULL OR btrim(NEW.response) = '' THEN
        RAISE EXCEPTION 'question %: an open answer needs a value', q.code;
    END IF;
    CASE q.dtype
    WHEN 'integer', 'numeric' THEN
        IF NOT pg_input_is_valid(NEW.response, CASE q.dtype WHEN 'integer' THEN 'bigint' ELSE 'numeric' END) THEN
            RAISE EXCEPTION 'question %: % is not a %', q.code, NEW.response, q.dtype;
        END IF;
        num := NEW.response::numeric;
        IF num < q.min_value OR num > q.max_value THEN
            RAISE EXCEPTION 'question %: % is outside % to %', q.code, NEW.response, q.min_value, q.max_value;
        END IF;
    WHEN 'boolean' THEN
        IF NEW.response NOT IN ('true','false') THEN
            RAISE EXCEPTION 'question %: % is not true or false', q.code, NEW.response;
        END IF;
    WHEN 'date', 'month' THEN
        IF NEW.response !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' OR NOT pg_input_is_valid(NEW.response, 'date') THEN
            RAISE EXCEPTION 'question %: % is not a date (YYYY-MM-DD)', q.code, NEW.response;
        END IF;
        IF q.dtype = 'month' AND extract(day FROM NEW.response::date) <> 1 THEN
            RAISE EXCEPTION 'question %: a month is stored as its first day, not %', q.code, NEW.response;
        END IF;
    WHEN 'yes_no' THEN
        RAISE EXCEPTION 'question %: a yes/no question is closed, with yes and no as its choices', q.code;
    ELSE
        NULL;
    END CASE;
    IF q.pattern IS NOT NULL AND NEW.response !~ q.pattern THEN
        RAISE EXCEPTION 'question %: % does not match %', q.code, NEW.response, q.pattern;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER health_worker_profile_responses_check_trg
    BEFORE INSERT OR UPDATE OR DELETE ON health_worker_profile_responses
    FOR EACH ROW EXECUTE FUNCTION health_worker_profile_responses_check();

-- The submission as a whole, at commit, when every answer is in: an answer in
-- a branch needs the answer that opens it, and a subset's answers sit within
-- its parent's. Deferred, because the answers of one submission arrive in
-- whatever order the writer chooses.
-- +goose StatementBegin
CREATE FUNCTION health_worker_profile_responses_check_rules() RETURNS trigger AS $$
DECLARE
    pq record;
BEGIN
    SELECT p.code, p.depends_on_id, p.depends_on_option, p.subset_of_id,
           dq.response_options AS depends_options
      INTO pq
      FROM profile_questions p
      LEFT JOIN profile_questions d ON d.id = p.depends_on_id
      LEFT JOIN question_pool dq    ON dq.id = d.question_id
     WHERE p.id = NEW.profile_question_id;

    IF pq.depends_on_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM health_worker_profile_responses r,
                      jsonb_array_elements(pq.depends_options) o
         WHERE r.health_worker_profile_id = NEW.health_worker_profile_id
           AND r.profile_question_id = pq.depends_on_id
           AND (o->>'id')::int = r.response_option_id
           AND o->>'code' = pq.depends_on_option) THEN
        RAISE EXCEPTION 'question % is only asked when % is answered %',
            pq.code, (SELECT code FROM profile_questions WHERE id = pq.depends_on_id), pq.depends_on_option;
    END IF;

    IF NEW.response_option_id IS NOT NULL AND EXISTS (
        SELECT 1 FROM health_worker_profile_responses r
          JOIN profile_questions p ON p.id = r.profile_question_id
          JOIN question_pool q     ON q.id = p.question_id,
               jsonb_array_elements(q.response_options) o
         WHERE r.health_worker_profile_id = NEW.health_worker_profile_id
           AND r.profile_question_id = NEW.profile_question_id
           AND r.id <> NEW.id
           AND (o->>'id')::int IN (r.response_option_id, NEW.response_option_id)
           AND o->>'code' = 'none') THEN
        RAISE EXCEPTION 'question %: none is an answer on its own', pq.code;
    END IF;

    IF pq.subset_of_id IS NOT NULL AND NEW.response_option_id <> 0 AND NOT EXISTS (
        SELECT 1 FROM health_worker_profile_responses r
         WHERE r.health_worker_profile_id = NEW.health_worker_profile_id
           AND r.profile_question_id = pq.subset_of_id
           AND r.response_option_id = NEW.response_option_id) THEN
        RAISE EXCEPTION 'question %: option % was not also given to %',
            pq.code, NEW.response_option_id, (SELECT code FROM profile_questions WHERE id = pq.subset_of_id);
    END IF;
    RETURN NULL;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER health_worker_profile_responses_rules_trg
    AFTER INSERT ON health_worker_profile_responses
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION health_worker_profile_responses_check_rules();

-- Submissions are not deleted either: they are what the register said.
-- +goose StatementBegin
CREATE FUNCTION refuse_delete() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION '% rows are history and are never deleted', TG_TABLE_NAME;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER health_worker_profiles_no_delete_trg BEFORE DELETE ON health_worker_profiles
    FOR EACH ROW EXECUTE FUNCTION refuse_delete();

------------------------------------------------- frozen once anyone answers
-- An answer is read back through its question. Changing a question's type,
-- range or code after it has answers would reinterpret every one of them, and
-- dropping or recoding a choice would orphan the responses that hold its id.
-- So once answered, a question may only be reworded, re-helped, given more
-- choices, or retired — the same rule as cadres_freeze_trg.
-- +goose StatementBegin
CREATE FUNCTION question_pool_freeze_when_answered() RETURNS trigger AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM health_worker_profile_responses r
                     JOIN profile_questions pq ON pq.id = r.profile_question_id
                    WHERE pq.question_id = OLD.id) THEN
        RETURN NEW;
    END IF;
    IF (NEW.code, NEW.response_type_id, NEW.value_type_id, NEW.value_data_type_id,
        NEW.min_value, NEW.max_value, NEW.pattern)
       IS DISTINCT FROM (OLD.code, OLD.response_type_id, OLD.value_type_id, OLD.value_data_type_id,
        OLD.min_value, OLD.max_value, OLD.pattern) THEN
        RAISE EXCEPTION 'question % has answers; its code, type and range are fixed — retire it and add a new question instead', OLD.code;
    END IF;
    IF EXISTS (SELECT 1 FROM jsonb_array_elements(OLD.response_options) o
                WHERE NOT NEW.response_options @> jsonb_build_array(jsonb_build_object('id', o->'id', 'code', o->'code'))) THEN
        RAISE EXCEPTION 'question % has answers; its choices may be added to and reworded, not removed or recoded', OLD.code;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER question_pool_freeze_trg BEFORE UPDATE ON question_pool
    FOR EACH ROW EXECUTE FUNCTION question_pool_freeze_when_answered();

-- +goose StatementBegin
CREATE FUNCTION profile_questions_freeze_when_answered() RETURNS trigger AS $$
BEGIN
    IF (NEW.profile_id, NEW.question_id, NEW.code, NEW.depends_on_id, NEW.depends_on_option, NEW.subset_of_id)
       IS DISTINCT FROM (OLD.profile_id, OLD.question_id, OLD.code, OLD.depends_on_id, OLD.depends_on_option, OLD.subset_of_id)
       AND EXISTS (SELECT 1 FROM health_worker_profile_responses WHERE profile_question_id = OLD.id) THEN
        RAISE EXCEPTION 'profile question % has answers; only its order and whether it is required may change', OLD.code;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER profile_questions_freeze_trg BEFORE UPDATE ON profile_questions
    FOR EACH ROW EXECUTE FUNCTION profile_questions_freeze_when_answered();

--------------------------------------------------- the CHW baseline survey
-- The survey the ODK form ran, as questions. What it asked that is a fact
-- about the person rather than a survey answer is not here: phone numbers are
-- person_contacts, education is person_education, English is person_languages
-- (0003). What remains is what a CHW reported about their work.
INSERT INTO profiles (code, profile_name, profile_description, sort_order) VALUES
    ('chw_baseline', 'CHW baseline survey',
     'The Community Health Worker survey: phone use, service history, incentive, supervision, tools held and services offered.', 1);

INSERT INTO profile_applicable_cadre (profile_id, cadre_id)
SELECT p.id, c.id FROM profiles p CROSS JOIN cadres c
 WHERE p.code = 'chw_baseline' AND c.code IN ('vht','chew');

-- +goose StatementBegin
DO $$
DECLARE
    opt_yes_no  jsonb := '[{"id":1,"code":"yes","prompt":"Yes"},{"id":2,"code":"no","prompt":"No"}]';
    opt_freq    jsonb := '[{"id":1,"code":"monthly","prompt":"Monthly","aliases":["month"]},
                           {"id":2,"code":"quarterly","prompt":"Quarterly","aliases":["quarter"]},
                           {"id":3,"code":"annually","prompt":"Annually","aliases":["annual","yearly","year"]},
                           {"id":4,"code":"one_off","prompt":"One-off","aliases":["once","oneoff"]}]';
    -- A multi-select's "None" is a recorded empty answer, which an empty set of
    -- rows could not say: no rows is "not asked". It is option 0, exclusive.
    opt_none     jsonb := '[{"id":0,"code":"none","prompt":"None"}]';
    opt_tools    jsonb := opt_none || (SELECT jsonb_agg(jsonb_build_object('id', id, 'code', code, 'prompt', label) ORDER BY sort_order) FROM tools);
    opt_services jsonb := opt_none || (SELECT jsonb_agg(jsonb_build_object('id', id, 'code', code, 'prompt', label) ORDER BY sort_order) FROM services);
    id_single smallint := (SELECT id FROM response_types WHERE code = 'single_value');
    id_multi  smallint := (SELECT id FROM response_types WHERE code = 'multi_value');
    id_open   smallint := (SELECT id FROM value_types WHERE code = 'open');
    id_closed smallint := (SELECT id FROM value_types WHERE code = 'closed');
    id_profile smallint := (SELECT id FROM profiles WHERE code = 'chw_baseline');
BEGIN
    INSERT INTO question_pool (code, question_prompt, response_type_id, value_type_id, value_data_type_id,
                               response_options, min_value, max_value)
    SELECT v.code, v.prompt,
           CASE v.multi WHEN true THEN id_multi ELSE id_single END,
           CASE WHEN v.options IS NULL THEN id_open ELSE id_closed END,
           (SELECT id FROM value_data_types WHERE code = v.dtype),
           coalesce(v.options, '[]'), v.min, v.max
      FROM (VALUES
        ('owns_phone',           'Do you own a phone?',                                 false, 'yes_no',    opt_yes_no,          NULL::numeric, NULL::numeric),
        ('phone_for_reporting',  'Do you use your phone for reporting?',                false, 'yes_no',    opt_yes_no,          NULL, NULL),
        ('service_start_year',   'In what year did you start serving?',                 false, 'integer',   NULL,            1960, 2100),
        ('households_served',    'How many households do you serve?',                   false, 'integer',   NULL,            3, 100000),
        ('other_languages',      'Which other languages do you speak?',                 false, 'text',      NULL,            NULL, NULL),
        ('receives_incentive',   'Do you receive an incentive?',                        false, 'yes_no',    opt_yes_no,          NULL, NULL),
        ('incentive_frequency',  'How often is the incentive paid?',                    false, 'character', opt_freq,        NULL, NULL),
        ('incentive_amount_ugx', 'How much is the incentive, in UGX?',                  false, 'integer',   NULL,            1000, 500000),
        ('received_supervision', 'Have you received support supervision?',              false, 'yes_no',    opt_yes_no,          NULL, NULL),
        ('last_supervised_on',   'In what month were you last supervised?',             false, 'month',     NULL,            NULL, NULL),
        ('tools_held',           'Which tools do you hold?',                            true,  'character', opt_tools,       NULL, NULL),
        ('tools_functional',     'Which of those tools are functional?',                true,  'character', opt_tools,       NULL, NULL),
        ('services_provided',    'Which services do you provide?',                      true,  'character', opt_services,    NULL, NULL),
        ('services_trained',     'In which of those were you trained in the last two years?', true, 'character', opt_services,    NULL, NULL)
      ) AS v(code, prompt, multi, dtype, options, min, max);

    INSERT INTO profile_questions (profile_id, question_id, code, sort_order)
    SELECT id_profile, q.id, q.code, row_number() OVER (ORDER BY q.id)
      FROM question_pool q;

    UPDATE profile_questions pq SET depends_on_id = d.id, depends_on_option = 'yes'
      FROM profile_questions d
     WHERE pq.profile_id = id_profile AND d.profile_id = id_profile
       AND (pq.code, d.code) IN (('phone_for_reporting', 'owns_phone'),
                                 ('incentive_frequency', 'receives_incentive'),
                                 ('incentive_amount_ugx', 'receives_incentive'),
                                 ('last_supervised_on', 'received_supervision'));

    UPDATE profile_questions pq SET subset_of_id = s.id
      FROM profile_questions s
     WHERE pq.profile_id = id_profile AND s.profile_id = id_profile
       AND (pq.code, s.code) IN (('tools_functional', 'tools_held'),
                                 ('services_trained', 'services_provided'));
END $$;
-- +goose StatementEnd
