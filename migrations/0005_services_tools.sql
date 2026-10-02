-- +goose Up
-- 0005: services and tools — what a cadre does and carries, and when
--
-- Two vocabularies, each with the cadres it applies to, and two kinds of
-- dated event against them:
--
--   services ── service_applicable_cadre ── cadres
--      └ health_worker_service_update_details
--            └ health_worker_service_updates      a worker reports, on a date,
--                                                 the services they gave
--   tools ── tool_applicable_cadre ── cadres
--      └ health_worker_tool_distribution_details  which worker got which tool
--            └ health_worker_tool_distributions   one hand-out, in one
--                                                 district, on one date
--
-- An event is checked against the posting the worker held ON ITS DATE, not
-- today's: a VHT promoted to CHEW last month still received their tools as a
-- VHT. A worker with no posting on that date cannot have an event on it.
--
-- What a CHW said in the baseline survey about the services they offer and
-- the tools they hold is not an event and is not recorded here: it is a
-- survey answer, held by the questionnaire (0006) as one.

--------------------------------------------------------------- vocabularies
CREATE TABLE services (
    id         smallserial PRIMARY KEY,
    code       text NOT NULL UNIQUE CHECK (code ~ '^[a-z][a-z0-9_]{1,31}$'),
    label      text NOT NULL CHECK (btrim(label) <> ''),
    sort_order smallint NOT NULL DEFAULT 0,
    active     boolean NOT NULL DEFAULT true
);
SELECT record_columns('services');

CREATE TABLE tools (
    id         smallserial PRIMARY KEY,
    code       text NOT NULL UNIQUE CHECK (code ~ '^[a-z][a-z0-9_]{1,31}$'),
    label      text NOT NULL CHECK (btrim(label) <> ''),
    sort_order smallint NOT NULL DEFAULT 0,
    active     boolean NOT NULL DEFAULT true
);
SELECT record_columns('tools');

INSERT INTO services (code, label, sort_order) VALUES
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

INSERT INTO tools (code, label, sort_order) VALUES
    ('bicycle','Bicycle',1), ('gumboots','Gumboots',2), ('thermometer','Thermometer',3),
    ('medicine_box','Medicine Box',4), ('muac_tape','MUAC Tape',5), ('torch','Torch',6),
    ('register','VHT Reporting Tools',7);
-- NB: the source list's "None" member is deliberately absent. It means
-- "no tools", which is the empty set here, not a tool called None.

--------------------------------------------------------------- applicability
CREATE TABLE service_applicable_cadre (
    id         serial PRIMARY KEY,
    service_id smallint NOT NULL REFERENCES services(id),
    cadre_id   smallint NOT NULL REFERENCES cadres(id),
    UNIQUE (service_id, cadre_id)
);
SELECT record_columns('service_applicable_cadre');
CREATE INDEX service_applicable_cadre_cadre_idx ON service_applicable_cadre (cadre_id);

CREATE TABLE tool_applicable_cadre (
    id       serial PRIMARY KEY,
    tool_id  smallint NOT NULL REFERENCES tools(id),
    cadre_id smallint NOT NULL REFERENCES cadres(id),
    UNIQUE (tool_id, cadre_id)
);
SELECT record_columns('tool_applicable_cadre');
CREATE INDEX tool_applicable_cadre_cadre_idx ON tool_applicable_cadre (cadre_id);

-- Every service and every tool applies to both CHW cadres to begin with.
INSERT INTO service_applicable_cadre (service_id, cadre_id)
SELECT s.id, c.id FROM services s CROSS JOIN cadres c WHERE c.code IN ('vht','chew');
INSERT INTO tool_applicable_cadre (tool_id, cadre_id)
SELECT t.id, c.id FROM tools t CROSS JOIN cadres c WHERE c.code IN ('vht','chew');

