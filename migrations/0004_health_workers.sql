-- +goose Up
-- 0004: health workers, cadres, deployments, worker codes
--
-- A health worker is a person's place in the workforce: `health_workers`
-- points at a `persons` row (0003) and carries what the register itself
-- decides about them — their status in the workforce, the district that owns
-- their record, and the worker code it issued. What they do and where they do
-- it is a `deployments` row: a posting in a cadre at a location, over a
-- period. A transfer or a promotion ends one row and opens another, so the
-- register itself answers "who was deployed at X on date D".
--
-- Cadres are DATA, not an enum: a two-level taxonomy of category (Community
-- Health Workers) and cadre (VHT, CHEW). The placement rule — which level of
-- the hierarchy a cadre serves — is a column on the cadre, so adding one is an
-- INSERT, and a national administrator makes that INSERT from the UI.

CREATE TYPE worker_status AS ENUM ('active','inactive');

---------------------------------------------------- cadre taxonomy, as data
-- Codes travel in URLs (?cadre=vht), in staged import records read back weeks
-- later, and in the export's cadre column. Lower-case, starting with a letter,
-- no spaces: what survives all three unescaped.
CREATE TABLE cadre_categories (
    id         smallserial PRIMARY KEY,
    code       text NOT NULL UNIQUE CHECK (code ~ '^[a-z][a-z0-9_]{1,31}$'),
    label      text NOT NULL CHECK (btrim(label) <> ''),
    sort_order smallint NOT NULL DEFAULT 0,
    active     boolean NOT NULL DEFAULT true
);
SELECT record_columns('cadre_categories');

CREATE TABLE cadres (
    id              smallserial PRIMARY KEY,
    cadre_category_id smallint NOT NULL REFERENCES cadre_categories(id),
    -- Unique across categories, not merely within one: the filter, the export
    -- and the importer all name a cadre by code alone, and two categories
    -- sharing one would make ?cadre=nurse mean two things.
    code            text NOT NULL UNIQUE CHECK (code ~ '^[a-z][a-z0-9_]{1,31}$'),
    label           text NOT NULL CHECK (btrim(label) <> ''),
    -- Which hierarchy level a deployment in this cadre serves. The placement
    -- trigger reads it from here; there is no CASE left to edit. The cascade
    -- reaches district > subcounty > parish > village: region has no district
    -- ancestor and county is never selected, so a cadre at either level would
    -- be one nobody could deploy.
    placement_level location_level NOT NULL
        CONSTRAINT cadres_placement_selectable
        CHECK (placement_level IN ('district','subcounty','parish','village')),
    -- Spellings an import may use for this cadre, matched case-folded with
    -- separators stripped, beside the code itself.
    import_aliases  text[] NOT NULL DEFAULT '{}',
    sort_order      smallint NOT NULL DEFAULT 0,
    active          boolean NOT NULL DEFAULT true
);
SELECT record_columns('cadres');

INSERT INTO cadre_categories (code, label, sort_order)
VALUES ('chw', 'Community Health Workers', 1);

INSERT INTO cadres (cadre_category_id, code, label, placement_level, import_aliases, sort_order)
SELECT c.id, v.code, v.label, v.placement_level::location_level, v.aliases, v.ord
  FROM cadre_categories c
  JOIN (VALUES
        ('vht',  'Village Health Team member',       'village', ARRAY['village health team'],                          1::smallint),
        ('chew', 'Community Health Extension Worker', 'parish',  ARRAY['chw', 'community health extension worker'],     2::smallint)
       ) AS v(code, label, placement_level, aliases, ord) ON c.code = 'chw';

