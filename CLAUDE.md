# National Health Worker Registry

A Go service that maintains Uganda's national register of health workers, beginning with
Community Health Workers. Three jobs: **manage health workers** (create, update, deploy,
deactivate), **scope access by role and district**, and **place every worker in the
official administrative hierarchy**.

Server-rendered. No SPA, no framework, no ORM, no JS build step.

## Stack

- Go 1.24 (`net/http` stdlib routing, 1.22+ patterns) — no web framework
- PostgreSQL 18 via `pgx/v5` with hand-written SQL — no ORM
- `goose` migrations, embedded in the binary
- `excelize/v2` reads uploaded `.xlsx` / `.xlsm` — the only dependency that is not the
  driver, the migration runner or the password hash
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
internal/importer/   CSV/Excel ingest: readers, name resolution, row validation
internal/web/        templates/ and static/
migrations/          0001_locations, 0002_users_auth, 0003_health_workers,
                     0004_chw_profile, 0005_imports
seed/                hierarchy extraction + load
data/                source workbooks and the district-to-region map (checked in)
docs/                detailed reference — see docs/README.md
```

## The model: person, cadre, posting

The register is of **people**, not postings. `health_workers` carries only who the
person is; what they do and where is a `deployments` row — one health worker, one
cadre, one location, one period. A transfer or a promotion ends one row and opens
another, so the register itself answers "who was deployed at X on date D".

Cadres are **data, not an enum**, in a two-level taxonomy: `cadre_categories`
(Community Health Workers) containing `cadres` (VHT, CHEW). Each cadre row carries
its own `placement_level` — VHT→village, CHEW→parish — so a new cadre is an INSERT,
not a migration. Each category owns its profile surface: `chw_profiles` and the
`chw_tools` / `chw_service_domains` junctions belong to the CHW category; a future
category gets its own tables. A worker holds **at most one active deployment**
(`ended_on IS NULL`, enforced by a partial unique index).

Reads see a worker through one posting — the active one, else the most recent — via a
lateral join (`workerFrom` in `internal/store/workers.go`), so an inactive worker is
still listed where they last served. Scoping anchors on `health_workers.district_id`,
a trigger-maintained denormalization of the latest deployment's district: it survives
deactivation, which a join against the active deployment could not do.

## Non-negotiable invariants

These are enforced in the schema, not just in application code. Do not work around them.

1. **Cadre determines placement.** A deployment's location must sit at the level its
   cadre's row declares. Checked by trigger on insert and on cadre/location change;
   the level is read from `cadres.placement_level`, never from a CASE.
2. **`district_id` is derived, never supplied.** A trigger walks `locations.path` to
   the district ancestor — on `deployments`, and from there onto `health_workers` as
   the RBAC anchor. Both are denormalized purely so scope filters stay indexed.
3. **Scope is a required argument.** Every `store` method takes a `Scope`; a handler
   cannot forget to apply it, because the call will not compile without it.
4. **Role and scope must agree.** `users_scope_matches_role` makes a district user without
   a district — or a national user with one — unrepresentable.
5. **Health workers are never deleted.** Deactivation sets `status`, `deactivated_at`
   and a reason, and the schema refuses it while a deployment is open — the posting
   ends first, in the same transaction. A deployment is *ended*, never deleted either.
6. **Every mutation writes to `audit_log`** with before/after JSONB. That table doubles as
   the register's change history, which is why there is no separate versioning table.
7. **Nothing is dropped silently on import.** Refused rows go to `import_quarantine` with
   a reason, and `import_rows_refusal_explained` makes a refusal without one impossible to
   store. A half-loaded register is worse than a rejected one.
8. **Facilities are parented to district, never lower.** The MFL's subcounty column
   resolves for 47% of rows; it is kept raw in `subcounty_label` and never matched.
9. **A deployment's facility is in the deployment's own district.** `deployments.facility_id`
   is an optional attachment, not a placement, and one trigger refuses a cross-district
   row in either direction — a cross-district attach, and a move that would strand one.
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
psql -d chwr -f seed/verify_constraints.sql      # 45 cases, all must say blocked
go run ./cmd/server                              # serves on ADDR, default :8080
```

