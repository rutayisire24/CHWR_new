# Roadmap

Go + `html/template` + vanilla CSS/JS, PostgreSQL. No framework, no ORM, no JS build step.

## Where we are

**Phase 1 complete. Phase 2 (auth) is next, and nothing in it is started.**

The database is real and populated: 84,635 hierarchy rows and 7,895 facilities, applied by
the binary and probed with 39 rejection cases. The Go side is a skeleton — it configures
itself, migrates, serves `/healthz` and shuts down cleanly. There is no login, no CHW
record, no HTML template and no `Scope` type yet.

| | State |
|---|---|
| `migrations/` 0001–0004 | applied and verified on PostgreSQL 18 |
| `seed/` hierarchy + facilities + constraint suite | complete, reproducible from the repo root |
| `cmd/server`, `internal/{config,db,http}` | skeleton: migrate, serve, health, graceful shutdown |
| `internal/{domain,store,auth,importer}`, `internal/web` | **empty — phases 2 onward** |

Immediate next steps, in order: `internal/domain` entities and the `Scope` type,
argon2id hashing, the sessions store, CSRF middleware, then the RBAC middleware that
phases 3+ depend on.

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
cmd/server/main.go                  config, migrate, serve, graceful shutdown
internal/config/                    env parsing, all problems reported at once
internal/db/                        pgxpool + goose over the embedded migrations
internal/http/                      router; /healthz pings the pool
internal/{domain,store,auth,importer}/   not started
internal/web/{templates,static}/    not started
migrations/                         *.sql + embed.go (go:embed)
```

Configuration: `DATABASE_URL` (required), `ADDR` (`:8080`), `ENV` (`dev`|`prod`),
`SHUTDOWN_TIMEOUT` (`15s`).

## Permissions

Capability matrix and the three enforcement layers are in [rbac.md](rbac.md). The short
version: middleware checks the capability, every store method takes a `Scope` argument so
a handler cannot forget to apply it, and `users_scope_matches_role` makes an unscoped
district user unrepresentable in the database.

## Phases

1. **Skeleton + geography** — config, pool, embedded migrations, hierarchy seeder,
   facility loader + quarantine report *(done)*
2. **Auth** — argon2id, sessions, CSRF, RBAC middleware, `Scope` plumbing
3. **CHW CRUD** — core record, deactivation with reason, audit on every mutation
4. **Optional attributes** — profile form, tools and service-domain junctions
5. **List UI** — search by name/NIN, filter cadre/status/location, pagination
6. **Import + export** — CSV importer with per-row error report, scoped CSV export
7. **Deploy** — Docker, backups, first admin bootstrap

Geography is phase 1 because nothing else is testable without it. Phase 1 is complete:
hierarchy seeder, facility loader, constraint suite and Go skeleton. Phase 2 (auth) is next.

## Source data

Full profiling results, defect inventory and reconciliation notes are in
[data-sources.md](data-sources.md). In brief: the hierarchy comes from
`data/Village-Admin Units 06-08-2026.xlsm` (clean, official codes, districts downward),
regions and facilities come from the ODK workbook, and the ODK hierarchy is unusable
because it omits the county tier.

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
- `path` materialization and `district_id` derivation confirmed on real deep paths
- `locations` totals 33 MB

Since the Go skeleton landed, the same schema is applied by the binary rather than by
psql, and re-verified through it: `goose` brings an empty database to version 3, a second
run is a no-op, the hierarchy still loads 84,635 rows in ~3s, and the constraint suite
still reports 36 blocked / 0 leaked.

Reproduce from the repo root:

```bash
createdb chwr && export DATABASE_URL=postgres:///chwr
go run ./cmd/server -migrate
python3 seed/extract_units.py "<National CHWR.xlsx>"
psql -d chwr -f seed/load_hierarchy.sql
python3 seed/extract_facilities.py
psql -d chwr -f seed/load_facilities.sql
psql -d chwr -f seed/verify_constraints.sql
```

## Known empty-on-import fields

The ODK export cannot source these; they fill in only through the web UI:

- `chw_profiles.last_supervised_on` / `received_supervision` — the form records supervision
  per service domain and carries no date
- `chws.age_captured_on` — provenance stripped, so imports stamp the import date
