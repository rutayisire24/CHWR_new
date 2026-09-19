# Data model

Five migrations, applied in order, embedded in the binary and run by `goose` at startup.
All verified against PostgreSQL 18.

- `0001_locations.sql` — hierarchy, facilities with their MFL attributes
- `0002_users_auth.sql` — users, sessions, audit log
- `0003_health_workers.sql` — the person, the cadre taxonomy, deployments, and the
  triggers that tie them together
- `0004_chw_profile.sql` — the Community Health Workers category's profile, junctions and
  vocabularies
- `0005_imports.sql` — import staging, the commit claim, the resolved record, quarantine

These five replaced an earlier CHW-only sequence (`0003_chws` … `0008_import_record`).
A database migrated under the old sequence does not upgrade in place; it is rebuilt and
re-seeded. See [decisions.md](decisions.md).

## Locations

One self-referencing table for all six levels, rather than six tables.

```
region > district > county > subcounty > parish > village
   15       146       353       2,198      10,716    71,207     = 84,635 rows (33 MB)
```

| Column | Purpose |
|---|---|
| `parent_id` | tree link; NULL only for regions |
| `level` | `location_level` enum, ordered as the ladder above |
| `code` | official segment code (e.g. `003`); NULL for regions |
| `code_path` | full concatenation (e.g. `00100301003001`) |
| `path` | materialized surrogate-id ancestors (`/1/14/233/`) |

**Identity is `(parent_id, code)`, not name.** There is deliberately no unique constraint
on `(parent_id, name)`: parish `095/235/04/038` genuinely contains two villages both
named `BUHOBA A`, codes `002` and `003`.

`locations_before_insert()` does two jobs: it materializes `path` from the parent, and it
enforces the ladder — a subcounty must hang off a county, a village off a parish, and so
on. Both halves were verified to reject violations. `bigserial` defaults are applied
before `BEFORE` triggers fire, so `NEW.id` is available for path construction.

`path` uses `text_pattern_ops` so subtree queries are indexed prefix scans. Names carry a
`gin_trgm_ops` index for search.

### Why county exists

No screen asks for county, but subcounty codes are unique only *within* a county.
Collapsing the tier merges 2,198 subcounties into 1,466 and 10,716 parishes into 9,807.
The UI cascades district > subcounty > parish > village and derives county from `path`.

## Facilities

7,895 rows from `data/MFL Updated - 21 feb.xlsx`, parented to **district**. A trigger
verifies the parent is genuinely a district.

| Column | Notes |
|---|---|
| `district_id` | the only hierarchy link; see below |
| `name`, `slug` | identity is `(district_id, name)` — the MFL carries no facility code |
| `level` | `HC II` … `Hospital`, `Clinic`, `Drug Shop`; free text, 17 values in the source |
| `ownership` | `GOV` (3,389) · `PFP` · `PNFP` |
| `authority` | `MOH`, `Private`, `UPDF`, … |
| `subcounty_label` | raw workbook text, **deliberately unresolved** |

**District is the parent by decision.** The workbook's subcounty column resolves for 3,696
of 7,907 rows, misses 4,201 and is ambiguous for 10; district resolves for all 7,907. See
[data-sources.md](data-sources.md) for the profiling and
[decisions.md](decisions.md) for the rejected alternatives.

### Worker-to-facility

`deployments.facility_id` is the supervising and reporting site — an optional attachment
on a posting, not a placement. Placement remains cadre-driven and untouched by it.

Because the attachment and the placement share one row, one trigger keeps the districts in
agreement: `deployments_facility_district_trg` fires after insert and after any change of
`facility_id`, `location_id` or `cadre_id`, and refuses a row whose facility is outside the
deployment's own district. That covers both directions the old schema needed two triggers
for — attaching across districts, and moving a posting that would strand its facility.

It raises rather than clearing the column. The application avoids the case by construction:
a transfer ends the old posting and opens a new one, carrying the facility only when the
new district is the old one.

## Users, sessions, audit

`users` carries `role` and a nullable `district_id`, kept consistent by
`users_scope_matches_role` plus a trigger checking the level. See [rbac.md](rbac.md).

`sessions` stores `token_hash` (SHA-256) as its primary key. The raw cookie token is never
persisted.

`audit_log` records actor, action, entity, `before`/`after` JSONB, IP and timestamp, plus
a `district_id` so district admins can read their own slice. It doubles as the register's
change history — there is no separate versioning table. Worker changes are
`health_worker.*`, postings `deployment.start` / `.end` / `.update`, and the CHW profile
`chw.profile_update`.

## Health workers

The register is of **people**, not postings.