------------------------------------------------------------- the worker
CREATE TABLE health_workers (
    id          bigserial PRIMARY KEY,
    -- One health worker per person for now. Relaxing that — a person on two
    -- registers' books — is dropping this UNIQUE, nothing more.
    person_id   bigint NOT NULL UNIQUE REFERENCES persons(id),
    -- The district that owns this record for scoping. Derived by trigger from
    -- the worker's deployments and never supplied: it tracks the latest
    -- posting, and it survives deactivation, which a join against the active
    -- deployment could not — a district must still see the workers who left.
    -- NULL only inside the transaction that creates the worker, before their
    -- first deployment lands.
    district_id bigint REFERENCES locations(id),
    -- Issued by trigger with the first deployment; see "worker codes" below.
    worker_code text CHECK (worker_code ~ '^[A-Z]{3}[0-9]{5}$'),
    -- Workers are never deleted. Deactivation means "left the workforce"; it
    -- requires every deployment to have ended first (trigger below).
    status      worker_status NOT NULL DEFAULT 'active',
    deactivated_at      timestamptz,
    deactivation_reason text,
    CONSTRAINT health_workers_deactivation_complete CHECK (
        (status = 'inactive') = (deactivated_at IS NOT NULL)
    ),
    -- NULL only in the creating transaction, before the first deployment
    -- derives a district: the same window district_id itself is NULL in.
    CONSTRAINT health_workers_code_assigned CHECK (worker_code IS NOT NULL OR district_id IS NULL)
);
SELECT record_columns('health_workers');

CREATE INDEX health_workers_district_idx ON health_workers (district_id);
CREATE UNIQUE INDEX health_workers_code_uniq ON health_workers (worker_code);

COMMENT ON COLUMN health_workers.worker_code IS
    'Human-legible permanent identifier, e.g. KYE00042. Assigned by trigger when the first deployment derives a district; never supplied, never changed.';

----------------------------------------------------------------- the posting
CREATE TABLE deployments (
    id               bigserial PRIMARY KEY,
    -- KYE00042-01: the worker's code and the posting's ordinal. Issued by
    -- trigger once the worker has a code, never supplied, never changed.
    code             text UNIQUE CHECK (code ~ '^[A-Z]{3}[0-9]{5}-[0-9]{2,}$'),
    health_worker_id bigint  NOT NULL REFERENCES health_workers(id),
    cadre_id         smallint NOT NULL REFERENCES cadres(id),
    location_id      bigint  NOT NULL REFERENCES locations(id),
    district_id      bigint  NOT NULL REFERENCES locations(id),  -- derived; denormalized for RBAC scoping
    -- The supervising facility: an optional attachment, not a placement.
    -- Must sit in the deployment's own district (trigger below).
    facility_id      bigint  REFERENCES facilities(id),
    started_on       date    NOT NULL DEFAULT current_date,
    ended_on         date,
    end_reason       text,
    -- A deployment ends with a reason; an open deployment carries none.
    CONSTRAINT deployments_end_complete CHECK (
        (ended_on IS NULL) = (end_reason IS NULL)
    ),
    CONSTRAINT deployments_dates_ordered CHECK (
        ended_on IS NULL OR ended_on >= started_on
    )
);
SELECT record_columns('deployments');

-- One active deployment per worker. A second concurrent posting is refused
-- here; relaxing that policy later is dropping this index, nothing more.
CREATE UNIQUE INDEX deployments_one_active_idx ON deployments (health_worker_id) WHERE ended_on IS NULL;
-- Every scoped read anchors on the active deployment's district.
CREATE INDEX deployments_district_idx     ON deployments (district_id) WHERE ended_on IS NULL;
CREATE INDEX deployments_worker_idx       ON deployments (health_worker_id);
CREATE INDEX deployments_location_idx     ON deployments (location_id) WHERE ended_on IS NULL;
CREATE INDEX deployments_cadre_idx        ON deployments (cadre_id);
CREATE INDEX deployments_facility_idx     ON deployments (facility_id) WHERE facility_id IS NOT NULL;

-- Placement rule + district derivation. district_id is never supplied by the
-- caller. The level a cadre serves comes from the cadres row: VHTs at village,
-- CHEWs at parish, and the next cadre at whatever its row says. An inactive
-- worker takes no new deployment; reactivate first.
-- +goose StatementBegin
CREATE FUNCTION deployments_set_placement() RETURNS trigger AS $$
DECLARE
    lvl      location_level;
    required location_level;
    w_status worker_status;
