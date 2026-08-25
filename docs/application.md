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
2. **`auth.CSRF`** — issues the double-submit token, parses the body, and rejects a
   mutating request whose `csrf_token` field does not match the cookie. Handlers can read
   `r.PostForm` directly because this middleware has already parsed it. A
   `multipart/form-data` body takes `ParseMultipartForm` rather than `ParseForm`, which
   does not read one: without that branch the token field of a file upload would look
   missing and every upload would be refused with a 403 before its handler ran. Multipart
   bodies are capped at `auth.MaxMultipartBytes` and their temp files are removed by the
   middleware, so no handler can forget to.
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
| `GET /{$}` | signed in | dashboard: scoped counts, five charts, two tables |
| `GET POST /account/password` | signed in | self-service change; the only way past a forced reset |
| `GET /chws` | `chw.view` | listing: `?q=`, `?cadre=`, `?status=`, a location id, `?after=` / `?before=` |
| `GET /chws/{id}` | `chw.view` | detail, with the ancestor breadcrumb |
| `GET POST /chws/new` | `chw.create` | |
| `GET /chws/{id}/edit`, `POST /chws/{id}` | `chw.update` | |
| `GET POST /chws/{id}/profile` | `chw.update` | the optional attributes, tools and service domains |
| `POST /chws/{id}/deactivate` | `chw.deactivate` | reason required by the handler |
| `POST /chws/{id}/reactivate` | `chw.deactivate` | |
| `GET /api/locations` | `chw.view` | `?level=&under=`, JSON, feeds the cascade |
| `GET /chws/export.csv` | `export` | the listing's own filters and Scope, streamed as CSV |
| `GET /imports` | `chw.import` | upload form, column reference, recent batches (scoped, pending first) |
| `GET /imports/template.csv` | `chw.import` | blank template; a district user's carries their district |
| `POST /imports` | `chw.import` | multipart; validates and stages. Writes nothing to `chws` |
| `GET /imports/{id}` | `chw.import` | the report: tallies, refusals with reasons, the decision |
| `GET /imports/{id}/errors.csv` | `chw.import` | every refused row, the file's own columns plus `error` |
| `POST /imports/{id}/commit` | `chw.import` | `?skip_duplicates` — writes the ready rows |
| `POST /imports/{id}/discard` | `chw.import` | marks the batch; the staged rows stay |
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

## The dashboard

`GET /` is a read-only summary of the same register the listing shows, through the same
`Scope`. `internal/store/stats.go` holds every query; the handler runs them and marshals
one payload for the canvases.

**The lede is one hero figure and three tiles.** Total CHWs, with an active/inactive
meter; then VHTs, CHEWs, and the areas reached out of the areas that exist. Every tile is
a count of the register itself, and each links into the listing filtered to it.

Completeness measures are deliberately *not* tiles. A "% of records carrying a NIN" or a
"% supervised" reads as a fact about community health workers when it is a fact about the
register's own filling-in, and a headline `0%` in particular reads as "nobody is
supervised" when it means "almost nobody has been asked". Those measures live in the
Record completeness chart, where the whole row of them sits together and the framing is
explicit. Supervision is the sharpest case: the ODK form records it per service domain and
carries no date, so `received_supervision` is NULL on every imported row and fills in only
through the web UI — it will read near zero for as long as that stays true.

**Scope reaches the charts, not just the tables.** Each query takes a `Scope` and puts it
in its own `WHERE`, so a district manager's dashboard is their district's dashboard. It
also *changes tier*: nationally the chart groups by region and the league table by
district; inside a district those become subcounty and parish. The country's regions say
nothing to someone who can only open one district.

**Grouping reads the ancestor out of `locations.path`.** `path` is
`/region/district/county/subcounty/parish/village/`, so `split_part(l.path, '/', n)` is
the ancestor id at any tier — one hash join over the register instead of a prefix join
against all 84,635 locations. `segment()` maps a `Level` to `n` and is unit-tested,
because an off-by-one there would not fail: it would group by the wrong tier and still
draw a chart.

**Zero-filled from the hierarchy side.** `Areas` and `Reach` join outward from
`locations`, not inward from `chws`, so a district with nobody in it is a row and counts
against coverage. That is the finding, not a row to omit.

**Completeness counts answers, not yeses.** Every profile column is nullable and "no" and
"not asked" are different answers, so the completeness bars count records carrying an
answer of any kind. The two junction tables are folded to one row per CHW and joined
rather than probed with `EXISTS` per row — the same answer for about a fortieth of the
work at 24,000 records.

