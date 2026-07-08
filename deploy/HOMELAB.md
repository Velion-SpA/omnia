# Homelab bring-up: Omnia Cloud server + two notebooks

This is a from-scratch runbook for getting `omnia cloud serve` running on a
homelab Linux box, behind TLS, with two notebooks syncing to it as isolated
accounts. It complements [`OMNIA-CLOUD.md`](../OMNIA-CLOUD.md) (feature
overview, permission model) with the actual deploy mechanics: systemd, the
env template, the reverse proxy, and the exact commands.

Related files in this directory:

- [`omnia-cloud.service`](./omnia-cloud.service) — systemd unit for the server
- [`omnia-agent.service`](./omnia-agent.service) — optional systemd **user**
  unit for a notebook's local agent (`omnia serve`)
- [`cloud.env.example`](./cloud.env.example) — every env var `omnia cloud
  serve` reads, annotated
- [`Caddyfile.example`](./Caddyfile.example) — reverse-proxy TLS termination

## Architecture at a glance

```
 notebook A ─┐                                   ┌─ Postgres
             ├─ HTTPS ─> Caddy ─> 127.0.0.1:8080 ─┤  (schema auto-migrates
 notebook B ─┘           (TLS)      omnia cloud   │   on every boot)
                                     serve         └─
```

- `omnia cloud serve` speaks **plain HTTP only** — there is no TLS listener in
  the binary. This is deliberate: a reverse proxy terminates TLS, the binary
  stays simple. **Never** expose `omnia cloud serve`'s raw port directly on
  the LAN or internet without a proxy in front of it.
- The binary is pure Go (`CGO_ENABLED=0`), so cross-compiling from macOS for
  a Linux homelab box is a plain `go build` with `GOOS`/`GOARCH` set — no
  toolchain, no Docker required (though `docker-compose.cloud.yml` /
  `docker/cloud/Dockerfile` remain a valid alternative if you'd rather
  containerize the server).
- Postgres schema migration is idempotent and runs on every boot
  (`CREATE TABLE IF NOT EXISTS ...`) — no manual migration step.

## 0. Prerequisites

- A Linux box for the server (the "homelab" machine) reachable from both
  notebooks over the LAN (or a domain name, for Option A TLS below).
- Postgres 14+ reachable from that box (local install, or a container — see
  `docker-compose.cloud.yml` for a disposable example).
- Two notebooks with `omnia` already built/installed for their own OS/arch,
  each with `$HOME/.omnia` as their local data dir (default).

## 1. Provision Postgres

On the homelab server (adjust for your Postgres install method):

```bash
sudo -u postgres psql <<'SQL'
CREATE ROLE omnia WITH LOGIN PASSWORD 'CHANGE_ME_STRONG_PASSWORD';
CREATE DATABASE omnia_cloud OWNER omnia;
SQL
```

You do not need to create any tables — `omnia cloud serve` migrates the
schema itself on first boot (accounts, memberships, sync_state, sync_chunks,
sync_mutations, etc.), and again idempotently on every subsequent boot.

## 2. Cross-compile the binary (from your Mac / dev machine)

Pure Go, `CGO_ENABLED=0` is already the project's default posture (see
`.goreleaser.yaml`; the SQLite driver is `modernc.org/sqlite`, pure-Go), so a
straight cross-compile is all that's needed — no Zig/cross-cc, no Docker.

```bash
# amd64 homelab server (most mini-PCs / NUCs / old desktops)
GOOS=linux GOARCH=amd64 go build -o /tmp/omnia-linux ./cmd/omnia

# arm64 (Raspberry Pi 4/5, or an ARM notebook running Linux)
GOOS=linux GOARCH=arm64 go build -o /tmp/omnia-linux-arm64 ./cmd/omnia
```

> `CGO_ENABLED` does not need to be set explicitly — the project has no cgo
> dependencies (pure-Go SQLite driver `modernc.org/sqlite`, pure-Go pgx
> stdlib driver), and Go disables cgo automatically when cross-compiling
> without a matching C toolchain; add `CGO_ENABLED=0` explicitly if you want
> the guarantee spelled out.