BEGIN
    SELECT placement_level INTO required FROM cadres WHERE id = NEW.cadre_id;
    IF required IS NULL THEN
        RAISE EXCEPTION 'cadre % not found', NEW.cadre_id;
    END IF;

    SELECT status INTO w_status FROM health_workers WHERE id = NEW.health_worker_id;
    IF w_status = 'inactive' AND NEW.ended_on IS NULL THEN
        RAISE EXCEPTION 'health worker % is inactive; reactivate before deploying', NEW.health_worker_id;
    END IF;

    SELECT level INTO lvl FROM locations WHERE id = NEW.location_id;
    IF lvl IS NULL THEN
        RAISE EXCEPTION 'location % not found', NEW.location_id;
    END IF;
    IF lvl <> required THEN
        RAISE EXCEPTION 'cadre % must be placed at % level, got %',
            (SELECT code FROM cadres WHERE id = NEW.cadre_id), required, lvl;
    END IF;

    SELECT a.id INTO NEW.district_id
      FROM locations c
      JOIN locations a ON c.path LIKE a.path || '%'
     WHERE c.id = NEW.location_id AND a.level = 'district';
    IF NEW.district_id IS NULL THEN
        RAISE EXCEPTION 'location % has no district ancestor', NEW.location_id;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER deployments_placement_trg BEFORE INSERT OR UPDATE OF location_id, cadre_id, health_worker_id ON deployments
    FOR EACH ROW EXECUTE FUNCTION deployments_set_placement();

-- The facility must sit in the deployment's own district. Attachment and
-- placement share one row, so this single AFTER trigger covers both
-- directions: attaching across districts, and moving a deployment whose
-- facility would be stranded. AFTER, because it reads the district_id the
-- placement trigger has just derived.
-- +goose StatementBegin
CREATE FUNCTION deployments_check_facility_district() RETURNS trigger AS $$
BEGIN
    IF NEW.facility_id IS NULL THEN
        RETURN NULL;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM facilities f
                    WHERE f.id = NEW.facility_id AND f.district_id = NEW.district_id) THEN
        RAISE EXCEPTION 'facility % is not in district %; attach a facility from the deployment''s own district, or move it in the same transaction',
            NEW.facility_id, NEW.district_id;
    END IF;
    RETURN NULL;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER deployments_facility_district_trg
    AFTER INSERT OR UPDATE OF facility_id, location_id, cadre_id ON deployments
    FOR EACH ROW EXECUTE FUNCTION deployments_check_facility_district();

-- Keeps the worker's district anchor pointing at their latest posting, and —
-- once the worker has a code, which that first anchor issues — gives the
-- posting its own. It deliberately does NOT clear the anchor on a deployment
-- ending: the last district still owns the record of a worker between
-- postings or out of the workforce.
-- +goose StatementBegin
CREATE FUNCTION deployments_sync_worker_district() RETURNS trigger AS $$
BEGIN
    UPDATE health_workers SET district_id = NEW.district_id
     WHERE id = NEW.health_worker_id;

    IF NEW.code IS NULL THEN
        UPDATE deployments d
           SET code = w.worker_code || '-' || lpad(n.ordinal::text, greatest(2, length(n.ordinal::text)), '0')
          FROM health_workers w,
               (SELECT count(*) AS ordinal FROM deployments
                 WHERE health_worker_id = NEW.health_worker_id AND id <= NEW.id) n
         WHERE d.id = NEW.id AND w.id = NEW.health_worker_id;
    END IF;
    RETURN NULL;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER deployments_worker_district_trg
    AFTER INSERT OR UPDATE ON deployments
    FOR EACH ROW EXECUTE FUNCTION deployments_sync_worker_district();

