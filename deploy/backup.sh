#!/bin/sh
# Nightly backup of the register.
#
#   sudo install -m 0755 deploy/backup.sh /opt/chwr/backup.sh
#   sudo install -d -o chwr -g chwr /var/backups/chwr
#
# Driven by deploy/chwr-backup.timer. Restore is documented in docs/deploy.md —
# a backup nobody has restored is a hope, not a backup.
#
# audit_log is not a log here: it doubles as the CHW change history, and it is
# the only record of who changed what. A backup that skipped it to save space
# would lose the register's provenance while appearing to have worked.
set -eu

: "${DATABASE_URL:?DATABASE_URL is required}"
DIR="${BACKUP_DIR:-/var/backups/chwr}"
KEEP="${BACKUP_KEEP_DAYS:-30}"

mkdir -p "$DIR"
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
OUT="$DIR/chwr-$STAMP.dump"

# Custom format: compressed, and restorable table by table with pg_restore.
pg_dump --format=custom --no-owner --no-privileges --file="$OUT.partial" "$DATABASE_URL"

# Rename only once pg_dump has succeeded, so a half-written file is never
# mistaken for a backup.
mv "$OUT.partial" "$OUT"

# Prove it is readable before trusting it. pg_restore --list fails on a
# truncated or corrupt archive, which is the failure this catches.
pg_restore --list "$OUT" > /dev/null

find "$DIR" -name 'chwr-*.dump' -type f -mtime "+$KEEP" -delete
find "$DIR" -name '*.partial' -type f -mtime +1 -delete

echo "backed up to $OUT ($(du -h "$OUT" | cut -f1))"
