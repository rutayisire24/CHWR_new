-- Loads the Master Facility List into `facilities`, parented to district.
--
--     python3 seed/extract_facilities.py            # writes seed/out/facilities.tsv
--     psql -d chwr -f seed/load_facilities.sql      # from the repo root
--
-- Requires the hierarchy to be loaded first: every facility hangs off a district.
--
-- \copy performs no variable interpolation, so the path below is literal and
-- relative to psql's working directory. Run from the repo root.
--
-- District is the parent by decision, not by omission — see the header of
-- migrations/0004_facilities_mfl.sql. The workbook's subcounty column is stored
-- raw in `subcounty_label` and never resolved.
--
-- Nothing is dropped: rows whose district will not resolve, and rows whose name
-- is already taken within their district, go to `import_quarantine` with the
-- workbook row number and the row that displaced them.

BEGIN;

CREATE TEMP TABLE stg(
    row_no int, name text, subcounty_label text, district text,
    region text, level text, ownership text, authority text);
\copy stg FROM 'seed/out/facilities.tsv' WITH (FORMAT csv, DELIMITER E'\t', QUOTE E'\b')

-- Same two reconciliations the hierarchy loader needs: the MFL spells these
-- districts the way the ODK form does, the admin-units file does not.
UPDATE stg SET district = 'Luweero'    WHERE district = 'Luwero';
UPDATE stg SET district = 'Ssembabule' WHERE district = 'Sembabule';

CREATE TEMP TABLE resolved AS
SELECT s.*,
       d.id AS district_id,
       regexp_replace(regexp_replace(lower(s.name), '[^a-z0-9]+', '-', 'g'), '^-|-$', '', 'g') AS slug
FROM stg s
LEFT JOIN locations d
       ON d.level = 'district'
      AND regexp_replace(lower(d.name), '[^a-z0-9]', '', 'g')
        = regexp_replace(lower(s.district), '[^a-z0-9]', '', 'g');

-- Identity is (district_id, name), per 0001; the MFL carries no facility code.
-- The workbook holds 12 collisions, all private clinics or drug shops: 5 are the
-- same row listed twice, 7 are genuinely different premises distinguished only
-- by a subcounty this loader does not resolve. Lowest workbook row wins.
CREATE TEMP TABLE ranked AS
SELECT r.*,
       row_number() OVER w AS rn,
       min(r.row_no)  OVER w AS kept_row
FROM resolved r
WHERE r.district_id IS NOT NULL
WINDOW w AS (PARTITION BY r.district_id, lower(r.name) ORDER BY r.row_no);

INSERT INTO facilities (district_id, name, slug, level, ownership, authority, subcounty_label)
SELECT district_id, name, slug,
       nullif(level, ''), nullif(ownership, ''), nullif(authority, ''), nullif(subcounty_label, '')
FROM ranked WHERE rn = 1;

INSERT INTO import_quarantine (source, row_ref, payload, reason, detail)
SELECT 'facilities', r.row_no::text,
       jsonb_build_object('name', r.name, 'subcounty', r.subcounty_label, 'district', r.district,
                          'region', r.region, 'level', r.level, 'ownership', r.ownership,
                          'authority', r.authority),
       'parent_missing',
       format('district %L is not in locations', r.district)
FROM resolved r WHERE r.district_id IS NULL;

INSERT INTO import_quarantine (source, row_ref, payload, reason, detail, candidates)
SELECT 'facilities', d.row_no::text,
       jsonb_build_object('name', d.name, 'subcounty', d.subcounty_label, 'district', d.district,
                          'region', d.region, 'level', d.level, 'ownership', d.ownership,
                          'authority', d.authority),
       'validation_failed',
       CASE WHEN (d.subcounty_label, d.level, d.ownership, d.authority)
               = (k.subcounty_label, k.level, k.ownership, k.authority)
            THEN format('exact duplicate of workbook row %s', d.kept_row)
            ELSE format('name already used in %s by workbook row %s', d.district, d.kept_row)
       END,
       jsonb_build_object('kept_row', d.kept_row, 'kept_subcounty', k.subcounty_label,
                          'kept_level', k.level, 'kept_ownership', k.ownership)
FROM ranked d
JOIN ranked k ON k.district_id = d.district_id AND lower(k.name) = lower(d.name) AND k.rn = 1
WHERE d.rn > 1;

COMMIT;

\echo
\echo === loaded ===
SELECT count(*) AS facilities,
       count(DISTINCT district_id) AS districts,
       count(*) FILTER (WHERE ownership = 'GOV') AS government
FROM facilities;
\echo === quarantined ===
SELECT reason, count(*) FROM import_quarantine
WHERE source = 'facilities' AND resolved_at IS NULL GROUP BY reason ORDER BY 1;
\echo === by level ===
SELECT coalesce(level, '(blank)') AS level, count(*) FROM facilities GROUP BY 1 ORDER BY 2 DESC;