-- The posting a worker held on a date: started by then and not yet ended.
-- At most one, because a worker holds at most one open posting and a new one
-- starts no earlier than the old one ended; the latest start wins a same-day
-- transfer, matching what the register shows from that day on.
-- +goose StatementBegin
CREATE FUNCTION deployment_on(worker bigint, on_date date) RETURNS deployments AS $$
    SELECT d.* FROM deployments d
     WHERE d.health_worker_id = worker
       AND d.started_on <= on_date
       AND (d.ended_on IS NULL OR d.ended_on >= on_date)
     ORDER BY d.started_on DESC, d.id DESC
     LIMIT 1
$$ LANGUAGE sql STABLE;
-- +goose StatementEnd

------------------------------------------------------------ service updates
CREATE TABLE health_worker_service_updates (
    id               bigserial PRIMARY KEY,
    health_worker_id bigint NOT NULL REFERENCES health_workers(id),
    reporting_date   date   NOT NULL,
    -- Both derived from the posting held on reporting_date, never supplied:
    -- district_id is the RBAC anchor (invariant 2), deployment_id is what the
    -- applicable services are read through.
    deployment_id    bigint NOT NULL REFERENCES deployments(id),
    district_id      bigint NOT NULL REFERENCES locations(id),
    UNIQUE (health_worker_id, reporting_date)
);
SELECT record_columns('health_worker_service_updates');
CREATE INDEX health_worker_service_updates_district_idx ON health_worker_service_updates (district_id, reporting_date DESC);

-- +goose StatementBegin
CREATE FUNCTION health_worker_service_updates_derive() RETURNS trigger AS $$
DECLARE
    dep deployments;
BEGIN
    IF NEW.reporting_date > current_date THEN
        RAISE EXCEPTION 'a service update cannot be reported for a future date (%)', NEW.reporting_date;
    END IF;
    dep := deployment_on(NEW.health_worker_id, NEW.reporting_date);
    IF dep.id IS NULL THEN
        RAISE EXCEPTION 'health worker % held no posting on %', NEW.health_worker_id, NEW.reporting_date;
    END IF;
    -- On insert the two are filled in whatever was supplied; on update, a
    -- statement naming them without moving the update is refused outright,
    -- the way 0004 refuses one on deployments.
    IF TG_OP = 'UPDATE' AND (NEW.deployment_id, NEW.district_id) IS DISTINCT FROM (dep.id, dep.district_id)
       AND (NEW.health_worker_id, NEW.reporting_date) IS NOT DISTINCT FROM (OLD.health_worker_id, OLD.reporting_date) THEN
        RAISE EXCEPTION 'service update % district and posting are derived from its date, not supplied', NEW.id;
    END IF;
    NEW.deployment_id := dep.id;
    NEW.district_id   := dep.district_id;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER health_worker_service_updates_derive_trg
    BEFORE INSERT OR UPDATE OF health_worker_id, reporting_date, deployment_id, district_id
    ON health_worker_service_updates
    FOR EACH ROW EXECUTE FUNCTION health_worker_service_updates_derive();

CREATE TABLE health_worker_service_update_details (
    id                               bigserial PRIMARY KEY,
    health_worker_service_update_id  bigint   NOT NULL REFERENCES health_worker_service_updates(id) ON DELETE CASCADE,
    service_id                       smallint NOT NULL REFERENCES services(id),
    UNIQUE (health_worker_service_update_id, service_id)
);
SELECT record_columns('health_worker_service_update_details');

-- +goose StatementBegin
CREATE FUNCTION health_worker_service_update_details_check() RETURNS trigger AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM health_worker_service_updates u
          JOIN deployments d ON d.id = u.deployment_id
          JOIN service_applicable_cadre a ON a.cadre_id = d.cadre_id AND a.service_id = NEW.service_id
         WHERE u.id = NEW.health_worker_service_update_id
    ) THEN
        RAISE EXCEPTION 'service % does not apply to the cadre the worker held on that date',
            (SELECT code FROM services WHERE id = NEW.service_id);
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER health_worker_service_update_details_check_trg
    BEFORE INSERT OR UPDATE ON health_worker_service_update_details
    FOR EACH ROW EXECUTE FUNCTION health_worker_service_update_details_check();

