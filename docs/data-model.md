# Data model

Four migrations, applied in order, embedded in the binary and run by `goose` at startup.
All verified against PostgreSQL 18.

- `0001_locations.sql` — hierarchy, facilities, import quarantine
- `0002_users_auth.sql` — users, sessions, audit log
- `0003_chws.sql` — the register, profiles, multi-select junctions
- `0004_facilities_mfl.sql` — facility attributes from the MFL, CHW-to-facility district agreement

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

### CHW-to-facility

`chw_profiles.facility_id` is the supervising and reporting site — an optional attachment,
not a placement. Placement remains cadre-driven and untouched by it.

Two triggers keep the districts in agreement:

- `chw_profiles_facility_district_trg` refuses an attachment to a facility outside the
  CHW's district, on insert and on any change of `facility_id`
- `chws_facility_after_move_trg` refuses a move that would strand an existing attachment
  in the district the CHW just left

The second raises rather than clearing the column, so a transfer must reassign or clear the
facility in the same transaction. A silent null would be a data loss the audit log could
only report after the fact.

## Users, sessions, audit

`users` carries `role` and a nullable `district_id`, kept consistent by
`users_scope_matches_role` plus a trigger checking the level. See [rbac.md](rbac.md).

`sessions` stores `token_hash` (SHA-256) as its primary key. The raw cookie token is never
persisted.

`audit_log` records actor, action, entity, `before`/`after` JSONB, IP and timestamp, plus
a `district_id` so district admins can read their own slice. It doubles as CHW change
history — there is no separate versioning table.

## CHWs

`chws` holds identity only; optional survey answers live in `chw_profiles`; multi-valued
answers live in junctions.

### Placement

```
cadre = 'chew'  ->  location_id must be a parish
cadre = 'vht'   ->  location_id must be a village
```

One `location_id` rather than nullable `parish_id`/`village_id` columns, which could both
be set or both be empty. `chws_set_placement()` enforces the level and derives
`district_id` by walking `path` to the district ancestor.

The trigger fires on `INSERT OR UPDATE OF location_id, cadre`. Including `cadre` matters:
re-cadring a VHT to CHEW without moving them would otherwise leave them at the wrong
level. Verified to reject.

### Constraints carried from the ODK form

| Constraint | Rule | Source |
|---|---|---|
| `nin` | `^[A-Z]{2}[A-Z0-9]{11}[A-Z]$`, unique where present | form regex; field is optional |
| `age_years` | 18–99 | form: `. > 17 and . <= 99` |
| `households_served` | 3–100,000 | form: `. > 2 and . <= 100000` |
| `incentive_amount_ugx` | 1,000–500,000 | form constraint |
| phone columns | `^[0-9]{9}$` | form regex |

`nin` is nullable with a partial unique index: the form does not require it and labels it
"NIN / Alternative No". `chws_dup_probe_idx` on `(location_id, lower(last_name),
lower(first_name))` supports soft duplicate detection where NIN is absent.

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

`chw_service_domains(chw_id, domain_id, provides, trained)` collapses what the form
modelled as two nested multi-selects over one 12-value vocabulary.
`trained_implies_provides` mirrors the form's `choice_filter`: a CHW cannot be trained on
a service they do not provide.

`chw_tools(chw_id, tool_id, functional)` puts functionality on the junction row, because
the form's `tool_functional` is choice-filtered to tools already held. A single global
flag could not say *which* tool is broken.

`chw_languages` holds parsed values; `chw_profiles.other_languages_raw` keeps the original
free text verbatim.

## Vocabularies

Closed lists are Postgres enums: `cadre`, `sex`, `chw_status`, `education_level`,
`incentive_frequency`, `user_role`, `user_status`, `location_level`. A closed two-value
list belongs in the type system, where an invalid value cannot be inserted.

Only `tools` (7) and `service_domains` (12) are reference tables, because they are junction
targets and MoH may extend them. Both are seeded in `0003`.

The source Tool list includes a member called `None`. It is deliberately absent from the
`tools` table — it means "no tools", which is the empty set, not a tool named None.

## Verified rejections

`seed/verify_constraints.sql` probes the schema with 39 bad-data cases against a live
database carrying the full national hierarchy. **39 blocked, 0 leaked.** It also asserts
one positive case by construction: attaching a CHW to a facility in their own district must
succeed, or the whole block aborts. It runs in a
transaction and rolls back, and raises if anything leaks — run it after any schema change.

| Group | Cases |
|---|---|
| Hierarchy ladder | region given a parent · district with no parent · county off a region · subcounty off a district · parish off a county · village off a subcounty · duplicate code under one parent |
| Cadre placement | VHT at parish · CHEW at village · CHW at subcounty · CHW at district · re-cadre without moving |
| CHW fields | malformed NIN · duplicate NIN · age below minimum · age above maximum · inactive without `deactivated_at` · active with `deactivated_at` |
| Users and RBAC | national admin with a district · national viewer with a district · district role without a district · `district_id` pointing at a village · duplicate email |
| Facilities | parented to a village · parented to a subcounty |
| CHW to facility | attached to another district's facility · reattached across districts · moved to another district while attached |
| Profile branching | incentive amount without receiving · incentive above maximum · incentive below minimum · phone-owner given fallback number · non-owner given primary phone · malformed phone · households below minimum · supervision date without yes · supervision date mid-month |
| Junctions | trained on unoffered service · duplicate tool for one CHW |
