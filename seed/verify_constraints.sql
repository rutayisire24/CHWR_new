-- Constraint verification suite. Run against a freshly migrated + seeded database:
--     psql -d chwr -f seed/verify_constraints.sql
-- Every case must report "blocked". Any "LEAKED" is a schema regression.
-- Runs inside a transaction and rolls back, leaving no trace.
\set QUIET on
BEGIN;
DO $$
DECLARE
    vil bigint; par bigint; sub bigint; cty bigint; dis bigint; reg bigint; u bigint; chw bigint;
    chw2 bigint; chw_dis bigint; dis_other bigint; par_other bigint;
    fac_same bigint; fac_other bigint;
    cases text[][]; i int; leaked int := 0; blocked int := 0;
BEGIN
    SELECT id INTO reg FROM locations WHERE level='region'    ORDER BY id LIMIT 1;
    SELECT id INTO dis FROM locations WHERE level='district'  ORDER BY id LIMIT 1;
    SELECT id INTO cty FROM locations WHERE level='county'    ORDER BY id LIMIT 1;
    SELECT id INTO sub FROM locations WHERE level='subcounty' ORDER BY id LIMIT 1;
    SELECT id INTO par FROM locations WHERE level='parish'    ORDER BY id LIMIT 1;
    SELECT id INTO vil FROM locations WHERE level='village'   ORDER BY id LIMIT 1;

    INSERT INTO users(email,full_name,password_hash,role)
        VALUES('verify@moh.go.ug','Verify','x','national_admin') RETURNING id INTO u;
    INSERT INTO chws(first_name,last_name,sex,cadre,age_years,location_id,nin)
        VALUES('Aisha','Nabirye','female','vht',34,vil,'CM96068100EM1J') RETURNING id INTO chw;
    INSERT INTO chws(first_name,last_name,sex,cadre,age_years,location_id)
        VALUES('Peter','Okello','male','chew',41,par) RETURNING id INTO chw2;

    -- Facility fixtures. The happy path is asserted by construction: if
    -- attaching a CHW to a facility in their OWN district raised, this DO block
    -- would abort here rather than report a leak.
    SELECT district_id INTO chw_dis FROM chws WHERE id = chw2;
    SELECT id INTO dis_other FROM locations
        WHERE level='district' AND id <> chw_dis ORDER BY id LIMIT 1;
    SELECT p.id INTO par_other FROM locations p JOIN locations d ON p.path LIKE d.path||'%'
        WHERE d.id = dis_other AND p.level = 'parish' ORDER BY p.id LIMIT 1;

    INSERT INTO facilities(district_id,name,slug)
        VALUES(chw_dis,'Verify Same HC','verify-same')   RETURNING id INTO fac_same;
    INSERT INTO facilities(district_id,name,slug)
        VALUES(dis_other,'Verify Other HC','verify-other') RETURNING id INTO fac_other;

    INSERT INTO chw_profiles(chw_id,facility_id) VALUES(chw2,fac_same);
    RAISE NOTICE 'allowed   CHW attached to a facility in their own district';

    cases := ARRAY[
      -- hierarchy ladder
      ['region given a parent',            format('INSERT INTO locations(parent_id,level,name,code) VALUES(%s,''region'',''B'',''99'')',dis)],
      ['district with no parent',          $q$INSERT INTO locations(parent_id,level,name,code) VALUES(NULL,'district','B','99')$q$],
      ['county hung off a region',         format('INSERT INTO locations(parent_id,level,name,code) VALUES(%s,''county'',''B'',''99'')',reg)],
      ['subcounty hung off a district',    format('INSERT INTO locations(parent_id,level,name,code) VALUES(%s,''subcounty'',''B'',''99'')',dis)],
      ['parish hung off a county',         format('INSERT INTO locations(parent_id,level,name,code) VALUES(%s,''parish'',''B'',''99'')',cty)],
      ['village hung off a subcounty',     format('INSERT INTO locations(parent_id,level,name,code) VALUES(%s,''village'',''B'',''99'')',sub)],
      ['duplicate code under one parent',  format('INSERT INTO locations(parent_id,level,name,code) SELECT %s,''village'',''Dup'',code FROM locations WHERE id=%s',(SELECT parent_id FROM locations WHERE id=vil),vil)],
      -- cadre placement
      ['VHT placed at parish',             format('INSERT INTO chws(first_name,last_name,sex,cadre,location_id) VALUES(''A'',''B'',''male'',''vht'',%s)',par)],
      ['CHEW placed at village',           format('INSERT INTO chws(first_name,last_name,sex,cadre,location_id) VALUES(''A'',''B'',''male'',''chew'',%s)',vil)],
      ['CHW placed at subcounty',          format('INSERT INTO chws(first_name,last_name,sex,cadre,location_id) VALUES(''A'',''B'',''male'',''vht'',%s)',sub)],
      ['CHW placed at district',           format('INSERT INTO chws(first_name,last_name,sex,cadre,location_id) VALUES(''A'',''B'',''male'',''chew'',%s)',dis)],
      ['re-cadre without moving',          format('UPDATE chws SET cadre=''chew'' WHERE id=%s',chw)],
      -- chw field constraints
      ['malformed NIN',                    format('INSERT INTO chws(nin,first_name,last_name,sex,cadre,location_id) VALUES(''12345'',''A'',''B'',''male'',''vht'',%s)',vil)],
      ['duplicate NIN',                    format('INSERT INTO chws(nin,first_name,last_name,sex,cadre,location_id) VALUES(''CM96068100EM1J'',''A'',''B'',''male'',''vht'',%s)',vil)],
      ['age below form minimum',           format('INSERT INTO chws(first_name,last_name,sex,cadre,age_years,location_id) VALUES(''A'',''B'',''male'',''vht'',15,%s)',vil)],
      ['age above form maximum',           format('INSERT INTO chws(first_name,last_name,sex,cadre,age_years,location_id) VALUES(''A'',''B'',''male'',''vht'',120,%s)',vil)],
      ['inactive without deactivated_at',  format('INSERT INTO chws(first_name,last_name,sex,cadre,location_id,status) VALUES(''A'',''B'',''male'',''vht'',%s,''inactive'')',vil)],
      ['active with deactivated_at',       format('INSERT INTO chws(first_name,last_name,sex,cadre,location_id,deactivated_at) VALUES(''A'',''B'',''male'',''vht'',%s,now())',vil)],
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
      ['incentive amount without receiving',format('INSERT INTO chw_profiles(chw_id,receives_incentive,incentive_amount_ugx) VALUES(%s,false,50000)',chw)],
      ['incentive above form maximum',     format('INSERT INTO chw_profiles(chw_id,receives_incentive,incentive_amount_ugx) VALUES(%s,true,900000)',chw)],
      ['incentive below form minimum',     format('INSERT INTO chw_profiles(chw_id,receives_incentive,incentive_amount_ugx) VALUES(%s,true,500)',chw)],
      ['phone-owner given fallback number',format('INSERT INTO chw_profiles(chw_id,owns_phone,phone_primary,phone_alternate) VALUES(%s,true,''700123123'',''700999999'')',chw)],
      ['non-owner given a primary phone',  format('INSERT INTO chw_profiles(chw_id,owns_phone,phone_primary) VALUES(%s,false,''700123123'')',chw)],
      ['malformed phone number',           format('INSERT INTO chw_profiles(chw_id,owns_phone,phone_primary) VALUES(%s,true,''+256700123123'')',chw)],
      ['households below form minimum',    format('INSERT INTO chw_profiles(chw_id,households_served) VALUES(%s,1)',chw)],
      ['supervision date without yes',     format('INSERT INTO chw_profiles(chw_id,received_supervision,last_supervised_on) VALUES(%s,false,''2026-03-01'')',chw)],
      ['supervision date mid-month',       format('INSERT INTO chw_profiles(chw_id,received_supervision,last_supervised_on) VALUES(%s,true,''2026-03-17'')',chw)],
      -- junctions
      ['trained on unoffered service',     format('INSERT INTO chw_service_domains(chw_id,domain_id,provides,trained) VALUES(%s,1,false,true)',chw)],
      ['duplicate tool for one CHW',       format('INSERT INTO chw_tools(chw_id,tool_id,functional) SELECT %s,1,true UNION ALL SELECT %s,1,false',chw,chw)],
      -- CHW-to-facility attachment must not cross a district boundary (0004)
      ['attached to another district''s facility',
                                           format('INSERT INTO chw_profiles(chw_id,facility_id) VALUES(%s,%s)',chw,fac_other)],
      ['reattached across districts',      format('UPDATE chw_profiles SET facility_id=%s WHERE chw_id=%s',fac_other,chw2)],
      ['moved to another district while attached',
                                           format('UPDATE chws SET location_id=%s WHERE id=%s',par_other,chw2)]
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