```
health_workers   who the person is: NIN, names, sex, age, status
  └ deployments  one cadre, one location, one period — at most one open at a time
      └ cadres   VHT, CHEW, … each with its own placement_level
          └ cadre_categories   Community Health Workers, …
```

`health_workers` holds identity only. What a worker does and where is a `deployments`
row; a transfer or a promotion ends one row and opens another, so the register itself
answers "who was deployed at X on date D". A partial unique index,
`deployments_one_active_idx ON (health_worker_id) WHERE ended_on IS NULL`, allows one open
posting per worker — relaxing that later is dropping the index.

A deployment ends with a date and a reason together (`deployments_end_complete`), never
before it started (`deployments_dates_ordered`), and is never deleted.

### Cadres are data

`cadre_categories` holds the category (Community Health Workers); `cadres` the types within
it, each carrying the level it is placed at and the spellings an import may use:

| slug | label | `placement_level` | `import_aliases` |
|---|---|---|---|
| `vht` | Village Health Team member | village | `village health team` |
| `chew` | Community Health Extension Worker | parish | `chw`, `community health extension worker` |

A new cadre is an `INSERT`, not a migration. Each category owns its own profile surface —
`chw_profiles` and its junctions belong to the CHW category; a future one gets its own.

### Placement

`deployments_set_placement()` reads the required level from the posting's `cadres` row —
there is no `CASE` to edit — refuses a location at any other level, and derives
`district_id` by walking `path` to the district ancestor. It also refuses an open posting
for an inactive worker.

It fires on `INSERT OR UPDATE OF location_id, cadre_id, health_worker_id`. Including
`cadre_id` matters: re-cadring a VHT to CHEW without moving them would otherwise leave them
at the wrong level. Verified to reject.

One `location_id` rather than nullable `parish_id`/`village_id` columns, which could both
be set or both be empty.

### The district anchor

Scoping reads `health_workers.district_id`, which `deployments_sync_worker_district` copies
from each posting as it is written. It deliberately does **not** clear when a posting ends:
the last district still owns the record of a worker between postings or out of the
workforce, which a join against the active deployment could not express. It follows the
latest *written* posting, so a transfer must end the old row before opening the new one —
the order `Deployments.changeTx` uses.

`health_workers_check_deactivation` refuses to mark a worker inactive while a posting is
open; deactivation ends the posting first, in the same transaction.

Reads see a worker through one posting — the open one, else the most recent — via a
lateral join (`workerFrom` in `internal/store/workers.go`).

### Constraints carried from the ODK form

| Constraint | Rule | Source |
|---|---|---|
| `nin` | `^[A-Z]{2}[A-Z0-9]{11}[A-Z]$`, unique where present | form regex; field is optional |
| `age_years` | 18–99 | form: `. > 17 and . <= 99` |
| `households_served` | 3–100,000 | form: `. > 2 and . <= 100000` |
| `incentive_amount_ugx` | 1,000–500,000 | form constraint |
| phone columns | `^[0-9]{9}$` | form regex |

`nin` is nullable with a partial unique index: the form does not require it and labels it
"NIN / Alternative No". Soft duplicate detection where NIN is absent — the same name at
the same location — runs on `deployments_location_idx`, which covers open postings only.

### Indexes for browsing

| Index | Answers |
|---|---|
| `health_workers_name_sort_idx (lower(last_name), lower(first_name), id)` | the listing and its keyset comparison |
| `health_workers_district_idx (district_id)` | every scoped read |
| `health_workers_name_trgm` | name search, on the first and last name joined by a space |
| `health_workers_nin_prefix_idx (nin text_pattern_ops) WHERE nin IS NOT NULL` | `LIKE 'CM90%'` as an index scan; `health_workers_nin_uniq` only answers equality |
| `deployments_district_idx`, `deployments_location_idx` | open postings by district and by place |

`lower()` because the source data is inconsistently cased and a case-sensitive sort
interleaves the same surname three ways.

`age_captured_on` exists because age is a snapshot, not a fact. ODK provenance is stripped
by decision, so imports stamp the import date.

### Profile constraints

Three CHECKs encode branching that the form expressed as `relevant` conditions:

- `phone_branch_exclusive` — the form asks "do you own a phone?" then branches.
  `phone_primary` and `phone_alternate` are mutually exclusive, not two lines for one
  person.
- `incentive_details_require_yes` — no frequency or amount unless `receives_incentive`.
- `supervision_date_requires_yes` — no date unless `received_supervision`;
  `last_supervised_on` must be the first of a month, since only year and month are captured.

### Junctions

`chw_profiles` is 1:1 on the worker, not the posting: the survey answers — phones,
education, incentive — stay true across a transfer.

