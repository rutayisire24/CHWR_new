# Bulk import

**Built and verified.** The register record and every optional survey attribute import
from CSV and Excel, through the UI, with per-row refusals and a scoped report. One thing
named here is not built: the scoped CSV export that finishes phase 6.

Every record on the register so far was typed one at a time. Districts hold their CHW
lists in spreadsheets and ODK exports, and the register is not usable until those get in.
This is how they get in without a half-loaded register on the other side.

[data-model.md](data-model.md) covers the schema this writes into, [rbac.md](rbac.md) the
scope rules it obeys, and [odk-mapping.md](odk-mapping.md) the field semantics the column
vocabulary follows.

## Shape

```
internal/importer/      parse, validate, resolve — no SQL of its own
  columns.go    the column vocabulary, the header matcher, the blank template
  read.go       encoding/csv and excelize, both to one Row shape; the limits
  resolve.go    district > subcounty > parish > village, by name within parent
  validate.go   one row → Record + []Problem, and the cross-file NIN pass
  profile.go    the optional attributes: the branches, the two nested lists
internal/domain/import.go     Batch, ImportRow, Problem, the status and code vocabularies
internal/domain/parse.go      the field rules the CHW form and the importer share
internal/store/imports.go     batches, staged rows, the commit claim, quarantine writes
internal/http/imports.go      the routes, the store adapter, the commit runner
internal/web/templates/       imports.html, import_report.html
migrations/0006_imports.sql   staging tables
migrations/0007_import_lease.sql   the commit claim
migrations/0008_import_record.sql  the resolved record on a staged row
docs/import.md                this file
```

The layering is the register's own: `importer` decides whether a row is acceptable and
what it means, `store` writes it, `http` renders the verdict. The importer holds no SQL —
location resolution reaches the database through a small interface that `store.Locations`
satisfies, the same arrangement that keeps `auth` from importing `store`.

## Formats

CSV via `encoding/csv`, and `.xlsx` / `.xlsm` via `github.com/xuri/excelize/v2`.

Excel is the format districts actually have, and "save it as CSV first" is a step that
gets skipped, gets done wrong, or silently mangles a leading zero in a location code. A
hand-rolled reader over `archive/zip` was considered — it is about two hundred lines for
shared strings and inline strings — and rejected: it would be a second implementation of a
format whose edge cases (styles, dates as serials, merged cells, `1.2E+09` in a numeric
cell) are exactly where a wrong answer looks like a right one. This is the one place the
no-dependency rule costs more than it saves.

Only the first worksheet is read. A workbook with the register on sheet three is a
question the operator can answer by moving it.

## Two phases: stage, review, commit

An upload validates every row and **writes nothing to `chws`**. It produces a report —
ready, warned, rejected, each with row numbers and reasons — and the operator commits or
discards it.

The staged rows live in a table, not in the session and not by re-reading the file at
commit time:

```sql
import_batches(id, filename, format, uploaded_by,
               district_id,          -- the uploader's scope at upload time; NULL = national
               status,               -- pending | committed | discarded
               total_rows, ready_rows, warning_rows, rejected_rows, imported_rows,
               created_at, committed_at)

import_rows(batch_id, row_number, raw jsonb, status,
            location_id, problems jsonb, chw_id,
            PRIMARY KEY (batch_id, row_number))
```

Re-parsing the file at commit was rejected. The register moves between the two requests —
a NIN gets claimed, a location is deactivated — so the file would validate differently
than it did on the screen the operator approved. What gets committed has to be the thing
that was reviewed. Staging also makes the report a permalink, gives a permanent record of
who uploaded what, and keeps a three-thousand-row file out of a cookie.

`raw` keeps the row exactly as it arrived, before any normalization. A rejected row has to
be explainable a month later, and "what did the file actually say" is the first question.

Nothing is swept automatically. `discard` is an explicit act by an operator and marks the
batch rather than deleting its rows; the listing sorts pending batches first so an
abandoned one nags rather than disappears.

A retention rule is a decision to make against real storage numbers, and there are none
yet — 10,000 rows of raw JSONB is on the order of 10 MB. Note which way it would go when
the time comes: a **pending** batch's rows are the only copy there is, because rejects
reach `import_quarantine` only at commit, so sweeping one would destroy the only record
that an upload was attempted and refused. A **committed** batch's rows are the most
redundant data in the system — every ready row became a CHW with its own `chw.create`
audit row, and every reject is already in quarantine. If anything is ever pruned it is
`import_rows.raw` on committed batches.

