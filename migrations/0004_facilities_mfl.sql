-- +goose Up
-- 0004: facilities re-sourced from the Master Facility List, and the CHW-to-
-- facility attachment tightened.
--
-- 0001 assumed facilities would come from the ODK workbook, whose parent column
-- (`subcountyfilter`) is district slugs for 7,846 of 7,896 rows and subcounty
-- slugs for the rest. The MFL workbook supersedes it:
--
--   data/MFL Updated - 21 feb.xlsx   7,907 rows, name/subcounty/district/region/
--                                    hflevel/ownership/authority
--
-- Every one of the 7,907 rows resolves to a district, using only the two aliases
-- the hierarchy already needs (Luwero > LUWEERO, Sembabule > SSEMBABULE).
--
-- DISTRICT IS THE PARENT, DELIBERATELY. Matching the workbook's `subcounty`
-- column against the hierarchy resolves 3,696 rows, misses 4,201 and is
-- ambiguous for 10: the column holds Town Councils, City Divisions and newer
-- units the admin-units file does not carry under those names. Parenting to
-- subcounty would quarantine over half the register to gain a tier nothing
-- queries. The raw label is kept verbatim in `subcounty_label` so a later
-- reconciliation has something to work from.

ALTER TABLE facilities
    -- MFL carries no facility code, so identity stays (district_id, name) from
    -- 0001. These are attributes, not identity.
    ADD COLUMN level           text,   -- 'HC II' | 'HC III' | 'HC IV' | 'Hospital' | 'Clinic' | 'Drug Shop' | ...
    ADD COLUMN ownership       text,   -- 'GOV' | 'PFP' | 'PNFP'
    ADD COLUMN authority       text,   -- 'MOH' | 'Private' | 'UPDF' | ...
    ADD COLUMN subcounty_label text,   -- raw, unresolved; see the note above
    ADD COLUMN source          text NOT NULL DEFAULT 'mfl_2026_02_21';

-- `level` is left as free text on purpose. The workbook already carries 17
-- distinct values including 'RRH', 'NBB' and one row labelled 'Bank of Uganda';
-- an enum would mean a migration every time the MFL gains a category, for a
-- column the register only ever filters on.

-- 3,389 of 7,907 facilities are government-owned. CHWs report to those; the
-- private clinics and drug shops are loaded for completeness but the picker
-- filters on this index rather than on a hardcoded list.
CREATE INDEX facilities_district_ownership_idx ON facilities (district_id, ownership);

------------------------------------------------- CHW-to-facility must not cross districts
-- chw_profiles.facility_id is the supervising facility, not a placement: the
-- CHW's location still comes from cadre (CHEW > parish, VHT > village). But a
-- CHW attached to a facility in another district breaks the reporting line and
-- would be invisible to the district that supervises them, so the two districts
-- must agree.
-- +goose StatementBegin
CREATE FUNCTION chw_profiles_check_facility_district() RETURNS trigger AS $$
DECLARE
    chw_district      bigint;
    facility_district bigint;
BEGIN
    IF NEW.facility_id IS NULL THEN
        RETURN NEW;
    END IF;

    SELECT district_id INTO chw_district      FROM chws       WHERE id = NEW.chw_id;
    SELECT district_id INTO facility_district FROM facilities WHERE id = NEW.facility_id;

    IF chw_district IS DISTINCT FROM facility_district THEN
        RAISE EXCEPTION 'facility % is in district %, but CHW % is in district %',
            NEW.facility_id, facility_district, NEW.chw_id, chw_district;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER chw_profiles_facility_district_trg
    BEFORE INSERT OR UPDATE OF facility_id ON chw_profiles
    FOR EACH ROW EXECUTE FUNCTION chw_profiles_check_facility_district();

-- The other direction: moving a CHW to another district would silently strand
-- an attachment the trigger above would have refused. Transfers must reassign
-- the facility in the same transaction, so this refuses rather than nulling the
-- column behind the operator's back.
-- +goose StatementBegin
CREATE FUNCTION chws_check_facility_after_move() RETURNS trigger AS $$
DECLARE
    stranded bigint;
BEGIN
    SELECT p.facility_id INTO stranded
      FROM chw_profiles p
      JOIN facilities f ON f.id = p.facility_id
     WHERE p.chw_id = NEW.id AND f.district_id <> NEW.district_id;

    IF stranded IS NOT NULL THEN
        RAISE EXCEPTION 'CHW % moved to district % but is still attached to facility % in another district; reassign or clear the facility in the same transaction',
            NEW.id, NEW.district_id, stranded;
    END IF;
    RETURN NULL;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER chws_facility_after_move_trg
    AFTER UPDATE OF location_id, cadre ON chws
    FOR EACH ROW WHEN (OLD.district_id IS DISTINCT FROM NEW.district_id)
    EXECUTE FUNCTION chws_check_facility_after_move();