Copy the binary matching the server's architecture (`uname -m`: `x86_64` →
amd64, `aarch64`/`arm64` → arm64) to the homelab box:

```bash
scp /tmp/omnia-linux homelab:/tmp/omnia   # or /tmp/omnia-linux-arm64 for an ARM server
```

## 3. Install on the homelab server

```bash
# Non-root service user, home dir doubles as its working directory.
sudo useradd --system --create-home --home-dir /var/lib/omnia --shell /usr/sbin/nologin omnia

sudo install -o root -g root -m 0755 /tmp/omnia /usr/local/bin/omnia

sudo mkdir -p /etc/omnia
sudo install -o omnia -g omnia -m 0600 deploy/cloud.env.example /etc/omnia/cloud.env
sudo editor /etc/omnia/cloud.env   # fill in real DSN, JWT secret, token, allowlist — see the file's comments

sudo install -o root -g root -m 0644 deploy/omnia-cloud.service /etc/systemd/system/omnia-cloud.service
sudo systemctl daemon-reload
```

Do **not** start the service yet — bring up the reverse proxy first so the
port is never briefly exposed unproxied.

## 4. Reverse proxy (Caddy) — TLS termination

The binary intentionally has no TLS/ACME of its own; see
[`Caddyfile.example`](./Caddyfile.example) for both a public-domain
(automatic Let's Encrypt) and a LAN-only (`tls internal`) option.

```bash
sudo apt install -y caddy   # or see https://caddyserver.com/docs/install
sudo install -o root -g root -m 0644 deploy/Caddyfile.example /etc/caddy/Caddyfile
sudo editor /etc/caddy/Caddyfile   # pick Option A (public domain) or B (LAN-only), fill in your domain/IP
caddy validate --config /etc/caddy/Caddyfile
sudo systemctl enable --now caddy
```

`OMNIA_CLOUD_HOST=0.0.0.0` in `cloud.env` is safe **only** because Caddy is
the sole thing meant to reach `127.0.0.1:8080` from outside — see the
LAN-firewall note below.

## 5. Start the server

```bash
sudo systemctl enable --now omnia-cloud.service
journalctl -u omnia-cloud.service -f
```

You should see `[omnia-cloud] listening on 0.0.0.0:8080` (or your configured
port) with no fatal errors. A fatal error here almost always means the boot
env gate rejected something in `cloud.env` — the message names the exact
variable (see `cmd/omnia/cloud.go` `validateCloudServeAuthConfig`, and the
comments in `cloud.env.example`).

Verify it answers through the proxy:

```bash
curl -i https://cloud.yourdomain.tld/         # Option A
# or
curl -ik https://homelab.local:8443/          # Option B (self-signed local CA)
```

Any HTTP response (even a 404 on `/`) confirms the proxy → server path works.

## 6. Bootstrap the first account

Run `omnia cloud bootstrap-admin` **on the server host**, against its own
storage (no HTTP involved) — this creates the first account directly and is
idempotent (it refuses if any account already exists):

```bash
omnia cloud bootstrap-admin --username admin --password 'CHANGE_ME'

# optional: also issue a managed token on the spot (shown once).
# Requires OMNIA_CLOUD_MANAGED_TOKENS=1 and a non-default OMNIA_CLOUD_TOKEN_PEPPER.
omnia cloud bootstrap-admin --username admin --password 'CHANGE_ME' --issue-token
```

Signup (`POST /auth/signup` / `omnia cloud signup`) is closed by default on a
LAN-reachable server. To provision additional accounts afterwards, use the
admin API, or temporarily set `OMNIA_CLOUD_OPEN_SIGNUP=1` on the server to
re-open self-signup:

```bash
omnia cloud config --server https://cloud.yourdomain.tld
omnia cloud signup --username otro --email otro@example.com --password 'CHANGE_ME_2'
```

That account becomes the `owner` of a project the first time it pushes to
it (see [`OMNIA-CLOUD.md`](../OMNIA-CLOUD.md) — "claim on first push").
`OMNIA_CLOUD_ADMIN` (if set in `cloud.env`) is a separate, independent
operator token for the dashboard — it is not a `cloud_users` row.

## 7. Connect each notebook

On **notebook A**:

```bash
omnia cloud config --server https://cloud.yourdomain.tld
omnia cloud login --username admin --device notebook-a
```

On **notebook B** (a second account, so isolation is actually exercised):

```bash
omnia cloud signup --username otro --email otro@example.com --password 'CHANGE_ME_2'
omnia cloud config --server https://cloud.yourdomain.tld
omnia cloud login --username otro --device notebook-b
```

`--device <name>` registers the login as a named device against the server.
Devices can be scoped to specific projects with `omnia cloud devices
list|scope|revoke` — a device with an empty scope is unrestricted (sees
everything its account can see); a non-empty scope narrows it further (it
never grants more than the account's project membership already allows).
The isolation boundary that matters for this test is **project membership**
(step 8), which is enforced independently of device scoping.

## 8. Enroll projects and grant access

On notebook A, for a project only A should own:

```bash
omnia cloud enroll homelab-a
omnia sync --cloud --project homelab-a
```

The first push claims `admin` as `homelab-a`'s owner. To let `otro`
(notebook B) read a *different* project you explicitly share, grant
membership (see [`OMNIA-CLOUD.md`](../OMNIA-CLOUD.md) §2 for the full
permission bitmask):

```bash
TOK=$(curl -s -X POST https://cloud.yourdomain.tld/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"CHANGE_ME"}' | jq -r .token)

curl -X POST https://cloud.yourdomain.tld/projects/shared-project/members \
  -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' \
  -d '{"account_id":"2","perms":1,"role":"member"}'   # perms=1 → Read only
```

## 9. Verify isolation

```bash
scripts/e2e-cloud-isolation.sh   # requires a local Postgres, jq, curl — run against a THROWAWAY server, not this one
```

Manually, from each notebook: `omnia cloud status` shows auth/sync readiness
for the configured server (and, once a sync attempt has run, the last
lifecycle + `reason_code`/message if degraded — the same data the local
dashboard's `/sync` page now renders per OBL-12). A pull/sync attempt against
a project the account has no membership on must be denied (403), not
silently return empty data. A project neither notebook has ever pushed
simply doesn't exist yet — that's expected, not a bug.

## LAN-firewall consideration

`OMNIA_CLOUD_HOST=0.0.0.0` means the process binds every interface on the
homelab box. With Caddy as the only intended path in:

- Firewall the box so port `8080` (or whatever `OMNIA_PORT` is) is reachable
  **only from localhost** (Caddy) — e.g. `ufw deny 8080` while leaving 80/443
  (or your chosen HTTPS port) open to the LAN/notebooks.
- If Postgres is on the same box, firewall `5432` to localhost only as well.
- Keep `OMNIA_CLOUD_HOST=127.0.0.1` (the code default) instead if Caddy and
  `omnia cloud serve` truly never need to cross a network boundary between
  them (e.g. same-host, proxy connects over loopback) — 0.0.0.0 is only
  required when the proxy runs on a different host/container than the Go
  process.

## Optional: keep a notebook's local agent running

If you want `omnia serve` (the local dashboard/MCP HTTP server) to survive
notebook reboots on Linux, see [`omnia-agent.service`](./omnia-agent.service)
— a systemd **user** unit, no root required.

## Troubleshooting

- **Server exits immediately with an auth-config error** — the message names
  the exact missing/invalid variable (see `validateCloudServeAuthConfig` in
  `cmd/omnia/cloud.go`); cross-check against `cloud.env.example`'s comments.
- **`curl` through Caddy times out / connection refused** — confirm
  `omnia cloud serve` is actually listening (`journalctl -u omnia-cloud -f`,
  or `ss -ltnp | grep 8080` on the server) before debugging Caddy.
- **A notebook's sync is rejected with 403** — that account has no
  membership on the target project (step 8), or `OMNIA_CLOUD_ALLOWED_PROJECTS`
  on the server doesn't include it (legacy allowlist gate, independent of
  per-account memberships).
