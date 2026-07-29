# Proposal: Omnia v0.3.2 — Time Travel

> **PLANNING ONLY.** Proposal → specs → design → tasks. NO implementation until the user explicitly approves. Story: "tu memoria tiene historia y es tuya."

## Intent

Omnia today is *presentist*: `UpdateObservation` overwrites content in place (`revision_count` is a bump-counter, not a version), and hard-delete drops the row. There is **no history table** — the store cannot answer "what did we believe in March?", cannot trace which memory introduced a regression, and its `omnia export` file is incomplete + non-idempotent. v0.3.2 turns memory into something with a past you can query, bisect, and carry out. Three capabilities: E (`--as-of`), F (`mem_bisect`), N (portable export).

## Scope

### In Scope (planning artifacts only)
- **E `--as-of <timestamp>`** on search/get/context: answer queries as memory *was recorded* at time T (recorded-time travel, Zep-style).
- **F `mem_bisect`**: binary-search the decision/observation timeline between a user-marked good and bad point to isolate the memory/decision that introduced a regression (CLI-first, interactive judgment).
- **N portable export**: complete, versioned, re-importable format; idempotent round-trip with existing `omnia import`.
- The minimal **history substrate** E requires (see Approach — this is the load-bearing decision, deferred to sdd-design).

### Out of Scope
- Plays M (sqlite-vec), I (learning-to-rank), J (repo cartridge) → v0.3.3.
- Any LLM synthesis; encryption at-rest → v0.4; cloud-side history.
- Backfilling history that predates the substrate. Implementation itself.

## Capabilities

### New Capabilities
- `time-travel-query`: recorded-time `--as-of` reads over search/get/context.
- `memory-bisect`: `mem_bisect` regression-hunt over the memory timeline.
- `portable-export`: complete, versioned, idempotent export/import format.

### Modified Capabilities
- Existing **export/import** capability (confirm exact spec name in sdd-spec): N extends `store.ExportData` (adds `memory_relations`, schema version discipline) and makes `Import` idempotent by `sync_id`.

## Approach

**Load-bearing finding — the history substrate (E depends on this):**
The `observations` table holds one mutable row per memory; updates overwrite, hard-deletes purge. The *only* append-only record of past states is `sync_mutations` — the cloud-sync **outbox**, which enqueues a full-state upsert payload (keyed `sync_id` + `seq` + `occurred_at`) on every insert/update/delete, unconditionally, even with cloud disabled, and does **not** prune acked rows in production. Replaying it up to `occurred_at <= T` reconstructs approximate past state.

But it is a *repurposed* outbox with three defects: (1) no history before the log → **time travel starts at upgrade, backfill impossible**; (2) hard-delete enqueues a content-less delete but leaves prior upsert payloads → **hard-deleted content resurrects via `--as-of`, violating provable deletion**; (3) it is coupled to sync semantics (`target_key`, `acked_at`).

→ **Design fork for sdd-design:** either (A) a purpose-built append-only `observation_revisions` table written on UPDATE/DELETE, or (B) a hardened, sync-decoupled read over `sync_mutations`. Both MUST extend provable deletion so a hard-delete also purges that memory's history rows (tombstone × time-travel rule).

**Slice sketch (delivery order):** N (self-contained, lowest risk, ships value alone) → E substrate + `--as-of` reads → F (rides on E's timeline). Each an independent PR.

**Business rules:** local-first, deterministic, no LLM. Default byte-for-byte unchanged (`--as-of` absent = today's behavior). History substrate optional retention config; hard-delete purges history too.

## Affected Areas

| Area | Impact | Description |
|------|--------|-------------|
| `internal/store/store.go` | Modified | History substrate on Update/Delete; `--as-of` read paths; idempotent `Import`; `ExportData` completeness |
| `internal/mcp` (search/get/context) | Modified | Thread `--as-of` param; default no-op |
| `cmd/omnia` (`export`/`import`, new `bisect`) | Modified/New | Versioned export; `mem_bisect` CLI |
| `internal/store/relations.go` | Read | supersedes lineage feeds F |

## Risks

| Risk | Likelihood | Mitigation |
|------|------------|------------|
| History storage growth | Med | Substrate starts at upgrade; optional retention/cap config |
| Hard-deleted content leaks via time travel | High | Provable-deletion rule: hard-delete purges history rows too (spec explicitly) |
| Bisect UX complexity | Med | CLI-first, interactive good/bad marking; no automated test harness |
| Export completeness vs schema drift | Med | Explicit `schema_version`; reject/upgrade on import |
| Reusing sync outbox couples history to sync bugs | Med | sdd-design fork: dedicated table vs hardened read |

## Rollback Plan

All three capabilities are additive and feature-flagged default-OFF (D7 convention): `--as-of` absent = current behavior byte-for-byte; new history writes gated by config; `mem_bisect` is a new command. Revert = disable flags / drop the new command; existing export/import stays backward-compatible (versioned format reads old files).

## Dependencies

- None external. Builds on existing `sync_mutations`, `memory_relations`, and `store.Export/Import`.

## Success Criteria

- [ ] `--as-of T` returns memory as recorded at T; absent = identical to today.
- [ ] Hard-deleted memories are invisible to `--as-of` (history purged) — provable deletion holds.
- [ ] `mem_bisect` converges to the offending memory via interactive good/bad marks.
- [ ] Export → import → export is idempotent and loses no relations/provenance.
- [ ] Substrate + retention documented; "time travel starts at upgrade" stated plainly to users.

## Proposal question round (needs user review before sdd-spec)

1. **E semantics** — recorded-time only ("what did we *store* by T"), or also *valid-time* (Zep's `valid_at` vs `recorded_at` bitemporal)? Recorded-only is far cheaper and matches the substrate.
2. **Substrate fork** — preference between a dedicated `observation_revisions` table (clean, honest, more storage) vs. hardened read over `sync_mutations` (reuse, but couples to sync)? Or leave to sdd-design.
3. **F "test"** — bisect is purely interactive human good/bad judgment, correct? No automated regression oracle in v0.3.2?
4. **N scope** — must the export also carry `sessions` + `user_prompts` + anchors, or observations + relations only?
5. **Retention** — default unbounded history with opt-in cap, or a default cap (e.g. N revisions / M months)?
