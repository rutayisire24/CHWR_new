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
| `package_import.sh` | Packs those CSVs for transfer, and verifies one against a register |

Order matters: hierarchy, then facilities, then the ODK conversion. `make seed` runs the
first three. The conversion is not part of it — it needs a register that is already
seeded, and it produces files a human reads before anything is committed.

`out/` is generated and git-ignored.

## The ODK conversion

```bash
export DATABASE_URL=postgres:///chwr
python3 seed/convert_odk_export.py "National CHWR (2).csv" ~/chwr-import
```

**The export is not in this repository and never will be.** It carries NINs and phone
numbers for tens of thousands of people, so it is not in `data/` beside the workbooks and
it is not something a clone can be expected to hold. A checkout on a fresh machine cannot
reproduce the numbers below until someone puts the file there; the path above is an
argument, not a location the script knows. Everything downstream — the district CSVs, the
rejects, the notes — is equally personal data and belongs outside the working tree.

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

### One cadre per run

```bash
CADRE=vht  python3 seed/convert_odk_export.py export.csv ~/chwr-vht    # default
CADRE=chew python3 seed/convert_odk_export.py export.csv ~/chwr-chew
```

Cadre decides placement, so it decides which rows a run is even about. A VHT run places
at village and requires one; a CHEW run places at **parish**, never consults the village
column, and emits the parish's code — with the village column left blank, because a
village name beside a parish code contradicts the chain and the importer would refuse it.

Splitting the two is not just tidiness. A mixed upload would produce one report covering
rows placed at two different levels, and "17 rows could not be placed" would not say which
question was being asked of them.

A row naming **both** cadres is refused by either run. There is no neutral reading: it
says village and parish at once. The two-value model has no room for `parasocial_worker`,
`mentor_mother` or `linkage_facilitator` either — a row carrying one of those *beside* a
cadre is imported as that cadre, and a row carrying only those is dropped.

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

63,554 submissions in. The two runs are disjoint and were loaded one after the other:

| Run | Converted | Imported | Districts |
|---|---|---|---|
| `CADRE=vht` | 42,956 | 42,956 | 46 |
| `CADRE=chew` | 1,588 | 1,490 | 30 |

The 98 CHEWs that did not land were refused by `chws_nin_uniq`: their NIN was already on
the register from a VHT submission. The same people had been enumerated twice under
different cadres, and the index caught every one without the converter having to guess.
That is also the best evidence about the rows naming both cadres — they look like
duplicate enumeration rather than a genuine dual role.

The VHT run's losses, which dominate:

| Dropped | Rows |
|---|---|
| No village recorded | 9,243 |
| Village not in the hierarchy | 4,631 |
| Names chew | 3,735 |
| Village name ambiguous within the district | 1,245 |
| No VHT cadre | 1,081 |
| Duplicate NIN within the file | 662 |
| Missing name | 1 |

"Names chew" is not a loss to the register — 2,392 of those rows are the CHEW run's input,
and 1,490 of them landed. The 1,343 naming *both* cadres are in neither run.

The village losses are a collection gap, not a matching failure: whole districts recorded
no village at all — Namutumba 2,851 of 2,862 rows, Bugiri 2,057 of 2,075, Jinja 519 of
727. A VHT is placed at village, so those rows cannot be imported as VHTs by anything
this script could do differently. They need re-collection. It is also why 46 districts
come out of an export covering 64.

Handling the district-suffix tagging and the `luwero`/`LUWEERO` spelling took village
placement from 52.6% to 74.2% of submissions.

The output was checked against the running importer twice over. Five districts — 9,241
rows — were staged and discarded: 9,240 `ready`, one duplicate-name `warning`, nothing
quarantined. Buliisa was then carried the whole way: 606 rows in, 606 imported, none
rejected, all placed at village level in Buliisa as VHTs, with 1,470 tool rows and 1,958
service-domain rows on the junctions and `chw.create` in `audit_log` up by exactly 606.

Two source values have nowhere to go and are logged rather than carried: `chw_type_other`
free text, and the `or_other` member of the tool list. Supervision is not imported at
all — the form records it per service domain and carries no date, so `last_supervised_on`
fills only through the UI.

## Packaging for another register

The converted files are the deliverable, and they do not live in this repository. To
carry them to a machine that cannot reach the export:

```bash
DATABASE_URL=postgres:///chwr seed/package_import.sh pack ~/chwr-import chwr-import.tar.gz
# on the far side, before importing anything:
DATABASE_URL="$PROD_DATABASE_URL" seed/package_import.sh verify chwr-import.tar.gz
```

`pack` stamps a `FINGERPRINT` into the archive — the row and district counts, and a
count-plus-checksum of every `code_path` and facility in the register it was built
against — then writes `SHA256SUMS` beside the files and a `.sha256` beside the tarball.
31 MB of CSV compresses to about 3 MB.

`verify` checks the sums, prints both fingerprints side by side, and **exits non-zero if
they differ**. A register seeded from a different workbook would resolve the same code to
a different village, or fail to resolve it at all; that is the one failure this pipeline
cannot detect from the inside, because a wrong-but-existing code imports quietly. Do not
skip it, and do not load a package that fails it — re-run the conversion instead.

`national.csv` is left out of the package deliberately: the importer refuses it by name at
14 MB, so shipping it only invites someone to try. `REJECTS.csv` and `NOTES.csv` do travel
— the districts chasing those rows are the reason they exist.

**The package carries NINs and phone numbers for tens of thousands of people.** Move it
over SSH, load it, delete it. Set `RECIPIENT` to a gpg key, or `PASSPHRASE` for symmetric
encryption, and `pack` will encrypt it at rest; with neither it says plainly that it did
not.

## Importing the result

```bash
cd ~/chwr-import
ls *.csv | grep -vE '(REJECTS|NOTES|national)\.csv$' \
  | BASE=https://chwr.example.org EMAIL=you@ministry.go.ug PASSWORD=... DRY_RUN=1 \
    xargs seed/import_batches.sh          # stage everything, commit nothing
ls *.csv | grep -vE '(REJECTS|NOTES|national)\.csv$' \
  | BASE=... EMAIL=... PASSWORD=... xargs seed/import_batches.sh
```

Filter the list rather than passing `*.csv`. The importer does refuse `REJECTS.csv` and
`NOTES.csv` on their columns and `national.csv` on its size, so a bare glob is survivable
— but it spends three uploads finding that out and reports three failures that are not
failures. Pipe through `xargs` rather than expanding into a variable: **zsh does not
word-split an unquoted expansion**, so `FILES=$(ls ...)` then `$FILES` hands the script one
argument containing every path and it silently processes almost nothing.

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

The CSVs are portable, but prove it before trusting it. `location_code` is the official
code path — `district_code || ea_code || scounty_code || parish_code || village_code`,
straight out of the workbook in `data/` — so it carries no database id and means the same
thing in any register seeded from that workbook. `package_import.sh verify` is what turns
that from an assumption into a check.

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
