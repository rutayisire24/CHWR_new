# Seeding

## Hierarchy

```bash
go run ./cmd/server -migrate                           # schema first, to version 4
python3 seed/extract_units.py "<National CHWR.xlsx>"   # writes seed/out/*.tsv
psql -d chwr -f seed/load_hierarchy.sql                # from the repo root
```

The schema comes from the binary, not from psql: `migrations/*.sql` are embedded and
applied by goose at startup, so an empty database and a running server are one step apart.

`extract_units.py` reads `data/Village-Admin Units 06-08-2026.xlsm` for the hierarchy and
the ODK workbook for regions, emitting three TSVs:

| File | Contents |
|---|---|
| `units.tsv` | 71,207 village rows, denormalized |
| `regions.tsv` | 15 regions |
| `dregion.tsv` | normalized district name to region slug |

`load_hierarchy.sql` stages the TSVs in temp tables, then inserts level by level with
`INSERT ... SELECT`, joining each level to its parent on `code_path`. One statement per
level, so the path trigger fires per row without per-row round trips.

**Run it from the repo root.** `\copy` performs no variable interpolation of any kind —
not `:var`, not `:'var'` — so the paths in the script are literal and relative to psql's
working directory.

### Expected result

```
region 15 | district 146 | county 353 | subcounty 2198 | parish 10716 | village 71207
```

84,635 rows, about 3 seconds, 33 MB. The load is verified to lose nothing: villages not
loaded = 0, districts without a region = 0, `code_path` mismatches = 0.

Re-running requires a truncate first; the loader is not idempotent.

## Verifying

```bash
psql -d chwr -f seed/verify_constraints.sql
```

36 constraint cases, all of which must report `blocked`. The script runs in a transaction,
rolls back, and raises on any leak. Run it after every schema change.

## Facilities

```bash
python3 seed/extract_facilities.py           # writes seed/out/facilities.tsv
psql -d chwr -f seed/load_facilities.sql     # from the repo root
```

Source is `data/MFL Updated - 21 feb.xlsx`, not the ODK workbook. Requires the
hierarchy to be loaded first, since every facility hangs off a district.

### Expected result

```
7,895 facilities across all 146 districts (3,389 government) | 12 quarantined
```

7,907 workbook rows in, 7,895 loaded, 12 quarantined as `validation_failed` —
duplicate `(district, name)`, all of them private clinics or drug shops. Nothing
fails to resolve a district.

**Facilities are parented to district, deliberately.** The workbook's `subcounty`
column resolves for only 3,696 of 7,907 rows; it is stored raw in
`subcounty_label` and never matched. See [data-sources.md](data-sources.md).

The loader runs in one transaction and is not idempotent — truncate `facilities`
and the `facilities` rows of `import_quarantine` before re-running.

## Quarantine

`import_quarantine` is the reconciliation worklist, not an error log. Anything that cannot
be resolved to exactly one parent lands there with its payload, a reason and candidate
parents where the ambiguity is known. Nothing is dropped silently.

| Reason | Meaning |
|---|---|
| `parent_missing` | referenced parent absent from the source |
| `parent_ambiguous` | more than one candidate parent |
| `parent_level_mismatch` | parent resolved but at the wrong level |
| `validation_failed` | row failed a field constraint |
