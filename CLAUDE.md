# National Community Health Worker Registry

A Go service that maintains Uganda's national register of Community Health Workers.
Three jobs: **manage CHWs** (create, update, deactivate), **scope access by role and
district**, and **place every CHW in the official administrative hierarchy**.

Server-rendered. No SPA, no framework, no ORM, no JS build step.

## Stack

- Go 1.24 (`net/http` stdlib routing, 1.22+ patterns) — no web framework
- PostgreSQL 18 via `pgx/v5` with hand-written SQL — no ORM
- `goose` migrations, embedded in the binary
- `html/template` + vanilla CSS; vanilla JS for the cascading location selects and the
  dashboard charts, which use Chart.js vendored into `internal/web/static/vendor/`
- Cookie sessions stored in Postgres, argon2id passwords, CSRF on all mutating forms

## Layout

```
cmd/server/          entrypoint
internal/config/     env parsing
internal/db/         pool, embedded migrations
internal/domain/     entities, no I/O
internal/store/      SQL; every method takes a Scope
internal/auth/       Scope, capabilities, argon2id, sessions, CSRF, middleware
internal/http/       handlers, routing, form decoding
internal/importer/   CSV/ODK ingest (not started)
internal/web/        templates/ and static/
migrations/          0001_locations, 0002_users_auth, 0003_chws, 0004_facilities_mfl,
                     0005_chw_listing
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
10. **The raw session token is never stored.** The cookie carries it; only its SHA-256
   reaches `sessions.token_hash`. Revocation is a `DELETE`, which is the whole reason
   sessions are rows and not JWTs.

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

## The register

`chws` is the core record; `chw_profiles` and the two junctions hold the optional survey
attributes. Placement is one `location_id` whose level the cadre decides, and
`district_id` is derived from it — the form never posts a district for the record, only
for the cascade.

Every profile column is nullable, and **"no" and "not asked" are different answers**: the
Go side carries `*bool`, templates use `deref`, and an imported record that answered
nothing must not come back as a record that answered no. Junction sets are replaced on
save, not diffed — they are the answer to a multi-select.

The location selects cascade district > subcounty > parish > village against
`GET /api/locations?level=&under=`, which is scoped like every other read. County is
skipped in the UI and derived from the path.

The listing searches one box for either a name or a NIN, decided by the shape of the
input, and pages with a **keyset** on `(lower(last_name), lower(first_name), id)` — never
an offset. A cursor is a position, not a permission: the `Scope` still decides which rows
past it are visible.

CHW mutations run in a transaction that also writes their `audit_log` row
(`store.Audit.RecordTx`), which is what makes invariant 6 structural rather than
remembered. Duplicate NIN refuses; a duplicate name at the same location warns and
proceeds on a second submit.

## The dashboard

`GET /` summarises the same register through the same `Scope`, and **changes tier with
it**: nationally the chart groups by region and the league table by district; inside a
district those become subcounty and parish. Every query lives in `internal/store/stats.go`
and takes a `Scope` like any other read.

Grouping reads the ancestor id out of `locations.path` with `split_part` rather than
prefix-joining 84,635 locations; `segment()` maps a level to its position and is tested,
because an off-by-one would group by the wrong tier and still draw a chart. `Areas` and
`Reach` join outward from `locations`, so an area with nobody in it is a row and counts
against coverage.

Charts are Chart.js, vendored — no CDN, and the CSP would refuse one. Figures reach the
page in a `<script type="application/json">` block, which is never executed and so is not
gated by `script-src`. The same policy forbids the `style` attribute, so any width that is
data (the meters, the league table bars) is drawn in SVG. Every chart carries a
server-rendered `<details>` table of its own numbers: the accessibility twin, and the
no-JavaScript rendering.

The `--viz-*` palette in `app.css` is assigned by the job the colour does. No chart uses
more than two identities, and the status colours stay out of charts entirely — green here
means "this CHW is active" and nothing else.

## Auth

Accounts are provisioned by a `national_admin` (or `-create-admin` for the first one)
with a temporary password and `must_reset` set; every route redirects to
`/account/password` until the user chooses their own. Sessions last 12 hours, or 2 hours
idle. Passwords are argon2id (64 MiB, t=3, p=4), minimum 12 characters mixing letters
with a digit or symbol.

Two store methods take no `Scope`, both documented at their definitions and both
pre-authentication: `Users.Credentials` (login) and `Sessions.Authenticate` (the call
that produces the principal a `Scope` is derived from). Nothing else may be added to that
list.

## Conventions

- Plain SQL in `internal/store`, one file per aggregate. No query builders.
- Handlers decode, authorize, delegate, render. No SQL in `internal/http`.
- Errors wrap with `fmt.Errorf("...: %w", err)`; sentinel errors live in `internal/domain`.
- Templates are parsed once at startup and embedded with `go:embed`; every page is
  `layout.html` plus its own file, and rendering buffers before writing the status.
- Enums and `citext` are cast to `text` in the projection, so pgx needs no type
  registration; parameters cast the other way (`$1::user_role`).
- Migrations are append-only. Never edit one that has been applied.
- Verify schema changes against a real database before claiming they work. The
  cascading selects need a browser, not curl: Selenium is on `:4444`, and the app is
  reachable from it on the docker gateway rather than `127.0.0.1`.

## Documentation

`docs/README.md` indexes the detail: data model, the application layer (middleware,
routes, forms, the cascade), RBAC, data sources and their quirks, the ODK field mapping,
seeding, decision log, and roadmap.
