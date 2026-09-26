# Interoperability API (read-only)

The registry exposes a read-only HTTP/JSON API so other national systems can
consume CHW data while CHWR remains the single authoritative source. It is
additive: the browser application, its sessions and its data guarantees are
untouched. Two consumers are served today — the eCHIS user-management tool
(which provisions VHT users into eCHIS) and the National Data Warehouse — and
any future consumer uses the same surface.

Design decisions behind this API are recorded in the consultancy decision log
(D1–D13). In short: the layer is **read-only** and CHWR stays authoritative
(D1); the **HW-ID is the CHW's `id`** (D2); consumers are **national,
read-only** (D9); **all fields are exposed** over an authenticated TLS channel
(D10); the JSON contract is plain (FHIR deferred, D7).

Everything here is served under `/api/v1/`. It is mounted outside the browser
cookie/CSRF chain: consumers authenticate with a bearer token, not a session.

## Authentication

A consumer is provisioned once as an **API client** with a `client_id` and a
secret:

```bash
go run ./cmd/server -create-api-client echis_umt -api-name "eCHIS UMT"
# prints the secret ONCE; only its argon2id hash is stored.
```

A client is national by default. To restrict one to a district (optional, D9):
`-create-api-client somebody -api-name "…" -api-district <district_id>`.

The client exchanges its credentials for a short-lived **bearer token**:

```
POST /api/v1/auth/token
Content-Type: application/json

{ "client_id": "echis_umt", "client_secret": "<secret>" }
```

`email`/`password` are accepted as aliases for `client_id`/`client_secret`,
which is what the eCHIS user-management configuration sends. The response:

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

The token is valid for `API_TOKEN_TTL` (default 90 minutes). Send it on every
data request:

```
Authorization: Bearer <token>
```

Only the token's SHA-256 is stored, so a database leak yields no usable tokens;
revoking a client (or letting the token expire) cuts access at once. A wrong
secret, an unknown `client_id` and a disabled client are one answer — `401` —
and the unknown-client path still pays for one password verification, so timing
does not reveal which client_ids exist.

Every request that reaches the API is recorded in `api_access_log` (client,
method, path, query, status, row count, IP, time). CHWR audits writes in
`audit_log`; this is the equivalent record for reads.

## Endpoints

| Method & path | Purpose |
|---|---|
| `POST /api/v1/auth/token` | Exchange client credentials for a bearer token. |
| `GET /api/v1/chws` | Query CHWs. Filters + paging below. |
| `GET /api/v1/chws/{hwid}` | One CHW by HW-ID. |
| `GET /api/v1/chws/{hwid}/supervisees` | The VHTs a CHEW oversees (D5/D13). |
| `GET /api/v1/chws/{hwid}/supervisor` | The CHEW who supervises a VHT (D5/D13). |
| `GET /api/v1/health` | Unauthenticated liveness; returns no data. |

### `GET /api/v1/chws` — query parameters

