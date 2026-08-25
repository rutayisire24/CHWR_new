# seed/

Everything that fills an empty register: the administrative hierarchy, the Master
Facility List, the constraint probe, and the one-off conversion that turns an ODK
export into CHWs.

[`docs/seeding.md`](../docs/seeding.md) explains the hierarchy and facility loaders —
their sources, their expected counts, and why `\copy` forces you to run them from the
repo root. This file is the directory's index and the reference for the ODK import,
which is the only part with no loader of its own.

| File | Does |
|---|---|
| `extract_units.py` | `data/Village-Admin Units 06-08-2026.xlsm` → `out/units.tsv`, `regions.tsv`, `dregion.tsv` |
| `load_hierarchy.sql` | Stages those TSVs and inserts level by level — 84,635 rows |
| `extract_facilities.py` | `data/MFL Updated - 21 feb.xlsx` → `out/facilities.tsv` |
| `load_facilities.sql` | 7,895 facilities parented to district; 12 quarantined |
| `verify_constraints.sql` | 39 bad-data cases, every one of which must say `blocked` |
| `convert_odk_export.py` | ODK export → importer-canonical CSVs, one per district |
| `import_batches.sh` | Uploads and commits those CSVs against a running server |

Order matters: hierarchy, then facilities, then the ODK conversion. `make seed` runs the
first three. The conversion is not part of it — it needs a register that is already
seeded, and it produces files a human reads before anything is committed.

`out/` is generated and git-ignored.

## The ODK conversion

```bash
export DATABASE_URL=postgres:///chwr
python3 seed/convert_odk_export.py "National CHWR (2).csv" ~/chwr-import
```

The export is an ODK Central submission dump: 56 columns with group prefixes
(`Hirechy-`, `other_individual-`, `Capacity-`), multi-selects separated by spaces, and
service domains written as squashed lowercase run-ons. Every field's destination is in
[`docs/odk-mapping.md`](../docs/odk-mapping.md); this script is that mapping executed.

It **writes nothing to the database**. It reads the hierarchy and the facility list, and
emits CSVs in the vocabulary of [`docs/import.md`](../docs/import.md) — which then go
through the ordinary `/imports` review-then-commit path like any district's own upload.

### Why it emits `location_code`

The importer's name cascade is strict: district, then subcounty among that district's
subcounties, then parish, then village. The ODK form's names cannot survive it. The form
tags a name with its district on **either side, inconsistently** — `kihungya_bulliisa`
but `kyegegwa_rwentuha` — misspells the tag (`bulliisa` for Buliisa), appends the word
`subcounty` to some names, and puts Kampala's *divisions* in the subcounty cell, where
they are counties.

So placement is resolved here instead, where every reading of a cell can be tried
against the real siblings, and handed to the importer as a `location_code` — the one
channel migration 0001 makes authoritative. The name columns are written back from the
resolved chain, so the importer's own code-versus-name check has something consistent to
agree with rather than a contradiction to refuse.

The matching is exact throughout. Fragments of a cell are tried, never fuzzy matches: a
village name that folds onto two siblings is reported ambiguous and dropped, not guessed.
Identity in `locations` is `(parent_id, code)` and explicitly not name.

### Why VHT only

By instruction, not by limitation. Rows naming `chew` are dropped, including the 1,343
that name both — cadre decides placement level, so a row claiming both says village and
parish at once and there is no neutral reading. The two-value model has no room for
`parasocial_worker`, `mentor_mother` or `linkage_facilitator` either; a row carrying one
of those *beside* `vht` is imported as a VHT, and a row carrying only those is dropped.

To take CHEWs as well, change the cadre test at the top of the row loop.

### What it produces

| Output | Contents |
|---|---|
| `<district>.csv` | Importer-canonical rows, one file per district |
| `national.csv` | The same rows in one file — **too large to upload**, see below |
| `REJECTS.csv` | Every dropped row, with the reason and its original cells |
| `NOTES.csv` | Every value blanked rather than carried, with why |
| `manifest.json` | District, filename, row count |

Nothing is dropped silently. A row that does not reach a district file is in
`REJECTS.csv`; a *value* that was blanked so its row could still import — a malformed
NIN, an out-of-range household count, a facility name matching nothing in the district —
is in `NOTES.csv`.

**The outputs carry NINs and phone numbers. Do not commit them.** Write them outside the
repository, as the example above does.

### Results on the August 2026 export

63,554 submissions in, 42,956 importable rows out across 46 districts.

| Dropped | Rows |
|---|---|
| No village recorded | 9,243 |
| Village not in the hierarchy | 4,631 |
| Names chew | 3,735 |
| Village name ambiguous within the district | 1,245 |
| No VHT cadre | 1,081 |
| Duplicate NIN within the file | 662 |
| Missing name | 1 |

The village losses are a collection gap, not a matching failure: whole districts recorded
no village at all — Namutumba 2,851 of 2,862 rows, Bugiri 2,057 of 2,075, Jinja 519 of
727. A VHT is placed at village, so those rows cannot be imported as VHTs by anything
this script could do differently. They need re-collection. It is also why 46 districts
come out of an export covering 64.

Handling the district-suffix tagging and the `luwero`/`LUWEERO` spelling took village
placement from 52.6% to 74.2% of submissions.

Two source values have nowhere to go and are logged rather than carried: `chw_type_other`
free text, and the `or_other` member of the tool list. Supervision is not imported at
all — the form records it per service domain and carries no date, so `last_supervised_on`
fills only through the UI.

## Importing the result

```bash
BASE=https://chwr.example.org EMAIL=you@ministry.go.ug PASSWORD=... \
  DRY_RUN=1 seed/import_batches.sh ~/chwr-import/*.csv    # stage, commit nothing
BASE=... EMAIL=... PASSWORD=... seed/import_batches.sh ~/chwr-import/*.csv
```

`DRY_RUN=1` uploads and stages every file without committing, which is the whole point of
the two-step import: a human reads the reports at `/imports` and then decides. Set
`NEW_PASSWORD` when the account still carries `must_reset`, and `TIMEOUT` to override the
30-minute per-request ceiling.

**Per district, never the national file.** `national.csv` is 14 MB; the importer refuses
an upload over 10 MB by name and `auth.MaxMultipartBytes` caps the body at 12 MB, so it
cannot be uploaded at all. The split is also what keeps a commit inside the proxy's 180s
read timeout — the largest district is 2,199 rows, about seven seconds — and what makes a
failure cost one district instead of the country.

The script commits each batch only after reading its report back, and treats `warning`
rows as committable because [`internal/http/imports.go`](../internal/http/imports.go)
does.

### Before running it against a live register

Take a backup. CHWs are never deleted — invariant 5 — so a bad import cannot be undone
through the application, and a restore is the only rollback. `/opt/chwr/backup.sh` is the
same script the nightly timer runs.

Re-run the conversion against the target database rather than shipping CSVs generated
elsewhere: the `location_code` values must resolve in the hierarchy they are being
imported into.

Expect more quarantined rows on a register that already holds CHWs than on an empty one.
`convert_odk_export.py` deduplicates NINs only within the file; `chws_nin_uniq` is a
national index, and the importer checks it against the register.

### Verifying afterwards

```sql
SELECT cadre, status, count(*) FROM chws GROUP BY 1,2;
SELECT count(*) FROM audit_log WHERE action = 'chw.create';
```

Every created CHW wrote its `audit_log` row in the same transaction, so a gap between
those two counts is a real problem and not a reporting lag.
