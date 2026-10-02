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

**`district_id` denormalized onto `deployments` and `health_workers`.** Derivable from
`location_id`, but every RBAC-scoped query filters on it. Triggers own both columns so
placement and scope cannot drift. The worker's copy tracks the latest posting rather than
the open one, so a district still sees the workers who left it.

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

**Bulk import stages, and a human commits.** An upload writes nothing to the register. It
validates every row, stores the verdicts, and produces a report; a separate act imports
them. Re-reading the file at commit was rejected because the register moves between the
two requests — a NIN gets claimed, a location is deactivated — and what is committed has
to be the thing that was reviewed.

**Commit calls `Workers.CreateTx` once per row, inside a transaction it shares with the audit
row and the staged row's mark.** Bulk `INSERT` or `COPY` would be a second implementation
of the derived `district_id`, the post-insert scope check and invariant 6, and the second
implementation is the one that gets it wrong. Marking the row after the insert committed
was rejected too: a process killed in between leaves a worker whose import row still reads
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

**The administrative tier word is folded per level: redundant ones dropped, identifying
ones expanded, and at village neither.** A district's spreadsheet writes the tier beside
the name — `Romogi Sc`, `Mpigi T/C`, `Ludaracounty` — and refusing all of them cost real
rows: on a 31,462-row eCHIS export only 40% placed. Blanket suffix stripping was the
obvious fix and is wrong. The gazetteer holds 588 subcounties named `… TOWN COUNCIL` and
112 named `… DIVISION`, and `LUWEERO` sits beside `LUWEERO TOWN COUNCIL` in one county —
279 such pairs. Stripping the tier word merges them and files a CHW in the wrong one
silently, which is the single outcome this package exists to prevent. So a tier word is
dropped only where **no** canonical name at that level carries it, and otherwise expanded
to the gazetteer's spelling. At village nothing is folded: `CELL`, `ZONE`, `TC` and
`VILLAGE` all end real village names.

A collision count alone is not the test. Bare `TC` at subcounty collides with nothing —
canonical names spell `TOWN COUNCIL` out — yet stripping it would match `Mpigi T/C` to
`MPIGI`, a different subcounty. The rule is about what the word *means* at that level, and
the collision count only catches the half of the mistake that shows up as a merge.
`seed/verify_name_folding.sql` checks that half against the real hierarchy, because it is
a fact about the gazetteer rather than about Go, and `make verify` runs it. On the August
2026 hierarchy the safe fold takes the same export from 40% placed to 52%, and adds no
ambiguity at all.

**A name matching nothing is refused, and the refusal says where the name does exist.**
The alternative — resolving to the one place the name is found one tier up — is the same
silent guess that preferring a code over a contradicting name would be, and it would
quietly overrule the parish the file actually named. But `No village called Waibuga in
Kasonga.` cannot be told from a misspelling by the person who has to fix it. So the
resolver looks one tier wider purely to build the message and offers what it finds as
`candidates`, which the report already renders and which a `location_code` already
settles. The search radius widens; the match does not. Widening stops at the district, so
it can never name a location outside the uploader's scope — the same reason
`/api/locations` and the CHW form answer the way they do.

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

**A bad profile value refuses the whole row.** The profile form silently drops a value
posted into a branch its own JavaScript had hidden — a "no" to owning a phone arriving with
a number stores neither. That is right for a form, where the hidden field is a leftover,
and wrong for an import, where a district that wrote a phone number is owed either the
number or a reason. Every branch CHECK is pre-checked and reported instead. Rejected:
importing the CHW and dropping the field, which is the silent loss invariant 7 exists to
forbid.

**A row whose profile columns are all empty writes no `chw_profiles` row.** An all-null row
would claim the questions were asked and unanswered. It also keeps a plain register import
as fast as it was, since the profile write and its audit row are skipped entirely.

**The resolved record is stored on the staged row, not re-derived at commit.** Re-parsing
`raw` worked while every field was a pure function of its own cell; a facility is resolved
by name within the deployment's district, and re-resolving at commit would answer from a
register that has moved — a facility renamed between the report and the commit would
silently change which one a worker reports to. `import_rows.record` holds it, and "what is
committed is what was reviewed" stops being an argument.

**The export's columns are the import's columns.** A file that comes out of the register
goes back into it — verified by re-importing an exported row under a different name and
getting back its placement, phone, facility, education, both junction sets, and
`english_write` as a recorded false rather than a null. The columns an upload must not be
able to set — the id, the derived placement, the status, the timestamps — are present but
named as unknown on import and ignored. Rejected: a narrower export of only what the
importer reads, which would have made the file useless for the reading it is mostly for.

**The export streams and drops the cursor.** Paging is for a reader moving a page at a
time; an export of "page three" is a file nobody asked for. Rows go through a callback
flushed every 500, so a national export starts arriving at once and never exists in memory.
The cost is that a failure part-way through cannot become an error page — the status went
out with the first byte — so the file ends short and the log carries why, the same bargain
errors.csv makes.

