# CHWR Interoperability API: worked examples

National-scoped and district-scoped examples for the eCHIS and National Data
Warehouse teams. Every response below is real output from the verification run
against the seeded national dataset (four CHWs in HOIMA, one in APAC).

Set the base URL to your CHWR deployment. The examples use
`https://chwr.example` as a placeholder.

## 1. Provision a client

A national client reads the whole register. A district client reads one
district. Provision on the CHWR host; the secret is printed once.

```bash
# National: for the eCHIS user-management tool and the National Data Warehouse
go run ./cmd/server -create-api-client echis_umt -api-name "eCHIS UMT"

# District-scoped: restricted to one district (58 = HOIMA)
go run ./cmd/server -create-api-client wh_hoima -api-name "NDW HOIMA" -api-district 58
```

## 2. Get a bearer token

```bash
curl -s -X POST https://chwr.example/api/v1/auth/token \
  -H 'Content-Type: application/json' \
  -d '{"client_id":"echis_umt","client_secret":"<secret>"}'
```

```json
{
  "token": "…",
  "access_token": "…",
  "token_type": "Bearer",
  "expires_in": 5400,
  "expires_at": "2026-09-26T18:30:00Z",
  "scope": "national"
}
```

`email`/`password` are accepted as aliases for `client_id`/`client_secret`,
which is what the eCHIS user-management configuration sends. Send the token on
every request: `Authorization: Bearer <token>`. A district client's token
response shows `"scope": "district"`.

## 3. National-scoped example

The national client sees every district. Here it returns all five CHWs, across
both APAC and HOIMA:

```bash
curl -s "https://chwr.example/api/v1/chws?active=true&limit=100" \
  -H "Authorization: Bearer $TOKEN"
```

```
count = 5    districts seen = [APAC, HOIMA]
```

One record in full (a CHEW), showing every field the eCHIS mapping reads plus
the extras the warehouse can use:

```json
{
  "id": 1,
  "hwId": 1,
  "fullName": "Grace Nakato",
  "nationalId": "CM90000000001A",
  "phone": "772100200",
  "birthDate": "1985-01-01",
  "birthDateApproximate": true,
  "gender": "female",
  "cadre": "chew",
  "status": "active",
  "active": true,
  "parish": "BUTEMA",
  "district": { "name": "HOIMA" },
  "subcounty": { "name": "BUHANIKA" },
  "village": "",
  "villages": ["KIFUMURA I", "KIFURANSA", "KIHUURA I", "KIHUURA II", "KIKONKO"],
  "position": { "facility": { "name": "Bombo Health Centre II" } },
  "supervisor": null,
  "supervision": {
    "received": true,
    "lastSupervisedOn": "2026-06-01",
    "overdue": true,
    "intervalDays": 30
  },
  "updatedAt": "2026-09-26T17:12:36Z"
}
```

Note the CHEW covers every village in her parish (`villages[]`), has no single
`village`, and no `supervisor`. A VHT record instead has a single-entry
`villages`, its own `village`, and a `supervisor` pointing at the CHEW at its
parish.

## 4. District-scoped example

The same request, authenticated as a district client, returns only that
district. This is the whole difference between the two: the request is
identical; the client decides the scope.

```bash
# token obtained as client wh_hoima (district 58)
curl -s "https://chwr.example/api/v1/chws?active=true&limit=100" \
  -H "Authorization: Bearer $HOIMA_TOKEN"
```

```
count = 4    districts seen = [HOIMA]
```

```bash
# token obtained as client wh_apac (district 76)
curl -s "https://chwr.example/api/v1/chws?active=true&limit=100" \
  -H "Authorization: Bearer $APAC_TOKEN"
```

```
count = 1    districts seen = [APAC]
```

A district client cannot widen its view with query parameters: passing
`?district=APAC` to the HOIMA client still returns only HOIMA. Scope is enforced
in the query itself, not in the filter.

## 5. eCHIS user provisioning filters

When creating a VHT user, the user-management tool narrows by district,
facility and village:

```bash
curl -s "https://chwr.example/api/v1/chws?active=true&district=HOIMA&village=KIFURANSA" \
  -H "Authorization: Bearer $TOKEN"
# -> the VHT whose village is KIFURANSA
```

The `id` returned is written onto the eCHIS contact as `chwr_id`, and with the
`chwr_registry_link` ownership attribute this prevents the same CHW being
provisioned twice.

## 6. National Data Warehouse extraction

Full walk, paged by HW-ID (stable while records are added):

```bash
# page 1
curl -s "https://chwr.example/api/v1/chws?limit=500" -H "Authorization: Bearer $TOKEN"
# response: { "chws": [...], "pagination": { "limit": 500, "count": 500, "hasMore": true, "nextCursor": 500 } }

# page 2: pass the previous nextCursor
curl -s "https://chwr.example/api/v1/chws?limit=500&cursor=500" -H "Authorization: Bearer $TOKEN"
```

Incremental refresh since the last run:

```bash
curl -s "https://chwr.example/api/v1/chws?updated_since=2026-09-01T00:00:00Z&limit=500" \
  -H "Authorization: Bearer $TOKEN"
```

`updatedAt` reflects both record and profile changes, so a profile edit is
picked up as well as a core-record edit.

## 7. Supervision

```bash
# VHTs a CHEW (HW-ID 1) oversees
curl -s "https://chwr.example/api/v1/chws/1/supervisees" -H "Authorization: Bearer $TOKEN"
# -> { "chws": [John Okello, Mary Auma, Peter Musoke], "supervisorHwId": 1, "count": 3 }

# the CHEW who supervises a VHT (HW-ID 3)
curl -s "https://chwr.example/api/v1/chws/3/supervisor" -H "Authorization: Bearer $TOKEN"
# -> Grace Nakato (hwId 1, cadre chew)

# CHWs overdue for supervision (needs SUPERVISION_INTERVAL_DAYS set)
curl -s "https://chwr.example/api/v1/chws?supervision=overdue" -H "Authorization: Bearer $TOKEN"
```

## 8. Error responses

```
401  { "error": "invalid or expired token", "status": 401 }   # no / bad / expired token, or wrong secret
404  { "error": "no such CHW in scope", "status": 404 }        # unknown or out-of-scope HW-ID
400  { "error": "updated_since must be an RFC3339 timestamp", "status": 400 }
```

The eCHIS tool treats a `404` as "no results", so an out-of-scope id reads as an
empty result there rather than an error.
