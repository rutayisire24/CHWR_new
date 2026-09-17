#!/usr/bin/env bash
# Package converted import files for transfer to another register, and verify
# such a package against the register it is about to be loaded into.
#
#   seed/package_import.sh pack   ~/chwr-import  chwr-import.tar.gz
#   seed/package_import.sh verify chwr-import.tar.gz
#
# The CSVs are portable because `location_code` is the official code path —
# a concatenation of the codes in data/Village-Admin Units 06-08-2026.xlsm —
# and carries no database id. That holds only while both registers were seeded
# from the same workbook, which is exactly what the fingerprint checks.
#
# The package carries NINs and phone numbers for tens of thousands of people.
# Move it over SSH, load it, delete it. Set RECIPIENT (a gpg key) or PASSPHRASE
# to encrypt it at rest.
set -euo pipefail

fingerprint() {
    [ -n "${DATABASE_URL:-}" ] || { echo "DATABASE_URL is not set" >&2; exit 1; }
    psql "$DATABASE_URL" -tAc \
      "select 'locations '||count(*)||' '||md5(string_agg(code_path,'' order by code_path))
         from locations where code_path is not null"
    psql "$DATABASE_URL" -tAc \
      "select 'facilities '||count(*)||' '||md5(string_agg(district_id||':'||name,'|' order by district_id,name))
         from facilities"
}

case "${1:-}" in
pack)
    SRC="${2:?usage: pack <converted-dir> <out.tar.gz>}"
    OUT="${3:?usage: pack <converted-dir> <out.tar.gz>}"
    [ -f "$SRC/manifest.json" ] || { echo "$SRC has no manifest.json — is it a converter output?" >&2; exit 1; }

    STAGE="$(mktemp -d)"; trap 'rm -rf "$STAGE"' EXIT
    PKG="$STAGE/chwr-import"; mkdir -p "$PKG"

    # national.csv is deliberately left out: at 14 MB the importer refuses it by
    # name, so shipping it only invites someone to try.
    for f in "$SRC"/*.csv; do
        [ "$(basename "$f")" = national.csv ] && continue
        cp "$f" "$PKG/"
    done
    cp "$SRC/manifest.json" "$PKG/"

    {
        echo "# Built $(date -u +%Y-%m-%dT%H:%M:%SZ) by $(git -C "$(dirname "$0")" rev-parse --short HEAD 2>/dev/null || echo unknown)"
        # From the manifest, not from wc: other_languages is free text and a
        # quoted cell may hold a newline, so physical lines overcount rows.
        echo "rows $(python3 -c "import json,sys;print(sum(x['rows'] for x in json.load(open(sys.argv[1]))))" "$PKG/manifest.json")"
        echo "districts $(python3 -c "import json,sys;print(len(json.load(open(sys.argv[1]))))" "$PKG/manifest.json")"
        fingerprint
    } > "$PKG/FINGERPRINT"

    ( cd "$PKG" && sha256sum ./*.csv manifest.json > SHA256SUMS )
    tar -czf "$OUT" -C "$STAGE" chwr-import
    sha256sum "$OUT" > "$OUT.sha256"

    if [ -n "${RECIPIENT:-}" ]; then
        gpg --yes --encrypt --recipient "$RECIPIENT" "$OUT" && rm -f "$OUT"
        echo "==> $OUT.gpg (encrypted to $RECIPIENT)"
    elif [ -n "${PASSPHRASE:-}" ]; then
        gpg --yes --batch --symmetric --passphrase "$PASSPHRASE" "$OUT" && rm -f "$OUT"
        echo "==> $OUT.gpg (symmetric)"
    else
        echo "==> $OUT  (UNENCRYPTED — it carries NINs and phone numbers)"
    fi
    cat "$PKG/FINGERPRINT"
    ;;

verify)
    PKGF="${2:?usage: verify <out.tar.gz>}"
    STAGE="$(mktemp -d)"; trap 'rm -rf "$STAGE"' EXIT
    tar -xzf "$PKGF" -C "$STAGE"
    D="$STAGE/chwr-import"
    ( cd "$D" && sha256sum -c SHA256SUMS >/dev/null ) && echo "checksums   ok"

    echo "--- built against ---"; grep -v '^#' "$D/FINGERPRINT"
    echo "--- this register ---"; fingerprint
    if diff <(grep -E '^(locations|facilities) ' "$D/FINGERPRINT") <(fingerprint) >/dev/null; then
        echo "fingerprint ok — same hierarchy, the location codes will resolve"
    else
        echo "fingerprint MISMATCH — this register was seeded differently." >&2
        echo "Re-run convert_odk_export.py against it rather than loading these files." >&2
        exit 1
    fi
    ;;
*)
    sed -n '2,14p' "$0"; exit 1 ;;
esac
