# Omnia — Architecture

## Vision

Omnia is a **knowledge layer**, not just an ingestor. Its job is to turn the ambient communication channels of a software team — GitHub issues and PRs, Discord conversations, and eventually Jira and WhatsApp — into structured, searchable memory that AI agents can query without manual copy-paste.

The canonical text store is **Engram** (a local HTTP daemon at `127.0.0.1:7437`). Omnia does not modify Engram's engine; it is a producer that writes observations to it. Everything Omnia writes is **derived** — reconstructible from the original source — so Omnia owns ingestion and will own its own index layer in the future.

## Current Foundations (v1)

### 1. Per-Project Routing

Every observation Omnia writes lands in the **correct Engram project** — matching the project name that Engram detects when a developer opens Claude Code inside a repo's folder.

Resolution order for each ingested item:
1. **Explicit route** in `config.routes` map (e.g. `github/arratiabenjamin/saluvita: saluvita`)
2. **Default derivation**: for GitHub → repo name without owner; for Discord → guild slug
3. **Last-resort fallback**: `engram.default_project`

Project names are normalized (lowercase, trimmed) so they match Engram's working-folder detection.

### 2. Versioned Structured Metadata (omnia-meta v1)

Every observation ends with a fenced `omnia-meta` block:

````
```omnia-meta
schema_version: 1
source: github
kind: pull_request
layer: ingested
project: saluvita
repo: arratiabenjamin/saluvita
source_id: "5"
status: closed
author: arratiabenjamin
participants: arratiabenjamin, RoberCornejo
url: https://github.com/arratiabenjamin/saluvita/pull/5
created_at: 2026-05-21T23:38:21Z
updated_at: 2026-06-13T01:31:48Z
ingested_at: 2026-06-13T02:00:00Z
```
````

The block is **machine-facing** (the human-readable body sections are preserved above it). It is appended to every chunk of a multi-chunk observation so each chunk is independently parseable and indexable.

The `internal/meta` package provides `Render(Meta) string` and `Parse(content string) (Meta, bool)` — a stable public contract that the future index will call to backfill from existing Engram observations.

### 3. Type Taxonomy

| Type | Source | Description |
|------|--------|-------------|
| `github-pr` | GitHub | Pull request observation |
| `github-issue` | GitHub | Issue observation |
| `github-commit-digest` | GitHub | Future: daily commit digest |
| `discord-digest` | Discord | Daily message digest |
| `jira-issue` | Jira | Future: Jira ticket |
| `whatsapp-digest` | WhatsApp | Future: daily chat digest |

The `type` field is the positive-filter axis for consumers.

## The Two Layers: Derived vs. Curated

| Property | Derived (Omnia) | Curated (human) |
|----------|-----------------|-----------------|
| Origin | GitHub / Discord / Jira / WhatsApp | Human decisions, team conventions |
| Reconstructible? | YES — re-ingest from source | NO — irreplaceable |
| omnia-meta block? | Always present | Absent |
| layer field | `ingested` | N/A |
| Typical types | `github-pr`, `discord-digest` | Any — no omnia-meta |

**How they're separated:** A curated observation has no `omnia-meta` block. Any consumer can distinguish them with `meta.Parse(content)` — returns `false` for curated observations.

## Data Model & Mapping

### Item → Observation mapping

```
core.Item {
    Type      string    // type taxonomy identifier (github-pr, discord-digest, …)
    Title     string    // human-readable title
    Content   string    // markdown body + omnia-meta block
    Project   string    // Engram project (resolved by Router)
    TopicKey  string    // stable upsert key
    Source    string    // "github" | "discord" | …
    FetchedAt time.Time
}
```

Maps to Engram observation:
```
POST /observations {
    session_id: "omnia-{project}",
    type:        item.Type,
    title:       item.Title,
    content:     item.Content,   // markdown + omnia-meta block
    project:     item.Project,
    scope:       "project",
    topic_key:   item.TopicKey
}
```

### topic_key scheme

| Source | Format |
|--------|--------|
| GitHub issue | `github/{owner}-{repo}/issue-{N}` |
| GitHub PR | `github/{owner}-{repo}/pr-{N}` |
| Chunked (part 2+) | `{base}-part{N}` |
| Discord daily digest | `discord/{guild}/{channel}/{YYYY-MM-DD}` |

topic_keys are normalized (lowercase, spaces→hyphens) and capped at 120 chars total (base capped at 113 to leave room for `-partNN`).

### Project routing config

```yaml
routes:
  github/owner/repo: project-name
  discord/channel-id: project-name
```

### omnia-meta field reference

See `docs/METADATA.md` for the full field reference and versioning policy.

## The Future Index

Omnia's current write target is Engram (text search). The plan is to add **Omnia's own index** alongside it:

```
Sources → Omnia ingestor → Engram (text store, canonical)
                         ↘ Omnia index (vector DB + structured metadata table)
                              ↓
                         omnia_search MCP server (agents query this)
```

The index will support:
- **Vector search** over observation embeddings
- **Structured filters** on omnia-meta fields (type, source, author, date range, status)
- **`omnia_search` MCP server** — the agent will call this instead of querying Engram directly

**Why it is NOT built now:** Current ingestion volume (a few hundred observations) does not justify a vector DB. The foundations built in v1 make the future index cheap to add:
- Every observation already carries a parseable `omnia-meta` block → structured metadata table is a backfill, not a re-design
- `meta.Parse` is the stable contract the backfill job will call
- Re-ingest from source is always an option (derived data)

### Backfill strategy (two paths)

1. **Re-ingest from source** — run `omnia sync --since=beginning`. Idempotent (upserts by topic_key). Slower but always authoritative.
2. **Re-parse from Engram** — scan Engram observations, call `meta.Parse(content)`, insert into the index. Faster, no API rate limits. Works because every observation already has a complete omnia-meta block.

Path 2 is why the meta block must be present in EVERY chunk and why the parser must be backward-tolerant.

## Schema Versioning & Migration

- `schema_version: 1` is the current version.
- **v2+ adds fields** — the parser ignores unknown fields (forward-compatible).
- **Old v1 records never fall behind**: derived data can be re-ingested; `meta.Parse` is backward-tolerant for older records that have fewer fields.
- Bump `meta.SchemaVersion` when adding fields. Document in `docs/METADATA.md`.
- Do NOT remove or rename existing fields (breaking change). Deprecate by documenting as legacy.

## Roadmap

```
v1 (current)  Foundations: per-project routing, omnia-meta block, type taxonomy
v2            Jira source adapter
v3            WhatsApp source adapter (whatsmeow)
v4            Omnia index: vector DB + structured metadata table + omnia_search MCP
v5            Multi-agent queries via omnia_search MCP server
```
