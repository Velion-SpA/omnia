# Design: Omnia v0.3.2 — Time Travel

> PLANNING ONLY. Artifact store: hybrid (this file + engram `sdd/omnia-0.3.2-time-travel/design`).
> Grounded on LIVE code (main 41d8348), NOT the stale mirror the proposal cited. Strict TDD active (`CGO_ENABLED=0 go test ./...`). Reads: proposal #1690, business-rules #1691, project ctx #1638.

## Technical Approach

Three capabilities riding on one new substrate. E (`--as-of`) reads state-at-T from a dedicated append-only revision table; F (`mem_bisect`) rides E's state-at-T rendering; N (portable export) extends `store.ExportData`. All gated by a single default-OFF flag (`time_travel.enabled`), byte-for-byte no-op when disabled (D7). Hard-delete purge is EXTENDED, not reinvented, over the live `internal/purge.HardDeleteWithPurge` + `deletion_tombstones` machinery.

## Architecture Decisions

### Decision 1 — History substrate (the load-bearing fork) — VERDICT: dedicated `observation_revisions`, before-image

| Option | Tradeoff | Verdict |
|--------|----------|---------|
| A. Dedicated `observation_revisions` table, before-image on UPDATE/soft-DELETE | +decoupled +trivial atomic purge +storage∝edits (cap-coherent) +zero insert-path cost; −union query w/ live row | **CHOSEN** |
| B. Hardened read over `sync_mutations` | reuse; but couples history durability to the sync outbox | Rejected |
| A′. After-image + seed genesis on every insert | pure single-table query; −uncappable 2× baseline, taxes every save | Rejected |

**Live-code evidence that decided it:** `store.go:1373-1379` — the code's OWN comment calls `sync_mutations` "the (prunable) sync_mutations journal ... pruned post-ack, so it cannot serve as a durable deletion proof on its own," which is exactly why `deletion_tombstones` was made a dedicated table. Production never prunes it today (only `store_test.go:2309/2358` DELETE), but it is architecturally reserved as ephemeral. Building time-travel history on it would inherit both that prunability AND the resurrection defect (`deleteObservation` hard branch keeps prior `SyncOpUpsert` payloads while enqueuing a content-less `SyncOpDelete` — proposal defect #2 confirmed at `store.go:4040`). A dedicated table repeats the proven `deletion_tombstones` pattern. Before-image (not after-image) because: never-edited memories cost ZERO history bytes (the common case — most saves are one-off), the retention cap (`N revisions/memory`) then directly bounds churny memories, and the common INSERT path pays no capture cost.

**Chosen DDL** (additive `CREATE TABLE IF NOT EXISTS`, same convention as `memory_anchors`/`procedures`; ships EMPTY, no backfill):
```sql
CREATE TABLE IF NOT EXISTS observation_revisions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    obs_sync_id TEXT    NOT NULL,          -- cross-machine identity (memory_relations/anchors convention)
    op          TEXT    NOT NULL,          -- 'update' | 'soft_delete'
    valid_from  TEXT    NOT NULL,          -- pre-mutation updated_at (or created_at): when this state became live
    valid_to    TEXT    NOT NULL,          -- this mutation's timestamp: when it stopped being live
    snapshot    TEXT    NOT NULL,          -- full JSON PRE-image of observation columns (schema-drift-proof, sync_mutations payload convention)
    recorded_at TEXT    NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_obsrev_asof   ON observation_revisions(obs_sync_id, valid_from, valid_to);
CREATE INDEX IF NOT EXISTS idx_obsrev_syncid ON observation_revisions(obs_sync_id);
```
Written inside the SAME `withTx` as the mutation (piggyback on existing tx in `deleteObservation` / `UpdateObservation` / `SaveObservation` revision branch), before `enqueueSyncMutationTx`. On CAP>0, prune oldest rows for that `obs_sync_id` past the cap in-tx.

### Decision 2 — Provable-deletion × time-travel (the tombstone rule)

