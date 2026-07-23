# Design: omnia-provenance-foundation (verifiable-provenance + trust substrate) — 0.2 H1

Artifact store: hybrid. File: `openspec/changes/omnia-provenance-foundation/design.md` + engram `sdd/omnia-provenance-foundation/design`.
Slice of the locked 0.2 H1 foundation (#1592). Sources: research #1585-1590. Strict TDD active (`go test ./...`), `CGO_ENABLED=0`.

## Technical Approach
Additive substrate over the existing SQLite store, MCP boundary, embed store, and sync rails. Four capabilities, all cgo-free: (1) capture a **source-trust tag at write time** (`mem_save`), not retrieval-time — retrieval-only filtering is proven insufficient (UW "Bad Memory"); (2) **provable physical deletion** across local store + embeddings + cloud replica, recorded in a persistent tombstone; (3) a **read/write/delete audit log** reusing `internal/audit`; (4) **encrypt-at-rest** — designed here, recommended as a 0.3 fast-follow (rationale below). Nothing forks: provenance rides `AddObservation`, deletion rides `DeleteObservation` + the existing `SyncOpDelete`/`HardDelete` rail, embed purge rides the `enqueueAutoEmbed` store↔embed bridge so `internal/store` never imports `internal/embed`.

## Architecture Decisions
| Decision | Choice | Rejected | Rationale |
|---|---|---|---|
| Where to tag trust | Additive nullable `source`,`trust_tag` columns on `observations`, captured in `handleSave`→`AddObservationParams` | Retrieval-time scoring only | Poisoning persists at ingestion regardless of storage (#1585); tag the boundary |
| Trust taxonomy | `user` / `agent` / `ingest:tool|web|doc`; unknown→`unverified` | Numeric trust score | Attribution is honest; a score implies authentication we cannot provide |
| Deletion proof | NEW `deletion_tombstones` table (sync_id PK), written in the hard-delete tx | Rely on `sync_mutations` delete op | Sync journal is pruned post-ack; GDPR Art.17 needs a durable record (#1587) |
| Physical purge | Keep existing `DELETE FROM observations` (FTS auto-cleaned by `obs_fts_delete` trigger) + new `embed.Store.DeleteBySyncID` | Soft-delete / mark | Ghost Vectors: soft-deleted vectors stay reconstructible (#1590) |
| Embed fan-out site | MCP `handleDelete` (imports both), mirroring `enqueueAutoEmbed` | `store` calls `embed` | Preserves the documented store-must-not-import-embed boundary |
| Audit log | Extend `internal/audit.Entry` (+`Source`,`TrustTag`,`SyncID`,`SessionID`; +`ActionRead`/`ActionWrite`) | Parallel log | Task mandate; JSONL is forward-compatible (old lines omit new fields) |
| Encrypt-at-rest | **Fast-follow 0.3**; ship perms-hardening + doctor warning in 0.2 | SQLCipher (cgo, ruled out); app-level now | At-rest ciphertext breaks FTS5 indexing + vector cosine; FileVault covers powered-off theft; single-user key mgmt adds risk without closing the live-process/inversion gap |

## Data Flow
```
mem_save(source?) → classifyTrust() → AddObservation(+source,trust_tag) → audit.Append(write)
mem_delete(hard)  → store.DeleteObservation ─┬─ DELETE row (FTS trigger cleans) 
                                             ├─ INSERT deletion_tombstones
                                             └─ enqueue SyncOpDelete{HardDelete}
                  → embed.DeleteBySyncID(syncID)        (MCP fan-out)
                  → audit.Append(hard_delete)
cloud pull → applyObservationDeleteTx → purge row + INSERT deletion_tombstones (proof replicates)
```

## File Changes
| File | Action | Description |
|---|---|---|
| `internal/audit/audit.go` | Modify | Add `Source`,`TrustTag`,`SyncID`,`SessionID` to `Entry`; add `ActionRead`,`ActionWrite` |
| `internal/store/store.go` | Modify | `migrate()`: `addColumnIfNotExists` `source`,`trust_tag`; CREATE `deletion_tombstones`; `AddObservationParams`+`Source`/`TrustTag` (upsert preserves via `COALESCE(NULLIF)`); `DeleteObservation` writes tombstone; `applyObservationDeleteTx` writes tombstone on pull |
| `internal/store/provenance.go` | Create | `classifyTrust()` pure fn + trust-tag constants (leaf, testable) |
| `internal/embed/store.go` | Modify | Add `DeleteBySyncID(ctx, syncID)` — physical `DELETE FROM vectors WHERE sync_id=?` |
| `internal/mcp/mcp.go` | Modify | `handleSave`: `source` arg + classify + `audit.Append`; `handleDelete`: embed purge fan-out + `audit.Append`; add embed store handle to `MCPConfig` |
| `cmd/omnia/doctor*.go` | Modify | Warn if store dir is world/group-readable or inside iCloud/Dropbox/OneDrive |

## Interfaces
```go
// internal/store
type AddObservationParams struct { /* … */ Source string; TrustTag string }
// deletion_tombstones: sync_id TEXT PRIMARY KEY, entity TEXT, project TEXT,
//   actor TEXT, reason TEXT, content_hash TEXT, hard INTEGER, deleted_at TEXT
// internal/embed
func (s *Store) DeleteBySyncID(ctx context.Context, syncID string) (int, error)
```

## Testing Strategy (strict TDD)
| Layer | What | How |
|---|---|---|
| Unit | `classifyTrust` taxonomy + `unverified` default | pure fn, table tests |
| Store | provenance columns persist + upsert preserves; tombstone written on hard-delete; migration idempotent | real `:memory:` sqlite |
| Embed | `DeleteBySyncID` physically removes vector; count-after=0 | real embed store |
| MCP | `audit.Append` fires on save/delete with source+trust_tag; embed fan-out on hard-delete | fakes |
| Sync | tombstone + purge replicate on push AND pull | `applyObservationDeleteTx` |
| Regression | `CGO_ENABLED=0 go test ./...` green; unanchored/legacy saves unchanged | build gate |

## Migration / Rollout
Additive migration only (no backfill, no schema break; legacy rows read `unverified`). Flag `provenance.enabled` (default true; capture is backward-compatible). Order: (1) audit Entry extension; (2) provenance columns + params; (3) handleSave capture+audit; (4) tombstone table + DeleteObservation; (5) embed.DeleteBySyncID + handleDelete fan-out+audit; (6) sync tombstone-proof replication. Each lands green independently. Likely 2-3 chained PRs (schema/capture, deletion/embed, sync-replication) — exceeds the 400-line budget as one PR.

## What local-first does NOT protect (honest scope — do not oversell)
- **Ingestion-time poisoning**: the trust tag is *attribution, not authentication* — it records that a doc/web/tool wrote a memory; it does NOT verify the source or stop a poisoned doc from being persisted. Refusal-to-act still persists content (#1585).
- **Device compromise / live process**: malware or another same-account process reads the unlocked DB regardless of at-rest encryption.
- **Embedding inversion**: a stolen `vectors` file is invertible (Vec2Text) whether local or cloud.
- **FileVault limits**: protects only powered-off theft, not the normal mounted state.

## Open Questions
- [ ] No dedicated slice `proposal` artifact was found; design derived from concept brief + epic #1592 + research #1585-1590. Confirm before tasks.
- [ ] Trust default when `source` absent: `unverified` vs infer `agent` from MCP context — needs product call.
- [ ] `deletion_tombstones` retention: keep forever (proof) vs prune after N days — GDPR proof favors forever.
- [ ] Confirm encrypt-at-rest deferral to 0.3 (recommended) vs a minimal 0.2 sealed-blob for `content`+`embedding` only.
```
