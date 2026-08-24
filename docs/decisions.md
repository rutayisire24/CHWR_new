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

**Two session deadlines, not one.** A session dies 12 hours after issue or 2 hours idle.
The absolute deadline bounds a stolen cookie's usefulness; the idle one covers the real
failure mode in a district office, which is a shared machine left signed in. A single
long expiry would have to choose between the two.

**Length over composition in the password policy.** Twelve characters mixing letters with
one digit or symbol. Character-class rules produce `Password1!` — they push staff toward
predictable substitutions while feeling strict. The admin-facing forms suggest a random
password so the path of least resistance is a strong one.

**argon2id at 64 MiB, t=3, p=4.** RFC 9106's second recommended profile. Hashes carry
their own parameters in the PHC string, so raising the cost later re-hashes on next login
instead of invalidating stored passwords.

**A failed login pays for a hash it does not need.** An unknown email is verified against
a throwaway hash generated at startup, so timing does not separate "no such account" from
"wrong password". The registry's users are named public servants; an enumerable login
form is a staff list.

**CSRF as a double-submit cookie, not a session-stored token.** The token lives in an
HttpOnly cookie and in a hidden field on every mutating form. A cross-site post can reach
the endpoint but cannot read the cookie to fill the field. It needs no per-session storage
and no extra round trip, and the token is rotated at login and logout so one captured
before authentication cannot be replayed after it.

**Duplicate names warn, duplicate NINs refuse.** NIN is optional, so the partial unique
index cannot catch a double entry of someone without one. `chws_dup_probe_idx` backs a
soft probe on (location, name) that shows the matching records and proceeds on a second
submit. Blocking would be wrong: two people in one village genuinely can share a name,
and the register would rather hold a duplicate than lose a real CHW.

**Deactivation demands a reason, in the handler.** The schema only insists that `status`
and `deactivated_at` agree — a reason cannot be a CHECK without forbidding the NULL that
active rows need. A deactivation with no stated reason is unauditable a year later, so
the handler requires one.

**`age_captured_on` is re-stamped only when the age changes.** Age is a snapshot, not a
fact. Re-stamping on every save would claim a fresh snapshot for a record whose age
nobody re-asked about, which is worse than a date that is honestly old.

**Only national roles move a CHW between districts.** A district manager editing a CHW is
scoped in twice: the update will not match a CHW outside their district, and a placement
that would carry one out is rolled back. A transfer is a two-district event, and the
receiving district is not theirs to decide.

**Keyset pagination, not offset.** The register runs to tens of thousands of rows. An
offset makes every page slower than the last, and — worse — a record inserted mid-browse
shifts every subsequent page by one, so a clerk paging through a district silently skips
somebody. The cursor is the sort key itself, so it stays valid as rows appear and
disappear around it. The cost is that there are no page numbers, only next and previous.

**One search box, not a name field and a NIN field.** Whether the input is a NIN is
decided by its shape: two leading letters, digits, no spaces or punctuation. A radio
button asking users to classify their own input is a button they get wrong, and the two
kinds of input are trivially distinguishable.

**"Not asked" is a third answer, everywhere.** Every `chw_profiles` column is nullable
because the ODK export answers none of them. Yes/no questions are three radios and `*bool`
in Go, so an unanswered question survives as unanswered. Collapsing it to false would
turn a record nobody has surveyed into a record that answered no to everything, which is
a fabrication the register would then report on.

**Hidden branches are disabled, not just hidden.** A hidden field still submits its value.
Disabling it is what makes the JavaScript agree with `phone_branch_exclusive` and
`incentive_details_require_yes` rather than merely look like it does. The handler
re-derives every branch anyway; the client-side half is for the operator, not for the
data.

**A tampered profile post is corrected, not rejected.** Training on an unoffered service,
an incentive amount with a "no" — the only way to produce these is to bypass the form, and
a field message about a combination the user never saw would mean nothing. They are read
the safe way and stored, with the CHECK still behind them.

**Junction sets are replaced on save.** `chw_tools` and `chw_service_domains` answer
multi-selects, so the submitted set is the new state. Diffing would add a way for the form
and the table to disagree, to gain nothing.

**The first admin is a flag, not a migration.** `-create-admin` provisions one account
and prints a one-use password. A seeded default admin in a migration is a known password
in a public repository; a bootstrap web route is an unauthenticated privilege escalation
for as long as somebody forgets to remove it.

**No `chw_assessments` snapshot table in v1.** Most optional attributes are survey answers
at a point in time and will drift. A snapshot table is real complexity and there may never
be a second survey round. `chw_profiles.updated_at` plus the audit log make history
recoverable, and the audit log could backfill snapshots later. The trigger to build it is
a second survey round being scheduled — not before.

**The district-to-region map is checked in, not extracted.** Regions were originally read
out of the ODK workbook's `choices` sheet, which meant a clean clone could not rebuild the
hierarchy without a file the repository does not carry. `data/district_region.tsv` replaces
it. The two were compared before the switch: identical on all 146 districts and all 15
regions, differing only in row order.

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

**The dashboard changes tier with the scope, rather than showing the country greyed out.**
A district manager sees the same charts grouped by subcounty and parish. Showing them the
national region chart with 14 bars they cannot open would be a menu of things they are not
allowed to have; showing only their own bar would be a one-bar chart. Rejected: a single
national dashboard gated behind the national roles, which leaves district managers with no
overview at all.

