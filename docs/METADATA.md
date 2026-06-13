# omnia-meta v1 — Field Reference

Every observation produced by Omnia ends with a fenced `omnia-meta` block. The block is machine-facing; the human-readable body sections above it are preserved.

## Format

````
```omnia-meta
schema_version: 1
source: github
kind: pull_request
layer: ingested
project: saluvita
...
```
````

## Field Reference

| Field | Required | Type | Values / Notes |
|-------|----------|------|----------------|
| `schema_version` | yes | int | Always `1` for v1 blocks |
| `source` | yes | string | `github` \| `discord` \| `jira` \| `whatsapp` |
| `kind` | yes | string | `pull_request` \| `issue` \| `commit_digest` \| `message_digest` |
| `layer` | yes | string | Always `ingested` for Omnia output |
| `project` | yes | string | Engram project name (normalized lowercase) |
| `repo` | no | string | `owner/repo` for GitHub; `guild/channel` for Discord; empty otherwise |
| `source_id` | no | string (quoted) | PR/issue number, snowflake ID, etc. |
| `status` | no | string | `open` \| `closed` \| `merged` \| empty for digests |
| `author` | no | string | Primary author login/username |
| `participants` | no | JSON array | JSON-encoded string array, e.g. `["alice","bob"]`. JSON encoding is required so participant names containing `", "` (e.g. display names, Jira full names) round-trip losslessly. |
| `url` | no | string | Canonical web URL |
| `created_at` | no | RFC3339 UTC | Zero → field omitted |
| `updated_at` | no | RFC3339 UTC | Zero → field omitted |
| `ingested_at` | no | RFC3339 UTC | Set to time.Now().UTC() at ingest time |
| `chunk` | no | `N/T` | Only present when observation is part of a multi-chunk sequence |

## Versioning Policy

- **Additive changes** (new optional fields): bump `schema_version` to the new version. The parser ignores unknown fields — old parsers remain compatible.
- **Breaking changes** (remove/rename fields): avoid. If unavoidable, bump major version and provide a migration guide.
- **Parser contract**: `meta.Parse` must always handle any `schema_version` value it has ever seen. Unknown `schema_version` values should parse best-effort (tolerate unknown fields).
- **Re-parse backfill**: the future index backfill will call `meta.Parse` on every Engram observation. The parser must be tolerant of observations written by older Omnia versions.

## Multi-chunk Observations

When an observation's content exceeds the 45,000-character budget, Omnia splits it into numbered chunks. Each chunk contains a complete `omnia-meta` block so it is independently parseable.

Example for a 3-chunk observation:
- Chunk 1: topic_key = `github/owner-repo/pr-5`, meta has `chunk: 1/3`
- Chunk 2: topic_key = `github/owner-repo/pr-5-part2`, meta has `chunk: 2/3`
- Chunk 3: topic_key = `github/owner-repo/pr-5-part3`, meta has `chunk: 3/3`

The chunk field is omitted entirely when the observation fits in a single chunk (`ChunkCurrent == 0`).
