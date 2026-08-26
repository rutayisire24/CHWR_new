-- +goose Up
-- 0009: the CHW code — the register's human-legible identifier
--
-- `chws.id` is a surrogate key: correct, and unusable by a district officer
-- reading a printed list at a parish or saying a number down a phone. This adds
-- a second identifier that a person can carry:
--
--     KYE00042   =  Kyenjojo, the 42nd CHW registered there
--     └┬┘└─┬─┘
--      │   └───── serial, five digits, one counter per district
--      └───────── three-letter district code, curated and checked in
--
-- Three decisions are baked into that shape, and docs/decisions.md carries the
-- argument in full:
--
--  * NO CADRE LETTER. An earlier draft was `VKYE00042` / `CKYE00042`. Both
--    encoded facts are mutable — a VHT may be trained as a CHEW, and this
--    trigger already fires ON UPDATE OF cadre — so the letter would eventually
--    lie, and a two-valued field that is wrong is misleading rather than vague.
--    Either it is true or it should not be there.
--
--  * FROZEN FOR LIFE. Nothing about the code is recomputed. A transfer, a
--    re-cadring, a district split (Uganda has gone 112 -> 146 within living
--    memory) leaves it untouched. An identifier that changes is not one: it
--    breaks paper already in the field and the join back through audit_log.
--    The code says where a CHW entered the register, which is permanently true;
--    the record's own columns say where they are now.
--
--  * DIGITS, FIVE OF THEM. Not base-32, not four. The largest district already
--    holds 3,046 CHWs at partial coverage and invariant 5 means a serial is
--    never released, so 9,999 does not survive Wakiso. Digits also survive
--    being read aloud and hand-copied, which is the entire job.

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
    'Curated three-letter district codes; the leading segment of chws.chw_code.';

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
-- concurrent inserts into one district serialize on this row and inserts into
-- different districts do not touch each other. Serials are never reused, and a
-- rolled-back transaction leaves a gap — a serial is a position, not a count.
CREATE TABLE chw_code_counters (
    district_id bigint PRIMARY KEY REFERENCES locations(id),
    last_serial integer NOT NULL CHECK (last_serial BETWEEN 1 AND 99999)
);

ALTER TABLE chws ADD COLUMN chw_code text CHECK (chw_code ~ '^[A-Z]{3}[0-9]{5}$');

-- Backfill, ordered so the assignment is deterministic and reproducible: the
-- register's own arrival order within each district. Updating chw_code alone
-- fires neither chws_placement_trg nor chws_facility_after_move_trg, both of
-- which are scoped to location_id and cadre.
WITH numbered AS (
    SELECT c.id,
           dc.abbr,
           row_number() OVER (PARTITION BY c.district_id
                              ORDER BY c.created_at, c.id) AS serial
      FROM chws c
      JOIN locations l       ON l.id = c.district_id
      JOIN district_codes dc ON dc.district_code = l.code
)
UPDATE chws SET chw_code = numbered.abbr || lpad(numbered.serial::text, 5, '0')
  FROM numbered
 WHERE chws.id = numbered.id;

INSERT INTO chw_code_counters (district_id, last_serial)
SELECT district_id, count(*) FROM chws WHERE chw_code IS NOT NULL GROUP BY district_id;

ALTER TABLE chws ALTER COLUMN chw_code SET NOT NULL;
CREATE UNIQUE INDEX chws_code_uniq ON chws (chw_code);

COMMENT ON COLUMN chws.chw_code IS
    'Human-legible permanent identifier, e.g. KYE00042. Assigned by trigger, never supplied, never changed.';

-------------------------------------------------------------- assignment
-- Named to sort after chws_placement_trg: PostgreSQL fires BEFORE row triggers
-- in trigger-name order, and NEW.district_id is derived by that one. Renaming
-- either trigger without preserving 's' > 'p' would hand this an empty district.
-- +goose StatementBegin
CREATE FUNCTION chws_assign_code() RETURNS trigger AS $$
DECLARE
    abbr   text;
    serial integer;
BEGIN
    IF NEW.chw_code IS NOT NULL THEN
        RAISE EXCEPTION 'chw_code is assigned by the register, not supplied';
    END IF;

    SELECT dc.abbr INTO abbr
      FROM locations l
      JOIN district_codes dc ON dc.district_code = l.code
     WHERE l.id = NEW.district_id;
    -- A district created after this migration has no code yet. Refusing loudly
    -- is the point: the alternative is a register with two ID schemes in it.
    IF abbr IS NULL THEN
        RAISE EXCEPTION 'district % has no district_codes entry; add one before registering CHWs there',
            NEW.district_id;
    END IF;

    INSERT INTO chw_code_counters AS c (district_id, last_serial)
         VALUES (NEW.district_id, 1)
    ON CONFLICT (district_id) DO UPDATE SET last_serial = c.last_serial + 1
      RETURNING last_serial INTO serial;

    IF serial > 99999 THEN
        RAISE EXCEPTION 'district % has exhausted its five-digit serial', NEW.district_id;
    END IF;

    NEW.chw_code := abbr || lpad(serial::text, 5, '0');
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER chws_serial_trg BEFORE INSERT ON chws
    FOR EACH ROW EXECUTE FUNCTION chws_assign_code();

-- Frozen for life. Without this the freeze is a convention someone can forget;
-- with it, a changed code is unrepresentable.
-- +goose StatementBegin
CREATE FUNCTION chws_freeze_code() RETURNS trigger AS $$
BEGIN
    IF NEW.chw_code IS DISTINCT FROM OLD.chw_code THEN
        RAISE EXCEPTION 'chw_code % is permanent and cannot be changed', OLD.chw_code;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER chws_code_frozen_trg BEFORE UPDATE OF chw_code ON chws
    FOR EACH ROW EXECUTE FUNCTION chws_freeze_code();

-- district_codes.district_code is not an FK — locations may be empty when this
-- runs — so the pairing is checked where it is used, above, and the reverse
-- direction (a district with no code) is a case in seed/verify_constraints.sql.
