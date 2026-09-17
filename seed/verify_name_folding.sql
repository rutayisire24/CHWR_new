-- Name-folding verification. Run against a seeded database:
--     psql -d chwr -f seed/verify_name_folding.sql
--
-- internal/importer/resolve.go matches a location name by folding it: upper
-- case, separators dropped, and then the administrative tier word handled by
-- the level being matched. Whether a tier word is decoration or part of the
-- name is a fact about the gazetteer, not about Go, so it is asserted here
-- against the loaded hierarchy rather than against a fixture.
--
-- The one thing that must never happen is a fold that merges two true siblings.
-- LUWEERO and LUWEERO TOWN COUNCIL are two different subcounties of one county;
-- a fold collapsing them would place a CHW in the wrong one and nothing
-- downstream would notice, because a name is not an identity here — (parent_id,
-- code) is.
--
-- fold_at below mirrors foldName. A change to one is a change to both.
-- Runs inside a transaction and rolls back, leaving no trace.
\set QUIET on
BEGIN;

-- normalizeName: upper case, every separator dropped.
CREATE OR REPLACE FUNCTION fold_nz(s text) RETURNS text LANGUAGE sql IMMUTABLE AS
$f$ SELECT upper(regexp_replace(coalesce(s,''), '[ \t\n\r.''’\-–_/]', '', 'g')) $f$;

-- foldName. Each '.WORD$' requires a character before the tier word, which is
-- dropSuffix's guard: a name that is nothing but its tier word is left alone.
CREATE OR REPLACE FUNCTION fold_at(lvl text, s text) RETURNS text LANGUAGE sql IMMUTABLE AS
$f$
    SELECT CASE lvl
    WHEN 'subcounty' THEN
        -- Drop the redundant, longest first: SUBCOUNTY itself ends in COUNTY.
        CASE
            WHEN x ~ '.SUBCOUNTIES$' THEN regexp_replace(x, 'SUBCOUNTIES$', '')
            WHEN x ~ '.SUBCOUNTY$'   THEN regexp_replace(x, 'SUBCOUNTY$', '')
            WHEN x ~ '.COUNTY$'      THEN regexp_replace(x, 'COUNTY$', '')
            WHEN x ~ '.SC$'          THEN regexp_replace(x, 'SC$', '')
            ELSE x END
    WHEN 'parish' THEN
        CASE WHEN x ~ '.PARISH$' THEN regexp_replace(x, 'PARISH$', '') ELSE x END
    ELSE fold_nz(s)
    END
    FROM (SELECT CASE
        -- Expand the identifying, so MPIGI T/C meets MPIGI TOWN COUNCIL rather
        -- than MPIGI. Only at subcounty, and before the drop step above.
        WHEN lvl <> 'subcounty'          THEN fold_nz(s)
        WHEN fold_nz(s) ~ 'TOWNCOUNCIL$' THEN fold_nz(s)
        WHEN fold_nz(s) ~ '.TC$'         THEN regexp_replace(fold_nz(s), 'TC$', 'TOWNCOUNCIL')
        ELSE fold_nz(s) END AS x) e
$f$;

DO $$
DECLARE
    lvl text; folded int; shipped int; raw int; sample text;
    regressions int := 0;
BEGIN
    -- Districts are matched among the scope's districts rather than among
    -- siblings, so they are checked as one set.
    SELECT count(*) INTO folded FROM (
        SELECT fold_nz(name) FROM locations WHERE level='district' AND active
        GROUP BY 1 HAVING count(*) > 1) x;
    IF folded > 0 THEN
        RAISE WARNING 'MERGED  <- % district name(s) fold together', folded;
        regressions := regressions + folded;
    ELSE
        RAISE NOTICE 'distinct  district';
    END IF;

    FOREACH lvl IN ARRAY ARRAY['subcounty','parish','village'] LOOP
        -- Three counts, and only the last comparison is the assertion:
        --   raw      siblings that already share a name outright
        --   shipped  siblings normalizeName merges, separators dropped
        --   folded   siblings foldName merges, tier word handled too
        --
        -- raw < shipped is the cost the shipped fold already accepts, and it is
        -- bounded by design: a fold that matches two siblings is quarantined as
        -- an ambiguity with both candidates, never resolved. It is reported here
        -- so the number is known, not to fail the run.
        --
        -- shipped < folded is the regression this file exists to catch: a tier
        -- word that turns out to be part of a name somewhere in the gazetteer.
        SELECT count(*) INTO raw FROM (
            SELECT parent_id FROM locations WHERE level=lvl::location_level AND active
            GROUP BY parent_id, name HAVING count(*) > 1) a;
        SELECT count(*) INTO shipped FROM (
            SELECT parent_id FROM locations WHERE level=lvl::location_level AND active
            GROUP BY parent_id, fold_nz(name) HAVING count(*) > 1) b;
        SELECT count(*) INTO folded FROM (
            SELECT parent_id FROM locations WHERE level=lvl::location_level AND active
            GROUP BY parent_id, fold_at(lvl, name) HAVING count(*) > 1) c;

        IF folded > shipped THEN
            SELECT string_agg(n, ' || ') INTO sample FROM (
                SELECT DISTINCT l.name AS n FROM locations l WHERE l.level=lvl::location_level AND l.active
                 AND EXISTS (SELECT 1 FROM locations s
                     WHERE s.level=l.level AND s.active AND s.parent_id=l.parent_id
                       AND s.id <> l.id AND fold_at(lvl, s.name) = fold_at(lvl, l.name)
                       AND fold_nz(s.name) <> fold_nz(l.name))
                 ORDER BY 1 LIMIT 6) y;
            RAISE WARNING 'MERGED  <- the % tier word merges % sibling group(s), was % — e.g. %',
                lvl, folded, shipped, coalesce(sample, '?');
            regressions := regressions + (folded - shipped);
        ELSIF shipped > raw THEN
            RAISE NOTICE 'distinct  % (tier word merges nothing; separators already merge % group(s), quarantined as ambiguous)',
                lvl, shipped - raw;
        ELSE
            RAISE NOTICE 'distinct  % (tier word merges nothing)', lvl;
        END IF;
    END LOOP;

    RAISE NOTICE '---';
    IF regressions > 0 THEN
        RAISE EXCEPTION 'fold regression: % sibling group(s) merged that normalizeName kept apart', regressions;
    END IF;
    RAISE NOTICE 'the tier word merges no two siblings anywhere';
END $$;

ROLLBACK;