-- The posting code is issued, never supplied: the sync trigger above writes it
-- from inside a trigger, so any write at depth 1 is a statement someone issued
-- directly, and once written it never changes.
-- +goose StatementBegin
CREATE FUNCTION deployments_guard_code() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.code IS NOT NULL THEN
            RAISE EXCEPTION 'deployment code is assigned by the register, not supplied';
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.code IS NOT NULL AND NEW.code IS DISTINCT FROM OLD.code THEN
        RAISE EXCEPTION 'deployment code % is permanent and cannot be changed', OLD.code;
    END IF;
    IF OLD.code IS NULL AND NEW.code IS NOT NULL AND pg_trigger_depth() <= 1 THEN
        RAISE EXCEPTION 'deployment code is assigned by the register, not supplied';
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER deployments_code_trg
    BEFORE INSERT OR UPDATE OF code ON deployments
    FOR EACH ROW EXECUTE FUNCTION deployments_guard_code();

-- A worker with an active deployment cannot be deactivated: the application
-- ends the deployment in the same transaction first. This is what keeps
-- "inactive worker, open posting" unrepresentable.
-- +goose StatementBegin
CREATE FUNCTION health_workers_check_deactivation() RETURNS trigger AS $$
BEGIN
    IF NEW.status = 'inactive' AND OLD.status = 'active' AND EXISTS (
        SELECT 1 FROM deployments
         WHERE health_worker_id = NEW.id AND ended_on IS NULL
    ) THEN
        RAISE EXCEPTION 'health worker % has an active deployment; end it in the same transaction before deactivating',
            NEW.id;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER health_workers_deactivation_trg
    BEFORE UPDATE OF status ON health_workers
    FOR EACH ROW EXECUTE FUNCTION health_workers_check_deactivation();

------------------------------------------------- district_id: derived only
-- Invariant 2: district_id is derived, never supplied. The placement trigger
-- derives it when a posting's placement changes; these refuse every other way
-- of writing it, for the psql session repairing a row by hand.
--
-- deployments — AFTER, not BEFORE: it has to see the row once the placement
-- trigger is done with it. A statement setting location_id and district_id
-- together has had its district re-derived by then and passes; one naming
-- district_id alone keeps the value it supplied, and that value must be the
-- one the path says.
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

-- health_workers — the worker's district is written by exactly one thing: the
-- sync trigger on deployments, which runs its UPDATE from inside a trigger. Any
-- write to the column made at trigger depth 1 is therefore a statement a person
-- or the application issued directly, and is refused.
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

--------------------------------------------------- frozen once anyone serves
-- The placement trigger checks a deployment when the deployment changes, not
-- when its cadre does. Moving VHTs from village to parish after 40,000 of them
-- are deployed would leave every one of those rows violating invariant 1 with
-- nothing to notice. Likewise a category change would move serving workers onto
-- another category's profile, and a code change would orphan staged import
-- rows that name the old one.
--
-- So a cadre is editable freely until its first deployment, and afterwards only
-- in what it is called (label), how imports spell it (aliases), where it sorts,
-- and whether it is still offered (active).
-- +goose StatementBegin
CREATE FUNCTION cadres_freeze_when_used() RETURNS trigger AS $$
BEGIN
    IF (NEW.code, NEW.cadre_category_id, NEW.placement_level)
       IS DISTINCT FROM (OLD.code, OLD.cadre_category_id, OLD.placement_level)
       AND EXISTS (SELECT 1 FROM deployments WHERE cadre_id = OLD.id) THEN
        RAISE EXCEPTION 'cadre % has deployments; its code, category and placement level are fixed — retire it and add a new cadre instead',
            OLD.code;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER cadres_freeze_trg
    BEFORE UPDATE OF code, cadre_category_id, placement_level ON cadres
    FOR EACH ROW EXECUTE FUNCTION cadres_freeze_when_used();

