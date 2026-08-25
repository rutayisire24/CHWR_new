# Deployment

One binary, one Postgres database, one reverse proxy. No container, no runtime
dependencies: the templates, static assets and migrations are all `go:embed`ed, and
`make dist` links statically, so the artifact is a single file with no libc to install
beside it.

Files referenced here live in [`deploy/`](../deploy).

## What is deployed

| Piece | Where |
|---|---|
| `chwr-server` | `/opt/chwr/chwr-server`, owned by `chwr` |
| Configuration | `/etc/chwr/chwr.env`, mode `0640`, `root:chwr` — it carries the database password |
| Service | `chwr.service`, `Type=exec`, restarts on failure |
| Backups | `chwr-backup.timer` nightly at 01:30, into `/var/backups/chwr` |
| TLS | nginx or Caddy on the same host |

The service holds no state of its own. Everything is in Postgres, which is why
`ProtectSystem=strict` and `ReadOnlyPaths=/opt/chwr` cost nothing.

## First install

```bash
# The account the service runs as. No login shell, no home directory to protect.
sudo useradd --system --no-create-home --shell /usr/sbin/nologin chwr

sudo install -d -o chwr -g chwr /opt/chwr
sudo install -d -m 0750 -o root -g chwr /etc/chwr
sudo install -d -o chwr -g chwr /var/backups/chwr

make dist                                        # chwr-server-linux-amd64
sudo install -o chwr -g chwr -m 0755 chwr-server-linux-amd64 /opt/chwr/chwr-server
sudo install -o chwr -g chwr -m 0755 deploy/backup.sh /opt/chwr/backup.sh

sudo install -m 0640 -o root -g chwr deploy/chwr.env.example /etc/chwr/chwr.env
sudo editor /etc/chwr/chwr.env                   # see the next section

sudo cp deploy/chwr.service deploy/chwr-backup.service deploy/chwr-backup.timer \
        /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now chwr chwr-backup.timer
```

Then the database, once:

```bash
sudo -u postgres createuser chwr --pwprompt
sudo -u postgres createdb chwr --owner chwr

# Migrations run at every start, but running them first turns a schema problem
# into a failed command rather than a service that will not come up.
sudo -u chwr DATABASE_URL='...' /opt/chwr/chwr-server -migrate

# The hierarchy and the facilities. From a checkout, on any machine that can
# reach the database — the workbooks are checked in and the loaders are idempotent
# only in the sense that they are run once, on an empty register.
make seed DATABASE_URL='...'

# The first administrator. Every account after this one is created in the UI.
sudo -u chwr DATABASE_URL='...' /opt/chwr/chwr-server \
    -create-admin you@ministry.go.ug -name "Your Name"
```

`-create-admin` prints a temporary password and sets `must_reset`, so the account is
held on `/account/password` until it chooses its own.

## Configuration

`/etc/chwr/chwr.env`, read by systemd's `EnvironmentFile`. The server reads plain
environment variables and loads no `.env` itself.

| Variable | Notes |
|---|---|
| `DATABASE_URL` | Required. No default — a wrong-database default is worse than a refusal to start |
| `ADDR` | `127.0.0.1:8080`. **Loopback only**: a public bind lets anyone skip the proxy, and with it the TLS that makes the cookies work |
| `ENV` | `prod` puts `Secure` on the session, CSRF and flash cookies. Set it only once TLS is terminating in front, or nobody can sign in |
| `SHUTDOWN_TIMEOUT` | `15s`. A bulk import commit is the longest thing the service does — about 3 seconds per thousand rows |
| `TRUSTED_PROXY` | The proxy's address. See below; getting this wrong costs the audit trail |

### `TRUSTED_PROXY` and the audit trail

`clientIP` records the peer's own address and believes no header, because anyone can
send `X-Forwarded-For` and `audit_log` doubles as the CHW change history — a forged one
would be a lie in the register's own provenance.

