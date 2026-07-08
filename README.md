# Omnia

Omnia is persistent memory for AI coding agents — local-first, single binary, with optional self-hosted multi-tenant cloud sync. It runs as a local daemon (HTTP + MCP) that agents save observations to and search against, so context survives across sessions and compactions instead of being re-explained every time.

See [DOCS.md](DOCS.md) for the full technical reference (database schema, HTTP API, MCP tools, cloud sync internals).

## Installation

### Homebrew (macOS/Linux)

```sh
brew tap velion-spa/tap
brew install omnia
```

### curl | sh (Linux)

```sh
curl -fsSL https://raw.githubusercontent.com/Velion-SpA/omnia/main/scripts/install.sh | sh
```

Detects your OS/arch, downloads the matching release from [GitHub Releases](https://github.com/Velion-SpA/omnia/releases), verifies its checksum, and installs to `~/.local/bin` (or `/usr/local/bin` with `sudo` if run as root).

### go install

```sh
go install github.com/Velion-SpA/omnia/cmd/omnia@latest
```

### Quickstart

```sh
omnia cloud add <alias> --server https://your-cloud-server
omnia cloud login
omnia sync
```

`omnia setup` walks through first-run configuration (agent integration, data directory) interactively.

## Knowledge ingestion (external sources)

This repository also carries source-adapter packages (`internal/core`, `internal/source/discord`,
`internal/source/github`, `internal/sink/engram`, `internal/config`, `internal/state`,
`internal/enrich`) for a companion ingestor that syncs Discord/GitHub activity into memory.
The sections below describe that ingestor's original CLI surface; it predates the current
`cmd/omnia` binary and is not wired into it today — treat the commands below as
aspirational/legacy until re-integrated. See `internal/source/*` for the underlying code.

### Sync GitHub

> **Note:** Global flags (`--source`, `--dry-run`, `--since`, `--config`) must come
> **before** the subcommand, e.g. `omnia --source github sync`.
> Flags placed after the subcommand are treated as arguments and silently ignored.

```sh
# Uses GITHUB_TOKEN env var, or gh auth token fallback
export GITHUB_TOKEN=$(gh auth token)
omnia --source github sync

# Dry run (preview without writing)
omnia --source github --dry-run sync

# Sync only issues updated in the last 7 days
omnia --source github --since $(date -u -v-7d +%Y-%m-%dT%H:%M:%SZ) sync
```

**Note on incremental sync:** After the first run, per-repo cursors are stored in
`~/.local/state/omnia/state.json`. On each subsequent run omnia reads these cursors
and passes the latest `updated_at` timestamp to the GitHub API as the `since` parameter,
so only items updated after the last sync are fetched. The cursor is only advanced after
all sink writes succeed — if a write fails, omnia exits non-zero and does **not** move
the cursor forward. Re-running is safe because Engram upserts on `topic_key+project`
(no duplicates, just a revision bump).

### Sync Discord

```sh
export DISCORD_BOT_TOKEN=your_bot_token
omnia --source discord sync
```

### Check status

```sh
omnia status
```

## Per-project routing

By default, Omnia routes GitHub items to the repo name (without owner) as the Engram project, and Discord items to the guild slug. You can override this with the `routes` map:

```yaml
routes:
  github/arratiabenjamin/saluvita: saluvita   # explicit override
  # discord/123456789: saluvita
```

Resolution order per item: `routes` map → default derivation (repo name / guild slug) → `engram.default_project`. Project names are normalized to lowercase. This means a PR from `arratiabenjamin/saluvita` lands in the `saluvita` project — the same project Engram detects when a developer opens that repo in Claude Code.

## Structured metadata (omnia-meta)

Every observation Omnia writes ends with a fenced `omnia-meta` block containing structured metadata: source, kind, project, author, participants, URL, timestamps, and chunk info. The block is machine-facing and appended to every chunk so each chunk is independently parseable. It is the foundation for the future Omnia index (vector search + structured filters).

See [docs/METADATA.md](docs/METADATA.md) for the full field reference and versioning policy.

## Config reference

| Key | Default | Description |
|-----|---------|-------------|
| `engram.base_url` | `http://127.0.0.1:7437` | Engram daemon URL |
| `engram.default_project` | `omnia` | Default Engram project (last-resort fallback) |
| `routes` | `{}` | Per-origin project routing map |
| `sources.github.enabled` | `false` | Enable GitHub ingestion |
| `sources.github.repos` | `[]` | List of `owner/repo` strings |
| `sources.discord.enabled` | `false` | Enable Discord ingestion |
| `sources.discord.channels` | `[]` | List of `{id, name, guild}` |
| `backfill_days` | `30` | Days to look back on first run |

## Omnia Cloud (optional)

Omnia is local-first: local SQLite is the source of truth; cloud features are optional replication/shared access.

To set up cloud replication:

```bash
omnia cloud config --server http://127.0.0.1:18080
omnia cloud enroll smoke-project
omnia cloud upgrade doctor --project smoke-project
omnia cloud upgrade repair --project smoke-project --dry-run
omnia cloud upgrade repair --project smoke-project --apply
omnia cloud upgrade bootstrap --project smoke-project
omnia cloud upgrade status --project smoke-project
omnia cloud serve
```

Cloud env vars: `ENGRAM_CLOUD_TOKEN` (bearer token), `ENGRAM_JWT_SECRET` (required in auth mode), `ENGRAM_CLOUD_ADMIN` (optional admin token).
Dashboard routes: `/dashboard/login`, `/dashboard/contributors`.

## Architecture

```
cmd/omnia/           CLI (sync, status)
internal/core/       Domain model + ports + pipeline
internal/config/     YAML config loader
internal/state/      Cursor persistence (JSON)
internal/enrich/     Normalization, chunking, keywords
internal/source/     Source adapters (github, discord)
internal/sink/       Sink adapters (engram)
```

## Adding a new source

1. Create `internal/source/<name>/<name>.go`
2. Implement the `core.Source` interface (`Name() string`, `Fetch(ctx, since) ([]Item, error)`)
3. Wire it up in `cmd/omnia/main.go` under `runSync`
4. Add config fields to `internal/config/config.go`

See `docs/WHATSAPP.md` and `docs/JIRA.md` for in-progress source plans.

## Scheduled runs (macOS)

Copy and edit the launchd plist:

```sh
# 1. Build and install the binary first
go build -o /usr/local/bin/omnia ./cmd/omnia

# 2. Copy the plist and replace the token placeholders
cp deploy/com.velion.omnia.plist ~/Library/LaunchAgents/
# Open the plist and replace REPLACE_WITH_YOUR_GITHUB_TOKEN and
# REPLACE_WITH_YOUR_DISCORD_BOT_TOKEN with real values before loading.

# 3. Load the agent
launchctl load ~/Library/LaunchAgents/com.velion.omnia.plist
```

This runs `omnia sync` every night at 2am.

**Failure behavior:** If omnia exits non-zero (e.g. Engram is unreachable or a write fails),
the state cursors are not advanced. The next scheduled run will re-fetch the same window
and attempt to write again. Because Engram upserts by `topic_key+project`, re-ingesting
the same items is always safe.