--------------------------------------------------------------- worker codes
--
--     KYE00042   =  Kyenjojo, the 42nd health worker registered there
--     └┬┘└─┬─┘
--      │   └───── serial, five digits, one counter per district
--      └───────── three-letter district code, curated and checked in
--
-- One code per health worker, whatever their cadre; no cadre letter, because
-- cadre is mutable and a letter would eventually lie; frozen for life, through
-- transfers and district splits — the code says where a worker ENTERED the
-- register. docs/decisions.md carries the argument in full.
--
-- The worker's district is not known when the health_workers row is inserted:
-- it is derived from the first deployment, which lands a statement later in
-- the same transaction. So the code is assigned when district_id first goes
-- from NULL to a value, and health_workers_code_assigned makes "has a
-- district, has no code" unrepresentable.

-- Reference data, keyed by the official numeric district code rather than by
-- locations.id: this migration runs against an empty hierarchy on a fresh
-- database, and the seed loads locations afterwards. Generated from
-- data/district_codes.tsv, which is the reviewable source of truth.
--
-- Curation rules, in order of precedence:
--   1. every code begins with the district's own initial (41 districts start
--      with K, which is why two letters is not merely tight but impossible);
--   2. the natural three-letter prefix goes to the older or larger district of
--      a colliding pair — KYE is Kyenjojo, Kyegegwa took KYG;
--   3. a code ends in C if and only if the district is a city.
CREATE TABLE district_codes (
    district_code text PRIMARY KEY CHECK (district_code ~ '^[0-9]{3}$'),
    abbr          text NOT NULL UNIQUE CHECK (abbr ~ '^[A-Z]{3}$'),
    -- the district's name when the code was assigned: provenance for a reviewer,
    -- never a key. Districts are renamed; locations.name is the live answer.
    district_name text NOT NULL
);
SELECT record_columns('district_codes');

COMMENT ON TABLE district_codes IS
    'Curated three-letter district codes; the leading segment of health_workers.worker_code.';

