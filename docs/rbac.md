# Roles and access control

## Roles

| Role | Scope | Purpose |
|---|---|---|
| `national_admin` | national | Full health worker management, user provisioning, audit |
| `national_viewer` | national | Read-only across the country |
| `district_manager` | one district | Health worker management within their district |
| `district_viewer` | one district | Read-only within their district |

The four roles are really a 2x2 of **capability** (manage / view) by **scope**
(national / one district). Modelling it that way keeps the permission code to a matrix
lookup plus a scope predicate.

## Capability matrix

| capability | national_admin | national_viewer | district_manager | district_viewer |
|---|---|---|---|---|
| `health_worker.view` | all | all | own district | own district |
| `health_worker.create` | yes | — | own district | — |
| `health_worker.update` | yes | — | own district | — |
| `health_worker.deactivate` | yes | — | own district | — |
| `health_worker.import` | all | — | own district | — |
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
func (s *Workers) List(ctx context.Context, sc auth.Scope, f Filter) (Page, error)
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

`health_workers.district_id` is denormalized by trigger from the worker's latest
deployment, whose own `district_id` is derived from its `location_id`. Every scoped query
is therefore a plain indexed equality on `health_workers.district_id` rather than an
ancestor walk. Placement and scope cannot drift apart, because the application never writes
`district_id` itself.

The anchor is the latest posting, not the open one, and it survives deactivation: a
district still sees the workers who left it. A worker transferred to another district
leaves the old district's view entirely — the record, its history and its profile
become a 404 there — while both districts keep their slice of the audit log.

For subtree queries that are not district-scoped, `locations.path` supports a prefix scan:

```sql
WHERE path LIKE (SELECT path FROM locations WHERE id = $1) || '%'
```

## Sessions

Server-side, in Postgres. The cookie carries a random 256-bit token; only its SHA-256
lands in `sessions.token_hash`. JWTs were rejected because staff departures require
instant revocation — see [decisions.md](decisions.md).

A session dies 12 hours after it was issued or 2 hours after its last request, whichever
comes first; `Sessions.Authenticate` enforces both in the same statement that refreshes
`last_seen_at`, and joins `users` on `status = 'active'`, so disabling an account ends it
mid-session even before the row is deleted. Disabling deletes the rows anyway, in the
same transaction as the status change. Changing a password deletes every session but the
current one — a password change is how a user responds to a suspected compromise, so it
has to evict the intruder.

## Implementation

| Concern | Where |
|---|---|
| Capability matrix | `internal/auth/capability.go` — mirrors the table above |
| `Scope` | `internal/auth/scope.go`; `Filter` returns the `AND district_id = $n` fragment |
| Session lookup | `auth.LoadUser`, backed by `store.Sessions` through an interface, so `auth` never imports `store` |
| Route guards | `auth.RequireAuth`, `auth.RequireCapability(cap, pages)` in `internal/http/router.go` |
| CSRF | `auth.CSRF` — double-submit cookie, rotated at login and logout |
| Forced reset | `RequireAuth` pins a `must_reset` user to `/account/password` |
| Worker routes | `health_worker.view` reads `/health-workers` and `/api/locations`; `health_worker.create` / `.update` / `.deactivate` gate the writes |
| Bulk import | `health_worker.import` gates every `/imports` route. It is its own capability rather than `health_worker.create` so that restricting bulk upload later is one line, and so that an upload reads as itself in the audit log. A batch is scoped by `import_batches.district_id`, recorded at upload: another district's report, its error file and its commit are all a 404 |

Two store methods take no `Scope`, both pre-authentication and both documented as such:
`Users.Credentials`, which the login handler uses, and `Sessions.Authenticate`, which is
the call that produces the principal a `Scope` is derived from. That list does not grow.

Handler-level safeguards that the schema cannot express: an account cannot disable
itself, and the last active `national_admin` cannot be demoted or disabled — otherwise
nobody can provision accounts and recovery needs a database console.

Scoping a worker write is two checks, not one, because `district_id` is derived by trigger
and so does not exist until the row does. The placement's district is checked before the
write, and the new deployment's own `district_id` is checked inside the same transaction afterwards;
a placement that would carry a worker out of the writer's district is rolled back. The
location feed answers "out of scope" and "no children" identically — an empty list — so a
district user cannot map the country by probing ids.
