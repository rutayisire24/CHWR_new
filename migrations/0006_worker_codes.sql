-- +goose Up
-- 0006: the worker code — the register's human-legible identifier
--
-- `health_workers.id` is a surrogate key: correct, and unusable by a district
-- officer reading a printed list at a parish or saying a number down a phone.
-- This adds a second identifier that a person can carry:
--
--     KYE00042   =  Kyenjojo, the 42nd health worker registered there
--     └┬┘└─┬─┘
--      │   └───── serial, five digits, one counter per district
--      └───────── three-letter district code, curated and checked in
--
-- Four decisions are baked into that shape, and docs/decisions.md carries the
-- argument in full:
--
--  * ONE CODE FOR EVERY HEALTH WORKER. The code lives on the person, not the
--    posting or the category: a VHT who becomes a CHEW, or a CHEW who trains
--    as an enrolled nurse, is the same person and keeps the same code.
--
--  * NO CADRE LETTER. Cadre is mutable — a VHT may be trained as a CHEW — so a
--    letter would eventually lie, and a field that is wrong is misleading
--    rather than vague. Either it is true or it should not be there.
--
--  * FROZEN FOR LIFE. Nothing about the code is recomputed. A transfer, a
--    re-cadring, a district split (Uganda has gone 112 -> 146 within living
--    memory) leaves it untouched. An identifier that changes is not one: it
--    breaks paper already in the field and the join back through audit_log.
--    The code says where a worker ENTERED the register, which is permanently
--    true; the deployment says where they are now.
--
--  * DIGITS, FIVE OF THEM. Not base-32, not four. The largest district already
--    holds 3,046 CHWs at partial coverage and invariant 5 means a serial is
--    never released, so 9,999 does not survive Wakiso. Digits also survive
--    being read aloud and hand-copied, which is the entire job.
--
-- The worker's district is not known when the health_workers row is inserted:
-- it is derived from the first deployment, which lands a statement later in
-- the same transaction (0003). So the code is assigned when district_id first
-- goes from NULL to a value, not on INSERT — and a CHECK makes "has a district,
-- has no code" unrepresentable.

------------------------------------------------------- the district codes
-- Reference data, keyed by the official numeric district code rather than by
-- locations.id: this migration runs against an empty hierarchy on a fresh
-- database, and the seed loads locations afterwards. Generated from
-- data/district_codes.tsv, which is the reviewable source of truth.
--
-- Curation rules, in order of precedence:
--   1. every code begins with the district's own initial (41 districts start
--      with K, which is why two letters is not merely tight but impossible:
--      41 districts cannot have 41 distinct second letters);
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

------------------------------------------------------------- the serial
-- One counter per district. `last_serial` is what was issued, not what is next:
-- the assigning trigger increments and returns in a single upsert, so two
-- concurrent registrations in one district serialize on this row and those in
-- different districts do not touch each other. Serials are never reused, and a
-- rolled-back transaction leaves a gap — a serial is a position, not a count.
CREATE TABLE worker_code_counters (
    district_id bigint PRIMARY KEY REFERENCES locations(id),
    last_serial integer NOT NULL CHECK (last_serial BETWEEN 1 AND 99999)
);

ALTER TABLE health_workers
    ADD COLUMN worker_code text CHECK (worker_code ~ '^[A-Z]{3}[0-9]{5}$');

-- Backfill, ordered so the assignment is deterministic and reproducible: the
-- register's own arrival order within each district. On a fresh database this
-- touches nothing.
WITH numbered AS (
    SELECT w.id,
           dc.abbr,
           row_number() OVER (PARTITION BY w.district_id
                              ORDER BY w.created_at, w.id) AS serial
      FROM health_workers w
      JOIN locations l       ON l.id = w.district_id
      JOIN district_codes dc ON dc.district_code = l.code
)
UPDATE health_workers SET worker_code = numbered.abbr || lpad(numbered.serial::text, 5, '0')
  FROM numbered
 WHERE health_workers.id = numbered.id;

INSERT INTO worker_code_counters (district_id, last_serial)
SELECT district_id, count(*) FROM health_workers WHERE worker_code IS NOT NULL GROUP BY district_id;

-- After the backfill, not before: on a register that already holds workers
-- the constraint would otherwise refuse every one of them.
-- NULL only in the creating transaction, before the first deployment derives a
-- district: the same window district_id itself is NULL in.
ALTER TABLE health_workers ADD CONSTRAINT health_workers_code_assigned
    CHECK (worker_code IS NOT NULL OR district_id IS NULL);

CREATE UNIQUE INDEX health_workers_code_uniq ON health_workers (worker_code);

COMMENT ON COLUMN health_workers.worker_code IS
    'Human-legible permanent identifier, e.g. KYE00042. Assigned by trigger when the first deployment derives a district; never supplied, never changed.';

-------------------------------------------------------------- assignment
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

