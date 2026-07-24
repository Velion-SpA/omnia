# Proposal: Omnia v0.3 — Context Economy

## Intent

Recall is mature (FTS5+semantic RRF fusion, v0.2 ranking, sentinel/signature lanes). The gap is downstream at INJECTION: what reaches the agent is 100% char/count-based, never token-aware. `handleSearch` flat-truncates 10 items to 300 chars; `store.FormatContext` truncates per-item but has ZERO aggregate cap (pre-existing unbounded-growth bug). No token-estimation primitive exists anywhere. This directly answers Hugo's token-economy ask: give the agent only what's needed, only when needed. Why now: v0.3 tells ONE demo-able story, measured by v0.2's per-token quality eval.

## Scope

### In Scope (4 plays + 1 infra slice)
- Slice 0 — token-estimation primitive (char/4, no CGO, no dep).
- Slice 1 (Play B) — injection token budget: how much.
- Slice 2 (Play H) — MMR diversity: no dupes.
- Slice 3 (Play O) — signal-gated activation: when.
- Slice 4 (Play A) — type-as-lens: which type.

### Out of Scope (Non-Goals — deferred to own releases)
Bet 1 code↔decisions graph + mem_blame; Play K at-rest encryption; Play G spaced repetition; Bet 2 memory compiler/enforcement; Bet 3 sleep consolidation; Plays E/F/I/J/M/N; v0.2 tech debt (anchor non-ASCII/locale, firstGrepMatch decoy-shadowing, internal/mcp flake).

## Capabilities

### New Capabilities
- `token-estimation`: char/4 token-count primitive (shared, no CGO).
- `injection-budget`: token-based trim of injected results (Play B).
- `injection-diversity`: MMR near-dup suppression on hydrated content (Play H).
- `signal-gated-recall`: new-topic/uncertainty triggers in prompt hook (Play O).
- `type-as-lens`: situational type boost/filter (Play A).

### Modified Capabilities
None (no existing `openspec/specs`).

## Approach (intent-level; detailed design deferred)

All gated, default-OFF, byte-for-byte no-op when disabled (D7).
- Slice 0: pure leaf estimator, exported for reuse; everything depends on it.
- Slice 1: replace flat truncate+count in `internal/mcp/mcp.go` handleSearch AND `internal/store/store.go` FormatContext with one exported token-budget primitive (also caps FormatContext's uncapped buckets). Shared so `cmd/omnia/recall_fix.go` and mem_search don't drift.
- Slice 2: post-ranking MMR pass over hydrated content (`recall_adapter` HydrateFusedResults), cheap lexical similarity, no new embeddings.
- Slice 3: extend `plugin/claude-code/scripts/user-prompt-submit.sh` with a new-topic/uncertainty classifier, reusing the dedup-marker idiom of post-tool-error-recall.sh.
- Slice 4: extend the importance type-tier in `internal/mcp/recall_ranking.go` with situational type boost/filter.

## Business Rules (non-negotiable)
- All features default-OFF, opt-in, no-op when disabled.
- Token-based, not char-based. Trim = top-N complete by ranking, cut the rest.
- Sentinel (topic_key rank -1000) and signature-match rows ALWAYS complete and OUTSIDE the budget; every new re-rank (budget, MMR) MUST preserve this "recurring-bug fix always surfaces" pre-emption.

## Risks

| Risk | Likelihood | Mitigation |
|------|------------|------------|
| No token primitive exists | High | build Slice 0 first |
| FormatContext uncapped bug | Med | fix in Slice 1, cover with eval |
| Re-rank breaks sentinel/signature pre-emption | High | invariant test; budget/MMR run outside sentinel lane |
| recall-fix vs mem_search drift | Med | one shared exported primitive |

## Rollback
Each slice behind its own config flag; disable → v0.2 behavior. Stacked-to-main; revert per-slice PR.

## Success Criteria
- [ ] Token estimator exported, reused by all injection paths (no drift).
- [ ] Injection respects a token budget in handleSearch and FormatContext; uncapped-bucket bug fixed.
- [ ] Sentinel + signature rows still always complete, outside budget (invariant test passes).
- [ ] Per-token quality (v0.2 eval) holds/improves at lower token cost with flags ON.
- [ ] Flags default-OFF → no-op vs v0.2.

## Proposal question round (could not ask directly — for user review)
1. Default token budget value — ship a conservative default with flag OFF?
2. Delivery order — Slice 0→1 first (core value), O/A as low-risk add-ons?
3. MMR similarity threshold — tune via eval, or expose as config from day one?
