-- +goose Up
-- 0008: district_id is derived, and now nothing else can write it
--
-- Invariant 2 says district_id is derived, never supplied. 0003 derived it, but
-- only when a posting's placement changed: the placement trigger fires on
-- UPDATE OF location_id, cadre_id, health_worker_id, so a statement naming
-- district_id alone went straight through, and the sync trigger then carried
-- the wrong district onto the worker. A worker could equally be handed a
-- district directly. district_id is the RBAC anchor, so either write moved a
-- worker into another district's register without moving them anywhere.
--
-- The application never issued either statement. This makes them impossible,
-- not merely absent, for the psql session repairing a row by hand.

------------------------------------------------------------ deployments
-- AFTER, not BEFORE: it has to see the row once the placement trigger is done
-- with it. A statement setting location_id and district_id together has had
-- its district re-derived by then and passes; one naming district_id alone
-- keeps the value it supplied, and that value must be the one the path says.
-- +goose StatementBegin
CREATE FUNCTION deployments_check_district_derived() RETURNS trigger AS $$
DECLARE
    derived bigint;
BEGIN
    SELECT a.id INTO derived
      FROM locations c
      JOIN locations a ON c.path LIKE a.path || '%'
     WHERE c.id = NEW.location_id AND a.level = 'district';
    IF NEW.district_id IS DISTINCT FROM derived THEN
        RAISE EXCEPTION 'deployment % district_id is derived from its location (%), not supplied (%)',
            NEW.id, derived, NEW.district_id;
    END IF;
    RETURN NULL;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER deployments_district_derived_trg
    AFTER UPDATE OF district_id ON deployments
    FOR EACH ROW EXECUTE FUNCTION deployments_check_district_derived();

------------------------------------------------------------ health_workers
-- The worker's district is written by exactly one thing: the sync trigger on
-- deployments (0003), which runs its UPDATE from inside a trigger. Any write to
-- the column made at trigger depth 1 is therefore a statement a person or the
-- application issued directly, and is refused — on insert, where a new worker
-- has no posting to derive a district from yet, and on update.
--
-- Depth rather than comparing against "the latest deployment": the sync trigger
-- follows whichever posting was written last, and a second definition of
-- "latest" here would be a second rule free to disagree with it.
-- +goose StatementBegin
CREATE FUNCTION health_workers_check_district_derived() RETURNS trigger AS $$
BEGIN
    IF pg_trigger_depth() > 1 THEN
        RETURN NEW;
    END IF;
    IF TG_OP = 'INSERT' AND NEW.district_id IS NOT NULL THEN
        RAISE EXCEPTION 'health worker district_id is derived from their deployment, not supplied';
    END IF;
    IF TG_OP = 'UPDATE' AND NEW.district_id IS DISTINCT FROM OLD.district_id THEN
        RAISE EXCEPTION 'health worker % district_id is derived from their deployment, not supplied', NEW.id;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER health_workers_district_derived_trg
    BEFORE INSERT OR UPDATE OF district_id ON health_workers
    FOR EACH ROW EXECUTE FUNCTION health_workers_check_district_derived();