`make` prints the shorthand for all of the above — `make seed` runs the four database
steps in order, `make restart` rebuilds and restarts a background server, `make check`
formats, vets and tests. Every variable is overridable: `make restart ADDR=:8099`.

The server migrates on every start; `-migrate` stops after that. Migration files carry
goose annotations (`-- +goose Up`, and `StatementBegin/End` around plpgsql bodies, whose
`$$` bodies goose's statement splitter cannot otherwise see).

`\copy` performs no variable interpolation — the seeder uses literal paths relative to the
repo root. Run it from there.

## The register

`health_workers` is the person; their posting is a `deployments` row; `chw_profiles`
and the two junctions hold the CHW category's optional survey attributes, 1:1 on the
worker. Placement is the deployment's `location_id` whose level the cadre decides, and
`district_id` is derived from it — the form never posts a district for the record, only
for the cascade.

Every profile column is nullable, and **"no" and "not asked" are different answers**: the
Go side carries `*bool`, templates use `deref`, and an imported record that answered
nothing must not come back as a record that answered no. Junction sets are replaced on
save, not diffed — they are the answer to a multi-select.

The location selects cascade district > subcounty > parish > village against
`GET /api/locations?level=&under=`, which is scoped like every other read. County is
skipped in the UI and derived from the path. The cadre radios carry the cadre row's
`placement_level` as `data-level`, so the cascade's depth is data-driven too.

The listing searches one box for either a name or a NIN, decided by the shape of the
input, and pages with a **keyset** on `(lower(last_name), lower(first_name), id)` — never
an offset. A cursor is a position, not a permission: the `Scope` still decides which rows
past it are visible.

Worker mutations run in a transaction that also writes their `audit_log` rows
(`store.Audit.RecordTx`), which is what makes invariant 6 structural rather than
remembered. Creating or editing writes the worker and — when cadre or location changed —
ends the old deployment and opens the new one beside it, in the same transaction.
Duplicate NIN refuses; a duplicate name at the same location warns and proceeds on a
second submit. The supervising facility is an attachment on the open posting, edited
on its own (`Deployments.SetFacility`); a cross-district transfer does not carry it.

## The dashboard

`GET /` summarises the same register through the same `Scope`, and **changes tier with
it**: nationally the chart groups by region and the league table by district; inside a
district those become subcounty and parish. Every query lives in `internal/store/stats.go`
and takes a `Scope` like any other read.

The tiles count the register — workers, one tile per active cadre, areas reached.
**Completeness measures are not tiles**: a "% carrying a NIN" or "% supervised" is a fact
about how filled-in the register is, not about workers, and a headline `0%` reads as an
operational failure rather than an unasked question. They belong to the Record
completeness chart, which states that distinction. Supervision is NULL on every imported
row by construction, so it will read near zero until it is captured through the UI.

Grouping reads the ancestor id out of `locations.path` with `split_part` rather than
prefix-joining 84,635 locations; `segment()` maps a level to its position and is tested,
because an off-by-one would group by the wrong tier and still draw a chart. `Areas` and
`Reach` join outward from `locations`, so an area with nobody in it is a row and counts
against coverage. Workers are counted through the same posting the listing sees them
through (`seenThrough`), so a chart and the register beneath it cannot disagree.

Charts are Chart.js, vendored — no CDN, and the CSP would refuse one. Figures reach the
page in a `<script type="application/json">` block, which is never executed and so is not
gated by `script-src`. The same policy forbids the `style` attribute, so any width that is
data (the meters, the league table bars) is drawn in SVG. Every chart carries a
server-rendered `<details>` table of its own numbers: the accessibility twin, and the
no-JavaScript rendering.

The `--viz-*` palette in `app.css` is assigned by the job the colour does. No chart uses
more than two identities, and the status colours stay out of charts entirely — green here
means "this worker is active" and nothing else.

## Bulk import

`/imports` takes a CSV or Excel file, checks every row, and **writes nothing**. It stages
the verdicts in `import_batches` / `import_rows`; a human reads the report and commits or
discards. What is committed is what was reviewed — re-reading the file would validate
against a register that has since moved.

Commit calls `store.Workers.CreateTx` once per row, in a transaction it shares with the
worker's first deployment, every audit row and the staged row's mark. That is what keeps
invariant 6 structural here too, and what stops a killed process leaving a worker whose
import row still reads `ready`. A row that fails does not abort the batch; it is marked,
quarantined, and the rest continue. Committing is claimed first
(`import_batches.committing_at`) because the run takes tens of seconds and two concurrent
runs would both write the rows neither had marked yet.

A district user's file cannot reach another district. Four layers say so and only the last
is load-bearing: the `health_worker.import` capability, a template carrying their district,
a resolver preloaded with only the districts their `Scope` allows, and `CreateTx`'s
post-insert check against the derived `district_id`. **A row naming another district is
refused, never relocated**, and the refusal names neither that district nor the location it
matched — a district user must not map the country by probing names.

Location names are matched by dropping every separator and nothing fuzzier; two siblings
matching is an ambiguity, quarantined with both candidates and the code that settles it.
`location_code` decides the placement when given, and a contradicting name column is a
refusal, not a preference. The cadre column matches against `cadres.import_aliases` — a
new cadre is importable the moment its row lands. Rules the worker form already applies —
the NIN pattern, the age range, the sex vocabulary — live in `internal/domain` and are
shared, so the importer can never accept what the form refuses.

The optional attributes import too, all seventeen columns. Blank and "no" stay different
answers: an empty cell is NULL, only an explicit `no` writes false, and a row whose profile
columns are all empty writes **no `chw_profiles` row at all**. Every branch CHECK is
pre-checked and reported rather than dropped — the profile *form* silently discards a value
posted into a hidden branch, which is right for a form and wrong for an import.

A staged row carries `record`, the resolved register record as JSON (worker, deployment
and profile sections), beside `raw`. The commit reads it back rather than re-deriving it:
a facility is resolved by name within the deployment's district, and re-resolving at
commit would answer from a register that has moved.

## The export

`GET /health-workers/export.csv` is the listing as a file: the same query string, the same
`decodeFilter`, the same `Scope`. The cursor is dropped — an export is the whole
selection, not the page being looked at — and rows stream through a callback, flushed in
batches, so the national register never sits in memory.

**Its columns are the importer's columns.** A row that comes out can go back in, junction
sets and all, which is why `internal/http/export.go` spells them with the `importer.Col*`
constants rather than string literals. The register-only columns beside them — the id, the
derived placement, the status, the timestamps, supervision — are named as unknown by an
import and ignored, which is right for values an upload must not set. The export is
CHW-shaped while CHW is the only category; a second category will want its own shape.

## Auth

Accounts are provisioned by a `national_admin` (or `-create-admin` for the first one)
with a temporary password and `must_reset` set; every route redirects to
`/account/password` until the user chooses their own. Sessions last 12 hours, or 2 hours
idle. Passwords are argon2id (64 MiB, t=3, p=4), minimum 12 characters mixing letters
with a digit or symbol.

Two store methods take no `Scope`, both documented at their definitions and both
pre-authentication: `Users.Credentials` (login) and `Sessions.Authenticate` (the call
that produces the principal a `Scope` is derived from). Nothing else may be added to that
list. (Vocabulary reads — cadres, tools, service domains — take none either; they are
the same for every caller.)

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
- Product naming: the module, binary and cookies keep the `chwr` name; the concept in
  the schema, the Go types and the UI is the health worker.

## Documentation

`docs/README.md` indexes the detail: data model, the application layer (middleware,
routes, forms, the cascade), RBAC, data sources and their quirks, the ODK field mapping,
seeding, decision log, and roadmap.
