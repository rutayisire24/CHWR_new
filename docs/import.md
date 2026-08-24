# Bulk import

**Status: design, not built.** Phase 6. `internal/importer` is an empty directory and
`migrations/0006_imports.sql` does not exist yet. This document is the plan the
implementation is measured against; when the code lands, the tense changes and this line
goes away.

Every record on the register so far was typed one at a time. Districts hold their CHW
lists in spreadsheets and ODK exports, and the register is not usable until those get in.
This is how they get in without a half-loaded register on the other side.

[data-model.md](data-model.md) covers the schema this writes into, [rbac.md](rbac.md) the
scope rules it obeys, and [odk-mapping.md](odk-mapping.md) the field semantics the column
vocabulary follows.

## Shape

```
internal/importer/     parse, validate, resolve — no SQL of its own
  columns.go   the column vocabulary; also generates the blank template
  csv.go       encoding/csv, header-normalized, order-independent
  xlsx.go      excelize → the same row shape
  row.go       one row → store.CHWInput + []Problem
  resolve.go   district > subcounty > parish > village, by name within parent
internal/store/imports.go     batches, staged rows, quarantine writes
internal/http/imports.go      upload, report, commit, discard, template, errors.csv
internal/web/templates/       imports.html, import_report.html
migrations/0006_imports.sql
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

Commit calls `store.CHWs.CreateTx` once per ready row. Not a bulk `INSERT`, not `COPY`.

That is what keeps invariant 6 structural: the create already writes the CHW's `audit_log`
row inside the CHW's own transaction, and already re-checks the derived `district_id`
against the `Scope` after the trigger has set it. An importer with its own `INSERT` would
be a second implementation of both, and the second one is the one that gets it wrong.

`CreateTx` joins the *caller's* transaction — the `Tx` half of the pair `Audit.Record` and
`Audit.RecordTx` already established — so that one transaction carries the CHW, its audit
row, and `Imports.MarkImportedTx` marking the staged row. Marking afterwards would leave a
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

### Core record — first pass

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

### Profile — second pass

`phone_owner`, `phone_primary`, `phone_for_reporting`, `phone_alternate`, `facility`,
`service_start_year`, `households_served`, `education`, `english` (a `;`-delimited subset
of `speak;read;write`), `other_languages`, `receives_incentive`, `incentive_frequency`,
`incentive_amount_ugx`, `tools` and `tools_functional` (`;`-delimited slugs),
`services` and `trained` (`;`-delimited slugs).

Blank and "no" stay different answers all the way through: an empty cell leaves the column
NULL, and only an explicit `no` writes `false`. A profile imported from a file that never
asked about incentives must not come back as a CHW who said they receive none.

The branch CHECKs are pre-checked per row for the same reason the profile form pre-checks
them — a `phone_branch_exclusive` violation reaching the operator as a 500 tells them
nothing. `support_supervision` has no column here at all: the source form carries no date,
so `last_supervised_on` fills only through the UI.

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

Commit is synchronous. Ten thousand `Create` calls, each a transaction with its triggers
and its audit row, is a matter of seconds; a background worker with a polling status page
is machinery this does not yet need. If the cap ever rises, that is the change to make,
and the batch table is already the place a worker would keep its state.

## CSRF and multipart

`auth.CSRF` calls `r.ParseForm()`, which does not read a `multipart/form-data` body:
`PostForm` comes back empty, the token compare fails, and every upload would be refused
with a 403 before any handler ran.

The middleware gains a multipart branch — `http.MaxBytesReader` for the global cap, then
`ParseMultipartForm`, which populates `PostForm` from the body and spills the file to a
temp file rather than memory. It is the first thing to build, and it carries its own test:
a multipart POST without a token is 403, with one it passes.

Putting the token in the query string, or posting it by `fetch` with `X-CSRF-Token`, were
both rejected — the first leaks it into logs and history, and the second makes the upload
form depend on JavaScript when nothing else on the register does.

## Order of work

1. The CSRF multipart branch, with its test
2. `github.com/xuri/excelize/v2`; migration 0006; `store.Imports`
3. `internal/importer` — the column vocabulary, both readers, row validation, the
   resolver. Table-driven tests; the parsing and validation need no database
4. Handlers, the two templates, the nav entry, the template download, `errors.csv`
5. Verification against the real database and in a browser
6. This document brought to the present tense; roadmap, application, decisions and
   `CLAUDE.md` updated
7. The profile columns on the same machinery
8. Scoped CSV export, which shares `store.Filter` with the listing — the other half of
   phase 6

## Verification plan

Against the seeded hierarchy and a real database, not only unit tests. Each of these is a
case the design claims to handle:

- a clean file of both cadres imports, and each CHW's `district_id` is derived, never read
  from the file
- an ABIM manager's file naming GULU rows: those rows rejected `outside_scope`, the ABIM
  rows imported, the report naming neither GULU nor the matched location
- an ABIM manager cannot open a national batch's report or its `errors.csv`
- a village name that exists twice under one parish is quarantined with both candidates,
  not resolved to the first, and the same row imports once `location_code` names which
- a `location_code` contradicting its name columns is refused with both readings in the
  message; the same code pointing outside the uploader's district says only
  `outside_scope`, naming nothing
- a name differing from the code's location only by punctuation or case is not a mismatch
- a CHEW given a village and a VHT given only a parish are both `placement_level`
- a NIN already on the register rejects and names the existing record; two rows in one
  file sharing a NIN reject each other
- a name already at that location warns, imports, and is skipped when the box is ticked
- a file missing `last_name` is refused whole, with nothing staged
- a row that passed validation but loses a NIN race at commit is rejected and quarantined
  while the rest of the batch lands, and the report says so
- every imported CHW has its own `chw.create` audit row, and the batch has one
  `chw.import`
- an upload with no CSRF token is 403; with one it reaches the handler
- an `.xlsx` and a CSV of the same 500 rows produce identical results
