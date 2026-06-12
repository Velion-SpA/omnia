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
cp deploy/com.velion.omnia.plist ~/Library/LaunchAgents/
# Edit the plist to set the correct binary path
launchctl load ~/Library/LaunchAgents/com.velion.omnia.plist
```

This runs `omnia sync` every night at 2am.
