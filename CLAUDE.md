# National Community Health Worker Registry

A Go service that maintains Uganda's national register of Community Health Workers.
Three jobs: **manage CHWs** (create, update, deactivate), **scope access by role and
district**, and **place every CHW in the official administrative hierarchy**.

Server-rendered. No SPA, no framework, no ORM, no JS build step.

## Stack

- Go 1.24 (`net/http` stdlib routing, 1.22+ patterns) — no web framework
- PostgreSQL 18 via `pgx/v5` with hand-written SQL — no ORM
- `goose` migrations, embedded in the binary
- `html/template` + vanilla CSS; vanilla JS only for the cascading location selects
- Cookie sessions stored in Postgres, argon2id passwords, CSRF on all mutating forms

## Layout

```
cmd/server/          entrypoint
internal/config/     env parsing
internal/db/         pool, embedded migrations
internal/domain/     entities, no I/O
internal/store/      SQL; every method takes a Scope
internal/auth/       password, session, RBAC middleware
internal/http/       handlers, routing, form decoding
internal/importer/   CSV/ODK ingest
internal/web/        templates/ and static/
migrations/          0001_locations, 0002_users_auth, 0003_chws, 0004_facilities_mfl
seed/                hierarchy extraction + load
data/                source workbooks and the district-to-region map (checked in)
docs/                detailed reference — see docs/README.md
```

## Non-negotiable invariants

These are enforced in the schema, not just in application code. Do not work around them.

1. **Cadre determines placement.** CHEWs sit at parish level, VHTs at village level.
   One `chws.location_id`; the level is checked by trigger on insert *and* on cadre change.
2. **`district_id` is derived, never supplied.** A trigger walks `locations.path` to the
   district ancestor. It is denormalized purely so RBAC filters stay indexed.
3. **Scope is a required argument.** Every `store` method takes a `Scope`; a handler
   cannot forget to apply it, because the call will not compile without it.
4. **Role and scope must agree.** `users_scope_matches_role` makes a district user without
   a district — or a national user with one — unrepresentable.
5. **CHWs are never deleted.** Deactivation sets `status`, `deactivated_at` and a reason.
   `chws_deactivation_complete` keeps those consistent.
6. **Every mutation writes to `audit_log`** with before/after JSONB. That table doubles as
   CHW change history, which is why there is no separate versioning table.
7. **Nothing is dropped silently on import.** Unresolvable rows go to `import_quarantine`
   with a reason. A half-loaded register is worse than a rejected one.
8. **Facilities are parented to district, never lower.** The MFL's subcounty column
   resolves for 47% of rows; it is kept raw in `subcounty_label` and never matched.
9. **A CHW's facility is in the CHW's own district.** `chw_profiles.facility_id` is an
   optional attachment, not a placement, and triggers refuse it in both directions — a
   cross-district attach, and a transfer that would strand one.

## Hierarchy

`region > district > county > subcounty > parish > village` — 84,635 rows, plus 7,895
health facilities hanging off district.

**County is mandatory** even though no UI selects it: subcounty codes are unique only
within a county. The old ODK form omitted the tier and consequently lost 732 subcounties
and 909 parishes to code collisions. The UI cascades district > subcounty > parish >
village and derives county from the path.

Identity at every level is `(parent_id, code)`, **not** name — one parish genuinely holds
two villages sharing a name.

## Roles

`national_admin` · `national_viewer` · `district_manager` · `district_viewer`

Read the matrix in `docs/rbac.md` before touching authorization. Only `national_admin`
manages users.

## Working here

```bash
createdb chwr
export DATABASE_URL=postgres:///chwr
go run ./cmd/server -migrate                     # goose, embedded; idempotent
python3 seed/extract_units.py                   # writes seed/out/
psql -d chwr -f seed/load_hierarchy.sql          # run from repo root; ~3s
python3 seed/extract_facilities.py               # MFL -> seed/out/facilities.tsv
psql -d chwr -f seed/load_facilities.sql         # 7,895 loaded, 12 quarantined
psql -d chwr -f seed/verify_constraints.sql      # 39 cases, all must say blocked
go run ./cmd/server                              # serves on ADDR, default :8080
```

The server migrates on every start; `-migrate` stops after that. Migration files carry
goose annotations (`-- +goose Up`, and `StatementBegin/End` around plpgsql bodies, whose
`$$` bodies goose's statement splitter cannot otherwise see).

`\copy` performs no variable interpolation — the seeder uses literal paths relative to the
repo root. Run it from there.

## Conventions

- Plain SQL in `internal/store`, one file per aggregate. No query builders.
- Handlers decode, authorize, delegate, render. No SQL in `internal/http`.
- Errors wrap with `fmt.Errorf("...: %w", err)`; sentinel errors live in `internal/domain`.
- Templates are parsed once at startup and embedded with `go:embed`.
- Migrations are append-only. Never edit one that has been applied.
- Verify schema changes against a real database before claiming they work.

## Documentation

`docs/README.md` indexes the detail: data model, RBAC, data sources and their quirks, the
ODK field mapping, seeding, decision log, and roadmap.
