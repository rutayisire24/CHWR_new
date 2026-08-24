# Roles and access control

## Roles

| Role | Scope | Purpose |
|---|---|---|
| `national_admin` | national | Full CHW management, user provisioning, audit |
| `national_viewer` | national | Read-only across the country |
| `district_manager` | one district | CHW management within their district |
| `district_viewer` | one district | Read-only within their district |

The four roles are really a 2x2 of **capability** (manage / view) by **scope**
(national / one district). Modelling it that way keeps the permission code to a matrix
lookup plus a scope predicate.

## Capability matrix

| capability | national_admin | national_viewer | district_manager | district_viewer |
|---|---|---|---|---|
| `chw.view` | all | all | own district | own district |
| `chw.create` | yes | — | own district | — |
| `chw.update` | yes | — | own district | — |
| `chw.deactivate` | yes | — | own district | — |
| `user.manage` | yes | — | — | — |
| `audit.view` | all | — | own district | — |
| `export` | all | all | own district | own district |

District managers cannot provision users. All account creation is centralised with
`national_admin`, which keeps the privilege-escalation surface at zero — a district role
can never mint another district role.

## Enforcement, in three layers

**1. Middleware** checks the capability for the route and rejects early.

**2. The store signature.** Every method in `internal/store` takes a `Scope`:

```go
func (s *CHWStore) List(ctx context.Context, sc auth.Scope, f Filter) ([]domain.CHW, error)
```

`Scope` carries either "national" or a `DistrictID`, and the query appends
`AND district_id = $n` for the district case. A handler cannot forget to pass it — the
call does not compile. This is the layer that actually prevents cross-district leaks;
middleware alone would not, because a missing check fails open.

**3. The schema.** `users_scope_matches_role` is a CHECK constraint:

```sql
CHECK ((role IN ('district_manager','district_viewer')) = (district_id IS NOT NULL))
```

A district user with no district, or a national user pinned to one, cannot be stored at
all. A trigger additionally verifies `district_id` points at a row whose level is
actually `district`. Both were verified to reject.

## Scoping mechanics

`chws.district_id` is denormalized from `location_id` by trigger, so every scoped query is
a plain indexed equality on `chws (district_id, status)` rather than an ancestor walk.
Placement and scope cannot drift apart, because the application never writes
`district_id` itself.

For subtree queries that are not district-scoped, `locations.path` supports a prefix scan:

```sql
WHERE path LIKE (SELECT path FROM locations WHERE id = $1) || '%'
```

## Sessions

Server-side, in Postgres. The cookie carries a random token; only its SHA-256 lands in
`sessions.token_hash`. JWTs were rejected because staff departures require instant
revocation — see [decisions.md](decisions.md).
