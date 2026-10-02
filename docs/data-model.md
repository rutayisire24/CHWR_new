# Data model

Seven migrations, applied in order, embedded in the binary and run by `goose` at startup.
All verified against PostgreSQL 18.

- `0001_locations.sql` — hierarchy, facilities (with an external `code`)
- `0002_users_auth.sql` — users, sessions, audit log; the record columns (`stamp_row`,
  `record_columns`) and the `districts` view
- `0003_persons.sql` — the person and their satellites, `identifier_types`, `languages`
- `0004_health_workers.sql` — cadres, health workers, deployments (with codes), worker
  codes, and the triggers that tie them together
- `0005_services_tools.sql` — services, tools, per-cadre applicability, service updates,
  tool distributions
- `0006_questionnaire.sql` — the questionnaire engine and the `chw_baseline` survey
- `0007_imports.sql` — import staging, the commit claim, the resolved record, quarantine

These seven replaced the earlier 0001–0008 when the reviewer's target schema was adopted
(see [decisions.md](decisions.md)). A database migrated under the old sequence does not
upgrade in place; it is rebuilt and re-seeded.

## Record columns

Every record table ends with the same five columns: `uuid` (unique, defaulted, kept for
exchange with other systems — joins stay on the bigint `id`), `created_on`, `created_by`,
`last_updated_on`, `last_updated_by`. `record_columns(regclass)` adds them and two
triggers; `stamp_row()` fills them from `current_setting('hwr.actor_id', true)`, which
`store.begin`/`store.ActAs` set per transaction. `uuid`, `created_on` and `created_by` are
refused if an update changes them. The update trigger fires only when the row changed.
Logs carry none of this: `sessions`, `audit_log`, `worker_code_counters`, `import_rows`.

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

A new cadre is an `INSERT`, not a migration — made by a national admin at `/cadres`, which
writes it and its `audit_log` row in one transaction. Since people now write the table,
0007 puts the rules a migration author kept in their head into the schema:

| Constraint | Refuses |
|---|---|
| `cadres_placement_selectable` | a cadre at region or county: region has no district ancestor and county is never selected, so nobody could be deployed in it |
| `cadres_slug_shape`, `cadre_categories_slug_shape` | a slug that would not survive a URL, a staged import record or the export unescaped |
| `cadres_slug_uniq` | one slug in two categories: the filter, export and importer name a cadre by slug alone |
| `cadres_freeze_trg` | a change of slug, category or placement level once any deployment names the cadre |

The freeze exists because `deployments_set_placement` checks a posting when the posting
changes, not when its cadre does: moving VHTs to parish level after they are deployed
would leave every one of those rows violating invariant 1 with nothing to notice. Label,
aliases, sort order and `active` stay editable. A retired cadre is no longer offered or
imported; the workers serving in it keep it until they are re-cadred.

Which survey a cadre answers is data too: `profile_applicable_cadre`. A worker re-cadred
out of a survey's cadres keeps the submissions they made, readable but no longer edited.

### The worker code

`worker_code` is the register's human-legible identifier: three letters of district, five
digits of serial, e.g. `KYE00042` for the 42nd health worker registered in Kyenjojo.
`health_workers.id` remains the key everything joins on; this is the one a person reads off
a printed list or says down a phone. It sits on the person, not the posting or the
category, so every cadre shares one sequence per district.

| Piece | Source | Mutability |
|---|---|---|
| `KYE` | `district_codes.abbr`, keyed by the official numeric district code | curated, checked in as `data/district_codes.tsv` |
| `00042` | `worker_code_counters.last_serial` for that district | issued once, never reused |

A worker's district is not known when their row is inserted: `district_id` is copied from
the first deployment by `deployments_sync_worker_district`, a statement later in the same
transaction. So `health_workers_assign_code()` fires `BEFORE INSERT OR UPDATE OF
worker_code, district_id` and does three things: refuses a code supplied on insert or
before placement; refuses any change to a code once issued; and, the first time
`district_id` goes from NULL to a value, takes the serial from a single upsert (`ON
CONFLICT DO UPDATE ... RETURNING`) so concurrent registrations in one district serialize on
that row and those in different districts do not touch each other. A district with no code
is refused rather than invented. The CHECK `health_workers_code_assigned` makes "has a
district, has no code" unrepresentable, so the window without a code is exactly the one
without a district.

The code is permanent: a transfer, a re-cadring and a district split all leave it alone,
because an identifier that changes is not one. The refusals and the district-code shape
constraints are cases in `seed/verify_constraints.sql`.

`district_codes` is populated by migration 0006 rather than by a seed step, because
`locations` is empty when migrations run on a fresh database and the trigger cannot wait.
That is why it is keyed by `district_code text` and not by `locations.id`. A district
created after 0006 needs a row added before workers can be registered there — the refusal
is loud on purpose; the alternative is a register carrying two ID schemes.

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

Nothing else writes either district column (0008). On `deployments`, an AFTER trigger on
`UPDATE OF district_id` refuses any value the location's path does not give — 0003's
placement trigger re-derives only when the placement changes, so a statement naming
`district_id` alone used to pass, and the sync then carried it onto the worker. On
`health_workers`, a write to `district_id` made at trigger depth 1 — a statement issued
directly rather than by the sync trigger — is refused on insert and on update.

`health_workers_check_deactivation` refuses to mark a worker inactive while a posting is
open; deactivation ends the posting first, in the same transaction.

Reads see a worker through one posting — the open one, else the most recent — via a
lateral join (`workerFrom` in `internal/store/workers.go`).

### Constraints carried from the ODK form