| Parameter | Meaning |
|---|---|
| `active` | `true` or `false`; omit for both statuses. |
| `district` | District name (exact, case-insensitive). |
| `district_id` | District id, as an alternative to `district`. |
| `facility` | Supervising-facility name. |
| `village` | Village name (matches a VHT's own village). |
| `cadre` | `vht` or `chew`. |
| `supervision` | `overdue` or `never` (targeted supervision, D13). |
| `updated_since` | RFC3339 timestamp; returns records changed since (record or profile). |
| `limit` | Page size, 1–1000 (default 50). |
| `cursor` | Keyset cursor: pass the previous page's `nextCursor`. |

The eCHIS user-management tool sends `active`, `district`, `facility` and
`village`. The National Data Warehouse uses `limit` + `cursor` for a full walk
and `updated_since` for incremental refresh.

### Response

```json
{
  "chws": [
    {
      "id": 12345,
      "hwId": 12345,
      "fullName": "Grace Nakato",
      "firstName": "Grace",
      "lastName": "Nakato",
      "nationalId": "CM90210987654X",
      "phone": "772100200",
      "birthDate": "1985-01-01",
      "birthDateApproximate": true,
      "gender": "female",
      "cadre": "vht",
      "status": "active",
      "active": true,
      "parish": "BUTEMA",
      "district": { "name": "HOIMA" },
      "subcounty": { "name": "BUHANIKA" },
      "village": "KIFURANSA",
      "villages": ["KIFURANSA"],
      "position": { "facility": { "name": "Bombo HC II" } },
      "supervisor": { "hwId": 1, "name": "John Chew" },
      "supervision": {
        "received": true,
        "lastSupervisedOn": "2026-09-01",
        "overdue": false,
        "intervalDays": 30
      },
      "updatedAt": "2026-09-26T17:12:36Z"
    }
  ],
  "pagination": { "limit": 50, "count": 1, "hasMore": false }
}
```

`pagination.nextCursor` is present when `hasMore` is true; pass it as `cursor`
for the next page. A single-record endpoint returns the CHW object directly.

### Field notes

- **`id` / `hwId`** — the HW-ID (decision D2), stable across systems. eCHIS
  writes it onto the CHW contact as `chwr_id`, which is the deduplication key.
- **`birthDate` / `birthDateApproximate`** — a real captured date of birth when
  present, otherwise an approximation of 1 January of the estimated birth year
  from age and its capture date (decision D4). `birthDateApproximate: true`
  marks the estimate; treat it as year-accurate, not an exact clinical date. It
  is absent entirely when no age is recorded — never fabricated.
- **`villages`** — a VHT's single village; for a CHEW, **every village under the
  CHEW's parish** (decision D5). `village` is the CHW's own village (empty for a
  CHEW).
- **`supervisor` / `supervision`** — see below (decision D13).
- Enumerations are stable: `gender` is `male`/`female`; `cadre` is `vht`/`chew`;
  `status` is `active`/`inactive`.

## Supervisory assignment and targeted supervision (D13)

Supervision is derived from the administrative hierarchy, which already encodes
the relationship: a CHEW is placed at a parish and a VHT at a village under some
parish, so **the CHEW placed at a VHT's parish is that VHT's supervisor**, and a
CHEW's supervisees are the VHTs in its parish's villages.

- Each VHT record carries `supervisor: { hwId, name }` (null when no CHEW is
  placed at its parish). Each CHEW record has `supervisor: null`.
- `GET /chws/{hwid}/supervisees` lists a CHEW's VHTs; `GET /chws/{hwid}/supervisor`
  returns a VHT's CHEW.
- **Targeted supervision.** `supervision.lastSupervisedOn` is the last recorded
  supervision month (null if none). `supervision.overdue` is computed against
  `SUPERVISION_INTERVAL_DAYS`: a CHW is overdue if never supervised or last
  supervised longer ago than the interval. Until the Ministry sets the interval
  (D13), `SUPERVISION_INTERVAL_DAYS` is 0 and `overdue` is null (the interval is
  unknown, not the CHW compliant). Filter with `?supervision=overdue` or
  `?supervision=never`.
  Note: supervision is empty on every bulk-imported record by construction (the
  source form carries no supervision date), so these read near-empty until
  supervision is captured through the web UI.

## Errors

JSON with a `status` field and standard HTTP codes:

```json
{ "error": "invalid or expired token", "status": 401 }
```

`400` bad request (e.g. a malformed `updated_since`), `401` unauthenticated,
`404` no such CHW in scope, `500` internal. The eCHIS consumer treats `404` as
"no results", so an out-of-scope or unknown id reads as an empty result there.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `API_TOKEN_TTL` | `90m` | Bearer-token lifetime (Go duration). |
| `SUPERVISION_INTERVAL_DAYS` | `0` | Days between supervision visits for the overdue calculation; 0 disables it (D13). |

## The National Data Warehouse (D8)

The warehouse reads CHWR through this same API; there is no separate pipeline in
CHWR. Hand the warehouse team a national API client and these notes. For a full
load, page with `limit` + `cursor` (ordering is by `id`, so a keyset walk is
stable while records are added). For incremental refresh, poll with
`updated_since` set to the previous run's start time; `updatedAt` reflects both
record and profile changes. Every record carries the HW-ID as the stable join
key and the full administrative placement (district, subcounty, parish,
village) for geographic analysis.

## Verifying a deployment

Provision a client, start the server, then run the contract check:

```bash
go run ./cmd/server -create-api-client echis_umt -api-name "eCHIS UMT"   # note the secret
API_BASE=http://127.0.0.1:8080 API_CLIENT=echis_umt API_SECRET=<secret> \
  bash seed/verify_api.sh
```

It asserts the token flow, the response shape the consumers depend on, paging,
and that unauthenticated and bad-credential requests are refused.
