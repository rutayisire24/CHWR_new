# ODK field mapping

Every field in the National CHWR XLSForm, mapped to its destination. Group prefixes
(`Hirechy-`, `other_individual-`, …) are stripped.

## Identity — `chws`

| ODK field | Type | Column | Notes |
|---|---|---|---|
| `nin` | text | `nin` | **not** `required` in the form; labelled "NIN / Alternative No". Nullable, unique where present, form regex kept as a CHECK |
| `first_name` | text | `first_name` | |
| `last_name` | text | `last_name` | labelled "Other Names", space-separated |
| `Sex` | select_one | `sex` | `male` / `female` |
| `Age` | integer | `age_years` | form range `> 17 and <= 99` |
| `chw_type` | select_multiple `type or_other` | `cadre` | see below |
| `village_select` / `parish_select` | select_one | `location_id` | which one applies depends on cadre |

### Cadre

The choice list is exactly `vht` (Village Health Team) and `chew` (Community Health
Extension Workers). The form's widget is `select_multiple` with `or_other`, but the
registry stores a single-valued enum by decision.

The importer therefore **rejects rather than truncates**: any row with multiple cadres, or
with `chw_type_other` text, goes to `import_quarantine`. Discarding that text would lose
the record of a CHW who did not fit the two-value model at collection time. Case variants
(`VHT`, `vht`, `CHEW`, `CHW`) are normalized; anything else is rejected with its row
number.

### Placement

The form collects the full cascade region > district > subcounty > parish > village. The
registry stores only `location_id`, at the level the cadre implies — parish for CHEWs,
village for VHTs — and derives every ancestor from `locations.path`.

Note the form's cascade has no county tier, which is the root of its hierarchy defects.
See [data-sources.md](data-sources.md).

## Contact — `chw_profiles`

The form asks `phones` ("Do you own a phone?") and then branches. This was not obvious
from the field names alone:

| ODK field | `relevant` | Column |
|---|---|---|
| `phones` | always | `owns_phone` |
| `phone_number` | `phones = yes` | `phone_primary` |
| `phone_reporting` | `phones = yes` | `phone_for_reporting` |
| `other_number` | `phones = **no**` | `phone_alternate` |

`other_number` is **not** an alternate line — it is the fallback for CHWs who own no
phone. The two are mutually exclusive, enforced by `phone_branch_exclusive`.

`phone_reporting` is a yes/no ("do you use this phone for reporting"), not a second
number.

## Service and capacity — `chw_profiles`

| ODK field | Type | Column | Notes |
|---|---|---|---|
| `service_year` | date, `year` appearance | `service_start_year` | year of first appointment |
| `facility` | select_one | `facility_id` | primary facility attached to |
| `households` | integer | `households_served` | form range `> 2 and <= 100000` |
| `education` | select_one | `education` | `none` / `ple` / `uce` / `uace` / `tertiary` |

## Language

`English` is `select_multiple` over **Ably Speak / Ably Read / Ably Write / None** — a
proficiency question, not yes/no. Stored as three booleans:

```
english_speak, english_read, english_write
```

A plain "speaks English" view is `english_speak OR english_read OR english_write`. This
keeps the requested yes/no answer without discarding two-thirds of the data.

`Other_language` is free text ("separate languages with commas"). The raw string is kept
in `other_languages_raw`; parsed values populate `chw_languages`.

## Tools

`Tool` is `select_multiple Tool or_other` (8 choices). `tool_functional` is
`select_multiple Tool` with `choice_filter=selected(${Tool},name)` — a **subset of tools
already held**.

So functionality is per tool, and lives on the junction row:

```sql
chw_tools(chw_id, tool_id, functional)
```

A single global flag could not express which tool is broken.

The choice list's `None` member means "no tools" and maps to the empty set. It is not
seeded as a tool.

## Financial

| ODK field | `relevant` | Column |
|---|---|---|
| `recieve_financial` | always | `receives_incentive` *(source typo not carried forward)* |
| `Frequency` | `recieve_financial = yes` | `incentive_frequency` |
| `financial_incentive` | `recieve_financial = yes` | `incentive_amount_ugx` |

Amount in UGX, form range 1,000–500,000. Frequency: `monthly` / `quarterly` / `annually` /
`one_off`. The branch is enforced by `incentive_details_require_yes`.

## Training and supervision

Three `select_multiple` questions over **one** 12-value `Service_domain` vocabulary,
nested by `choice_filter`:

| ODK field | Meaning | Destination |
|---|---|---|
| `Service_domains` | services the CHW offers | `chw_service_domains.provides` |
| `training` | subset trained on, last 2 years | `chw_service_domains.trained` |
| `support_supervision` | subset supervised on, last 2 years | **not imported** — see below |

One junction row per CHW per domain, with flags. `trained_implies_provides` encodes the
form's own `choice_filter` as a database invariant.

Corroboration: the workbook contains an unused choice list named `training` whose values
are literally `provide` / `trained_last_year` / `Supervised_mentored_last_year` — the form
author's own flag model, never wired up.

### Supervision divergence

By decision, supervision is stored as **year and month** on `chw_profiles`
(`received_supervision`, `last_supervised_on`), not per service domain.

**The ODK export cannot source this.** The form records supervision per domain and carries
no date, so `last_supervised_on` is NULL on every imported row and only fills in through
the web UI. The form's `support_supervision` answers are discarded on import.

## Dropped

| ODK field | Reason |
|---|---|
| `start`, `end`, `today`, `deviceid`, `phonenumber`, `username`, `audit` | provenance stripped by decision |
| `data_collector` | provenance stripped by decision |
| `chw_gps` (geopoint) | not captured by decision |
| `chw_type_other` | cadre is a closed two-value list |
| `region`, `district`, `subcounty` | derived from `location_id` via `locations.path` |

Dropping `today` means `chws.age_captured_on` falls back to the import date. Age is a
snapshot; without the collection date its staleness is only approximately known.