| Constraint | Rule | Where |
|---|---|---|
| `persons.nin` | `^[A-Z]{2}[A-Z0-9]{11}[A-Z]$`, unique where present | CHECK and partial unique index |
| age | 18–99 | `domain.ValidAge`, on the age the birth date gives (a CHECK cannot read today) |
| `households_served` | 3–100,000 | the question's `min_value`/`max_value` |
| `incentive_amount_ugx` | 1,000–500,000 | the question's bounds |
| phone numbers | `^[0-9]{9}$` | `person_contacts_phone_shape` |

Soft duplicate detection where NIN is absent — the same name at the same location —
runs on `deployments_location_idx`, which covers open postings only.

### Indexes for browsing

| Index | Answers |
|---|---|
| `persons_name_sort_idx (lower(last_name), lower(first_name), id)` | the listing's order; the keyset's last column is the worker id |
| `health_workers_district_idx (district_id)` | every scoped read |
| `persons_name_trgm` | name search, on the first and last name joined by a space |
| `health_workers_code_uniq (worker_code)` | the search box's exact match on a code |
| `deployments_district_idx`, `deployments_location_idx` | open postings by district and by place |

## The person and their details

`persons` holds `nin`, `first_name`, `last_name`, `other_name`, `dob`, `dob_estimated`,
`sex`, and a record `status` (active/deceased/merged). An age from a form or a file is
stored as the birth date it implies (1 July of that year) with `dob_estimated`; the age is
always computed. `health_workers.person_id` is NOT NULL UNIQUE — one worker per person,
until the UNIQUE is dropped.

| Table | Holds | Notable rules |
|---|---|---|
| `person_contacts` | phone / email / address; `owned`, `for_reporting`, `is_primary` | phone shape, email shape, ownership only on a phone, one primary per kind |
| `person_kins` | name, relationship, phone, `is_emergency` | |
| `person_ids` | `identifier_type_id`, `number` | one of a kind per person, one person per document |
| `person_education` | `education_level`, institution, qualification, year | |
| `person_courses`, `person_training`, `person_workhistory` | dated history | end not before start |
| `person_languages` | per language: `understanding_grade`, `reading_grade`, `writing_grade` | `proficiency` enum; NULL is not asked, `none` is cannot |

## The questionnaire

| Table | Role |
|---|---|
| `response_types` | `single_value`, `multi_value` |
| `value_types` | `open`, `closed` |
| `value_data_types` | `text`, `boolean`, `yes_no`, `integer`, `numeric`, `character`, `date`, `month` |
| `question_pool` | a question: prompt, help, the three types, `response_options` `[{id, code, prompt, aliases?}]`, min/max, pattern, active |
| `profiles` | a survey: code, name, description, active |
| `profile_applicable_cadre` | which cadres answer it |
| `profile_questions` | a profile's question: code, order, required, `depends_on_id` + `depends_on_option`, `subset_of_id` |
| `health_worker_profiles` | one dated submission (`captured_on`, `source` form/import) |
| `health_worker_profile_responses` | one answer: `response_option_id` for a closed question, `response` text for an open one |

Rules, by trigger: a question is sound (closed has choices, ids and codes unique, `none`
is option 0); a branch and a subset point into the same profile at a question of the
right shape; each response matches its question (choice exists, value parses as the data
type, in range, pattern, one answer for a single question); at commit (deferred) a
branch's answer has its opener, a subset's answer is among its parent's, and `none` is
alone. Responses cannot be updated or deleted; submissions cannot be deleted or moved; a
submission's profile must apply to the worker's cadre. An answered question's code, types,
range and existing choices are frozen; choices may be added.

"Not asked" is no response row. The CHW survey is `chw_baseline`, 14 questions, applying
to VHT and CHEW; its tools and services questions take their choices from `tools` and
`services`.

## Services and tools

`services` (12) and `tools` (7) are vocabularies with `service_applicable_cadre` and
`tool_applicable_cadre`. Events:

- `health_worker_service_updates(health_worker_id, reporting_date)` — unique per worker and
  date; `deployment_id` and `district_id` derived from the posting held on that date
  (`deployment_on()`); no future dates. `health_worker_service_update_details` names the
  services; each must apply to the cadre held then.
- `health_worker_tool_distributions(district_id, reporting_date, note)` — a hand-out in a
  district. `health_worker_tool_distribution_details(worker, tool, quantity)`: the worker
  must have been posted in that district on that date, the tool must apply to their cadre
  then. A distribution with recipients cannot move district or date.

## Vocabularies

Closed lists are Postgres enums: `sex`, `person_status`, `worker_status`,
`education_level`, `contact_kind`, `proficiency`, `user_role`, `user_status`,
`location_level`. Extensible lists are tables: cadres and categories, `identifier_types`,
`languages`, `services`, `tools`, and the questionnaire's own.

## Import staging

`0007` adds the tables a bulk upload passes through, the claim
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

`seed/verify_constraints.sql` probes the schema with 108 bad-data cases against a live
database carrying the full national hierarchy. **108 blocked, 0 leaked.** It also asserts
positive cases by construction — the record columns carry the actor, the first posting
issues the worker code and `<code>-01`, a facility in the posting's own district attaches,
a baseline submission with a branch and a subset saves, a service update and a
distribution land in the worker's own district — and the block aborts if any fails. It
runs in a transaction (with `SET CONSTRAINTS ALL IMMEDIATE`, so the questionnaire's
deferred rules fire per statement) and rolls back. Run it after any schema change.

Groups: hierarchy ladder; record columns; cadre placement; deployments and their codes;
person fields; satellites; the worker; users and RBAC; facilities; posting to facility;
each answer against its question; the submission as a whole; history not rewritten; a
sound question; service updates and distributions; worker codes and district codes; the
cadre taxonomy; `district_id` derived; import refusals.
