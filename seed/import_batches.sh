#!/usr/bin/env bash
# Upload and commit the converted district files against a running CHWR server.
#
#   BASE=https://chwr.example.org EMAIL=you@example.org PASSWORD=... ./import.sh out/*.csv
#
# One file at a time: a failure costs one district, not the country. Each file
# is staged, the report is read, and the batch is committed only if the server
# staged at least one ready row.
set -uo pipefail

BASE="${BASE:-http://127.0.0.1:8080}"
JAR="$(mktemp)"; trap 'rm -f "$JAR"' EXIT
CURL=(curl -sS --cookie-jar "$JAR" --cookie "$JAR" --max-time "${TIMEOUT:-1800}")

csrf() { grep -o 'name="csrf_token"[^>]*value="[^"]*"' | head -1 | sed 's/.*value="\([^"]*\)".*/\1/'; }

echo "==> signing in to $BASE as $EMAIL"
T=$("${CURL[@]}" "$BASE/login" | csrf)
"${CURL[@]}" -o /dev/null -X POST "$BASE/login" \
  --data-urlencode "csrf_token=$T" --data-urlencode "email=$EMAIL" \
  --data-urlencode "password=$PASSWORD"

# A freshly provisioned account must choose its own password before any route works.
if [ -n "${NEW_PASSWORD:-}" ]; then
  echo "==> setting a new password (account was provisioned with must_reset)"
  T=$("${CURL[@]}" "$BASE/account/password" | csrf)
  "${CURL[@]}" -o /dev/null -X POST "$BASE/account/password" \
    --data-urlencode "csrf_token=$T" --data-urlencode "current=$PASSWORD" \
    --data-urlencode "new=$NEW_PASSWORD" --data-urlencode "confirm=$NEW_PASSWORD"
fi

if ! "${CURL[@]}" "$BASE/imports" | grep -q 'name="csrf_token"'; then
  echo "!! sign-in failed — /imports did not come back as a signed-in page" >&2; exit 1
fi

ok=0; skipped=0
for f in "$@"; do
  base=$(basename "$f")
  T=$("${CURL[@]}" "$BASE/imports" | csrf)
  loc=$("${CURL[@]}" -o /dev/null -w '%{redirect_url}' -X POST "$BASE/imports" \
        -F "csrf_token=$T" -F "file=@$f;type=text/csv")
  id="${loc##*/}"
  if [ -z "$id" ]; then echo "!! $base: upload did not stage a batch"; skipped=$((skipped+1)); continue; fi

  report=$("${CURL[@]}" "$BASE/imports/$id")
  ready=$(printf '%s' "$report" | grep -oiE '([0-9,]+) (rows? )?ready' | head -1)
  echo "==> $base -> batch $id  ${ready:-(see report)}"

  if [ "${DRY_RUN:-0}" = "1" ]; then skipped=$((skipped+1)); continue; fi

  T=$(printf '%s' "$report" | csrf)
  "${CURL[@]}" -o /dev/null -X POST "$BASE/imports/$id/commit" --data-urlencode "csrf_token=$T"
  echo "    committed batch $id"
  ok=$((ok+1))
done
echo "==> $ok committed, $skipped skipped"