Behind a proxy the peer *is* the proxy, so every row would record `127.0.0.1`.
`TRUSTED_PROXY` names the addresses whose header may be read. The chain is then walked
from the right, stopping at the first address that is not trusted: a value the client
invented sits to the left of the real hops and can never be reached.

It is a list of addresses or CIDR blocks, not a boolean, because trusting whatever sent
the header is trusting the client. Leave it empty when the service is reached directly.

## TLS

Either [`deploy/nginx.conf`](../deploy/nginx.conf) or
[`deploy/Caddyfile`](../deploy/Caddyfile). Two settings in each are not decoration:

- **Body size** at 16 MB. `auth.MaxMultipartBytes` caps the body at 12 MB and the
  importer refuses a file over 10 MB *by name*; if the proxy refuses first, the operator
  gets a bare 413 instead of a sentence about files.
- **Read timeout** at 180s. Committing a ten-thousand-row import takes about 30 seconds,
  and nginx's default 60s would cut it off mid-write.

Do not add security headers at the proxy. The application sets its own, including a
strict CSP; a second copy will drift from the first.

## Upgrading

Migrations are append-only and run at startup, so an upgrade is a file swap:

```bash
make check && make dist
sudo systemctl stop chwr
sudo install -o chwr -g chwr -m 0755 chwr-server-linux-amd64 /opt/chwr/chwr-server
sudo systemctl start chwr
sudo systemctl status chwr
curl -sf https://chwr.example.org/healthz && echo ok
```

Take a backup first if the release carries a migration — `/opt/chwr/backup.sh` is the
same script the timer runs.

There is no zero-downtime story here and none is needed: the register is a working-hours
service, restarts take under a second, and `Type=exec` with `Restart=on-failure` means a
binary that will not start leaves the old one stopped rather than flapping.

## Backups

`chwr-backup.timer` runs `backup.sh` nightly at 01:30 with a persistent catch-up, so a
machine that was off still gets its backup. Custom-format `pg_dump`, thirty days kept.

Two details are deliberate:

- The dump is written to `.partial` and renamed only once `pg_dump` has succeeded, so a
  half-written file is never mistaken for a backup.
- Every dump is then read back with `pg_restore --list`, which fails on a truncated or
  corrupt archive. A backup that has never been read is a hope.

**`audit_log` is included and must stay included.** It is not a log: it doubles as the
CHW change history, and it is the only record of who changed what. Excluding it to save
space would lose the register's provenance while appearing to have worked.

### Restoring

Into a scratch database first, always — a restore straight over the live one turns a
suspected problem into a certain outage:

```bash
createdb chwr_restore
pg_restore --dbname=chwr_restore --no-owner --no-privileges /var/backups/chwr/chwr-*.dump

# Does it hold what it should?
psql -d chwr_restore -c "SELECT
    (SELECT count(*) FROM locations) AS locations,
    (SELECT count(*) FROM chws)      AS chws,
    (SELECT count(*) FROM audit_log) AS audit"

# And is it still a register, rather than a table of rows? A restored schema
# that lost its triggers would accept anything.
psql -d chwr_restore -c "INSERT INTO chws (first_name,last_name,sex,cadre,location_id,district_id)
    VALUES ('A','B','female','chew',(SELECT id FROM locations WHERE level='village' LIMIT 1),0)"
# expected: ERROR: a chew must be placed at parish level, got village
```

This procedure was run against a 24,573-record database: 2.1 MB dump, every table's count
identical, 7 triggers and 21 check constraints restored, and the placement trigger still
refusing a CHEW at village level.

## Health and logs

`GET /healthz` pings the pool, so a 200 means the database is reachable too — it is what
a monitor should watch, not the TCP port.

The binary logs to stdout as structured `slog`, so `journalctl -u chwr` is the log.
Nothing writes a file that could fill a disk unattended.

```bash
systemctl status chwr
journalctl -u chwr -f
journalctl -u chwr -p err --since today
systemctl list-timers chwr-backup.timer
```
