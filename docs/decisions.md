# Decision log

Decisions taken, with the options rejected and why. Where a decision has a known cost, the
cost is recorded rather than smoothed over.

## Confirmed by the project owner

| # | Decision | Rejected alternative |
|---|---|---|
| 1 | Local email + argon2id passwords, Postgres sessions | reusing the `dhi/sso` service; an SSO-ready abstraction |
| 2 | Bulk CSV/ODK import, then maintenance in the UI | manual entry only |
| 3 | NIN optional, unique where present | NIN required and unique; NIN as a plain attribute |
| 4 | User provisioning by `national_admin` only | district managers provisioning their own district |
| 5 | Cadre single-valued `vht` / `chew`, no "other" | the form's `select_multiple ... or_other` |
| 6 | CHEWs at parish level, VHTs at village level | a single village-level placement for all cadres |
| 7 | Supervision as year + month | per-domain flags as the form collects them |
| 8 | ODK provenance stripped entirely | keeping `today` + collector; keeping all metadata |
| 9 | GPS not captured | lat/lon/accuracy columns; PostGIS geography |
| 10 | Hierarchy from the admin units workbook, districts downward | the ODK choices sheet |
| 11 | Facilities from the Master Facility List, parented to district | the ODK workbook's facility list; parenting to subcounty |
| 12 | CHW-to-facility as an optional attachment on the profile | facility as the CHW's placement; a required field |

Decisions 7, 8 and 9 have a recorded cost — see **Known costs** below.

## Technical

**One locations table, not six.** Six level-specific tables would need six joins for an
ancestor query and could not express "everything under district X" in one indexed scan.
A materialized `path` with `text_pattern_ops` makes subtree queries prefix scans.

**County retained despite no UI surface.** Subcounty codes are unique only within a
county. Dropping the tier merges 2,198 subcounties into 1,466 and 10,716 parishes into
9,807. The ODK form dropped it and lost ~6% of villages as a result.

**`(parent_id, code)` as identity, not name.** Names are not unique among siblings —
verified: one parish holds two villages named `BUHOBA A`. An earlier check reported zero
collisions, but that check was wrong: it deduplicated by `(parent, name)` before counting,
so it could only ever return zero. The load failed on the real constraint, which is how
the defect surfaced.

**`district_id` denormalized onto `chws`.** Derivable from `location_id`, but every
RBAC-scoped query filters on it. A trigger owns the column so placement and scope cannot
drift.

**One `location_id`, not `parish_id` + `village_id`.** Two nullable columns could both be
set or both be empty. One column plus a cadre-dependent trigger makes the invalid states
unrepresentable.

**Enums for closed lists, tables for extensible ones.** `cadre`, `sex`, `education_level`,
`incentive_frequency` and the rest are Postgres enums — a closed two-value list belongs in
the type system. Only `tools` and `service_domains` are reference tables, because they are
junction targets MoH may extend. An earlier plan for five generic "option sets" was
dropped as over-general.

**One junction with flags, not three tables.** The form's `Service_domains` / `training` /
`support_supervision` are three nested multi-selects over one vocabulary, so they collapse
to `chw_service_domains(provides, trained)` with a CHECK mirroring the form's
`choice_filter`.

**Functionality on the tool junction row.** `tool_functional` is choice-filtered to tools
already held, so a single global flag could not say which tool is broken.

**Server-side sessions, not JWT.** Staff departures require instant revocation. A JWT
remains valid until expiry unless a denylist is maintained, which reintroduces the state
JWTs were meant to avoid.

**`audit_log` doubles as change history.** Before/after JSONB on every mutation makes a
separate versioning table unnecessary in v1.

**No `chw_assessments` snapshot table in v1.** Most optional attributes are survey answers
at a point in time and will drift. A snapshot table is real complexity and there may never
be a second survey round. `chw_profiles.updated_at` plus the audit log make history
recoverable, and the audit log could backfill snapshots later. The trigger to build it is
a second survey round being scheduled — not before.

**Facilities parented to district, not subcounty.** The MFL's `subcounty` column resolves
against the hierarchy for 3,696 of 7,907 rows; 4,201 miss and 10 are ambiguous, because the
column holds Town Councils, City Divisions and newer units the admin-units file does not
carry under those names. District resolves for all 7,907 with the two aliases the hierarchy
loader already needs. Parenting deeper would quarantine over half the register to gain a
tier nothing queries. The raw label is kept in `facilities.subcounty_label`, unresolved, so
a later reconciliation has something to work from.

**CHW-to-facility is an attachment, not a placement.** `chw_profiles.facility_id` records
the supervising and reporting site; placement stays cadre-driven (CHEW > parish, VHT >
village) and is unaffected. Making the facility the placement was rejected: it would put
CHWs at whatever tier the MFL happens to resolve to, and 47% of the list cannot resolve
below district at all.

**The facility must be in the CHW's own district.** Enforced by trigger in both directions
— a cross-district attach, and a transfer that would strand an existing one. The transfer
case raises rather than nulling the column, so reassignment is an explicit act by the
operator instead of a silent loss the audit log would have to explain.

**`facilities.level` is free text, not an enum.** The workbook already carries 17 distinct
values including 18 blanks, `RRH`, `NRH`, `RBB`, `NBB`, `BCDP` and one row labelled `Bank of
Uganda`. An enum would mean a migration every time the MFL gains a category, for a column
the register only filters on. `ownership` and `authority` are text for the same reason.

**The whole MFL is loaded, the picker filters.** Only 3,389 of 7,895 facilities are
government-owned; the rest are private clinics, drug shops and PFP sites CHWs do not report
to. Loading only GOV rows would silently drop facilities a district may legitimately name,
so everything loads and `facilities (district_id, ownership)` is indexed for the picker.

**Quarantine over silent truncation.** A half-loaded register is worse than a rejected
row, because nobody knows what is missing.

## Known costs

**`last_supervised_on` is NULL on every imported row.** The form records supervision per
service domain and carries no date, so decision 7 cannot be sourced from the export. The
column fills in only through the web UI, and the form's `support_supervision` answers are
discarded.

**Age staleness is only approximately known.** Decision 8 drops `today`, so
`age_captured_on` falls back to the import date rather than the collection date.

**The MFL carries no facility code, so identity is `(district_id, name)`.** Twelve rows
collide: five are the same row listed twice, seven are genuinely different premises
separated only by the subcounty this loader does not resolve — `Chinese Clinic` exists in
both Makindye and Nakawa divisions of Kampala. The lowest workbook row loads and the rest
are quarantined. All twelve are private clinics or drug shops, so no facility a CHW would
attach to is affected, but the collision is unresolvable without a code column.

**4,201 facilities have an unresolved subcounty label.** Accepted as the cost of decision
11; the raw text is retained, not discarded.

## Open

**The ODK form's location lists are now wrong** relative to the admin units source —
missing the county tier, 208 subcounties short, ~6% of villages unreachable. The registry
should become authoritative and the form regenerated from it. Roadmap item, not a blocker.

**`phone_for_reporting` retention.** Flagged for possible removal; kept for now as a
single boolean.