### Charts

Chart.js is vendored under `internal/web/static/vendor/` — no CDN, no build step, and the
CSP would refuse both. The figures reach the page inside a
`<script type="application/json">` block: a data block, never executed, so the policy that
forbids inline script is not bent to draw a chart. `json.Marshal` escapes `<` to `\u003c`,
so the payload cannot close the element it sits in.

The CSP also forbids the `style` attribute, which means a width that *is* data cannot be
carried by one. The meters and the league table's inline bars are therefore SVG, where
`width` is a presentational attribute.

Every chart card carries a `<details>` table of the same numbers, server-rendered. That is
the accessibility twin — no value is reachable only by hovering a canvas — and it doubles
as the no-JavaScript rendering.

The palette lives in `app.css` as `--viz-*`, assigned by the job the colour does:
identity, magnitude, whole-and-part, or de-emphasis. Slot 1 is the brand blue itself. No
chart carries more than two identities, and the status green/amber/red stay out entirely —
on this site green means one thing, "this CHW is active", and a chart that borrowed it for
a series would spend that meaning.

## The export

`GET /chws/export.csv` is the listing as a file. It takes the same query string, decodes
it with the same `decodeFilter`, and hands the result to `store.Export.Rows` with the
caller's `Scope` — so a filter that selects 20 records on screen exports those 20, and a
district user exports their district. The page and the file cannot disagree, because they
are built from the same predicate.

The cursor is dropped. Paging is for a reader moving through a page at a time; an export
of "page three" would be a file nobody asked for.

Rows are streamed through a callback and flushed every 500, so the whole national register
never exists in memory at once and a large download starts arriving immediately. An error
part-way through cannot become an error page — the status went out with the first byte — so
the file ends short and the log carries why, the same bargain `errors.csv` makes.

The columns the importer reads keep the importer's own names, so a file that comes out of
the register can go back into it: an exported row re-imported under a different name comes
back with its placement, its phone, its facility, its education, both junction sets and
`english_write` as a recorded *false* rather than a null. The rest — the id, the derived
placement, the status and the timestamps — are named as unknown by an import and ignored,
which is the right answer for a column an upload must not be able to set. Supervision is
among them: the export shows it, and an import cannot supply it, because the source form
carries no date.

## Searching and paging the register

One search box, not two. Whether the input is a name or a NIN is decided by its shape —
two leading letters and a digit, no spaces or punctuation, means NIN — because a radio
button asking the user to classify their own input is a button they get wrong. A NIN
matches from the start, the way someone reads one off a form; a name matches anywhere
within `first_name || ' ' || last_name`, which is the exact expression `chws_name_trgm`
is built on, so the search has to be written against it rather than against the two
columns separately.

Paging is **keyset, not offset**. The register runs to tens of thousands of rows across
71,207 villages: an offset makes every page slower than the last, and a record inserted
mid-browse shifts every subsequent page by one, which is how a clerk paging through a
district silently skips somebody. The cursor is the sort key itself —
`(lower(last_name), lower(first_name), id)` — compared row-wise, which is what lets one
predicate use the whole three-column index. `id` is in the key because two people in one
village genuinely share a name and the sort still has to be total.

Both directions are supported: `?after=` steps forward, `?before=` reads backward from
the cursor and flips the slice. A register is browsed both ways — a clerk who pages past
a name goes back for it.

Cursors are base64 in the URL. Decoding is total: anything malformed means "start at the
beginning", never an error page, because cursors arrive from bookmarks and shared links.
**A cursor is a position, not a permission** — it names a sort key, and the scope filter
still decides which rows past it are visible, so a district user handed a national cursor
sees their own district from that point.

The count on the result line is a second query, deliberately. The page itself is a keyset
scan that never counts, and counting on every page would give that away.

`store.Filter` builds its predicate in one place, shared by the listing and the count: a
count that filtered differently from the page it counts is a bug nobody notices until the
two numbers disagree. Phase 6's export will take the same `Filter`.

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
(`15s`), `TRUSTED_PROXY` (empty). Every problem is reported at once, before anything is
dialled. `ENV=prod` is what puts `Secure` on the session, CSRF and flash cookies.

`TRUSTED_PROXY` is the addresses whose `X-Forwarded-For` may be read. `clientIP`
otherwise records the peer's own address and believes no header, because anyone can send
one and `audit_log` doubles as the CHW change history. Behind a proxy the peer is the
proxy, so leaving this empty there costs every audit row its client address; setting it
to something too broad accepts a forged one. See [deploy.md](deploy.md).

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