## Flow

| Method and path | Capability | Notes |
|---|---|---|
| `GET /imports` | `chw.import` | recent batches, scoped; the upload form; the template link |
| `GET /imports/template.csv` | `chw.import` | the blank template, district pre-filled for a district user |
| `POST /imports` | `chw.import` | multipart; parses, validates, stages; redirects to the report |
| `GET /imports/{id}` | `chw.import` | the report |
| `GET /imports/{id}/errors.csv` | `chw.import` | the rejected rows, original columns plus `error` |
| `POST /imports/{id}/commit` | `chw.import` | writes the ready rows |
| `POST /imports/{id}/discard` | `chw.import` | marks the batch discarded; the staged rows stay |

A missing required column rejects the **whole file** before anything is staged. There is
nothing to review when the columns are wrong, and a report claiming three thousand
identical failures is not a report.

`errors.csv` is the rejected rows with their original columns and an added `error`
column. The district fixes that file and uploads it again; they never have to find row
412 in the original by counting.

## Commit

Commit calls `store.CHWs.CreateTx` and, where the row answered anything,
`store.Profiles.SaveTx`, once per ready row. Not a bulk `INSERT`, not `COPY`.

That is what keeps invariant 6 structural: the create already writes the CHW's `audit_log`
row inside the CHW's own transaction, and already re-checks the derived `district_id`
against the `Scope` after the trigger has set it. An importer with its own `INSERT` would
be a second implementation of both, and the second one is the one that gets it wrong.

Both join the *caller's* transaction — the `Tx` half of the pair `Audit.Record` and
`Audit.RecordTx` already established — so that one transaction carries the CHW, its
profile, both audit rows, and `Imports.MarkImportedTx` marking the staged row. A profile
written in a second transaction could be lost while the CHW it describes survived, which
is the half-loaded record invariant 7 forbids, one row at a time rather than one file at a
time. Marking afterwards would leave a
window one row wide: a process killed between the insert committing and the mark landing
would leave a CHW on the register whose import row still read `ready`, and the next commit
attempt would create them a second time. Three writes in one transaction close it.

A row that fails at commit — a NIN claimed between validation and commit, a location
deactivated underneath it — is marked rejected and quarantined. It does not abort the
batch. So a partial import is possible; the report states exactly which rows landed and
which did not, and `errors.csv` picks up the remainder. An all-or-nothing transaction over
ten thousand inserts was rejected: one lost race would discard a correct nine-thousand-row
import, and the operator's next move would be to upload the identical file again.

One batch-level `chw.import` audit entry records the file, the counts and the actor. Each
CHW still gets its own `chw.create` row, because that is where the register's change
history lives.

## The district boundary

A `district_manager` cannot import into another district. Four layers say so, and only the
last one is load-bearing.

| Layer | Mechanism |
|---|---|
| Capability | `chw.import`, held by `national_admin` and `district_manager`. A viewer never sees the screen. |
| Template | a district user's downloaded template arrives with the district column filled in. |
| Resolution | resolution starts from the scope; a row naming another district is rejected `outside_scope`. |
| Write | `CHWs.Create` derives `district_id` by trigger and rolls back when the `Scope` disallows it. |

The write is the guarantee. The three above exist so the operator gets a row number and a
sentence instead of a rolled-back transaction.

Rejecting the row rather than the file was chosen so one stray line cannot block a
three-thousand-row upload. Silently forcing the row into the uploader's own district was
rejected outright: a CHW recorded under the wrong district is a data error, and rewriting
it on the way in makes that error invisible and permanent.

The report does not name the district a rejected row resolved to. It says the location is
not in your district, which is the same answer `/api/locations` and the CHW form already
give — a district user must not be able to map the country by probing names. Reading a
batch is scoped on `import_batches.district_id`, so a report and its `errors.csv` are
invisible to every district but the one that uploaded them.

## Columns

Header matching is case-insensitive and order-independent: lowercased, trimmed, internal
spaces to underscores. Unknown columns are ignored and listed on the report — a district's
own working columns (`notes`, `phone owner`, a serial number) should not stop an import,
but nobody should assume they were stored.

The first pass carries the core record. The profile columns follow on the same machinery,
and the vocabulary below is fixed now so the template does not change under people who
have already started filling it in.