**The register is of people; postings are rows.** The first schema was a CHW register:
`chws` carried the person, a `cadre` enum and a `location_id` on one row, and a transfer
overwrote the placement, so "who served at X on date D" was answerable only from audit
JSONB. It became three things. `health_workers` is the person. `deployments` is a posting
— one cadre, one location, one period, ended rather than deleted — with at most one open
per worker, enforced by a partial unique index so that allowing concurrent postings later
is dropping an index. `cadres` are rows in a two-level taxonomy under `cadre_categories`,
each carrying its own `placement_level` and `import_aliases`, so the next cadre is an
`INSERT` that the placement trigger, the form's cascade, the dashboard tiles and the
importer all pick up without a code change. Rejected: keeping `chws` and adding a
`deployments` history beside it, which would have left two answers to "where does this
worker serve"; and a single register table with a nullable column per future cadre's
attributes. Each category owns its own profile surface instead — `chw_profiles` and its
junctions belong to the CHW category.

The supervising facility moved from the profile onto the posting, because it is
location-bound: it now travels with a transfer inside a district and is left behind by one
across districts, instead of blocking the move.

**The migration sequence was rewritten, not appended to.** `0003_chws` … `0008_import_record`
were replaced by `0003_health_workers`, `0004_chw_profile` and `0005_imports`, against the
append-only rule: a history that created `chws` only to dismantle it three migrations
later would be the schema's most misleading page. The cost
is recorded below.

**The worker code is `KYE00042`: district, then serial, frozen for life.** It arrived as a
CHW code on `chws` and was carried onto `health_workers` when the register generalised —
one code per *person*, whatever their category, so a VHT who trains as a CHEW or a nurse
keeps the paper they already carry. `health_workers.id` is a surrogate key — correct, and unusable by a district officer reading a printed list at a
parish or saying a number down a phone. The register needed a second identifier a person
can carry. Three sub-decisions, each with a rejected alternative:

*No cadre letter.* The first draft was `VKYE00042` / `CKYE00042`, a `v`/`c` for VHT and
CHEW. Rejected because both encoded facts are mutable — a change of cadre is a new
deployment — and a two-valued field that is wrong is misleading rather than
merely vague. Either the letter is true or it should not be there. Dropping it also
collapses two counters per district into one.

*Frozen, never recomputed.* Rejected: recomputing the code on transfer or re-cadring, and
reissuing with the old code retained as an alias. An identifier that changes is not one —
it breaks paper already in the field and the join back through `audit_log` — and Uganda has
gone from 112 districts to 146 within living memory, so a single split would churn
thousands of codes at once. The code says where a worker *entered* the register, which stays
true; the deployment says where they are now.

*Five digits, not four and not base-32.* Four caps a district at 9,999 and the largest
already holds over 3,000 at partial coverage, with invariant 5 meaning a serial is never
released. Crockford base-32 was considered for compactness — it drops `I L O U` and folds
`O`→`0`, `I`/`L`→`1` on input — and rejected: it saves exactly one character over five
digits, needs an input-normalization rule and a check character to be safe, and still
leaves `5`/`S`, `2`/`Z`, `8`/`B` confusable. Its home is 128-bit random tokens, not a
counter that will spend its life under five figures. Digits also let an officer read the
serial as a count and check it against their own paperwork.

**Two letters of district was impossible, not merely tight.** 41 of the 146 districts begin
with K, so no scheme where the code starts with the district's own initial can give them
distinct second letters. Assigning arbitrary pairs is possible — 146 into 676 — but
produces `AA` for Kaabong and `ER` for Kaberamaido, at which point the code is a lookup
table and the official numeric district code in `locations.code` would have done. Three
letters gives every district a code starting with its own initial with room to curate:
`KYE` Kyenjojo, `KYG` Kyegegwa, and a code ends in `C` if and only if the district is a
city. The 146 are reviewed and checked in as `data/district_codes.tsv`, and carried into
`district_codes` by migration 0006 rather than by a seed step, because the trigger that
assigns codes cannot wait for a seed the way the hierarchy can.

**The listing's search box takes a code as well as a name.** This is not the NIN exclusion
in reverse. A NIN is a national identifier and searching by one lets the register be probed
with it; a worker code is issued by this register and exists to be looked up. `Scope` still
decides which rows come back. A code has a shape a name cannot, so `NormalizeWorkerCode`
recognises one and matches it exactly — forgiving case, spaces and the hyphen someone adds
to make it readable, because a code arrives copied off paper.

**The code is issued with the first deployment, not on insert.** `chws` carried its own
`location_id`, so a `BEFORE INSERT` trigger knew the district. `health_workers` does not:
its `district_id` is copied from the first deployment, a statement later in the same
transaction. So `health_workers_assign_code` issues the code the first time `district_id`
goes from NULL to a value, refuses a supplied or changed one, and the CHECK
`health_workers_code_assigned` makes "has a district, has no code" unrepresentable.

