# Proposal: Omnia v0.3.1 — Write Hygiene

## Intent

v0.3 made Omnia *receive* only what's needed. Every save still lands verbatim: the real DB holds duplicate clusters (#1360/#1359, stacked SDD artifacts) and cross-system fragmentation — the codesign/exit-137 and 33GB-disk fixes live only in Claude Code's file memory, invisible to Omnia. v0.3.1 makes Omnia *store* only what's needed — deterministic, no LLM.

## Scope

### In Scope
- **Write-gate on mem_save**: search similars (reuse v0.3 primitives — token-set Jaccard, `internal/token`, FTS) → NOOP / AUTO-UPDATE / SAVE+RELATE. Deterministic, default-ON, config kill-switch, conservative thresholds, full envelope transparency.
- **Pre-save normalization + junk warnings** (empty / too-short / missing-Keywords / oversized).
- **`omnia dedupe`**: offline near-dup clustering over the base; propose-only (`--dry-run` default, `--apply` per cluster). First deterministic step toward Bet 3 consolidation.
- **Fixes**: #147 sessionStart/End project override; `injection.budget` default recalibration (eval-driven, ~300-500); FTS 0-hit relaxation fallback.
- **`omnia import claude-memory <dir>`**: bridge Claude file-memory → observations with provenance tag, deduped through the same write-gate.

### Out of Scope
- LLM consolidation (Bet 3), enforcement (Bet 2), encryption, spaced repetition.
- Retrieval changes beyond the FTS 0-hit fallback.
- Lens-signal tuning + CJK bigrams (only if trivially cheap; else defer).

## Capabilities

### New Capabilities
- `write-gate`: deterministic save-side similarity gate (NOOP / auto-update / save+relate); default-ON, kill-switch, conservative thresholds, envelope transparency.
- `save-normalization`: pre-save cleaning + junk warnings (empty/short/no-Keywords/oversized).
- `dedupe-scan`: `omnia dedupe` offline near-dup clustering, propose-only merges.
- `claude-memory-import`: `omnia import claude-memory <dir>` bridge with provenance tag, idempotent via write-gate.

### Modified Capabilities
- `fts-recall`: 0-hit relaxation fallback (retry with progressively relaxed term set).
- `injection-budget`: recalibrate `injection.budget` default (`context_budget` stays 1500).
- `session-hooks`: fix #147 sessionStart/End project override.

## Approach

Extend the existing Save path (`internal/store` normalized_hash/DedupeWindow + topic_key upsert) with a deterministic pre-insert gate; route SAVE+RELATE through the existing `mem_judge` judgment_required flow. Promote v0.3 similarity primitives (Jaccard/tokenize, unexported in `internal/mcp/mmr.go`) into a shared leaf package. `dedupe` and `import` are additive offline CLI subcommands reusing those primitives. All behavior gated behind `write_hygiene.enabled` (D7: disabled = byte-for-byte v0.3). Depends only on v0.3 primitives already on main.

## Affected Areas

| Area | Impact | Description |
|------|--------|-------------|
| `internal/store/store.go` | Modified | Pre-insert NOOP/auto-update gate |
| `internal/mcp/mcp.go` | Modified | Envelope transparency; SAVE+RELATE via judgment_required |
| shared similarity pkg | New | Exported Jaccard/tokenize |
| `cmd/omnia/main.go` | Modified | `dedupe` + `import claude-memory` |
| config | Modified | `write_hygiene.*`, `injection.budget` |

## Risks

| Risk | Likelihood | Mitigation |
|------|------------|------------|
| Default-ON changes every user's saves | High | Kill-switch, conservative thresholds, envelope transparency, config revert |
| Auto-update wrong merge | Med | Same-topic-key/near-certain only; history preserved; envelope names target #N |
| Dedupe O(n²) on 1660 obs | Med | FTS candidate pre-filter, not all-pairs |
| Import re-run duplicates | Med | Route through write-gate + provenance tag; idempotent |

## Rollback Plan

`write_hygiene.enabled: false` → save reverts to current 15-min hash dedupe. New subcommands additive/offline. Budget default is config-flagged.

## Dependencies
- v0.3 primitives (`internal/token`, MMR Jaccard) on main.

## Success Criteria
- [ ] NOOP/auto-update rates measured on a replay of real saves.
- [ ] `omnia dedupe` yields merge proposals on the real snapshot.
- [ ] `omnia eval` unchanged-or-better; budget decided by eval.
- [ ] Import idempotent (re-run adds zero duplicates).
- [ ] CI green (`CGO_ENABLED=0 go test ./...`, build, vet, gofmt).
