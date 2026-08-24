#!/usr/bin/env python3
"""Extract the national hierarchy to TSV for loading by seed/load_hierarchy.sql.

Sources, both checked in:
  data/Village-Admin Units 06-08-2026.xlsm   districts downward, with official codes
  data/district_region.tsv                   the 15 regions and their district map

The region map used to be lifted from the ODK workbook's `choices` sheet. It is
now a plain checked-in TSV supplied by the project owner, so the hierarchy is
reproducible from a clean clone. The two files agree exactly: all 146 districts,
all 15 regions, no disagreement on a single pairing.

Usage: python3 seed/extract_units.py [out_dir]   (default seed/out)
"""
import openpyxl, csv, re, sys, os

norm = lambda s: re.sub(r'[^a-z0-9]', '', (s or '').lower())
HERE = os.path.dirname(os.path.abspath(__file__))
UNITS = os.path.join(HERE, "..", "data", "Village-Admin Units 06-08-2026.xlsm")
DREGION = os.path.join(HERE, "..", "data", "district_region.tsv")


def main(out):
    os.makedirs(out, exist_ok=True)

    ws = openpyxl.load_workbook(UNITS, read_only=True, data_only=True).active
    it = ws.iter_rows(values_only=True); next(it)
    n = 0
    with open(os.path.join(out, "units.tsv"), "w", newline="") as f:
        w = csv.writer(f, delimiter="\t", quoting=csv.QUOTE_NONE, escapechar="\\")
        for r in it:
            if not r or r[0] in (None, ""):
                continue
            w.writerow([str(x).strip() if x is not None else "" for x in r[:10]])
            n += 1

    # Region slug is the normalized label: 'West Nile' -> 'westnile'. Districts
    # are keyed on the same normalization, which is what load_hierarchy.sql
    # joins on — the two files spell several districts differently.
    regions, dmap = {}, {}
    with open(DREGION) as f:
        for line_no, line in enumerate(f, start=1):
            line = line.rstrip("\n")
            if not line.strip():
                continue
            parts = line.split("\t")
            if len(parts) != 2:
                raise SystemExit(f"{DREGION}:{line_no}: expected 'district<TAB>region'")
            district, region = (p.strip() for p in parts)
            regions[norm(region)] = region
            if norm(district) in dmap:
                raise SystemExit(f"{DREGION}:{line_no}: district {district!r} listed twice")
            dmap[norm(district)] = norm(region)

    with open(os.path.join(out, "regions.tsv"), "w") as f:
        for slug in sorted(regions):
            f.write(f"{slug}\t{regions[slug]}\n")
    with open(os.path.join(out, "dregion.tsv"), "w") as f:
        for d in sorted(dmap):
            f.write(f"{d}\t{dmap[d]}\n")

    print(f"units={n} regions={len(regions)} district_region_map={len(dmap)}")


if __name__ == "__main__":
    main(sys.argv[1] if len(sys.argv) > 1 else os.path.join(HERE, "out"))
