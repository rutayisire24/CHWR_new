# Application layer

Server-rendered Go over `net/http`. No framework, no ORM, no SPA, no JS build step. This
document covers the Go side; [data-model.md](data-model.md) covers the schema and
[rbac.md](rbac.md) covers authorization.

## Layering

```
cmd/server      →  config, pool, migrate, serve, shutdown
internal/http   →  decode, authorize, delegate, render.  No SQL.
internal/store  →  hand-written SQL, one file per aggregate.  Every method takes a Scope.
internal/auth   →  Scope, capabilities, argon2id, sessions, CSRF, middleware
internal/domain →  entities and sentinel errors.  No I/O at all.
internal/web    →  templates and static assets, embedded
```

The dependency runs `http → store → auth → domain`. `auth` does **not** import `store`,
even though the session middleware needs a database lookup: the lookup arrives through
an `Authenticator` interface that `store.Sessions` satisfies. Reversing that would make
`Scope` — which every store method takes — a cycle.

## Request lifecycle

Middleware is applied outermost first, in `internal/http/router.go`:

1. **`securityHeaders`** — CSP, `nosniff`, `DENY`, `Referrer-Policy`. The CSP is strict
   (`default-src 'self'`) because every script and stylesheet is our own file on our own
   origin. `fetch` to `/api/locations` is covered by the same `'self'`.
2. **`auth.CSRF`** — issues the double-submit token, calls `ParseForm`, and rejects a
   mutating request whose `csrf_token` field does not match the cookie. Handlers can read
   `r.PostForm` directly because this middleware has already parsed it.
3. **`auth.LoadUser`** — resolves the session cookie to a user and attaches it to the
   request context. It never rejects; public routes pass through it too.
4. **`auth.RequireAuth`** — rejects requests without a user, and pins a `must_reset` user
   to `/account/password` until they choose their own password.
5. **`auth.RequireCapability`** — the capability check for the route.

A handler then decodes, authorizes what middleware cannot (row-level and scope
questions), delegates to a store method with a `Scope`, and renders.

## Routes

| Method and path | Capability | Notes |
|---|---|---|
| `GET /healthz` | public | pings the pool; unhealthy if the database is unreachable |
| `GET /static/…` | public | embedded assets |
| `GET POST /login` | public | same message and cost for unknown email, wrong password and disabled account |
| `POST /logout` | signed in | deletes the session row, rotates the CSRF token |
| `GET /{$}` | signed in | dashboard |
| `GET POST /account/password` | signed in | self-service change; the only way past a forced reset |
| `GET /chws` | `chw.view` | register listing, `?status=active\|inactive` |
| `GET /chws/{id}` | `chw.view` | detail, with the ancestor breadcrumb |
| `GET POST /chws/new` | `chw.create` | |
| `GET /chws/{id}/edit`, `POST /chws/{id}` | `chw.update` | |
| `GET POST /chws/{id}/profile` | `chw.update` | the optional attributes, tools and service domains |
| `POST /chws/{id}/deactivate` | `chw.deactivate` | reason required by the handler |
| `POST /chws/{id}/reactivate` | `chw.deactivate` | |
| `GET /api/locations` | `chw.view` | `?level=&under=`, JSON, feeds the cascade |
| `GET /users`, `/users/new`, `/users/{id}` and their posts | `user.manage` | national admin only |
| `POST /users/{id}/status`, `/users/{id}/reset` | `user.manage` | |
| `GET /audit` | `audit.view` | scoped: a district manager reads their district's slice |
| `GET /` | — | catch-all 404 |

Mutations are `POST` to the resource path rather than `PUT`/`DELETE`: HTML forms speak
`GET` and `POST`, and a hidden `_method` field to pretend otherwise buys nothing.

## Forms and validation

Validation happens three times over, deliberately:

| Layer | Purpose |
|---|---|
| HTML attributes | `required`, `pattern`, `maxlength` — immediate feedback, no round trip |
| Handler | `domain.ValidationError` collects per-field messages; the form redisplays what was typed |
| Schema | CHECKs and triggers — the actual guarantee |

Two schema rules the profile makes reachable are pre-checked in the handler purely so the
operator gets an instruction instead of a 500: a supervising facility must be in the CHW's
own district, and a CHW who still reports to a facility cannot be moved to another
district until that attachment is changed. Both triggers stay exactly as they are — the
pre-check is a message, not the enforcement.