### The register record

| Column | Required | Notes |
|---|---|---|
| `first_name` | yes | |
| `last_name` | yes | "other names" in the source form; space-separated |
| `sex` | yes | `male` / `female`; `m` / `f` accepted |
| `cadre` | yes | `vht` / `chew`, any case; `chw` reads as `chew` |
| `age_years` | no | 18–99, or blank |
| `nin` | no | uppercased; 14 characters, `^[A-Z]{2}[A-Z0-9]{11}[A-Z]$` |
| `district` | yes | |
| `subcounty` | yes | |
| `parish` | yes | the placement for a CHEW |
| `village` | VHT only | the placement for a VHT; must be blank for a CHEW |
| `location_code` | no | the official code path; when present it *is* the placement, and the name columns are checked against it |

There is no `district_id` column and no `status` column. `district_id` is derived by
trigger from the placement and nothing outside the database sets it (invariant 2); a
register is seeded with serving CHWs, and deactivation is a deliberate act with a reason
attached, not a spreadsheet cell.

`age_captured_on` is stamped with the import date. ODK provenance is stripped, so the
collection date is not available — see [odk-mapping.md](odk-mapping.md).

### `location_code`

Migration 0001 settles which of the two channels is authoritative, in a comment on the
table itself: *code is the identity; name is a label.* So when the column is filled in, the
code decides the placement.

That does not mean a contradicting label is ignored. If the code and the names disagree,
one of them is wrong and **nothing in the file says which**. Preferring the code quietly
would file a CHW somewhere no human confirmed — the failure that is never noticed, which
is the same ground on which fuzzy name matching and the silent district rewrite were both
rejected. A mismatch is `location_code_mismatch`, and the message carries both readings so
the fix is one edit:

```
row 412 · location_code · code 00100301003001 is NAMOKORA village, OMIYA ANYIMA
subcounty. The row says LAGORO.
```

Two rules on that message:

- **Scope is checked before the mismatch is described.** A code resolving outside the
  uploader's district is `outside_scope` and nothing further — the descriptive form would
  name another district's locations, which is precisely what the report is not allowed to
  do. Only a code inside their own district earns the sentence above.
- **The comparison runs on normalized names**, so `Buhoba-A` against `BUHOBA A` is a
  match, not a rejection. Only a genuine contradiction stops the row.

The honest cost of strictness is a renamed location: a file carrying last year's name with
a correct code is refused, though it was right. That case is indistinguishable from a wrong
code, and one refusal costs an upload cycle while a silent preference costs a misplaced
record that looks correct indefinitely.

This column ships in the first pass rather than with the profile columns, because it is
the **remedy for `location_ambiguous`**. A district holding two villages named `BUHOBA A`
has no other way to repair their file. Rejecting a row for ambiguity while offering no
escape hatch in the same release would be a dead end.

### Profile — the optional attributes

| Column | Notes |
|---|---|
| `phone_owner` | yes / no. Blank means not asked |
| `phone_primary` | their own number, when they own a phone |
| `phone_for_reporting` | yes / no — is that phone used for reporting |
| `phone_alternate` | a number to reach them on when they own **no** phone |
| `facility` | matched by name inside the CHW's own district |
| `service_start_year` | 1960–2100 |
| `households_served` | 3–100,000 |
| `education` | `none` / `ple` / `uce` / `uace` / `tertiary` |
| `english` | any of `speak; read; write`; `none` is a recorded no on all three |
| `other_languages` | free text, kept verbatim |
| `receives_incentive` | yes / no |
| `incentive_frequency` | `monthly` / `quarterly` / `annually` / `one_off` |
| `incentive_amount_ugx` | 1,000–500,000 |
| `tools` | `;`-separated slugs or labels |
| `tools_functional` | which of those work — a subset of `tools` |
| `services` | `;`-separated service domains offered |
| `trained` | trained on in the last 2 years — a subset of `services` |

Blank and "no" stay different answers all the way through: an empty cell leaves the column
NULL, and only an explicit `no` writes `false`. A profile imported from a file that never
asked about incentives must not come back as a CHW who said they receive none. **A file
whose profile columns are all empty writes no `chw_profiles` row at all**, rather than a
row of nulls — "nothing recorded" and "recorded as nothing" are the same distinction one
level up.

