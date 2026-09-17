# Roadmap

Go + `html/template` + vanilla CSS/JS, PostgreSQL. No framework, no ORM, no JS build step.

## Where we are

**Phases 1–7 complete, and the register now holds a real national seed:** 44,446 CHWs
converted from the August 2026 ODK export and imported district by district — 42,956 VHTs
and 1,490 CHEWs across 54 districts, every one of them with an `audit_log` row.

The register is usable at scale — search by name or CHW code, filter by cadre, status and
any level of the hierarchy, page with a keyset — and it is filled from the files districts
already hold. A CSV or Excel upload is checked row by row against the hierarchy and the
register, staged, and reported on; a human commits or discards it. The roadmap's own list
is done. What is left is a second source, not a second feature: see
[Open work](#open-work-the-second-source) below.

| | State |
|---|---|
| `migrations/` 0001–0005 | applied and verified on PostgreSQL 18 |
| `seed/` hierarchy + facilities + constraint suite | complete, reproducible from the repo root |
| `cmd/server`, `internal/{config,db}` | migrate, serve, health, graceful shutdown, admin bootstrap, hourly session purge |
| `internal/domain` | `User`, `CHW`, `Profile`, the enums, `Level`, sentinel errors |
| `internal/auth` | `Scope`, capability matrix, argon2id, session tokens, CSRF, middleware |
| `internal/store` | users, sessions, audit, locations, chws, profiles, stats — every method takes a `Scope` |
| `internal/http` | auth, user admin, audit, dashboard (scoped stats + charts), CHW CRUD, profiles, search and paging |
| `internal/web` | layout + eleven pages, one stylesheet, three scripts, Chart.js vendored |
| `migrations/` 0006–0008 | import staging, the commit claim, the resolved record — applied and probed |
| `internal/importer` | readers, resolver, row validation — 30 tests, none needing a database |
| Import UI | upload, report, commit, discard, template and `errors.csv` |
| Profile columns on import | all seventeen, with every branch CHECK pre-checked |
| CSV export | `GET /chws/export.csv`, streamed, sharing `store.Filter` and the `Scope` with the listing |
| `chw_languages` | **empty by design — `other_languages_raw` is kept verbatim; the parsed junction waits for an agreed vocabulary** |
| `migrations/` 0009 | `chw_code` — three-letter district code plus a five-digit serial, assigned by trigger, never supplied and never changed |
| Register search | one box, name **or** CHW code; the NIN filter was removed rather than kept — a national identifier must not be a probe |
| `seed/convert_odk_export.py` | ODK export → importer-canonical CSVs, one cadre per run, one file per district |
| `seed/package_import.sh` | fingerprinted, checksummed transfer to a machine that cannot reach the export |
| Importer name folding | the administrative tier word folded per level — dropped where redundant, expanded where identifying, untouched at village |
| ODK seed loaded | 44,446 CHWs, 54 districts — see [The national seed](#the-national-seed) |

## Decisions locked

Full rationale, including rejected alternatives, is in [decisions.md](decisions.md).

| Area | Decision |
|---|---|
| Auth | Local email + argon2id password, Postgres-backed sessions, admin-created accounts, forced first-login reset |
| Roles | `national_admin`, `national_viewer`, `district_manager`, `district_viewer` |
| User admin | National admin only |
| CHW seeding | Bulk CSV/ODK import, then day-to-day maintenance in the UI |
| NIN | Optional, unique where present, form regex enforced |
| Cadre | Single-valued `vht` / `chew`; no "other" |
| Placement | **CHEWs at parish level, VHTs at village level** — one `location_id`, level enforced by trigger |
| Facilities | Master Facility List, parented to **district**; subcounty label kept raw, never resolved |
| CHW to facility | Optional attachment on `chw_profiles`, not a placement; must be in the CHW's own district |
| Hierarchy | region > district > county > subcounty > parish > village |
| Supervision | Year + month on the CHW, not per service domain |
| ODK provenance | Stripped |
| GPS | Not captured |
| Import formats | CSV and Excel, read by `excelize`; first worksheet only |
| Import flow | Upload validates and stages, a human commits or discards; nothing is written by uploading |
| Import scope | A row naming another district is refused, never silently relocated |
| `location_code` | Decides the placement when given; a contradicting name column is a refusal |
| CHW code | `KYE00042` — district letters plus a serial, assigned on insert, never recomputed on transfer or district split; encodes no cadre |
| Register search | Name or CHW code only. A NIN is a national identifier; searching by one lets the register be probed with it |
| Name folding | Tier word folded by level against the gazetteer, never blanket-stripped: 279 subcounties sit beside their own `TOWN COUNCIL` |
| Unmatched name | Refused, never relocated — but the message looks one tier wider, never past the district, so the refusal is actionable |

## Stack

- `net/http` stdlib routing (1.22+), no framework
- `pgx/v5` + hand-written SQL
- `excelize/v2` for `.xlsx` / `.xlsm` uploads — the only third-party code that is not the
  driver, the migration runner or the password hash
- `goose` migrations embedded in the binary
- `html/template` server-rendered; vanilla JS only for the cascading location selects
- Cookie sessions (sha256-hashed in DB, raw token never stored), CSRF tokens on all mutating forms

```
cmd/server/main.go                  config, migrate, serve, admin bootstrap, shutdown
internal/config/                    env parsing, all problems reported at once
internal/db/                        pgxpool + goose over the embedded migrations
internal/domain/                    entities and sentinel errors, no I/O
internal/auth/                      Scope, capabilities, argon2id, sessions, CSRF, middleware
internal/store/                     SQL, one file per aggregate, every method takes a Scope
internal/http/                      router, handlers, form decoding, flashes
internal/web/{templates,static}/    layout + pages, one stylesheet, cascading selects
internal/importer/                  CSV/Excel readers, name resolution, row validation
migrations/                         *.sql + embed.go (go:embed)
```

Configuration: `DATABASE_URL` (required), `ADDR` (`:8080`), `ENV` (`dev`|`prod`),
`SHUTDOWN_TIMEOUT` (`15s`), `TRUSTED_PROXY` (empty). `ENV=prod` is what puts `Secure` on
the session, CSRF and flash cookies. `TRUSTED_PROXY` names the addresses whose
`X-Forwarded-For` may be believed — without it, a service behind a proxy records the
proxy in every audit row. See [deploy.md](deploy.md).

The CHW form's location selects cascade district > subcounty > parish > village against
`GET /api/locations?level=&under=`, which is scoped like every other read. County is
skipped in the UI and derived from the path — it is mandatory in the data, because
subcounty codes are unique only within a county.

Sessions expire 12 hours after issue and 2 hours after the last request, whichever comes
first. Passwords must be at least 12 characters mixing letters with a digit or symbol;
length carries the strength, because composition rules push staff toward predictable
substitutions.

## Permissions

Capability matrix and the three enforcement layers are in [rbac.md](rbac.md). The short
version: middleware checks the capability, every store method takes a `Scope` argument so
a handler cannot forget to apply it, and `users_scope_matches_role` makes an unscoped
district user unrepresentable in the database.

## Phases

1. **Skeleton + geography** — config, pool, embedded migrations, hierarchy seeder,
   facility loader + quarantine report *(done)*
2. **Auth** — argon2id, sessions, CSRF, RBAC middleware, `Scope` plumbing *(done)*
3. **CHW CRUD** — core record, deactivation with reason, audit on every mutation *(done)*
4. **Optional attributes** — profile form, tools and service-domain junctions *(done)*
5. **List UI** — search by name/code, filter cadre/status/location, pagination *(done)*
6. **Import + export** — CSV/Excel importer with a per-row error report, scoped CSV
   export *(done)*
7. **Deploy** — a plain binary under systemd, a reverse proxy for TLS, nightly backups
   with a rehearsed restore *(done — [deploy.md](deploy.md); no container, by decision)*

Geography is phase 1 because nothing else is testable without it. Phases 1–5 are
complete: hierarchy, facilities, constraint suite, the authentication and authorization
layer, the core register record, every optional attribute around it, and the search and
paging that make a register of that size navigable. Phase 6 is complete: the whole record
imports in bulk and comes back out through the same vocabulary. Phase 7 deploys it as a
binary under systemd, behind a proxy, with a rehearsed restore.

## The national seed

The register was filled from the August 2026 ODK Central export — 63,554 submissions,
which never enter this repository because they carry NINs and phone numbers.
`seed/convert_odk_export.py` resolves placement against the hierarchy and emits
importer-canonical CSVs, one cadre per run and one file per district; those go through the
ordinary `/imports` review-then-commit path. Full method and losses in
[`seed/README.md`](../seed/README.md).

| Run | Converted | Imported | Districts |
|---|---|---|---|
| `CADRE=vht` | 42,956 | 42,956 | 46 |
| `CADRE=chew` | 1,588 | 1,490 | 30 |
| **register** | | **44,446** | **54** |

The 98 CHEWs that did not land were refused by `chws_nin_uniq` — the same people
enumerated twice under two cadres, caught by the index rather than guessed at by the
converter. `SELECT count(*) FROM chws` and `count(*) FROM audit_log WHERE action =
'chw.create'` both read 44,446, which is invariant 6 holding across a 44k-row load.

The dominant loss is a collection gap, not a matching failure: 9,243 submissions recorded
no village at all, and whole districts recorded none — Namutumba 2,851 of 2,862, Bugiri
2,057 of 2,075. A VHT is placed at village, so those rows need re-collection before
anything can import them. That is why 46 districts come out of an export covering 64.

## Open work: the second source

The ODK export is one of two rosters. The other is **eCHIS** — `cht.mv_chw_hierarchy` in
`uganda_dwh`, 43,895 rows over 73 districts, seventeen more than the ODK export reached.
`seed/convert_echis_export.py` converts it, on the same contract as the ODK converter:
placement resolved here and handed over as `location_code`. Method and full counts in
[`seed/README.md`](../seed/README.md); measured 1 Sep 2026 against the seeded hierarchy:

| | VHT | CHEW |
|---|---|---|
| rows of that cadre | 38,609 | 5,286 |
| placed by the importer's own name cascade | 20,021 (52.3%) | — |
| **placed by the converter** | **26,913 (73.9%)** | **3,712 (70.2%)** |
| of those, already on the register | 7,294 | 536 |
| written out as new people | 19,317 | 3,173 |
| districts | 51 | 38 |

Every emitted `location_code` was checked back against `locations`: all 22,490 resolve, sit
at the level their cadre requires, and carry name columns matching the resolved chain, so
the importer's code-versus-name check passes on all of them.

**7,830 rows are people the register already holds.** eCHIS carries no NIN, so
`chws_nin_uniq` cannot see them, the importer only *warns* on a duplicate name at one
location, and a CHW is never deleted — a duplicate loaded from this file would be
permanent. The converter probes `chws` directly instead, on the token set rather than the
name pair, because eCHIS stores one name string whose order is unreliable.

**What is still open is `sex`, and only `sex`.** `chws.sex` is `NOT NULL` and the importer
requires it; eCHIS fills it 0 times in 43,895. So the converter writes `ready/` and
`pending/`, and today `ready/` is empty: **22,490 people are placed, de-duplicated and
canonical in every other column, waiting on one answer.** It is never guessed — a defaulted
sex is a wrong fact that would flow into the reporting the register feeds.

Two ways to close it, and they compose:

- `SEX_FILE=map.csv` merges a `chw_id,sex` extract, if CHT holds the field anywhere the
  view does not expose.
- Otherwise a `pending/` file *is* the district worklist: placement, phone and facility
  already settled, one column to fill, uploaded through the ordinary `/imports` path.

Beyond sex, eCHIS fills 10 of 28 columns — no NIN, no age, no education, incentives, tools
or services. That is not a reason to hold it back: every profile column is nullable, blank
imports as NULL, and a row from eCHIS is an honest placement and phone number rather than a
survey. The completeness chart is where that shows, which is what it is for.

## Source data

Full profiling results, defect inventory and reconciliation notes are in
[data-sources.md](data-sources.md). In brief: the hierarchy comes from
`data/Village-Admin Units 06-08-2026.xlsm` (clean, official codes, districts downward),
regions come from the checked-in `data/district_region.tsv`, facilities from the MFL
workbook, and the ODK hierarchy is unusable because it omits the county tier.

| Level | Count |
|---|---|
| region | 15 |
| district | 146 |
| county | 353 |
| subcounty | 2,198 |
| parish | 10,716 |
| village | 71,207 |
| **total** | **84,635** |

Plus 7,895 facilities parented to district (7,907 in the MFL, 12 quarantined as
duplicate names), of which 3,389 are government-owned.

## Verification status

Schema applied to PostgreSQL 18 and probed, not just written:

- full hierarchy loads in ~3s, 84,635 rows, zero losses, zero `code_path` mismatches
- 39 bad-data cases rejected, 0 leaked (`seed/verify_constraints.sql`) — see
  [data-model.md](data-model.md) for the full list
- the importer's tier-word fold merges no two siblings anywhere in the loaded hierarchy
  (`seed/verify_name_folding.sql`, run by `make verify`). The gazetteer counts the fold
  rests on were re-checked against the database: 588 subcounties end in `TOWN COUNCIL`,
  112 in `DIVISION`, 3,228 parishes in `WARD`, and 279 subcounties sit beside their own
  `… TOWN COUNCIL` under one county
- facilities load 7,895 of 7,907 rows across all 146 districts, 12 quarantined, none
  unresolvable; cross-district CHW attachment refused in both directions
- the whole path rebuilds from a dropped database and a deleted `seed/out/` using only
  checked-in files: every count identical, every district under its region
- `path` materialization and `district_id` derivation confirmed on real deep paths
- `locations` totals 33 MB

Since the Go skeleton landed, the schema is applied by the binary rather than by psql, and
re-verified through it end to end from a dropped database: `goose` reaches version 4, a
second run is a no-op, the hierarchy loads 84,635 rows in ~3s, facilities load 7,895 of
7,907, and the constraint suite reports 39 blocked / 0 leaked.

Phase 2 was exercised against the running server, not just unit-tested. Confirmed by
request:

- a wrong password, an unknown email and a disabled account all return the same 401 and
  the same message; the unknown-email path still pays for one argon2id verification
- a POST without the CSRF field is refused with 403 before the handler runs
- a freshly provisioned account is held on `/account/password` — every other path
  redirects there — until it chooses its own password
- weak, mismatched and wrong-current passwords are each refused with a field message
- a `district_manager` gets 403 on `/users`, sees no Users link, and reads only their own
  district's audit rows
- a national role offered a district, and a district role offered none, are both refused
  with a field message before the CHECK constraint is reached
- `MGR@example.org` collides with `mgr@example.org` — `citext` uniqueness holds
- disabling an account deletes its sessions in the same transaction; the browser holding
  one is bounced to `/login` on its next request
- the last active national admin cannot be demoted or disabled, and no account can
  disable itself
- a session idled past 2 hours, and one past its 12-hour expiry, are both refused and the
  cookie is cleared
- `audit_log` holds `auth.login_failed`, `auth.login`, `auth.password_change`,
  `user.create` and `user.status`, each stamped with the actor's district

Phase 3 was exercised the same way, against the populated hierarchy:

- a VHT offered a parish and a CHEW offered a village are both refused with a field
  message before `chws_set_placement` has to raise; so is re-cadring a VHT to CHEW
  without moving them off the village
- `district_id` is derived, not posted: a CHW placed in ABONGEPACH village comes back
  attached to ABIM without the form ever naming a district id
- `age_captured_on` is re-stamped only when the age changes — a save that left it alone
  kept a snapshot date of 2020-01-01
- a duplicate NIN is refused; a duplicate name at the same location is *warned*, with the
  matching records shown, and proceeds on a second submit
- deactivation demands a reason, writes `status`, `deactivated_at` and the reason
  together, and reactivation clears the first two while the audit row keeps the reason
- a GULU manager sees an empty register, 404s on an ABIM CHW, and gets `[]` from the
  location feed when probing ABIM's id; their district select offers one district
- a national viewer reads the register and gets 403 on create, edit and deactivate, with
  no controls rendered
- an ABIM manager cannot transfer a CHW to GULU; a national admin can, after which the
  ABIM manager 404s on the record and the GULU manager sees it. Both districts remain in
  the CHW's audit history
- the cascade was driven in a real browser: selecting a district loads 17 subcounties, a
  subcounty 7 parishes, a parish 8 villages; switching to CHEW hides *and clears* the
  village select; opening an existing record rebuilds the whole chain from the server's
  prefill, with no console errors

Phase 4 was exercised the same way. Each of the profile CHECKs was probed by posting the
combination it forbids, past the JavaScript that hides it:

- `0772 123-456` stores as `772123456`, `25,000` as `25000`, and June 2026 as `2026-06-01`
- answering "no" to owning a phone while posting a primary number and a reporting flag
  stores the alternate only — `phone_branch_exclusive` never sees the crossing
- an incentive amount and frequency posted with "no", and a supervision month posted with
  "no", are both dropped rather than stored or rejected
- training posted for a service that is not provided does not appear at all
- a facility in another district is a field message naming the CHW's district, not the
  500 the trigger alone would produce
- moving a CHW who still reports to a facility in their old district is refused with an
  instruction to reassign it first; clearing the facility lets the same move through
- a district manager 404s on another district's profile, both reading and writing; a
  national viewer 403s on the form and sees no edit link
- `audit_log` carries the whole profile plus both junction sets as before/after JSONB
- in the browser: branches stay hidden and *disabled* until their question is answered
  yes, a tool's condition unlocks only once the tool is held, and unticking a service
  clears its training box

Phase 5 was verified against 24,573 CHWs — a synthetic fixture loaded into the dev
database, since the real register arrives with the importer in phase 6:

- every filter's count was checked against the same count taken in SQL: 1,279 inactive,
  347 CHEWs, 202 in ABIM, 58 for a two-word name search, 20 for a NIN prefix — the NIN
  filter has since been removed, and search is name or CHW code
- paging the CHEW filter to exhaustion returned all 347 rows over 7 pages, in exactly the
  order the database returns them, with no row repeated and none skipped
- walking back from the last page retraced the forward pages exactly
- filters survive paging; a corrupt cursor falls back to page one instead of erroring
- a district manager handed a cursor taken from the *national* listing still sees only
  their own district: a cursor is a position, not a permission
- `EXPLAIN ANALYZE` confirms the keyset pages are index scans on
  `chws_district_name_idx` / `chws_name_sort_idx` (~1–2 ms at 24.5k rows), and NIN
  prefixes an index scan on `chws_nin_prefix_idx`. The trigram index is used for a
  selective name; for a term matching 4% of the table the planner prefers a sequential
  scan, which at this size is the cheaper plan and not a defect.
  **`chws_nin_prefix_idx` is now unreferenced** — nothing in `internal/store` searches a
  NIN since the filter was removed. Migrations are append-only, so dropping it is a new
  migration and not an edit to 0005; it is left in place until something else needs the
  write cost back

Phase 6's import half was exercised the same way, against the seeded hierarchy and a
24,573-record register — through the running server, and through a real browser on the
Selenium grid for the pages. The full list is in [import.md](import.md); in brief:

- an eleven-row file carrying one of every refusal imported four and refused seven, across
  six distinct quarantine reasons, with placement derived by trigger in every case
- `BUHOBA A` — two villages of that name under one parish, the real case the schema's
  `(parent_id, code)` identity exists for — is quarantined with both candidates and their
  chains, and imports once `location_code` names which
- an ABIM manager's file naming GULU imported the ABIM row and refused the other two, and
  the rendered page named neither the district nor any location inside it
- a `district_viewer` gets 403 on every import route and no rail entry; an ABIM manager
  gets 404 on a national batch's report, error file and commit
- 10,000 rows validate and stage in 4s and commit in 32s, one transaction per record
- **a batch committed twice at once imports each row once.** Before the claim in migration
  0007 this was measured creating 2,033 records from a 1,200-row file
- a row that lost a NIN race between the report and the commit is marked `failed` and
  quarantined while its neighbours import

Reproduce from the repo root:

```bash
createdb chwr && export DATABASE_URL=postgres:///chwr
go run ./cmd/server -migrate
python3 seed/extract_units.py
psql -d chwr -f seed/load_hierarchy.sql
python3 seed/extract_facilities.py
psql -d chwr -f seed/load_facilities.sql
psql -d chwr -f seed/verify_constraints.sql
go test ./...
go run ./cmd/server -create-admin you@example.org -name "Your Name"
go run ./cmd/server                              # sign in at http://localhost:8080/login
```

The cascading selects and the profile branches need a browser, not curl. Selenium is on
`:4444` and reaches the app on the docker gateway address, not `127.0.0.1`.

## Known empty-on-import fields

The ODK export cannot source these; they fill in only through the web UI:

- `chw_profiles.last_supervised_on` / `received_supervision` — the form records supervision
  per service domain and carries no date
- `chws.age_captured_on` — provenance stripped, so imports stamp the import date
