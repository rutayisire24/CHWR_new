#!/usr/bin/env python3
"""Extract the Master Facility List to TSV for loading by seed/load_facilities.sql.

Source: data/MFL Updated - 21 feb.xlsx   (7,907 rows, one sheet)

Columns emitted, in order:
    row_no name subcounty_label district region level ownership authority

`row_no` is the 1-based spreadsheet row, kept so quarantined rows can be traced
back to a line in the workbook. The subcounty column is carried through raw and
unresolved: see the note in migrations/0004_facilities_mfl.sql.

Usage: python3 seed/extract_facilities.py [out_dir]      (default seed/out)
"""
import openpyxl, csv, os, sys

HERE = os.path.dirname(os.path.abspath(__file__))
MFL = os.path.join(HERE, "..", "data", "MFL Updated - 21 feb.xlsx")

# The workbook's own column order; asserted rather than assumed, because a
# reordered export would otherwise load names into the ownership column.
EXPECTED = ["name", "subcounty", "district", "region", "hflevel", "ownership", "authority"]


def main(out):
    os.makedirs(out, exist_ok=True)

    ws = openpyxl.load_workbook(MFL, read_only=True, data_only=True).active
    it = ws.iter_rows(values_only=True)
    header = [str(x).strip().lower() if x is not None else "" for x in next(it)][:7]
    if header != EXPECTED:
        raise SystemExit(f"unexpected columns: {header}\nexpected: {EXPECTED}")

    n = 0
    with open(os.path.join(out, "facilities.tsv"), "w", newline="") as f:
        w = csv.writer(f, delimiter="\t", quoting=csv.QUOTE_NONE, escapechar="\\")
        # enumerate from 2: row 1 is the header, so row_no matches the workbook
        for row_no, r in enumerate(it, start=2):
            if not r or r[0] in (None, ""):
                continue
            w.writerow([row_no] + [str(x).strip() if x is not None else "" for x in r[:7]])
            n += 1

    print(f"facilities={n}")


if __name__ == "__main__":
    main(sys.argv[1] if len(sys.argv) > 1 else os.path.join(HERE, "out"))