--------------------------------------------------------- tool distributions
-- A distribution is an event in a district: the district is its own attribute,
-- chosen by whoever ran it (like a user's or an import batch's), not derived.
-- Every worker who receives something in it must have been posted in that
-- district on that date — the same rule invariant 9 applies to facilities.
CREATE TABLE health_worker_tool_distributions (
    id             bigserial PRIMARY KEY,
    district_id    bigint NOT NULL REFERENCES locations(id),
    reporting_date date   NOT NULL,
    note           text
);
SELECT record_columns('health_worker_tool_distributions');
CREATE INDEX health_worker_tool_distributions_district_idx
    ON health_worker_tool_distributions (district_id, reporting_date DESC);

-- +goose StatementBegin
CREATE FUNCTION health_worker_tool_distributions_check() RETURNS trigger AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM locations WHERE id = NEW.district_id AND level = 'district') THEN
        RAISE EXCEPTION 'tool distribution district_id % is not a district', NEW.district_id;
    END IF;
    IF NEW.reporting_date > current_date THEN
        RAISE EXCEPTION 'a tool distribution cannot be recorded for a future date (%)', NEW.reporting_date;
    END IF;
    IF TG_OP = 'UPDATE' AND (NEW.district_id, NEW.reporting_date) IS DISTINCT FROM (OLD.district_id, OLD.reporting_date)
       AND EXISTS (SELECT 1 FROM health_worker_tool_distribution_details WHERE health_worker_tool_distribution_id = NEW.id) THEN
        RAISE EXCEPTION 'tool distribution % has recipients; its district and date are fixed', NEW.id;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER health_worker_tool_distributions_check_trg
    BEFORE INSERT OR UPDATE ON health_worker_tool_distributions
    FOR EACH ROW EXECUTE FUNCTION health_worker_tool_distributions_check();

CREATE TABLE health_worker_tool_distribution_details (
    id                                 bigserial PRIMARY KEY,
    health_worker_tool_distribution_id bigint   NOT NULL REFERENCES health_worker_tool_distributions(id) ON DELETE CASCADE,
    health_worker_id                   bigint   NOT NULL REFERENCES health_workers(id),
    tool_id                            smallint NOT NULL REFERENCES tools(id),
    quantity                           smallint NOT NULL DEFAULT 1 CHECK (quantity BETWEEN 1 AND 999),
    UNIQUE (health_worker_tool_distribution_id, health_worker_id, tool_id)
);
SELECT record_columns('health_worker_tool_distribution_details');
CREATE INDEX health_worker_tool_distribution_details_worker_idx
    ON health_worker_tool_distribution_details (health_worker_id);

-- +goose StatementBegin
CREATE FUNCTION health_worker_tool_distribution_details_check() RETURNS trigger AS $$
DECLARE
    dist health_worker_tool_distributions;
    dep  deployments;
BEGIN
    SELECT * INTO dist FROM health_worker_tool_distributions WHERE id = NEW.health_worker_tool_distribution_id;
    dep := deployment_on(NEW.health_worker_id, dist.reporting_date);
    IF dep.id IS NULL OR dep.district_id <> dist.district_id THEN
        RAISE EXCEPTION 'health worker % was not posted in district % on %',
            NEW.health_worker_id, dist.district_id, dist.reporting_date;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM tool_applicable_cadre
                    WHERE tool_id = NEW.tool_id AND cadre_id = dep.cadre_id) THEN
        RAISE EXCEPTION 'tool % does not apply to the cadre the worker held on that date',
            (SELECT code FROM tools WHERE id = NEW.tool_id);
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER health_worker_tool_distribution_details_check_trg
    BEFORE INSERT OR UPDATE ON health_worker_tool_distribution_details
    FOR EACH ROW EXECUTE FUNCTION health_worker_tool_distribution_details_check();
