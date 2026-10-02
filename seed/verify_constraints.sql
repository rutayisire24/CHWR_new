-- Constraint verification suite. Run against a freshly migrated + seeded database:
--     psql -d hwr -f seed/verify_constraints.sql
-- Every case must report "blocked". Any "LEAKED" is a schema regression.
-- Runs inside a transaction and rolls back, leaving no trace.
\set QUIET on
BEGIN;
-- The questionnaire's branch and subset rules run at commit; this suite never
-- commits, so it asks for them at the end of each statement instead.
SET CONSTRAINTS ALL IMMEDIATE;
DO $$
DECLARE
    vil bigint; par bigint; sub bigint; cty bigint; dis bigint; reg bigint; u bigint;
    p1 bigint; p2 bigint; p3 bigint; p4 bigint; p5 bigint;
    w1 bigint; w2 bigint; w3 bigint; w4 bigint;
    dep1 bigint; dep2 bigint; dep3 bigint;
    vht smallint; chew smallint;
    d1 bigint; d2 bigint; par_other bigint;
    fac_same bigint; fac_other bigint;
    cat2 smallint; unused smallint;
    batch bigint;
    baseline smallint; sub1 bigint; sub_other bigint;
    q_owns int; q_reporting int; q_households int; q_amount int; q_supervised int;
    q_held int; q_functional int; q_frequency int;
    bicycle smallint; iccm smallint; upd bigint; dist bigint; dist_other bigint;
    created timestamptz; c_by bigint;
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

    SELECT id INTO vht  FROM cadres WHERE code = 'vht';
    SELECT id INTO chew FROM cadres WHERE code = 'chew';

    INSERT INTO users(email,full_name,password_hash,role)
        VALUES('verify@moh.go.ug','Verify','x','national_admin') RETURNING id INTO u;

    -- Record columns: the acting user is named once per transaction and lands
    -- on every row written in it.
    PERFORM set_config('hwr.actor_id', u::text, true);

    -- Happy-path fixtures. They are asserted by construction: if creating a
    -- worker with their first deployment raised, or attaching a facility in
    -- the deployment's OWN district raised, this DO block would abort here
    -- rather than report a leak.
    INSERT INTO persons(first_name,last_name,sex,dob,dob_estimated,nin)
        VALUES('Aisha','Nabirye','female','1992-07-01',true,'CM96068100EM1J') RETURNING id INTO p1;
    INSERT INTO health_workers(person_id) VALUES(p1) RETURNING id INTO w1;
    INSERT INTO deployments(health_worker_id,cadre_id,location_id,district_id)
        VALUES(w1,vht,vil,0) RETURNING id INTO dep1;

    INSERT INTO persons(first_name,last_name,sex) VALUES('Peter','Okello','male') RETURNING id INTO p2;
    INSERT INTO health_workers(person_id) VALUES(p2) RETURNING id INTO w2;
    INSERT INTO deployments(health_worker_id,cadre_id,location_id,district_id)
        VALUES(w2,chew,par,0) RETURNING id INTO dep2;

    IF (SELECT created_by FROM persons WHERE id = p1) IS DISTINCT FROM u
       OR (SELECT last_updated_by FROM deployments WHERE id = dep1) IS DISTINCT FROM u THEN
        RAISE EXCEPTION 'the record columns did not carry the acting user';
    END IF;
    RAISE NOTICE 'allowed   record columns stamped with the acting user';

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

    -- The first deployment issues the worker code, and the posting carries it
    -- with its ordinal.
    IF (SELECT worker_code FROM health_workers WHERE id = w1) !~ '^[A-Z]{3}[0-9]{5}$' THEN
        RAISE EXCEPTION 'the first deployment did not issue a worker code';
    END IF;
    IF (SELECT code FROM deployments WHERE id = dep1) <> (SELECT worker_code FROM health_workers WHERE id = w1) || '-01' THEN
        RAISE EXCEPTION 'the first deployment was not coded <worker_code>-01';
    END IF;
    RAISE NOTICE 'allowed   worker code and deployment code issued with the first posting';

    -- The cadre taxonomy as an administrator edits it. A cadre in use may still
    -- be renamed, re-aliased, re-sorted and retired; an unused one may change
    -- shape entirely.
    INSERT INTO cadre_categories(code,label) VALUES('verify_cat','Verify Category')
        RETURNING id INTO cat2;
    UPDATE cadres SET label = label || ' ', import_aliases = import_aliases || '{verify}',
                      sort_order = 9, active = false WHERE id = vht;
    UPDATE cadres SET label = btrim(label), active = true WHERE id = vht;
    INSERT INTO cadres(cadre_category_id,code,label,placement_level)
        VALUES(cat2,'verify_unused','Verify Unused','subcounty') RETURNING id INTO unused;
    UPDATE cadres SET placement_level='district', code='verify_moved', cadre_category_id=1 WHERE id = unused;
    RAISE NOTICE 'allowed   in-use cadre relabelled and retired; unused cadre reshaped';

    -- A worker who left: their posting ended before the deactivation, which is
    -- the only order the schema allows.
    INSERT INTO persons(first_name,last_name,sex) VALUES('Gone','Already','male') RETURNING id INTO p3;
    INSERT INTO health_workers(person_id) VALUES(p3) RETURNING id INTO w3;
    INSERT INTO deployments(health_worker_id,cadre_id,location_id,district_id)
        VALUES(w3,vht,vil,0) RETURNING id INTO dep3;
    UPDATE deployments SET ended_on = current_date, end_reason = 'left'
        WHERE id = dep3;
    UPDATE health_workers SET status='inactive', deactivated_at=now(),
        deactivation_reason='left' WHERE id = w3;

    -- A worker with no posting yet, for the insert-time facility refusal.
    INSERT INTO persons(first_name,last_name,sex) VALUES('Fresh','Worker','female') RETURNING id INTO p4;
    INSERT INTO health_workers(person_id) VALUES(p4) RETURNING id INTO w4;
    INSERT INTO persons(first_name,last_name,sex) VALUES('Spare','Person','female') RETURNING id INTO p5;

    -- Person satellites.
    INSERT INTO person_contacts(person_id,kind,value,owned,for_reporting,is_primary)
        VALUES(p1,'phone','772123456',true,true,true);
    INSERT INTO person_languages(person_id,language_id,understanding_grade,reading_grade)
        SELECT p1, id, 'good', 'none' FROM languages WHERE code = 'english';
    RAISE NOTICE 'allowed   a phone contact and a language with a "none" grade';

    -- The CHW baseline: a valid submission, branch opened before it is used.
    SELECT id INTO baseline FROM profiles WHERE code = 'chw_baseline';
    SELECT id INTO q_owns       FROM profile_questions WHERE profile_id = baseline AND code = 'owns_phone';
    SELECT id INTO q_reporting  FROM profile_questions WHERE profile_id = baseline AND code = 'phone_for_reporting';
    SELECT id INTO q_households FROM profile_questions WHERE profile_id = baseline AND code = 'households_served';
    SELECT id INTO q_amount     FROM profile_questions WHERE profile_id = baseline AND code = 'incentive_amount_ugx';
    SELECT id INTO q_frequency  FROM profile_questions WHERE profile_id = baseline AND code = 'incentive_frequency';
    SELECT id INTO q_supervised FROM profile_questions WHERE profile_id = baseline AND code = 'last_supervised_on';
    SELECT id INTO q_held       FROM profile_questions WHERE profile_id = baseline AND code = 'tools_held';
    SELECT id INTO q_functional FROM profile_questions WHERE profile_id = baseline AND code = 'tools_functional';
    SELECT id INTO bicycle FROM tools WHERE code = 'bicycle';
    SELECT id INTO iccm FROM services WHERE code = 'iccm';

    INSERT INTO health_worker_profiles(health_worker_id,profile_id,source)
        VALUES(w1,baseline,'form') RETURNING id INTO sub1;
    INSERT INTO health_worker_profile_responses(health_worker_profile_id,profile_question_id,response_option_id)
        VALUES(sub1,q_owns,1), (sub1,q_held,bicycle);
    INSERT INTO health_worker_profile_responses(health_worker_profile_id,profile_question_id,response_option_id)
        VALUES(sub1,q_reporting,2), (sub1,q_functional,bicycle);
    INSERT INTO health_worker_profile_responses(health_worker_profile_id,profile_question_id,response)
        VALUES(sub1,q_households,'120');
    RAISE NOTICE 'allowed   a baseline submission with a branch and a subset';

    -- Service updates and tool distributions, against the posting held today.
    INSERT INTO health_worker_service_updates(health_worker_id,reporting_date,deployment_id,district_id)
        VALUES(w1,current_date,0,0) RETURNING id INTO upd;
    INSERT INTO health_worker_service_update_details(health_worker_service_update_id,service_id) VALUES(upd,iccm);
    IF (SELECT district_id FROM health_worker_service_updates WHERE id = upd) <> (SELECT district_id FROM deployments WHERE id = dep1) THEN
        RAISE EXCEPTION 'a service update did not derive its district from the posting';
    END IF;
    INSERT INTO health_worker_tool_distributions(district_id,reporting_date) VALUES(d1,current_date) RETURNING id INTO dist;
    INSERT INTO health_worker_tool_distribution_details(health_worker_tool_distribution_id,health_worker_id,tool_id)
        VALUES(dist,w1,bicycle);
    INSERT INTO health_worker_tool_distributions(district_id,reporting_date) VALUES(d2,current_date) RETURNING id INTO dist_other;
    RAISE NOTICE 'allowed   a service update and a tool distribution in the worker''s own district';

    -- A pending import batch, for the refusal-explained cases.
    INSERT INTO import_batches(filename,format,uploaded_by)
        VALUES('verify.csv','csv',u) RETURNING id INTO batch;

    cases := ARRAY[
      -- hierarchy ladder
      ['region given a parent',            format('INSERT INTO locations(parent_id,level,name,code) VALUES(%s,''region'',''B'',''99'')',dis)],
      ['district with no parent',          $q$INSERT INTO locations(parent_id,level,name,code) VALUES(NULL,'district','B','99')$q$],
      ['county hung off a region',         format('INSERT INTO locations(parent_id,level,name,code) VALUES(%s,''county'',''B'',''99'')',reg)],
      ['subcounty hung off a district',    format('INSERT INTO locations(parent_id,level,name,code) VALUES(%s,''subcounty'',''B'',''99'')',dis)],
      ['parish hung off a county',         format('INSERT INTO locations(parent_id,level,name,code) VALUES(%s,''parish'',''B'',''99'')',cty)],
      ['village hung off a subcounty',     format('INSERT INTO locations(parent_id,level,name,code) VALUES(%s,''village'',''B'',''99'')',sub)],
      ['duplicate code under one parent',  format('INSERT INTO locations(parent_id,level,name,code) SELECT %s,''village'',''Dup'',code FROM locations WHERE id=%s',(SELECT parent_id FROM locations WHERE id=vil),vil)],
      -- record columns: identity and authorship are fixed at insert
      ['uuid rewritten',                   format('UPDATE persons SET uuid=gen_random_uuid() WHERE id=%s',p1)],
      ['created_by rewritten',             format('UPDATE persons SET created_by=NULL WHERE id=%s',p1)],
      ['created_on rewritten',             format('UPDATE deployments SET created_on=now()-interval ''1 day'' WHERE id=%s',dep1)],
      ['uuid shared by two rows',          format('INSERT INTO persons(first_name,last_name,sex,uuid) SELECT ''A'',''B'',''male'',uuid FROM persons WHERE id=%s',p1)],
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
      -- the deployment code is issued, never supplied, never changed
      ['deployment code supplied on insert',format('INSERT INTO deployments(health_worker_id,cadre_id,location_id,district_id,code) VALUES(%s,%s,%s,0,''ZZZ00001-01'')',w4,vht,vil)],
      ['deployment code changed',          format('UPDATE deployments SET code=''ZZZ00001-01'' WHERE id=%s',dep1)],
      ['deployment code cleared',          format('UPDATE deployments SET code=NULL WHERE id=%s',dep1)],
      -- person field constraints
      ['malformed NIN',                    $q$INSERT INTO persons(nin,first_name,last_name,sex) VALUES('12345','A','B','male')$q$],
      ['duplicate NIN',                    $q$INSERT INTO persons(nin,first_name,last_name,sex) VALUES('CM96068100EM1J','A','B','male')$q$],
      ['blank first name',                 $q$INSERT INTO persons(first_name,last_name,sex) VALUES(' ','B','male')$q$],
      ['birth date before 1900',           $q$INSERT INTO persons(first_name,last_name,sex,dob) VALUES('A','B','male','1899-12-31')$q$],
      ['an estimate with no date',         $q$INSERT INTO persons(first_name,last_name,sex,dob_estimated) VALUES('A','B','male',true)$q$],
      ['a person on two workers',          format('INSERT INTO health_workers(person_id) VALUES(%s)',p1)],
      -- satellites
      ['malformed phone contact',          format('INSERT INTO person_contacts(person_id,kind,value) VALUES(%s,''phone'',''+256700123123'')',p1)],
      ['email contact with no @',          format('INSERT INTO person_contacts(person_id,kind,value) VALUES(%s,''email'',''nobody'')',p1)],
      ['ownership on an email',            format('INSERT INTO person_contacts(person_id,kind,value,owned) VALUES(%s,''email'',''a@b.ug'',true)',p1)],
      ['two primary phones',               format('INSERT INTO person_contacts(person_id,kind,value,is_primary) VALUES(%s,''phone'',''700000001'',true)',p1)],
      ['one document on two people',       format('INSERT INTO person_ids(person_id,identifier_type_id,number) SELECT p, 1, ''A1'' FROM unnest(ARRAY[%s,%s]) p',p1,p2)],
      ['a language listed twice',          format('INSERT INTO person_languages(person_id,language_id) SELECT %s, id FROM languages WHERE code=''english''',p1)],
      ['training ending before it began',  format('INSERT INTO person_training(person_id,title,started_on,ended_on) VALUES(%s,''T'',''2026-02-02'',''2026-02-01'')',p1)],
      -- the worker
      ['inactive without deactivated_at',  format('INSERT INTO health_workers(person_id,status) VALUES(%s,''inactive'')',p5)],
      ['active with deactivated_at',       format('INSERT INTO health_workers(person_id,deactivated_at) VALUES(%s,now())',p5)],
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
      ['facility code shared',             format('UPDATE facilities SET code=''F1'' WHERE id IN (%s,%s)',fac_same,fac_other)],
      -- the facility must be in the deployment's own district, in both directions
      ['attached across districts',        format('UPDATE deployments SET facility_id=%s WHERE id=%s',fac_other,dep2)],
      ['attached across districts at insert',
                                           format('INSERT INTO deployments(health_worker_id,cadre_id,location_id,district_id,facility_id) VALUES(%s,%s,%s,0,%s)',w4,vht,vil,fac_other)],
      ['moved to another district while attached',
                                           format('UPDATE deployments SET location_id=%s WHERE id=%s',par_other,dep2)],
      -- the questionnaire: each answer against its question
      ['an option the question lacks',     format('INSERT INTO health_worker_profile_responses(health_worker_profile_id,profile_question_id,response_option_id) VALUES(%s,%s,99)',sub1,q_frequency)],
      ['a typed answer to a closed question',format('INSERT INTO health_worker_profile_responses(health_worker_profile_id,profile_question_id,response) VALUES(%s,%s,''torch'')',sub1,q_held)],
      ['a second answer to a single question',format('INSERT INTO health_worker_profile_responses(health_worker_profile_id,profile_question_id,response) VALUES(%s,%s,''90'')',sub1,q_households)],
      ['households below the minimum',     format('INSERT INTO health_worker_profiles(health_worker_id,profile_id,source) VALUES(%s,%s,''form''); INSERT INTO health_worker_profile_responses(health_worker_profile_id,profile_question_id,response) VALUES(currval(''health_worker_profiles_id_seq''),%s,''1'')',w2,baseline,q_households)],
      ['a number that is not one',         format('INSERT INTO health_worker_profiles(health_worker_id,profile_id,source) VALUES(%s,%s,''form''); INSERT INTO health_worker_profile_responses(health_worker_profile_id,profile_question_id,response) VALUES(currval(''health_worker_profiles_id_seq''),%s,''many'')',w2,baseline,q_households)],
      ['a month that is mid-month',        format('INSERT INTO health_worker_profiles(health_worker_id,profile_id,source) VALUES(%s,%s,''form''); INSERT INTO health_worker_profile_responses(health_worker_profile_id,profile_question_id,response) VALUES(currval(''health_worker_profiles_id_seq''),%s,''2026-03-17'')',w2,baseline,q_supervised)],
      -- the questionnaire: the submission as a whole
      ['incentive amount without receiving',format('INSERT INTO health_worker_profile_responses(health_worker_profile_id,profile_question_id,response) VALUES(%s,%s,''50000'')',sub1,q_amount)],
      ['a functional tool not held',       format('INSERT INTO health_worker_profile_responses(health_worker_profile_id,profile_question_id,response_option_id) SELECT %s,%s,id FROM tools WHERE code=''torch''',sub1,q_functional)],
      ['none beside a tool',               format('INSERT INTO health_worker_profile_responses(health_worker_profile_id,profile_question_id,response_option_id) VALUES(%s,%s,0)',sub1,q_held)],
      ['a question from another profile',  format('INSERT INTO profiles(code,profile_name) VALUES(''verify_p'',''Verify''); INSERT INTO profile_questions(profile_id,question_id,code) SELECT currval(''profiles_id_seq''), question_id, code FROM profile_questions WHERE id=%s; INSERT INTO health_worker_profile_responses(health_worker_profile_id,profile_question_id,response) VALUES(%s,currval(''profile_questions_id_seq''),''5'')',q_households,sub1)],
      ['a profile for a cadre it skips',   format('INSERT INTO profiles(code,profile_name) VALUES(''verify_q'',''Verify''); INSERT INTO health_worker_profiles(health_worker_id,profile_id,source) VALUES(%s,currval(''profiles_id_seq''),''form'')',w1)],
      -- the questionnaire: history is not rewritten
      ['a response edited',                format('UPDATE health_worker_profile_responses SET response=''121'' WHERE health_worker_profile_id=%s AND profile_question_id=%s',sub1,q_households)],
      ['a response deleted',               format('DELETE FROM health_worker_profile_responses WHERE health_worker_profile_id=%s',sub1)],
      ['a submission deleted',             format('DELETE FROM health_worker_profiles WHERE id=%s',sub1)],
      ['a submission moved to another worker',format('UPDATE health_worker_profiles SET health_worker_id=%s WHERE id=%s',w2,sub1)],
      ['an answered question retyped',     format('UPDATE question_pool SET max_value=500 WHERE id=(SELECT question_id FROM profile_questions WHERE id=%s)',q_households)],
      ['an answered choice removed',       format('UPDATE question_pool SET response_options=''[{"id":1,"code":"yes","prompt":"Yes"}]'' WHERE id=(SELECT question_id FROM profile_questions WHERE id=%s)',q_owns)],
      ['an answered choice recoded',       format('UPDATE question_pool SET response_options=''[{"id":1,"code":"y","prompt":"Yes"},{"id":2,"code":"no","prompt":"No"}]'' WHERE id=(SELECT question_id FROM profile_questions WHERE id=%s)',q_owns)],
      ['an answered profile question rebranched',format('UPDATE profile_questions SET depends_on_id=NULL, depends_on_option=NULL WHERE id=%s',q_reporting)],
      -- the questionnaire: a sound question
      ['a closed question with no choices',$q$INSERT INTO question_pool(code,question_prompt,response_type_id,value_type_id,value_data_type_id) SELECT 'verify_x','X',1,(SELECT id FROM value_types WHERE code='closed'),1$q$],
      ['an open question with choices',    $q$INSERT INTO question_pool(code,question_prompt,response_type_id,value_type_id,value_data_type_id,response_options) SELECT 'verify_y','Y',1,(SELECT id FROM value_types WHERE code='open'),1,'[{"id":1,"code":"a","prompt":"A"}]'$q$],
      ['a choice id repeated',             $q$INSERT INTO question_pool(code,question_prompt,response_type_id,value_type_id,value_data_type_id,response_options) SELECT 'verify_z','Z',1,(SELECT id FROM value_types WHERE code='closed'),1,'[{"id":1,"code":"a","prompt":"A"},{"id":1,"code":"b","prompt":"B"}]'$q$],
      ['a branch on an option not offered',format('UPDATE profile_questions SET depends_on_option=''maybe'' WHERE id=(SELECT id FROM profile_questions WHERE profile_id=%s AND code=''incentive_frequency'')',baseline)],
      -- service updates and tool distributions
      ['a service update in the future',   format('INSERT INTO health_worker_service_updates(health_worker_id,reporting_date,deployment_id,district_id) VALUES(%s,current_date+1,0,0)',w2)],
      ['a service update with no posting', format('INSERT INTO health_worker_service_updates(health_worker_id,reporting_date,deployment_id,district_id) VALUES(%s,current_date,0,0)',w4)],
      ['a service update before the posting',format('INSERT INTO health_worker_service_updates(health_worker_id,reporting_date,deployment_id,district_id) VALUES(%s,current_date-1,0,0)',w2)],
      ['a service for another cadre',      format('INSERT INTO service_applicable_cadre(service_id,cadre_id) VALUES(%s,%s); DELETE FROM service_applicable_cadre WHERE cadre_id=%s AND service_id=%s; INSERT INTO health_worker_service_update_details(health_worker_service_update_id,service_id) SELECT %s, id FROM services WHERE code=''hiv''',iccm,unused,vht,(SELECT id FROM services WHERE code='hiv'),upd)],
      ['a service update district rewritten',format('UPDATE health_worker_service_updates SET district_id=%s WHERE id=%s',d2,upd)],
      ['a tool to a worker in another district',format('INSERT INTO health_worker_tool_distribution_details(health_worker_tool_distribution_id,health_worker_id,tool_id) VALUES(%s,%s,%s)',dist_other,w1,bicycle)],
      ['a tool for another cadre',         format('DELETE FROM tool_applicable_cadre WHERE cadre_id=%s AND tool_id=(SELECT id FROM tools WHERE code=''torch''); INSERT INTO health_worker_tool_distribution_details(health_worker_tool_distribution_id,health_worker_id,tool_id) SELECT %s,%s,id FROM tools WHERE code=''torch''',vht,dist,w1)],
      ['a distribution in a parish',       format('INSERT INTO health_worker_tool_distributions(district_id,reporting_date) VALUES(%s,current_date)',par)],
      ['a distribution moved after hand-out',format('UPDATE health_worker_tool_distributions SET district_id=%s WHERE id=%s',d2,dist)],
      ['the same tool twice in one hand-out',format('INSERT INTO health_worker_tool_distribution_details(health_worker_tool_distribution_id,health_worker_id,tool_id) VALUES(%s,%s,%s)',dist,w1,bicycle)],
      -- The worker code is assigned by the register, and permanent
      ['worker_code supplied on insert',   format('INSERT INTO health_workers(person_id,worker_code) VALUES(%s,''ZZZ00001'')',p5)],
      ['worker_code supplied before placement',
                                           format('UPDATE health_workers SET worker_code=''ZZZ00001'' WHERE id=%s',w4)],
      ['worker_code changed after assignment',
                                           format('UPDATE health_workers SET worker_code=''ZZZ00001'' WHERE id=%s',w1)],
      ['worker_code cleared',              format('UPDATE health_workers SET worker_code=NULL WHERE id=%s',w1)],
      ['district code of the wrong shape', 'INSERT INTO district_codes(district_code,abbr,district_name) VALUES(''999'',''ZZZZ'',''Verify'')'],
      ['district code that is not numeric','INSERT INTO district_codes(district_code,abbr,district_name) VALUES(''99A'',''ZZZ'',''Verify'')'],
      ['three-letter code claimed twice',  'INSERT INTO district_codes(district_code,abbr,district_name) VALUES(''999'',''KYE'',''Verify'')'],
      -- The cadre taxonomy takes writes from people
      ['cadre placed at region',           format('INSERT INTO cadres(cadre_category_id,code,label,placement_level) VALUES(%s,''verify_r'',''Verify'',''region'')',cat2)],
      ['cadre placed at county',           format('INSERT INTO cadres(cadre_category_id,code,label,placement_level) VALUES(%s,''verify_c'',''Verify'',''county'')',cat2)],
      ['cadre code with a space',          format('INSERT INTO cadres(cadre_category_id,code,label,placement_level) VALUES(%s,''bad code'',''Verify'',''village'')',cat2)],
      ['cadre code in capitals',           format('INSERT INTO cadres(cadre_category_id,code,label,placement_level) VALUES(%s,''Nurse'',''Verify'',''village'')',cat2)],
      ['cadre with a blank label',         format('INSERT INTO cadres(cadre_category_id,code,label,placement_level) VALUES(%s,''verify_b'','' '',''village'')',cat2)],
      ['cadre code shared across categories',
                                           format('INSERT INTO cadres(cadre_category_id,code,label,placement_level) VALUES(%s,''vht'',''Verify'',''village'')',cat2)],
      ['category code with a space',       'INSERT INTO cadre_categories(code,label) VALUES(''bad code'',''Verify'')'],
      ['in-use cadre moved to another level',
                                           format('UPDATE cadres SET placement_level=''parish'' WHERE id=%s',vht)],
      ['in-use cadre given a new code',    format('UPDATE cadres SET code=''vht2'' WHERE id=%s',vht)],
      ['in-use cadre moved to another category',
                                           format('UPDATE cadres SET cadre_category_id=%s WHERE id=%s',cat2,vht)],
      -- district_id is derived, never supplied (invariant 2) — not on insert,
      -- and not by an UPDATE that leaves the placement alone either
      ['deployment district rewritten in place',
                                           format('UPDATE deployments SET district_id=%s WHERE id=%s',d2,dep1)],
      ['worker district rewritten in place',
                                           format('UPDATE health_workers SET district_id=%s WHERE id=%s',d2,w1)],
      ['worker district supplied on insert',
                                           format('INSERT INTO health_workers(person_id,district_id) VALUES(%s,%s)',p5,d2)],
      -- nothing is dropped silently on import (invariant 7)
      ['import row rejected without a reason',
                                           format('INSERT INTO import_rows(batch_id,row_number,raw,status) VALUES(%s,2,''{}'',''rejected'')',batch)],
      ['import row failed without a reason',
                                           format('INSERT INTO import_rows(batch_id,row_number,raw,status) VALUES(%s,3,''{}'',''failed'')',batch)],
      ['import batch committed without a time',
                                           format('UPDATE import_batches SET status=''committed'' WHERE id=%s',batch)],
      ['import batch in an unknown format', format('INSERT INTO import_batches(filename,format,uploaded_by) VALUES(''x.ods'',''ods'',%s)',u)]
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