**Cadres are administered in the UI, and frozen once used.** Making cadres data (0003)
only helps if someone other than a migration author can add one, so `national_admin` holds
`cadre.manage` and `/cadres` adds and edits them. Rejected: letting district managers add
cadres, since the taxonomy is one national vocabulary and a district's addition would
appear in every other district's form. The price of people writing the table is that the
schema has to hold what a migration author knew — hence `cadres_freeze_trg`, which fixes a
cadre's slug, category and level from its first deployment. Rejected: re-validating every
posting when a cadre's level changes, which would turn one admin click into a
tens-of-thousands-row transaction that either fails or re-places people nobody reviewed.
Retire-and-replace moves workers one reviewed re-cadring at a time.

**The CHW profile is gated by category in the application, not the schema.** A
`chw_profiles` row for a nurse is wrong, but a worker re-cadred out of the CHW category
has answers that are still true of the time they served, and a constraint would force
deleting them. So the show page keeps existing answers read-only, the profile routes 404,
and the importer refuses (`profile_category`) rather than drops profile columns on a row in
another category.

**Placement depth is the cadre's, all the way down.** The form's cascade and the importer's
name walk both used to know two depths, village and parish. Both now stop at whatever level
the cadre row declares, the importer refusing a blank cell with a filled one beneath it as a
gap rather than reading it as a shallower placement.

## Known costs

**A database migrated under the old sequence does not upgrade.** Its `goose_db_version`
already reads 5 or more (9 with the CHW code), so the new `0003`–`0006` never run against
it — and goose would stop at the first unapplied lower version anyway. Such a database is dropped,
re-migrated and re-seeded — the path in [seeding.md](seeding.md). Its records can come
across through an export taken before the drop: the export's columns are unchanged and
`cadre` still carries `vht` / `chew`, so the old file imports. What does not come across is
anything an import cannot set — deactivations arrive as active workers, and the audit
history and supervision answers stay behind in the old database.

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

## The reviewer's target schema (October 2026)

A reviewer's target schema was adopted as binding: a `person` separate from the
`health_worker`, satellite tables for contacts, next of kin, documents, education,
courses, training, work history and languages; a generic dated questionnaire in place
of the fixed `chw_profiles` columns; services and tools with per-cadre applicability
and dated events; and `uuid`, `created_on/by`, `last_updated_on/by` on every table.
The seeded worker data was disposable, so the migration sequence was **rewritten**
(0001–0007) rather than appended to, and every database is rebuilt and re-seeded.

Interpretations, where the target and an invariant met:

**Plural table names stay** (`persons`, `deployments`, `health_workers`): the repo's
convention, and `health_worker_deployment` is `deployments`.

**District is a view over `locations`, not a table.** Every derived `district_id`, every
user's scope and every facility's parent is a `locations` row reached by a path walk,
which cannot cross into a second table. `districts` exposes it as an entity.

**`district_id` stays derived, never supplied** — on deployments, on service updates
(from the posting held on the reporting date), and refused when written directly. A tool
distribution's district is the event's own attribute, like a user's.

**Two statuses.** `health_workers.status` is the workforce (active/inactive, tied to the
open posting); `persons.status` is the record (active/deceased/merged).

**Birth date, not age.** The field forms collected an age; it is stored as the birth
date it implies, flagged `dob_estimated`, and the age is computed. Estimates are dated
1 July of the implied year.

**Survey answers stay survey answers.** What a CHW said about the tools they hold and
the services they give became `multi_value` questions of the `chw_baseline` profile, not
distributions or service reports: moving them there would invent events that never
happened. Distributions and service updates start empty. Phones, education and English
are facts about the person and moved to the person's satellites; English is graded per
skill, with the ODK multi-select's "speak/read/write" recorded as `basic` and an unnamed
skill as `none`.

**The generic option sets dropped earlier are back**, as the reviewer's questionnaire.
What the typed CHECKs guaranteed is guaranteed twice over: a trigger checks each answer
against its question as it is written (choice exists, value parses, in range, one answer
to a single question), a deferred trigger checks each submission at commit (branch
opened, subset within parent, `none` alone), and `domain.Profile.Check` says the same
first, by field. Answered questions are frozen like used cadres.

**Submissions are history.** Saving writes a new dated submission; responses are
append-only and submissions undeletable. Unchanged answers write nothing.

**`none` is option 0.** No rows is "not asked"; a multi-select needs a recorded empty
answer too, and the ODK list had one.

**Record columns are structural.** `stamp_row()` fills them from a transaction-local
actor that `store.begin` sets, so no statement can forget them — the same reasoning as
the audit trail. Logs (sessions, audit_log, counters, staged import rows) carry none.

**Deployment codes** are `<worker_code>-NN`, issued when the posting lands, never
changed — the target asked for a code on the posting, and this one says whose it is.

**Typos in the target, read as meant:** `tools_aplicable_cadre.service_id` is `tool_id`;
the second `health_worker_service_update_detail` is the tool distribution's detail table.

Open with the reviewer: the language grade scale (`none/basic/good/fluent` proposed);
whether a person can ever be more than one health worker (the UNIQUE is one line to
drop); and a source for facility codes (`facilities.code` waits for one).