**Completeness measures are not dashboard tiles.** The tiles count the register — CHWs,
VHTs, CHEWs, areas reached. A share like "20% of records carry a NIN" or "0.1%
supervised" is a fact about how filled-in the register is, not about community health
workers, and at tile size it loses the framing that says so. They live in the Record
completeness chart instead, which states its own rule ("a recorded no counts; a field
nobody was asked does not") and shows the whole row of fields together, so a low bar reads
as a gap in capture rather than a finding about the field. Supervision was considered for
a tile and rejected on the same ground plus a harder one: it is NULL on every imported row
by construction (decision 7 — the form carries no supervision date), so the tile would
read 0% indefinitely and be read as an operational failure rather than an unasked
question. Rejected: keeping the NIN tile and adding a supervision one beside it, which
would have made two of four tiles measure paperwork rather than people.

**Chart.js is vendored, not linked.** 208 KB in `internal/web/static/vendor/`, MIT,
embedded in the binary like every other asset. A CDN would be a second origin the CSP
would have to admit and a runtime dependency on somebody else's uptime, in a service that
otherwise ships as one file. Rejected: hand-rolled SVG charts — the tooltip, hit-testing
and axis work is exactly the part a library has already done, and the vendored file costs
nothing at runtime.

**Chart figures travel in a JSON data block, not an inline script.** The CSP has no
`unsafe-inline` and is not going to acquire one to draw a chart. A
`<script type="application/json">` is never executed, so it is not gated by `script-src`;
`json.Marshal` escapes `<`, which is what makes it safe to put arbitrary register data
there. The same policy rules out the `style` attribute, so the meters and the inline table
bars are SVG, where a data-driven `width` is a presentational attribute.

**Every chart ships a server-rendered table of its own numbers.** In a `<details>` under
the canvas. It is the accessibility twin — nothing is reachable only by hover or only by
colour — and it means the dashboard degrades to a readable page with JavaScript off,
which the rest of the site already does.

**Charts carry at most two colours, and never a status colour.** The register's own
palette already spends green on "this CHW is active". A series in that green would take
the meaning back. Magnitude comparisons are one hue; whole-and-part is two steps of one
hue; the only two-identity charts are sex (blue/orange) and active/inactive, which is
emphasis — one hue plus the de-emphasis grey — rather than two identities.

**Bulk import stages, and a human commits.** An upload writes nothing to `chws`. It
validates every row, stores the verdicts, and produces a report; a separate act imports
them. Re-reading the file at commit was rejected because the register moves between the
two requests — a NIN gets claimed, a location is deactivated — and what is committed has
to be the thing that was reviewed.

**Commit calls `CHWs.CreateTx` once per row, inside a transaction it shares with the audit
row and the staged row's mark.** Bulk `INSERT` or `COPY` would be a second implementation
of the derived `district_id`, the post-insert scope check and invariant 6, and the second
implementation is the one that gets it wrong. Marking the row after the insert committed
was rejected too: a process killed in between leaves a CHW whose import row still reads
`ready`, and the next attempt creates them twice. The cost is measured and accepted — 3.2
ms a row, 32 seconds at the 10,000-row cap. All-or-nothing over that many rows was
rejected because one lost race would discard a correct nine-thousand-row import.

**A batch is claimed before it is committed.** Not a nicety: two concurrent runs both read
the same page of `ready` rows before either marks them, and a double-submitted 1,200-row
file was measured creating 2,033 records. `committing_at` is a lease rather than a flag, so
a process killed mid-commit does not wedge the batch.

**A row naming another district is refused, not relocated.** Silently rewriting it to the
uploader's own district would turn a data error into an invisible permanent one. Refusing
the whole file was also rejected: one stray line should not block a three-thousand-row
upload. The refusal names neither the district nor the location it matched, because a
district user must not be able to map the country by probing names.

**`location_code` decides the placement, and a contradicting name column is a refusal.**
Migration 0001 already settles which channel is authoritative — code is the identity, name
is a label — but when the two disagree one of them is wrong and nothing in the file says
which. Preferring the code quietly would file a CHW somewhere no human confirmed. The cost
is a renamed location: a file carrying last year's name with a correct code is refused
though it was right, and that case is indistinguishable from a wrong code.

**Location names are matched by dropping every separator, and nothing fuzzier.** Collapsing
whitespace instead matched "Kanu East" to `KANU EAST` and still missed `KANU-EAST`, which
is the spelling the workbook uses. Over-matching is bounded because a fold that hits two
siblings is an ambiguity — quarantined with both candidates — so it produces a question,
never a silently wrong answer. Trigram matching across 71,207 villages was rejected: it
would answer confidently and wrongly, and a CHW filed under the wrong village is not an
error anyone notices.

**Excel is read by `excelize`, pinned to v2.9.1.** A hand-rolled reader over `archive/zip`
is about two hundred lines and was rejected: it would be a second implementation of a
format whose edge cases — styles, dates as serials, merged cells, scientific notation in a
numeric cell — are exactly where a wrong answer looks like a right one. v2.11 requires Go
1.25, and a library choice should not bump the project's Go version as a side effect. Only
the first worksheet is read; guessing which sheet was meant is the kind of guess that
reads as a right answer.

**Nothing sweeps a staged batch.** A pending batch's rows are the only copy there is,
because refusals reach `import_quarantine` at commit; deleting one would destroy the only
record that an upload was attempted and refused. A committed batch's rows are the most
redundant data in the system. A timed sweep was written into the design and removed before
it was built, on those grounds.

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