INSERT INTO district_codes (district_code, abbr, district_name) VALUES
    ('070','ABI','ABIM'),
    ('040','ADJ','ADJUMANI'),
    ('111','AGA','AGAGO'),
    ('088','ALE','ALEBTONG'),
    ('057','AMO','AMOLATAR'),
    ('081','AMD','AMUDAT'),
    ('058','AMI','AMURIA'),
    ('071','AMU','AMURU'),
    ('001','APA','APAC'),
    ('002','ARU','ARUA'),
    ('136','ARC','ARUA CITY'),
    ('072','BDK','BUDAKA'),
    ('078','BUD','BUDUDA'),
    ('041','BUG','BUGIRI'),
    ('123','BGW','BUGWERI'),
    ('109','BUH','BUHWEJU'),
    ('082','BUI','BUIKWE'),
    ('079','BKD','BUKEDEA'),
    ('098','BKM','BUKOMANSIMBI'),
    ('059','BUK','BUKWO'),
    ('089','BLM','BULAMBULI'),
    ('073','BUL','BULIISA'),
    ('003','BUN','BUNDIBUGYO'),
    ('117','BNY','BUNYANGABU'),
    ('004','BSH','BUSHENYI'),
    ('042','BUS','BUSIA'),
    ('060','BTL','BUTALEJA'),
    ('099','BTM','BUTAMBALA'),
    ('118','BUT','BUTEBO'),
    ('090','BUV','BUVUMA'),
    ('083','BUY','BUYENDE'),
    ('074','DOK','DOKOLO'),
    ('139','FPC','FORT PORTAL CITY'),
    ('091','GOM','GOMBA'),
    ('005','GUL','GULU'),
    ('137','GUC','GULU CITY'),
    ('006','HOI','HOIMA'),
    ('145','HOC','HOIMA CITY'),
    ('061','IBA','IBANDA'),
    ('007','IGA','IGANGA'),
    ('062','ISI','ISINGIRO'),
    ('008','JIN','JINJA'),
    ('138','JIC','JINJA CITY'),
    ('063','KAA','KAABONG'),
    ('009','KAB','KABALE'),
    ('010','KBR','KABAROLE'),
    ('054','KBM','KABERAMAIDO'),
    ('113','KAG','KAGADI'),
    ('114','KAK','KAKUMIRO'),
    ('129','KLK','KALAKI'),
    ('011','KAL','KALANGALA'),
    ('064','KLR','KALIRO'),
    ('100','KLU','KALUNGU'),
    ('012','KAM','KAMPALA'),
    ('013','KML','KAMULI'),
    ('046','KMW','KAMWENGE'),
    ('055','KAN','KANUNGU'),
    ('014','KAP','KAPCHORWA'),
    ('124','KPL','KAPELEBYONG'),
    ('130','KAR','KARENGA'),
    ('015','KAS','KASESE'),
    ('125','KSS','KASSANDA'),
    ('043','KAT','KATAKWI'),
    ('047','KAY','KAYUNGA'),
    ('131','KAZ','KAZO'),
    ('016','KIB','KIBAALE'),
    ('017','KBG','KIBOGA'),
    ('102','KBK','KIBUKU'),
    ('126','KIK','KIKUUBE'),
    ('065','KIR','KIRUHURA'),
    ('092','KRY','KIRYANDONGO'),
    ('018','KIS','KISORO'),
    ('132','KTG','KITAGWENDA'),
    ('019','KIT','KITGUM'),
    ('066','KOB','KOBOKO'),
    ('103','KOL','KOLE'),
    ('020','KOT','KOTIDO'),
    ('021','KUM','KUMI'),
    ('127','KWA','KWANIA'),
    ('104','KWE','KWEEN'),
    ('093','KYA','KYANKWANZI'),
    ('084','KYG','KYEGEGWA'),
    ('048','KYE','KYENJOJO'),
    ('119','KYO','KYOTERA'),
    ('085','LAM','LAMWO'),
    ('022','LIR','LIRA'),
    ('144','LIC','LIRA CITY'),
    ('094','LUU','LUUKA'),
    ('023','LUW','LUWEERO'),
    ('105','LWE','LWENGO'),
    ('080','LYA','LYANTONDE'),
    ('133','MAD','MADI-OKOLLO'),
    ('067','MAN','MANAFWA'),
    ('077','MAR','MARACHA'),
    ('024','MAS','MASAKA'),
    ('141','MSC','MASAKA CITY'),
    ('025','MSD','MASINDI'),
    ('049','MAY','MAYUGE'),
    ('026','MBL','MBALE'),
    ('142','MBC','MBALE CITY'),
    ('027','MBR','MBARARA'),
    ('140','MRC','MBARARA CITY'),
    ('106','MTM','MITOOMA'),
    ('068','MIT','MITYANA'),
    ('028','MOR','MOROTO'),
    ('029','MOY','MOYO'),
    ('030','MPI','MPIGI'),
    ('031','MUB','MUBENDE'),
    ('032','MUK','MUKONO'),
    ('128','NAB','NABILATUK'),
    ('056','NKP','NAKAPIRIPIRIT'),
    ('069','NAK','NAKASEKE'),
    ('044','NKS','NAKASONGOLA'),
    ('095','NMY','NAMAYINGO'),
    ('120','NMS','NAMISINDWA'),
    ('075','NAM','NAMUTUMBA'),
    ('107','NAP','NAPAK'),
    ('033','NEB','NEBBI'),
    ('108','NGO','NGORA'),
    ('096','NTO','NTOROKO'),
    ('034','NTU','NTUNGAMO'),
    ('110','NWO','NWOYA'),
    ('134','OBO','OBONGI'),
    ('115','OMO','OMORO'),
    ('086','OTU','OTUKE'),
    ('076','OYA','OYAM'),
    ('050','PAD','PADER'),
    ('121','PAK','PAKWACH'),
    ('035','PAL','PALLISA'),
    ('036','RAK','RAKAI'),
    ('116','RUB','RUBANDA'),
    ('112','RBR','RUBIRIZI'),
    ('122','RKG','RUKIGA'),
    ('037','RUK','RUKUNGIRI'),
    ('135','RWA','RWAMPARA'),
    ('097','SER','SERERE'),
    ('101','SHE','SHEEMA'),
    ('051','SIR','SIRONKO'),
    ('038','SOR','SOROTI'),
    ('146','SOC','SOROTI CITY'),
    ('045','SSE','SSEMBABULE'),
    ('143','TER','TEREGO'),
    ('039','TOR','TORORO'),
    ('052','WAK','WAKISO'),
    ('053','YUM','YUMBE'),
    ('087','ZOM','ZOMBO')