`chw_service_domains(health_worker_id, domain_id, provides, trained)` collapses what the form
modelled as two nested multi-selects over one 12-value vocabulary.
`trained_implies_provides` mirrors the form's `choice_filter`: a CHW cannot be trained on
a service they do not provide.

`chw_tools(health_worker_id, tool_id, functional)` puts functionality on the junction row, because
the form's `tool_functional` is choice-filtered to tools already held. A single global
flag could not say *which* tool is broken.

`chw_languages` holds parsed values; `chw_profiles.other_languages_raw` keeps the original
free text verbatim.

## Vocabularies

Closed lists are Postgres enums: `sex`, `worker_status`, `education_level`,
`incentive_frequency`, `user_role`, `user_status`, `location_level`. A closed list belongs
in the type system, where an invalid value cannot be inserted.

Cadres are **not** an enum: MoH will add cadres, and each carries a placement level, so
they are rows (`0003`). `tools` (7), `service_domains` (12) and `languages` are reference
tables for the same reason — junction targets MoH may extend — seeded in `0004`.

The source Tool list includes a member called `None`. It is deliberately absent from the
`tools` table — it means "no tools", which is the empty set, not a tool named None.

## Import staging

`0005` adds the tables a bulk upload passes through, the claim
(`import_batches.committing_at`) that keeps two commits off one batch, and
`import_quarantine`. See [import.md](import.md) for the flow they serve.

`import_batches` is one uploaded file: its name, its format, the uploader, and
**`district_id` — the uploader's scope at upload time**, `NULL` for a national one. Batches
are read through that column, so another district's report is `ErrNotFound`. It is
recorded rather than re-derived because a user's role can change afterwards and a batch's
reach cannot. `columns` keeps the header exactly as the file spelled it, in order, which
is what `errors.csv` is rebuilt from.

`import_rows` is one line: `raw` as it arrived, a verdict, the resolved `location_id`, the
`problems` array, `health_worker_id` once it becomes a record — and `record`, the resolved
register record the commit will write, as JSON, in worker, deployment and profile sections.

`record` exists because not every field is a pure function of its own cell. A facility is
resolved by name within the deployment's district, so re-deriving it at commit would answer from
a register that has moved since the report — a facility renamed in between would silently
change which one a worker reports to. `raw` answers "what did the file say"; `record` is what
was reviewed, and what is written.

Two constraints carry rules that would otherwise live only in Go:

| Constraint | Says |
|---|---|
| `import_batches_commit_complete` | a status and its timestamp cannot disagree — the shape `health_workers_deactivation_complete` already uses |
| `import_rows_refusal_explained` | a `rejected` or `failed` row must carry at least one problem. A refusal without a stated reason is the silent drop invariant 7 forbids |

`import_batches.district_id` gets the same trigger `users.district_id` has, refusing an id
that is not a district.

Two foreign keys are deliberately restrictive. `import_quarantine.batch_id` refuses the
deletion of a batch that produced quarantine rows, so no future pruning can destroy the
record of a refusal by tidying away the batch it came from. And
`import_rows.health_worker_id` means a worker cannot be deleted while an import row points at them — invariant 5 arriving from a
second direction.

## Verified rejections

`seed/verify_constraints.sql` probes the schema with 45 bad-data cases against a live
database carrying the full national hierarchy. **45 blocked, 0 leaked.** It also asserts
positive cases by construction: creating a worker with their first deployment, attaching a
posting to a facility in its own district, and ending a posting before deactivating must
all succeed, or the whole block aborts. It runs in a
transaction and rolls back, and raises if anything leaks — run it after any schema change.

| Group | Cases |
|---|---|
| Hierarchy ladder | region given a parent · district with no parent · county off a region · subcounty off a district · parish off a county · village off a subcounty · duplicate code under one parent |
| Cadre placement | VHT at parish · CHEW at village · VHT at subcounty · CHEW at district · re-cadre without moving |
| Deployments | a second open posting · ended without a reason · a reason without an end · ended before it started |
| Worker fields | malformed NIN · duplicate NIN · age below minimum · age above maximum · inactive without `deactivated_at` · active with `deactivated_at` · deactivated with an open posting · deployed while inactive |
| Users and RBAC | national admin with a district · national viewer with a district · district role without a district · `district_id` pointing at a village · duplicate email |
| Facilities | parented to a village · parented to a subcounty |
| Posting to facility | attached across districts · attached across districts at insert · moved to another district while attached |
| Profile branching | incentive amount without receiving · incentive above maximum · incentive below minimum · phone-owner given fallback number · non-owner given primary phone · malformed phone · households below minimum · supervision date without yes · supervision date mid-month |
| Junctions | trained on unoffered service · duplicate tool for one worker |
