-- Loads the national administrative hierarchy into `locations`.
--
-- Run seed/extract_units.py first, then:
--     python3 seed/extract_units.py                  # writes seed/out/*.tsv
--     psql -d chwr -f seed/load_hierarchy.sql        # from the repo root
--
-- \copy performs no variable interpolation of any kind, so the paths below are
-- literal and relative to psql's working directory. Run from the repo root.
--
-- Verified against PostgreSQL 18: 84,635 rows loaded in ~3s
-- (15 regions / 146 districts / 353 counties / 2198 subcounties /
--  10716 parishes / 71207 villages), zero losses, zero code_path mismatches.
--
-- The two UPDATEs reconcile district spellings between the admin-units file
-- and data/district_region.tsv; every other district matches by normalized name.

CREATE TEMP TABLE stg(district_code text,district_name text,ea_code text,ea_name text,
  scounty_code text,scounty_name text,parish_code text,parish_name text,
  village_code text,village_name text);
\copy stg FROM 'seed/out/units.tsv' WITH (FORMAT csv, DELIMITER E'\t', QUOTE E'\b')
CREATE TEMP TABLE reg(slug text, label text);
\copy reg FROM 'seed/out/regions.tsv' WITH (FORMAT csv, DELIMITER E'\t', QUOTE E'\b')
CREATE TEMP TABLE dreg(dnorm text, rslug text);
\copy dreg FROM 'seed/out/dregion.tsv' WITH (FORMAT csv, DELIMITER E'\t', QUOTE E'\b')
UPDATE dreg SET dnorm='luweero' WHERE dnorm='luwero';
UPDATE dreg SET dnorm='ssembabule' WHERE dnorm='sembabule';

INSERT INTO locations(parent_id,level,name,code) SELECT NULL,'region',label,slug FROM reg;

INSERT INTO locations(parent_id,level,name,code,code_path)
SELECT r.id,'district',s.district_name,s.district_code,s.district_code
FROM (SELECT DISTINCT district_code,district_name FROM stg) s
JOIN dreg d ON d.dnorm=regexp_replace(lower(s.district_name),'[^a-z0-9]','','g')
JOIN locations r ON r.level='region' AND r.code=d.rslug;

INSERT INTO locations(parent_id,level,name,code,code_path)
SELECT p.id,'county',s.ea_name,s.ea_code,s.district_code||s.ea_code
FROM (SELECT DISTINCT district_code,ea_code,ea_name FROM stg) s
JOIN locations p ON p.level='district' AND p.code_path=s.district_code;

INSERT INTO locations(parent_id,level,name,code,code_path)
SELECT p.id,'subcounty',s.scounty_name,s.scounty_code,s.district_code||s.ea_code||s.scounty_code
FROM (SELECT DISTINCT district_code,ea_code,scounty_code,scounty_name FROM stg) s
JOIN locations p ON p.level='county' AND p.code_path=s.district_code||s.ea_code;

INSERT INTO locations(parent_id,level,name,code,code_path)
SELECT p.id,'parish',s.parish_name,s.parish_code,s.district_code||s.ea_code||s.scounty_code||s.parish_code
FROM (SELECT DISTINCT district_code,ea_code,scounty_code,parish_code,parish_name FROM stg) s
JOIN locations p ON p.level='subcounty' AND p.code_path=s.district_code||s.ea_code||s.scounty_code;

INSERT INTO locations(parent_id,level,name,code,code_path)
SELECT p.id,'village',s.village_name,s.village_code,
       s.district_code||s.ea_code||s.scounty_code||s.parish_code||s.village_code
FROM stg s
JOIN locations p ON p.level='parish' AND p.code_path=s.district_code||s.ea_code||s.scounty_code||s.parish_code;