Slugs are what the template documents; the labels are accepted too, because someone who
read the form rather than the template will write those. `None` in a tool or service list
means the empty set — it is not a tool named None, which is why 0003 does not seed one.

Both nested lists are checked against their parent before the schema has to: `trained`
must be among `services` (`trained_implies_provides`) and `tools_functional` among `tools`
(the form's `choice_filter`). A tool held but not named in `tools_functional` is recorded
as not working only when that column was filled in at all; left empty, the condition was
not asked and stays NULL.

`support_supervision` has no column: the source form records supervision per service
domain and carries no date, so `last_supervised_on` fills only through the UI.

**A bad profile value refuses the whole row**, as a bad value anywhere else does. The
profile *form* silently drops a value posted into a branch its own JavaScript had hidden —
a "no" to owning a phone arriving with a phone number stores neither. That is right for a
form, where the hidden field is a leftover; it is wrong for an import, where a district
that wrote a phone number is owed either the number or a reason. Every branch CHECK is
therefore pre-checked and reported: `phone_branch_exclusive`,
`incentive_details_require_yes`, `trained_implies_provides`.

### The resolved record

A staged row carries `record` beside `raw`: the resolved register record, as JSON, that a
commit will write. `raw` answers "what did the file say"; `record` is what was reviewed.

Storing it rather than re-deriving it at commit is what the profile columns forced.
Re-parsing worked while every field was a pure function of its own cell — a name is a name
— but a facility is resolved by name within the CHW's district, and re-resolving at commit
would answer from a register that has moved: a facility renamed between the report and the
commit would silently change which one a CHW reports to. Migration 0008 adds the column,
and "what is committed is what was reviewed" stops being an argument and becomes a fact.

## Validation

Rules the form already enforces are shared with it rather than restated. The predicates —
the NIN pattern, the age range, the cadre and sex vocabularies with their case variants —
move into `internal/domain`, and both `http.decodeCHW` and `importer.Row` call them. The
*messages* stay separate: a form says "Enter the first name", a report says
`row 412 · first_name · empty`. Sharing the rule is what stops the two from drifting;
sharing the sentence would only make both worse.

Per row, in order. Every field is checked — a report that stopped at the first problem
would take four uploads to surface four errors in one row.

| Code | Meaning |
|---|---|
| `required` | a required column is empty |
| `bad_value` | not in the vocabulary, or outside the range |
| `bad_nin` | fails the 14-character pattern |
| `cadre_multi` | more than one cadre, or free text in the cadre column |
| `location_missing` | no sibling of that name under the resolved parent |
| `location_ambiguous` | two siblings share the name; both offered as candidates |
| `location_code_unknown` | `location_code` matches no `code_path` |
| `location_code_mismatch` | the code and the name columns name different places |
| `placement_level` | a CHEW given a village, or a VHT given only a parish |
| `outside_scope` | the placement is not in the uploader's district |
| `duplicate_nin` | that NIN is already on the register; the record is named |
| `duplicate_nin_in_file` | two rows in this file carry the same NIN; both rejected |
| `possible_duplicate` | **warning** — same name at the same location |
| `lost_race` | raised at commit only: the row was acceptable when the report was produced, and the register moved underneath it |

`possible_duplicate` warns and imports, because two people in one village genuinely share
a name; that is why `chws_dup_probe_idx` exists and why the CHW form asks for a second
submit rather than refusing. A checkbox at commit skips the warned rows for an operator
who would rather check first.

`cadre_multi` is a rejection, not a truncation. The source form allowed a multi-select
with an "other" free-text box, and the register stores a single closed enum. Keeping the
first value and discarding the rest would erase the record of a CHW who did not fit the
two-value model at collection time.

## Resolving a location

Uppercase, then drop every separator — spaces, hyphens, apostrophes, stops, underscores.
Then match exactly, within the parent already resolved. `KANU-EAST`, `Kanu East` and
`KANUEAST` are one name.

Dropping separators rather than collapsing them to a space is deliberate, and the tests
pin it: collapsing matches `Kanu East` to `KANU EAST` and still misses `KANU-EAST`, which
is the spelling the source workbook actually uses. The cost is that two genuinely
different names could fold together — and that cost is bounded, because a fold matching
two siblings is an *ambiguity*, which is quarantined with both candidates rather than
resolved. Over-matching here produces a question, never a silently wrong answer.

Nothing fuzzier than that. Identity in `locations` is `(parent_id, code)` and explicitly
not name — one parish holds two villages both called `BUHOBA A`. Trigram matching across
71,207 villages would answer confidently and wrongly, and a CHW filed under the wrong
village is not an error anyone notices. Two matches is `location_ambiguous`, quarantined
with both candidates and the chain above each, and a human decides.

County is never a column. It is mandatory in the data — subcounty codes are unique only
within a county — and it is derived from the path, exactly as the cascading selects derive
it. A district's spreadsheet will not have it.

## Quarantine

Rejected rows are written to `import_quarantine` with `source = 'chw_csv'`, the row number
in `row_ref`, the untouched row in `payload`, the code in `reason`, the sentence in
`detail`, and any ambiguous matches in `candidates`. That table has been in the schema
since migration 0001 waiting for this.

`import_rows` and `import_quarantine` overlap, and that is deliberate. `import_rows` is
the working set behind one report and is prunable; `import_quarantine` is the permanent
answer to invariant 7 — nothing is dropped silently, and a year later the question is not
"what did batch 46 say" but "which rows never made it in, and why".

A committed batch's rows also hold `chw_id`, which means a CHW cannot be deleted while an
import row still points at them. That is invariant 5 — CHWs are never deleted — arriving
from a second direction, and it is the right answer: the row that says where a CHW came
from should not be the thing that quietly disappears with them.

`import_quarantine.batch_id` is what makes the permanent record traceable back to the
upload that produced it. It is nullable, because the hierarchy and facility seeders write
to that table too and have no batch. Its foreign key is left to restrict rather than
cascade or null: deleting a batch that produced quarantine rows is refused outright. That
is the same stance `chws_check_facility_after_move()` takes — raise rather than quietly
null a column — and here it means no future pruning can destroy the record of a refusal by
tidying away the batch it came from.

## Limits

10 MB and 10,000 rows per file, refused with a message that says so rather than by timing
out. A file larger than that is a migration, not an import, and belongs on the seeder path
in [seeding.md](seeding.md) where it can be checked against the source before it runs.

Commit is synchronous, and it is not fast. Measured against the seeded hierarchy and a
24,573-record register: **10,000 rows validate and stage in 4 seconds, and commit in 32** —
about 3.2 ms a row. Roughly a quarter of that is `chws_set_placement` deriving the district
by matching the path against all 146 districts, and the rest is the audit row, the row
mark, and a transaction per record. Batching rows into shared transactions was measured as
worth under a third of it, and would trade away the property that one bad row does not
abort its neighbours, so the loop stays as it is.

Two consequences, both of which are the design's to own rather than to hide:

- **The page says so.** The commit button carries the rate, and the operator is told to
  leave the page open. Thirty seconds of nothing is otherwise read as a hang.
- **A batch is claimed before it is committed** (`import_batches.committing_at`,
  migration 0007). This is not a nicety. Two runs walking one batch both read the same page
  of `ready` rows before either marks them, and both write the CHWs on it: a
  double-submitted 1,200-row file was measured creating **2,033 records**. The claim is a
  lease rather than a flag so a process killed mid-commit does not wedge the batch — after
  fifteen minutes another attempt may take it, and resuming is safe because a commit only
  ever walks rows that are not yet marked.

A background worker with a polling status page is the change to make if the cap rises, and
the batch table is already where such a worker would keep its state.

## CSRF and multipart

`auth.CSRF` calls `r.ParseForm()`, which does not read a `multipart/form-data` body:
`PostForm` comes back empty, the token compare fails, and every upload would be refused
with a 403 before any handler ran.

The middleware therefore has a multipart branch — `http.MaxBytesReader` for the global cap,
then `ParseMultipartForm`, which populates `PostForm` from the body and spills the file to
a temp file rather than memory, removed by the middleware so no handler can forget to. Its
test bites: reverting the branch turns the accept case into exactly the 403 above.

Putting the token in the query string, or posting it by `fetch` with `X-CSRF-Token`, were
both rejected — the first leaks it into logs and history, and the second makes the upload
form depend on JavaScript when nothing else on the register does.

## What is left

Nothing in the import itself. The scoped CSV export that completes phase 6 is built —
see [application.md](application.md#the-export) — and its columns are deliberately this
file's columns, so a register that comes out can go back in.

`chw_languages` stays empty. `other_languages` is kept verbatim in
`other_languages_raw` as the form collects it; splitting that free text into a vocabulary
nobody has agreed on would be inventing the vocabulary, so the parsed junction waits for
one.

If the row cap ever rises, the commit becomes a background job. The batch table is already
where such a worker would keep its state, and `committing_at` is already the claim it
would take.

## Verified

Against the seeded hierarchy and a 24,573-record register — driven through the running
server and, for the pages, a real browser on the Selenium grid. Not only unit tests.

An eleven-row file carrying one of every refusal imported four and refused seven, across
six distinct quarantine reasons:

- placement is derived, never read from the file: a VHT landed at their village, a CHEW at
  their parish, and `district_id` came from the trigger in both cases
- a village that does not exist under the named parish is refused, naming the parish
  searched
- `BUHOBA A` — the genuine collision under `SIGULU MUKANI`, two siblings sharing a name —
  is quarantined with both candidates, each carrying the chain above it and the code that
  settles it. The same row imports once `location_code` names which one
- a `location_code` contradicting its name columns is refused with both readings in the
  message; case and punctuation are not a contradiction
- a cadre column holding two cadres is refused, not truncated
- a CHEW handed a village and a VHT given only a parish are both `placement_level`
- a NIN already on the register is refused, naming the record it collides with; two rows
  of one file sharing a NIN refuse each other, naming both lines
- a row with several bad fields carries all of them, not the first

Scope, from both sides:

- an ABIM manager's file naming GULU imported the ABIM row and refused the other two — one
  by name, one by `location_code`. The rendered page contained none of `GULU`, `PAIBONA`,
  `ACUTOMER` or `ACUT OMER`: a district user must not map the country by probing names
- that manager gets a 404 on a national batch's report, its `errors.csv` and its commit
- a `district_viewer` gets a 403 on all four routes, and no rail entry

The file and the flow:

- a file missing `last_name` is refused whole, with no batch created
- the template round-trips as a file with no rows, and its example row is skipped when
  rows are added beneath it
- an `.xlsx` and a CSV of the same rows produce identical verdicts and identical records
- a warned row imports, and is skipped when the box is ticked
- discard marks the batch and keeps its rows; a second decision on a decided batch is
  refused
- an upload with no CSRF token is 403

At commit:

- every imported CHW has its own `chw.create` audit row; the batch has one `import.upload`
  and one `import.commit`
- a row that passed validation and then lost a NIN race is marked `failed`, quarantined
  with the reason, and its neighbours still import — the flash says so
- **a batch committed twice at once imports each row once.** Before the claim existed this
  was measured creating 2,033 records from a 1,200-row file; it now grows the register by
  exactly the file's row count, and the second attempt is refused in milliseconds
- a claim older than the lease is taken over, and the batch commits normally

The profile columns, against the real vocabularies and the real Master Facility List, on a
fixture built from the template the running app served:

- a full profile round-trips: `0772 123-456` stores as `772123456` and `+256 700 999888` as
  `700999888`, `1,250` households as `1250`, `speak;read` as speaks and reads with **write
  recorded as a no**, `Luo, Ateso` kept verbatim, and the facility resolved by name inside
  the CHW's own district
- `english=none` writes three recorded noes; a blank cell writes three NULLs
- a row carrying only the core columns writes **no `chw_profiles` row at all** — verified
  by asking the database, not by reading nulls off a join
- `bicycle;gumboots;register` with `bicycle;register` working stores three tools with
  gumboots false; `iccm;nutrition` trained on `iccm` stores two domains with one trained
- one row violating every branch at once reported **all seven** reasons together: the
  phone branch, an unknown education, an unknown proficiency, an amount out of range, a
  functional tool not held, a trained service not offered, and a facility in another
  district
- `received_supervision` and `last_supervised_on` are NULL on every imported row, by
  construction
- audit carries one `chw.create` per CHW and one `chw.profile_update` per profile actually
  written — three and two, not three and three

And the transaction the profile columns are the reason for: a facility moved out of the
district *between the report and the commit* fails its row at
`chw_profiles_facility_district`, and **no CHW is left behind** — the insert, the profile
and the mark roll back together, the row is marked `failed`, and its neighbour still
imports.

In the browser, at 1400px and 420px: the column reference opens to all 28, the report's
tiles, the seven reasons listed against one row, the decision panel disappearing once
decided, no horizontal scroll, and no script on the page at all — `app.css` is the only
resource the CSP has to allow.
