# Source data

Four sources feed the registry. Two of them disagree, and the disagreement matters.

| Source | Supplies | Quality |
|---|---|---|
| `data/Village-Admin Units 06-08-2026.xlsm` | district > county > subcounty > parish > village, with official codes | clean |
| `data/district_region.tsv` | the 15 regions and all 146 district-to-region pairings | clean |
| `data/MFL Updated - 21 feb.xlsx` | 7,907 health facilities | clean at district level, unusable below it |
| National CHWR ODK XLSForm | all CHW field definitions | defective hierarchy, sound field definitions |

**Everything the loaders need is checked in.** The ODK workbook is documentation of the
form's semantics now, not an input: its region map was replaced by
`data/district_region.tsv` and its facility list (7,896 rows, parented by a
`subcountyfilter` column holding mostly district slugs) by the MFL.

## District-to-region map

A plain 146-line TSV, `district<TAB>region`, supplied by the project owner. It was
checked against the map previously extracted from the ODK `choices` sheet: **the same 146
districts, the same 15 regions, and not one disagreement on a pairing** — the files differ
only in row order. Adopting it changes no loaded data and removes the last reason to hold
the ODK workbook.

It spells two districts the ODK way — `Luwero` and `Sembabule` — so the hierarchy loader's
two existing aliases still apply.

## Admin units workbook — authoritative

A single denormalized sheet, 71,207 rows, one per village:

```
district_code district_name ea_code ea_name scounty_code scounty_name
parish_code parish_name village_code village_name village_id lc_voters women_voters
```

`village_id` is the concatenated code path: `001` + `003` + `01` + `003` + `001` =
`00100301003001` (3+3+2+3+3 = 14 digits).

Profiled across all rows:

- 146 districts, 353 counties, 2,198 subcounties, 10,716 parishes, 71,207 villages
- every code maps to exactly one name, at every level
- zero duplicate village rows
- `village_id` equals the concatenated codes in **all 71,207 rows**
- exactly one sibling name collision nationwide (see below)

The `ea_*` columns are the **county** tier. `lc_voters` and `women_voters` are not
imported.

### The one defect

Parish `095/235/04/038` contains two villages both named `BUHOBA A`, codes `002` and
`003`. This is kept rather than deduplicated; `locations` is unique on `(parent_id, code)`
and deliberately not on `(parent_id, name)`.

## ODK workbook — hierarchy is unusable

Its `choices` sheet also carries a hierarchy, but it omits the county tier and is broken
in ways foreign keys would reject:

| Defect | Scale |
|---|---|
| Subcounties referenced by parishes but absent from the subcounty list | 173 subcounties, orphaning 832 parishes and ~5,900 villages |
| Duplicate parish names, so villages match several parents by name | 390 names, 4,673 villages affected |
| Village rows populating `parishfilter` while the form filters on `villagefilter` | 227 villages, silently unrenderable |
| Facilities keyed by district under a column named `subcountyfilter` | 7,846 of 7,896 |
| No official codes; inconsistent slugs (`kibaalesubcounty`, `kakumiro_kasambya_subcounty_`, mixed case) | throughout |

The root cause of the first two is the missing county tier. ODK matches choice filters on
*name*, and subcounty names are unique only within a county — so collapsing the tier
collides 2,198 subcounties down to 1,466 and 10,716 parishes down to 9,807.

**Consequence: roughly 6% of Uganda's villages cannot be selected in the current form.**

## What is still taken from ODK

**Regions.** The admin units file starts at district. The 15 regions and the
district-to-region mapping come from the ODK `district` list's `regionfilter` column.
144 of 146 districts match by normalized name; the loader aliases the two that do not:

| Admin units | ODK |
|---|---|
| `LUWEERO` | `luwero` |
| `SSEMBABULE` | `sembabule` |

**Facilities — no longer used.** 7,896 rows whose parent column is named `subcountyfilter`
but holds district slugs for 7,846 of them, with only 21 resembling subcounty names. The
MFL supersedes this list entirely: it covers the same ground with facility level and
ownership attached, and every one of its rows resolves to a district. See the MFL section
below.

**All CHW field definitions** — constraints, choice lists and branching logic. These are
sound and are documented in [odk-mapping.md](odk-mapping.md).

## Direction of authority

The registry should become the authoritative hierarchy, with the ODK form regenerated
*from* it. Today the dependency runs the wrong way, and the form cannot reach ~6% of
villages. This is a roadmap item, not a blocker.

## Master Facility List — facilities

`data/MFL Updated - 21 feb.xlsx`, one sheet, 7,907 rows:

```
name  subcounty  district  region  hflevel  ownership  authority
```

There is **no facility code**. Identity is `(district_id, name)`, which is why the
duplicates below have to be quarantined rather than distinguished.

Profiled against the loaded hierarchy:

- **district: 7,907 / 7,907 resolve** — needing only the two aliases the hierarchy
  loader already carries (`Luwero` > LUWEERO, `Sembabule` > SSEMBABULE). No district
  name is ambiguous.
- **subcounty: 3,696 resolve, 4,201 miss, 10 ambiguous** — the column holds Town
  Councils, City Divisions and newer units the admin-units file does not carry under
  those names. This is why facilities are parented to district and the raw label is
  kept, unresolved, in `facilities.subcounty_label`.
- ownership: 3,469 PFP, 3,389 GOV, 1,048 PNFP, 1 mislabelled `Bank of Uganda`
- hflevel: 17 distinct values including 18 blanks, `RRH`, `NRH`, `RBB`, `NBB`, `BCDP`
  and the same `Bank of Uganda` row — stored as free text rather than an enum

### The twelve collisions

Twelve rows share a `(district, name)` with an earlier row. Five are the same row
listed twice; seven are genuinely different premises separated only by a subcounty
this loader does not resolve — `Chinese Clinic` appears in both Makindye and Nakawa
divisions of Kampala. **All twelve are private clinics or drug shops; no government
facility collides.** The lowest workbook row loads, the rest go to
`import_quarantine` with the row that displaced them.
