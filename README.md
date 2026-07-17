# Omnia

Omnia is Velion's knowledge ingestor. It pulls content from external sources (Discord, GitHub; WhatsApp and Jira coming) and writes it into [Engram](https://github.com/velion/engram), the local persistent-memory daemon used by AI agents.

The goal: every meaningful piece of company knowledge — discussions, issues, decisions — flows nightly into the agent's memory so it knows context without being told.

## Quickstart

### Prerequisites

- Go 1.23+
- [Engram](https://github.com/velion/engram) daemon running (`omnia serve`)
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
| `embeddings.enabled` | `false` | Enable Omnia's own local semantic-search index |
| `embeddings.base_url` | `http://localhost:11434` | Ollama base URL |
| `embeddings.model` | `jina/jina-embeddings-v2-base-es` | Embedding model (also `bge-m3`) |
| `embeddings.dim` | `768` | Embedding dimension (must match the model) |
| `recall.enabled` | `false` | Enable hybrid (lexical+semantic) recall fusion for `mem_search` |
| `recall.rrf_k` | `60` | Reciprocal-rank-fusion `k` constant |
| `recall.dense_k` | `5` | "Many strong hits" threshold for the adaptive relevance floor |
| `recall.strong_floor` | `0.65` | Cosine floor once `dense_k` strong hits are present |
| `recall.base_floor` | `0.55` | Cosine floor otherwise (widen when hits are sparse) |
| `recall.max_results` | `50` | Cap on the final fused result count |

Both `embeddings` and `recall` default to disabled: off reproduces today's FTS5-only `mem_search` path byte-for-byte, so turning them on is a pure config flip with zero `engram.db` migration.

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

### Cloud semantic parity (optional)

The cloud dashboard's search matches the local hybrid (lexical+semantic) recall quality once cloud semantic parity is enabled. Disabled by default: off reproduces the cloud dashboard's original substring-only search byte-for-byte — zero migration risk, since `cloud_embeddings` is an additive-only table.

| Env var | Default | Description |
|---------|---------|-------------|
| `OMNIA_CLOUD_SEMANTIC_ENABLED` | `false` | Enable cloud semantic search (fuses substring + `cloud_embeddings` cosine hits via `recall.Fuse`) |
| `OMNIA_CLOUD_SEMANTIC_EMBED_BASE_URL` | `""` | OPTIONAL Ollama base URL for embedding interactive dashboard search queries. The cloud has no Ollama instance by default; set this only when one is reachable from the cloud host (e.g. the same homelab LAN). Empty disables interactive query embedding cleanly — vectors already synced in from devices that DO run Ollama stay searchable via any caller that supplies its own query vector. |
| `OMNIA_CLOUD_SEMANTIC_EMBED_MODEL` | `jina/jina-embeddings-v2-base-es` | Query embedding model (mirrors `embeddings.model`'s default) |
| `OMNIA_CLOUD_SEMANTIC_EMBED_DIM` | `768` | Query embedding dimension (must match the model and the vectors devices sync in) |

Every `OMNIA_CLOUD_*` var also accepts its legacy `ENGRAM_CLOUD_*` name.

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