**Choice:** In `deleteObservation`'s HARD branch (`store.go:3982`), inside the existing tx that physically `DELETE`s the row + writes the `deletion_tombstones` proof, ALSO `DELETE FROM observation_revisions WHERE obs_sync_id = ?`. Hard-delete writes NO new revision row. The tombstone thereby proves the purge covered history too (its `content_hash` still attests what was deleted). `HardDeleteWithPurge` is unchanged — the extension lives entirely under `store.DeleteObservationWithActor`, which it already calls, so all three entry points (MCP/HTTP/CLI) inherit it for free.
**Rationale:** Resurrection is structurally impossible — no genesis/edit pre-images survive and the live row is gone; the sync-outbox leak (defect #2) can't apply because history is NOT in the outbox. This is the highest-stakes test.

### Decision 3 — `--as-of` read path

**Choice:** A `StateAsOf(sync_id/id, T)` resolver: return the `observation_revisions` snapshot whose `valid_from <= T < valid_to`; else the LIVE row if `created_at <= T` AND (`deleted_at` IS NULL OR `deleted_at > T`); else not-visible. Thread a new `as_of` string arg through `handleSearch`/`handleGetObservation`/`handleContext` (`req.GetArguments()["as_of"]`, existing idiom) and CLI. `get`/`context` are EXACT (pure timeline query). **SEARCH is FTS-over-live-content only:** `observations_fts` is `content='observations'` (live table); history is NOT indexed. So `mem_search --as-of T` matches today's text, then rewrites each hit to its state-at-T (dropping rows not visible at T). Honest limitation: you cannot find a memory by words that only existed in its March version and were later edited out. A historical FTS index is expensive → deferred (v0.3.3).
**Rationale:** Exact where cheap (get/context), pragmatic where not (search); no new FTS storage.

### Decision 4 — `mem_bisect` (CLI-first, resumable)

**Choice:** Event timeline = observations in project/scope with `created_at` in `(good, bad]`, ordered by `created_at` (matches F grounding; edit-events deferred). Classic binary search `[lo,hi]`, `mid=(lo+hi)/2`; at each midpoint render the memory state as-of `event[mid].created_at` (RIDES Decision 3), user marks good→`lo=mid+1` / bad→`hi=mid`; converge at `lo==hi` = introducing memory. **Resumable file-backed state** (git-bisect style): `$OMNIA_DATA_DIR/bisect-state.json`. Subcommands: `omnia bisect start --good <ts|id> [--bad <ts|id=now>]` · `good` · `bad` · `status` · `reset`. Terse rendering ("Bisecting: N left to test", midpoint id/title/created_at). CLI-only in v1 (interactive judgment ill-suits stateless MCP; MCP wrapper deferred).
**Rationale:** Human-in-the-loop needs cross-invocation resumability; reuses the as-of substrate for zero new read logic.

### Decision 5 — portable-export (N, full graph)

**Choice:** Keep SINGLE JSON envelope (existing `omnia import <file.json>` reads one blob; JSONL would break it — deferred). Extend `ExportData`: add `schema_version` (int, =2), `counts` per collection, `checksum` (sha256 over canonically-ordered payload), and NEW collections — `relations` (`memory_relations`), `anchors` (`memory_anchors`), `procedures` (local-only in sync, but a file move should carry them). Deterministic: fixed collection order, each `ORDER BY sync_id`/`created_at,sync_id`. **Idempotent import = application-level upsert-by-`sync_id`** (`SELECT id WHERE sync_id=?` → update-or-insert), NOT a UNIQUE-index migration — `observations.sync_id` has only a non-unique index (`idx_obs_sync_id`) and legacy dupes could fail a `UNIQUE` migration. `import` dispatches on `schema_version`: v1/legacy (`0.1.0`, no field) → existing path untouched; v2 → full-graph idempotent path; unknown-future → reject with a clear message. `claude-memory` submode untouched.
**Rationale:** Zero disruption to the live import surface + safe (no risky schema migration) + true round-trip.

### Decision 6 — Config surface + retention

**Choice:** New block, default-OFF:
```yaml
time_travel:
  enabled: false               # D7 default-OFF: no capture, as_of ignored, bisect errors → byte-for-byte today
  max_revisions_per_memory: 0   # 0 = unlimited (business rule); >0 caps history per memory
```
Map via the `WriteHygieneEnabled` idiom (`store.go` composition points `cmd/omnia/main.go:876/1168/1252/1491/1829`): `cfg.TimeTravelEnabled = appCfg.TimeTravel.Enabled`, `cfg.HistoryRevisionCap = appCfg.TimeTravel.MaxRevisionsPerMemory`; `MCPConfig.TimeTravelEnabled` gates the `as_of` arg. "History starts when you enable time_travel (your upgrade moment); earlier states were never recorded" — disclaimed in `omnia doctor`/`stats`, in `--as-of` output when T predates the earliest revision, and docs.
**Rationale:** Reconciles D7 default-OFF with "history starts at upgrade" — enabling IS the user's upgrade; non-adopters pay zero storage and see zero change.

## Data Flow

    write:  Save/Update/SoftDelete ─(same tx)→ [before-image → observation_revisions] → enqueueSyncMutationTx
    read:   search/get/context + as_of=T ─→ StateAsOf(T): revision interval ∨ live-if-visible
    bisect: start/good/bad ─→ event list (created_at) ─→ midpoint ─→ StateAsOf(event.created_at) render
    purge:  HardDeleteWithPurge → DeleteObservationWithActor(hard) ─(same tx)→ DELETE row + DELETE revisions + tombstone

## File Changes

| File | Action | Description |
|------|--------|-------------|
| `internal/store/store.go` | Modify | `observation_revisions` DDL in `migrate()`; before-image capture in `UpdateObservation`/`SaveObservation`-revision/`deleteObservation`; purge extension in hard branch; `StateAsOf` resolver; `ExportData` v2 + collections + checksum; idempotent `Import` upsert |
| `internal/store/timetravel.go` | Create | `StateAsOf`, revision snapshot marshal/prune, event-timeline query (keeps store.go churn contained) |
| `internal/config/config.go` | Modify | `TimeTravelConfig` block, default-OFF probe |
| `cmd/omnia/main.go` | Modify | `time_travel` → store.Config wiring; `case "bisect"`; export/import v2 wiring |
| `cmd/omnia/bisect.go` | Create | `omnia bisect` subcommands + resumable `bisect-state.json` |
| `internal/mcp/mcp.go` | Modify | `as_of` arg in `handleSearch`/`handleGetObservation`/`handleContext`, gated |

## Testing Strategy (strict TDD, RED→GREEN)

| Slice | Layer | What / Approach |
|-------|-------|-----------------|
| N | Unit | export includes relations/anchors/procedures; round-trip export→import→export byte-identical (ordering+checksum); re-import idempotent (twice → no dupes); schema_version reject/legacy-v1 accept. **Scale note:** run with a few thousand observations (v0.3.1 realistic-scale lesson). |
| E | Unit | before-image captured on update/soft-delete, NOT on insert/when-disabled; `StateAsOf` correct across get/context; search as-of = live-recall + as-of content (limitation asserted); cap prune. |
| E | **Highest-stakes** | tombstone×time-travel purge: create→edit→hard-delete ⇒ (a) live gone, (b) ALL revisions for sync_id gone, (c) tombstone present, (d) `--as-of` at ANY T returns nothing (no resurrection). Scale note: many-edit history growth. |
| F | Unit/Integ | event-list order/filter; binary-search convergence; resumable state file round-trip (start→good→bad→status→reset); disabled errors; empty/single/all-good/all-bad edges; rides E's rendering. |

## Migration / Rollout

Additive only. `observation_revisions` ships EMPTY (no backfill possible — disclaimed). No UNIQUE-index migration (app-level upsert chosen). `time_travel.enabled=false` default ⇒ byte-for-byte today. Rollback = disable flag / drop table / drop `bisect`; v2 export reads old v1 files.

## Slice / PR Sketch (stacked-to-main, ≤400 lines each)

1. **PR1 — N portable-export** (self-contained, lowest risk, ships value alone): ExportData v2 + collections + checksum + deterministic order + idempotent import + schema_version dispatch + CLI. ~350-400 (split export-completeness / import-idempotency if over).
2. **PR2 — E substrate**: DDL + before-image capture + config gate + hard-delete purge extension + purge test. ~300-400.
3. **PR3 — E reads**: `StateAsOf` + `as_of` through search/get/context + MCP/CLI arg + FTS-limitation handling. ~350-400.
4. **PR4 — F bisect**: `omnia bisect` subcommands + resumable state + event list + midpoint + rendering (rides PR3). ~350-400.

**Review Workload Forecast:** Chained/stacked PRs recommended: Yes. 400-line budget risk: Medium-High per slice. Decision needed before apply: Yes (confirm 4 stacked slices vs size:exception). Delivery order N→E→F (proposal-locked).

## Open Questions

- [ ] Bisect timeline: creation-events only (v1, matches F grounding) vs also edit-events (an edit could introduce the regression) — v1 = creation-only, revisit in F spec.
- [ ] Export `checksum` scope: over payload only, or include envelope metadata — decide in N spec (recommend payload-only for stable round-trip).
