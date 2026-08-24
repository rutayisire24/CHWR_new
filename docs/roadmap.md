# Roadmap

Go + `html/template` + vanilla CSS/JS, PostgreSQL. No framework, no ORM, no JS build step.

## Where we are

**Phases 1–5 complete. Phase 6 (import and export) is next, and nothing in it is
started.**

The register is now usable at scale: search by name or NIN, filter by cadre, status and
any level of the hierarchy, and page through the result with a keyset. What is missing is
getting the existing register in — every record so far has been typed one at a time — and
getting a scoped slice back out.

| | State |
|---|---|
| `migrations/` 0001–0005 | applied and verified on PostgreSQL 18 |
| `seed/` hierarchy + facilities + constraint suite | complete, reproducible from the repo root |
| `cmd/server`, `internal/{config,db}` | migrate, serve, health, graceful shutdown, admin bootstrap, hourly session purge |
| `internal/domain` | `User`, `CHW`, `Profile`, the enums, `Level`, sentinel errors |
| `internal/auth` | `Scope`, capability matrix, argon2id, session tokens, CSRF, middleware |
| `internal/store` | users, sessions, audit, locations, chws, profiles — every method takes a `Scope` |
| `internal/http` | auth, user admin, audit, dashboard, CHW CRUD, profiles, search and paging |
| `internal/web` | layout + eleven pages, one stylesheet, two scripts |
| `internal/importer` | **empty — phase 6** |
| CSV export | **phase 6 — it shares `store.Filter` with the listing** |
| `chw_languages` | **empty by design — fills from the importer's parsing in phase 6** |

Immediate next steps, in order: the CSV/ODK reader, per-row validation writing failures to
`import_quarantine` with a reason, a dry-run that reports before it writes, and the scoped
export.

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

## Stack

- `net/http` stdlib routing (1.22+), no framework
- `pgx/v5` + hand-written SQL
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
internal/importer/                  not started (phase 6)
migrations/                         *.sql + embed.go (go:embed)
```

Configuration: `DATABASE_URL` (required), `ADDR` (`:8080`), `ENV` (`dev`|`prod`),
`SHUTDOWN_TIMEOUT` (`15s`). `ENV=prod` is what puts `Secure` on the session, CSRF and
flash cookies.

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
5. **List UI** — search by name/NIN, filter cadre/status/location, pagination *(done)*
6. **Import + export** — CSV importer with per-row error report, scoped CSV export
7. **Deploy** — Docker, backups (the first-admin bootstrap landed with phase 2:
   `-create-admin`)

Geography is phase 1 because nothing else is testable without it. Phases 1–5 are
complete: hierarchy, facilities, constraint suite, the authentication and authorization
layer, the core register record, every optional attribute around it, and the search and
paging that make a register of that size navigable. Phase 6 (import and export) is next.

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
  347 CHEWs, 202 in ABIM, 58 for a two-word name search, 20 for a NIN prefix
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
  scan, which at this size is the cheaper plan and not a defect

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