;

-- The literal block above is generated, so it is checked rather than trusted.
-- +goose StatementBegin
DO $$
BEGIN
    IF (SELECT count(*) FROM district_codes) <> 146 THEN
        RAISE EXCEPTION 'district_codes loaded % rows, expected 146',
            (SELECT count(*) FROM district_codes);
    END IF;
END $$;
-- +goose StatementEnd

-- One counter per district. `last_serial` is what was issued, not what is next:
-- the assigning trigger increments and returns in a single upsert, so two
-- concurrent registrations in one district serialize on this row and those in
-- different districts do not touch each other. Serials are never reused, and a
-- rolled-back transaction leaves a gap — a serial is a position, not a count.
CREATE TABLE worker_code_counters (
    district_id bigint PRIMARY KEY REFERENCES locations(id),
    last_serial integer NOT NULL CHECK (last_serial BETWEEN 1 AND 99999)
);

-- One function guards both directions. On INSERT a code may not be supplied.
-- On UPDATE a code, once there, may not change; and the first time district_id
-- arrives (from deployments_sync_worker_district) one is issued in that
-- district. A later transfer changes district_id and leaves the code alone.
-- +goose StatementBegin
CREATE FUNCTION health_workers_assign_code() RETURNS trigger AS $$
DECLARE
    abbr   text;
    serial integer;
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.worker_code IS NOT NULL THEN
            RAISE EXCEPTION 'worker_code is assigned by the register, not supplied';
        END IF;
        RETURN NEW;
    END IF;

    IF OLD.worker_code IS NOT NULL THEN
        IF NEW.worker_code IS DISTINCT FROM OLD.worker_code THEN
            RAISE EXCEPTION 'worker_code % is permanent and cannot be changed', OLD.worker_code;
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.worker_code IS NOT NULL THEN
        RAISE EXCEPTION 'worker_code is assigned by the register, not supplied';
    END IF;
    IF NEW.district_id IS NULL THEN
        RETURN NEW;
    END IF;

    SELECT dc.abbr INTO abbr
      FROM locations l
      JOIN district_codes dc ON dc.district_code = l.code
     WHERE l.id = NEW.district_id;
    -- A district created after this migration has no code yet. Refusing loudly
    -- is the point: the alternative is a register with two ID schemes in it.
    IF abbr IS NULL THEN
        RAISE EXCEPTION 'district % has no district_codes entry; add one before registering health workers there',
            NEW.district_id;
    END IF;

    INSERT INTO worker_code_counters AS c (district_id, last_serial)
         VALUES (NEW.district_id, 1)
    ON CONFLICT (district_id) DO UPDATE SET last_serial = c.last_serial + 1
      RETURNING last_serial INTO serial;

    IF serial > 99999 THEN
        RAISE EXCEPTION 'district % has exhausted its five-digit serial', NEW.district_id;
    END IF;

    NEW.worker_code := abbr || lpad(serial::text, 5, '0');
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER health_workers_code_trg
    BEFORE INSERT OR UPDATE OF worker_code, district_id ON health_workers
    FOR EACH ROW EXECUTE FUNCTION health_workers_assign_code();

-- district_codes.district_code is not an FK — locations may be empty when this
-- runs — so the pairing is checked where it is used, above, and the reverse
-- direction (a district with no code) is a case in seed/verify_constraints.sql.
