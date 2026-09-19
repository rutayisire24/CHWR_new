-- Constraint verification suite. Run against a freshly migrated + seeded database:
--     psql -d chwr -f seed/verify_constraints.sql
-- Every case must report "blocked". Any "LEAKED" is a schema regression.
-- Runs inside a transaction and rolls back, leaving no trace.
\set QUIET on
BEGIN;
DO $$
DECLARE
    vil bigint; par bigint; sub bigint; cty bigint; dis bigint; reg bigint; u bigint;
    w1 bigint; w2 bigint; w3 bigint; w4 bigint;
    dep1 bigint; dep2 bigint; dep3 bigint;
    vht smallint; chew smallint;
    d1 bigint; d2 bigint; par_other bigint;
    fac_same bigint; fac_other bigint;
    cases text[][]; i int; leaked int := 0; blocked int := 0;
BEGIN
    SELECT id INTO reg FROM locations WHERE level='region'    ORDER BY id LIMIT 1;
    SELECT id INTO dis FROM locations WHERE level='district'  ORDER BY id LIMIT 1;
    SELECT id INTO cty FROM locations WHERE level='county'    ORDER BY id LIMIT 1;
    SELECT id INTO sub FROM locations WHERE level='subcounty' ORDER BY id LIMIT 1;
    -- The fixture parish and village must sit in the SAME district, so the
    -- facility cases know which district a cross is across from.
    SELECT p.id INTO par FROM locations p
        WHERE p.path LIKE (SELECT path FROM locations WHERE id = dis)||'%'
          AND p.level = 'parish' ORDER BY p.id LIMIT 1;
    SELECT v.id INTO vil FROM locations v
        WHERE v.path LIKE (SELECT path FROM locations WHERE id = dis)||'%'
          AND v.level = 'village' ORDER BY v.id LIMIT 1;

    SELECT id INTO vht  FROM cadres WHERE slug = 'vht';
    SELECT id INTO chew FROM cadres WHERE slug = 'chew';

    INSERT INTO users(email,full_name,password_hash,role)
        VALUES('verify@moh.go.ug','Verify','x','national_admin') RETURNING id INTO u;

    -- Happy-path fixtures. They are asserted by construction: if creating a
    -- worker with their first deployment raised, or attaching a facility in
    -- the deployment's OWN district raised, this DO block would abort here
    -- rather than report a leak.
    INSERT INTO health_workers(first_name,last_name,sex,age_years,nin)
        VALUES('Aisha','Nabirye','female',34,'CM96068100EM1J') RETURNING id INTO w1;
    INSERT INTO deployments(health_worker_id,cadre_id,location_id,district_id)
        VALUES(w1,vht,vil,0) RETURNING id INTO dep1;

    INSERT INTO health_workers(first_name,last_name,sex,age_years)
        VALUES('Peter','Okello','male',41) RETURNING id INTO w2;
    INSERT INTO deployments(health_worker_id,cadre_id,location_id,district_id)
        VALUES(w2,chew,par,0) RETURNING id INTO dep2;

    -- The district anchor follows the deployment, and survives into other
    -- statements (the sync trigger fires within the inserting one).
    SELECT district_id INTO d1 FROM deployments WHERE id = dep2;
    IF d1 IS NULL OR d1 <> (SELECT district_id FROM health_workers WHERE id = w2) THEN
        RAISE EXCEPTION 'the worker district anchor did not follow the deployment';
    END IF;
    SELECT id INTO d2 FROM locations WHERE level='district' AND id <> d1 ORDER BY id LIMIT 1;
    SELECT p.id INTO par_other FROM locations p JOIN locations d ON p.path LIKE d.path||'%'
        WHERE d.id = d2 AND p.level = 'parish' ORDER BY p.id LIMIT 1;

    INSERT INTO facilities(district_id,name,slug)
        VALUES(d1,'Verify Same HC','verify-same')   RETURNING id INTO fac_same;
    INSERT INTO facilities(district_id,name,slug)
        VALUES(d2,'Verify Other HC','verify-other') RETURNING id INTO fac_other;

    UPDATE deployments SET facility_id = fac_same WHERE id = dep2;
    RAISE NOTICE 'allowed   deployment attached to a facility in its own district';

    -- A worker who left: their posting ended before the deactivation, which is
    -- the only order the schema allows.
    INSERT INTO health_workers(first_name,last_name,sex)
        VALUES('Gone','Already','male') RETURNING id INTO w3;
    INSERT INTO deployments(health_worker_id,cadre_id,location_id,district_id)
        VALUES(w3,vht,vil,0) RETURNING id INTO dep3;
    UPDATE deployments SET ended_on = current_date, end_reason = 'left'
        WHERE id = dep3;
    UPDATE health_workers SET status='inactive', deactivated_at=now(),
        deactivation_reason='left' WHERE id = w3;

    -- A worker with no posting yet, for the insert-time facility refusal.
    INSERT INTO health_workers(first_name,last_name,sex)
        VALUES('Fresh','Worker','female') RETURNING id INTO w4;

    cases := ARRAY[
      -- hierarchy ladder
      ['region given a parent',            format('INSERT INTO locations(parent_id,level,name,code) VALUES(%s,''region'',''B'',''99'')',dis)],
      ['district with no parent',          $q$INSERT INTO locations(parent_id,level,name,code) VALUES(NULL,'district','B','99')$q$],
      ['county hung off a region',         format('INSERT INTO locations(parent_id,level,name,code) VALUES(%s,''county'',''B'',''99'')',reg)],
      ['subcounty hung off a district',    format('INSERT INTO locations(parent_id,level,name,code) VALUES(%s,''subcounty'',''B'',''99'')',dis)],
      ['parish hung off a county',         format('INSERT INTO locations(parent_id,level,name,code) VALUES(%s,''parish'',''B'',''99'')',cty)],
      ['village hung off a subcounty',     format('INSERT INTO locations(parent_id,level,name,code) VALUES(%s,''village'',''B'',''99'')',sub)],
      ['duplicate code under one parent',  format('INSERT INTO locations(parent_id,level,name,code) SELECT %s,''village'',''Dup'',code FROM locations WHERE id=%s',(SELECT parent_id FROM locations WHERE id=vil),vil)],
      -- cadre placement, read from the cadres row
      ['VHT placed at parish',             format('INSERT INTO deployments(health_worker_id,cadre_id,location_id,district_id) VALUES(%s,%s,%s,0)',w4,vht,par)],
      ['CHEW placed at village',           format('INSERT INTO deployments(health_worker_id,cadre_id,location_id,district_id) VALUES(%s,%s,%s,0)',w4,chew,vil)],
      ['VHT placed at subcounty',          format('INSERT INTO deployments(health_worker_id,cadre_id,location_id,district_id) VALUES(%s,%s,%s,0)',w4,vht,sub)],
      ['CHEW placed at district',          format('INSERT INTO deployments(health_worker_id,cadre_id,location_id,district_id) VALUES(%s,%s,%s,0)',w4,chew,dis)],
      ['re-cadre without moving',          format('UPDATE deployments SET cadre_id=%s WHERE id=%s',chew,dep1)],
      -- one active posting per worker
      ['a second active deployment',       format('INSERT INTO deployments(health_worker_id,cadre_id,location_id,district_id) VALUES(%s,%s,%s,0)',w1,vht,vil)],
      -- the deployment lifecycle
      ['ended without a reason',           format('UPDATE deployments SET ended_on=current_date WHERE id=%s',dep1)],
      ['a reason without an end',          format('INSERT INTO deployments(health_worker_id,cadre_id,location_id,district_id,end_reason) VALUES(%s,%s,%s,0,''x'')',w4,vht,vil)],
      ['ended before it started',          format('INSERT INTO deployments(health_worker_id,cadre_id,location_id,district_id,started_on,ended_on,end_reason) VALUES(%s,%s,%s,0,''2026-01-10'',''2026-01-09'',''x'')',w4,vht,vil)],
      -- health worker field constraints
      ['malformed NIN',                    $q$INSERT INTO health_workers(nin,first_name,last_name,sex) VALUES('12345','A','B','male')$q$],
      ['duplicate NIN',                    $q$INSERT INTO health_workers(nin,first_name,last_name,sex) VALUES('CM96068100EM1J','A','B','male')$q$],
      ['age below form minimum',           $q$INSERT INTO health_workers(first_name,last_name,sex,age_years) VALUES('A','B','male',15)$q$],
      ['age above form maximum',           $q$INSERT INTO health_workers(first_name,last_name,sex,age_years) VALUES('A','B','male',120)$q$],
      ['inactive without deactivated_at',  $q$INSERT INTO health_workers(first_name,last_name,sex,status) VALUES('A','B','male','inactive')$q$],
      ['active with deactivated_at',       $q$INSERT INTO health_workers(first_name,last_name,sex,deactivated_at) VALUES('A','B','male',now())$q$],
      -- deactivation must end the posting first (w1's is still open)
      ['deactivated with an open posting', format('UPDATE health_workers SET status=''inactive'', deactivated_at=now(), deactivation_reason=''x'' WHERE id=%s',w1)],
      -- and an inactive worker takes no new posting (w3 left above)
      ['deployed while inactive',          format('INSERT INTO deployments(health_worker_id,cadre_id,location_id,district_id) VALUES(%s,%s,%s,0)',w3,vht,vil)],
      -- users / rbac
      ['national role with a district',    format('INSERT INTO users(email,full_name,password_hash,role,district_id) VALUES(''a@x'',''A'',''x'',''national_admin'',%s)',dis)],
      ['national viewer with a district',  format('INSERT INTO users(email,full_name,password_hash,role,district_id) VALUES(''b@x'',''B'',''x'',''national_viewer'',%s)',dis)],
      ['district role without a district', $q$INSERT INTO users(email,full_name,password_hash,role) VALUES('c@x','C','x','district_manager')$q$],
      ['district_id pointing at a village',format('INSERT INTO users(email,full_name,password_hash,role,district_id) VALUES(''d@x'',''D'',''x'',''district_viewer'',%s)',vil)],
      ['duplicate email',                  $q$INSERT INTO users(email,full_name,password_hash,role) VALUES('VERIFY@moh.go.ug','E','x','national_admin')$q$],
      -- facilities
      ['facility parented to a village',   format('INSERT INTO facilities(district_id,name,slug) VALUES(%s,''Bad HC'',''bad_hc'')',vil)],
      ['facility parented to a subcounty', format('INSERT INTO facilities(district_id,name,slug) VALUES(%s,''Bad HC'',''bad_hc'')',sub)],
      -- profile branching
      ['incentive amount without receiving',format('INSERT INTO chw_profiles(health_worker_id,receives_incentive,incentive_amount_ugx) VALUES(%s,false,50000)',w1)],
      ['incentive above form maximum',     format('INSERT INTO chw_profiles(health_worker_id,receives_incentive,incentive_amount_ugx) VALUES(%s,true,900000)',w1)],
      ['incentive below form minimum',     format('INSERT INTO chw_profiles(health_worker_id,receives_incentive,incentive_amount_ugx) VALUES(%s,true,500)',w1)],
      ['phone-owner given fallback number',format('INSERT INTO chw_profiles(health_worker_id,owns_phone,phone_primary,phone_alternate) VALUES(%s,true,''700123123'',''700999999'')',w1)],
      ['non-owner given a primary phone',  format('INSERT INTO chw_profiles(health_worker_id,owns_phone,phone_primary) VALUES(%s,false,''700123123'')',w1)],
      ['malformed phone number',           format('INSERT INTO chw_profiles(health_worker_id,owns_phone,phone_primary) VALUES(%s,true,''+256700123123'')',w1)],
      ['households below form minimum',    format('INSERT INTO chw_profiles(health_worker_id,households_served) VALUES(%s,1)',w1)],
      ['supervision date without yes',     format('INSERT INTO chw_profiles(health_worker_id,received_supervision,last_supervised_on) VALUES(%s,false,''2026-03-01'')',w1)],
      ['supervision date mid-month',       format('INSERT INTO chw_profiles(health_worker_id,received_supervision,last_supervised_on) VALUES(%s,true,''2026-03-17'')',w1)],
      -- junctions
      ['trained on unoffered service',     format('INSERT INTO chw_service_domains(health_worker_id,domain_id,provides,trained) VALUES(%s,1,false,true)',w1)],
      ['duplicate tool for one worker',    format('INSERT INTO chw_tools(health_worker_id,tool_id,functional) SELECT %s,1,true UNION ALL SELECT %s,1,false',w1,w1)],
      -- the facility must be in the deployment's own district, in both directions
      ['attached across districts',        format('UPDATE deployments SET facility_id=%s WHERE id=%s',fac_other,dep2)],
      ['attached across districts at insert',
                                           format('INSERT INTO deployments(health_worker_id,cadre_id,location_id,district_id,facility_id) VALUES(%s,%s,%s,0,%s)',w4,vht,vil,fac_other)],
      ['moved to another district while attached',
                                           format('UPDATE deployments SET location_id=%s WHERE id=%s',par_other,dep2)]
    ];

    FOR i IN 1..array_length(cases,1) LOOP
        BEGIN
            EXECUTE cases[i][2];
            RAISE WARNING 'LEAKED  <- %', cases[i][1];
            leaked := leaked + 1;
        EXCEPTION WHEN others THEN
            RAISE NOTICE 'blocked   %', cases[i][1];
            blocked := blocked + 1;
        END;
    END LOOP;

    RAISE NOTICE '---';
    RAISE NOTICE '% blocked, % leaked, % total', blocked, leaked, blocked + leaked;
    IF leaked > 0 THEN RAISE EXCEPTION 'schema regression: % case(s) leaked', leaked; END IF;
END $$;
ROLLBACK;
