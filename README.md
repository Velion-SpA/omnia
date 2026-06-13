# Omnia

Omnia is Velion's knowledge ingestor. It pulls content from external sources (Discord, GitHub; WhatsApp and Jira coming) and writes it into [Engram](https://github.com/velion/engram), the local persistent-memory daemon used by AI agents.

The goal: every meaningful piece of company knowledge — discussions, issues, decisions — flows nightly into the agent's memory so it knows context without being told.

## Quickstart

### Prerequisites

- Go 1.23+
- [Engram](https://github.com/velion/engram) daemon running (`engram serve`)
- A GitHub token (or `gh` CLI authenticated)

### Install

```sh
go install github.com/velion/omnia/cmd/omnia@latest
```

Or build from source:

```sh
git clone https://github.com/velion/omnia
cd omnia
go build -o bin/omnia ./cmd/omnia
```

### Configure

```sh
mkdir -p ~/.config/omnia
cp config.example.yaml ~/.config/omnia/config.yaml
# Edit ~/.config/omnia/config.yaml to add your repos and tokens
```

### Sync GitHub

```sh
# Uses GITHUB_TOKEN env var, or gh auth token fallback
export GITHUB_TOKEN=$(gh auth token)
omnia sync --source github

# Dry run (preview without writing)
omnia sync --source github --dry-run

# Sync only issues updated in the last 7 days
omnia sync --source github --since $(date -u -v-7d +%Y-%m-%dT%H:%M:%SZ)
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
omnia sync --source discord
```

### Check status

```sh
omnia status
```

## Config reference

| Key | Default | Description |
|-----|---------|-------------|
| `engram.base_url` | `http://127.0.0.1:7437` | Engram daemon URL |
| `engram.default_project` | `omnia` | Default Engram project |
| `sources.github.enabled` | `false` | Enable GitHub ingestion |
| `sources.github.repos` | `[]` | List of `owner/repo` strings |
| `sources.github.project` | `omnia` | Engram project for GitHub items |
| `sources.discord.enabled` | `false` | Enable Discord ingestion |
| `sources.discord.channels` | `[]` | List of `{id, name, guild}` |
| `sources.discord.project` | `omnia` | Engram project for Discord items |
| `backfill_days` | `30` | Days to look back on first run |

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