The handler layer exists because a CHECK violation is a 500, not a field message. It
never replaces the schema: `decodeCHW` checks a placement's level against the cadre, and
`chws_set_placement` still refuses the same case if the check is ever wrong.

Store methods return `domain.ErrNotFound`, `ErrConflict` or `ErrForbidden`; handlers map
those to a 404, a field message, or the forbidden page. A CHW outside the caller's scope
is `ErrNotFound`, never `ErrForbidden` — existence is itself scoped information.

## Templates

Parsed once at startup and embedded with `go:embed`; a broken template stops the process
rather than a request. Every page is `layout.html` plus its own file, so a page cannot
render without the chrome. `Render` buffers the whole page before writing a status, so a
template that fails halfway does not leave a half-written 200 on the wire.

Every template receives the same envelope: `.User`, `.Nav`, `.CSRFToken`, `.Flash`, and
`.Page` for whatever the handler supplies. Flashes are a one-shot cookie, read and
expired in the same response.

## The profile form

Every column on `chw_profiles` is nullable, and the form has to keep **"no" and "not
asked" apart** — an imported record answers none of these questions, and recording a "no"
it never gave would be a fabrication. Yes/no questions are therefore three radios, the
Go side carries `*bool`, and templates use the `deref` function rather than `{{if .Flag}}`,
which would be true for any non-nil pointer including one pointing at false.

The branches follow the source form: a question is asked, and its follow-ups appear only
for the answer that makes them meaningful. `profile.js` hides a branch **and disables its
fields**, because a disabled field is not submitted — which is what keeps a hidden branch
from posting the answer to a question nobody asked, and what keeps
`phone_branch_exclusive` and `incentive_details_require_yes` from ever seeing a crossing.

The handler assumes none of that. It re-derives every branch server-side: a primary phone
is read only when the CHW owns one, an incentive amount only when they receive one, a
supervision month only when supervision happened. A post that claims otherwise is
corrected to the safe reading rather than rejected, because the only way to produce one is
to tamper with the form, and a message about it would mean nothing to the person reading.

Two interlocks work the same way, in the UI for the operator and in the handler for the
data: a tool's condition is only asked about a tool the CHW holds, and training is a
subset of what they provide (`trained_implies_provides`).

The junction sets are **replaced, not diffed**. They are the answer to a multi-select, so
the submitted set is the new state and an unticked box means "no" — a diff would only add
a way for the form and the table to disagree.

## The cascading selects

`internal/web/static/locations.js` is the only JavaScript in the project. Each dependent
`<select>` declares the select it hangs off (`data-under`) and the level it fetches
(`data-level`), so the chain lives in the HTML rather than in the script.

The UI walks **district > subcounty > parish > village** and skips county. County is
mandatory in the data — subcounty codes are unique only within a county, and collapsing
the tier lost 732 subcounties to collisions — but nobody selects it, so it is derived
from the path server-side.

Two details that are easy to get wrong:

- Cadre decides the depth. A CHEW is placed at parish level, so the village select is
  hidden **and cleared and disabled** — hiding alone would still post its value.
- On edit, the server sends the existing placement down as `data-selected` and the script
  rebuilds the chain one level at a time. Those pending values are copied into a JS array
  at startup, because resetting a select has to forget its pending selection, and reading
  both from the same attribute makes the two jobs collide.

The feed answers "out of scope" and "no children" identically, with `[]`, so a district
user cannot map the country by probing ids.

## Configuration

`DATABASE_URL` (required), `ADDR` (`:8080`), `ENV` (`dev`|`prod`), `SHUTDOWN_TIMEOUT`
(`15s`). Every problem is reported at once, before anything is dialled. `ENV=prod` is
what puts `Secure` on the session, CSRF and flash cookies.

The server migrates on every start — one binary, one schema, no separate deploy step to
forget. `-migrate` stops after that; `-create-admin` provisions the first national
administrator and prints a one-use password.

## Testing

`go test ./...` covers the pure logic: the capability matrix, `Scope` and its SQL
fragment, argon2id round-trips and malformed-hash rejection, the password policy, the
open-redirect guard, the NIN pattern, and which posted field is the placement.

Anything touching SQL, triggers or scope is verified against a live database instead —
see the verification list in [roadmap.md](roadmap.md). The cascading selects need a real
browser: Selenium is on `:4444`, and reaches the app on the docker gateway address rather
than `127.0.0.1`.
