#!/usr/bin/env bash
# verify_api.sh — smoke and contract check for the read-only interoperability API.
#
# It authenticates as an API client, then asserts the response shape the eCHIS
# user-management tool and the National Data Warehouse depend on. It is
# structural (it checks fields and types, not specific names), so it runs
# against any populated register.
#
# Usage:
#   API_BASE=http://127.0.0.1:8080 API_CLIENT=echis_umt API_SECRET=... \
#     bash seed/verify_api.sh
#
# Provision a client first with:
#   go run ./cmd/server -create-api-client echis_umt -api-name "eCHIS UMT"
set -euo pipefail

BASE="${API_BASE:-http://127.0.0.1:8080}"
CLIENT="${API_CLIENT:?set API_CLIENT}"
SECRET="${API_SECRET:?set API_SECRET}"

say() { printf '  %-58s' "$1"; }
ok()  { echo "OK"; }

# --- token -----------------------------------------------------------------
say "POST /auth/token issues a bearer token"
TOKRESP=$(curl -fsS -X POST "$BASE/api/v1/auth/token" -H 'Content-Type: application/json' \
  -d "{\"client_id\":\"$CLIENT\",\"client_secret\":\"$SECRET\"}")
TOK=$(TOKRESP="$TOKRESP" python3 -c 'import os,json;print(json.loads(os.environ["TOKRESP"]).get("token",""))')
[ -n "$TOK" ] || { echo "FAIL: no token"; exit 1; }; ok

# --- list shape ------------------------------------------------------------
say "GET /chws returns the eCHIS contract shape"
LISTRESP=$(curl -fsS "$BASE/api/v1/chws?active=true&limit=25" -H "Authorization: Bearer $TOK")
LISTRESP="$LISTRESP" python3 <<'PY'
import os, json, re
d = json.loads(os.environ["LISTRESP"])
assert isinstance(d.get("chws"), list), "chws must be a list"
assert isinstance(d.get("pagination"), dict), "pagination must be present"
dob = re.compile(r"^\d{4}-\d{2}-\d{2}$")
for c in d["chws"]:
    for k in ("id", "fullName", "nationalId", "phone", "gender", "cadre", "parish", "villages"):
        assert k in c, f"missing field {k}"
    assert isinstance(c["id"], int), "id must be the numeric HW-ID"
    assert c["gender"] in ("male", "female"), c["gender"]
    assert c["cadre"] in ("vht", "chew"), c["cadre"]
    assert isinstance(c["district"], dict) and "name" in c["district"]
    assert isinstance(c["subcounty"], dict) and "name" in c["subcounty"]
    assert "name" in c["position"]["facility"]
    assert isinstance(c["villages"], list)
    if "birthDate" in c:
        assert dob.match(c["birthDate"]), c["birthDate"]
    if c["cadre"] == "chew":
        assert c["village"] == "", "a CHEW has no single village"
    else:
        assert len(c["villages"]) <= 1, "a VHT covers one village"
print("(%d CHWs checked) " % len(d["chws"]), end="")
PY
ok

# --- paging ----------------------------------------------------------------
say "GET /chws?limit=1 respects the limit"
PAGE=$(curl -fsS "$BASE/api/v1/chws?limit=1" -H "Authorization: Bearer $TOK")
PAGE="$PAGE" python3 -c 'import os,json;d=json.loads(os.environ["PAGE"]);assert len(d["chws"])<=1;assert d["pagination"]["limit"]==1'
ok

# --- auth failures ---------------------------------------------------------
say "GET /chws without a token is 401"
code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/v1/chws"); [ "$code" = 401 ] || { echo "FAIL ($code)"; exit 1; }; ok

say "GET /chws with a bad token is 401"
code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/v1/chws" -H 'Authorization: Bearer nope'); [ "$code" = 401 ] || { echo "FAIL ($code)"; exit 1; }; ok

say "POST /auth/token with a wrong secret is 401"
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/v1/auth/token" -H 'Content-Type: application/json' -d "{\"client_id\":\"$CLIENT\",\"client_secret\":\"wrong\"}"); [ "$code" = 401 ] || { echo "FAIL ($code)"; exit 1; }; ok

echo
echo "All API contract checks passed."
