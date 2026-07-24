# Tasks: Omnia v0.3 — Context Economy

## Review Workload Forecast

| Field | Value |
|-------|-------|
| Estimated changed lines | ~1650 total (200+350+250+350+300+200) |
| 400-line budget risk | Low per-PR (each PR designed ≤400); Medium if PR2/PR4 tests grow |
| Chained PRs recommended | Yes |
| Suggested split | PR1 → PR2 → PR3 → PR4 → PR5 → PR6 (stacked-to-main) |
| Delivery strategy | ask-on-risk (resolved: chained, already decided) |
| Chain strategy | stacked-to-main |

Decision needed before apply: No
Chained PRs recommended: Yes
Chain strategy: stacked-to-main
400-line budget risk: Low

### Suggested Work Units

| Unit | Goal | Likely PR | Notes |
|------|------|-----------|-------|
| 1 | `internal/token` leaf (EstimateTokens + TrimToBudget) | PR 1 | Base: main. Foundation, no wiring, no behavior change. |
| 2 | `ApplyTokenBudget` + handleSearch wiring + `InjectionConfig` + recall-fix migration | PR 2 | Base: main (after PR1 merged). Depends on PR1. Includes shared pre-emption invariant test. |
| 3 | FormatContext token budget (uncapped-bug fix) | PR 3 | Base: main (after PR2 merged). Depends on PR1 (token leaf); independent of PR2's mcp wiring but shares config block. |
| 4 | `ApplyMMR` diversity pass | PR 4 | Base: main (after PR3 merged). Depends on PR1+PR2 (pipeline + invariant test to extend). |
| 5 | `InferLensType` + `ApplyTypeLens` | PR 5 | Base: main (after PR4 merged). Depends on PR1+PR2 (invariant test to extend); inserted before MMR in pipeline order. |
| 6 | `user-prompt-submit.sh` signal-gated nudge (Play O) | PR 6 | Base: main (after PR5 merged). Bash-only, isolated, no Go dependency. |

---

## PR1 — `internal/token` leaf (Slice 0)

**Requirement**: token-estimation domain, all 5 REQs (spec obs #1642).
**Estimated changed lines**: ~200
**Files touched**: `internal/token/token.go` (new), `internal/token/token_test.go` (new), `internal/token/doc.go` (new, optional — package doc).
**Dependencies on previous PRs**: None (foundation).

- [x] 1.1 RED: `internal/token/token_test.go` — `EstimateTokens` golden table: empty string → 0; short ASCII string → exact rune-based expected value; unicode/CJK string → documented under-count value (pins the heuristic).
- [x] 1.2 RED: `internal/token/token_test.go` — `EstimateTokens` determinism test: same input called twice → identical result.
- [x] 1.3 RED: `internal/token/token_test.go` — `EstimateTokens` bounded edge cases: multi-MB input completes without panic/timeout (bounded time).
- [x] 1.4 RED: `internal/token/token_test.go` — `TrimToBudget[T]` table: whole-item-only (never partial item); `budget<=0` → `(nil, 0)`; huge budget → all items kept; exact-fit boundary (item exactly fills remaining budget → included).
- [x] 1.5 GREEN: implement `EstimateTokens(text string) int = (utf8.RuneCountInString(text)+3)/4` in `internal/token/token.go`.
- [x] 1.6 GREEN: implement generic `TrimToBudget[T any](items []T, sizeOf func(T) int, budget int) (kept []T, used int)` in `internal/token/token.go`.
- [x] 1.7 Docs: package doc comment on `internal/token` documenting the char/4 heuristic, ±25% English/code accuracy, CJK under-count caveat (spec REQ3 — heuristic documented, not exact tokenizer parity). Implemented as the package doc comment atop `token.go` (matches the repo convention in `internal/recall/recall.go`) instead of a separate `doc.go`.
- [x] 1.8 Verify: `CGO_ENABLED=0 go test ./internal/token/...` + `go build ./...` + `go vet ./...` + `gofmt -l internal/token/` all clean.
- [ ] 1.9 PR: branch `feat/token-estimation-leaf`, reference approved issue, `type:feature` label, squash-merge to main. (Left unchecked — sdd-apply does not create branches/commits/PRs per orchestrator instruction; orchestrator handles git.)

---

## PR2 — `ApplyTokenBudget` + handleSearch wiring + recall-fix migration (Slice 1a)

**Requirement**: injection-budget domain REQ1, REQ2, REQ4, REQ5, REQ6 (spec obs #1642); shared pre-emption invariant (design section 5).
**Estimated changed lines**: ~350
**Files touched**: `internal/mcp/token_budget.go` (new), `internal/mcp/token_budget_test.go` (new), `internal/mcp/preemption_invariant_test.go` (new, shared), `internal/mcp/mcp.go` (wiring, ~1289 preview loop area), `internal/config/config.go` (`InjectionConfig`, `TokenBudgetConfig`, `applyDefaults`), `internal/mcp/mcp.go` (`MCPConfig.Injection`), `cmd/omnia/main.go` (wiring cfg.Injection into MCPConfig), `cmd/omnia/recall_fix.go` (migrate `maxRecallFixTotalChars`), `cmd/omnia/recall_fix_test.go` (parity test).
**Dependencies on previous PRs**: PR1 (`internal/token`).

- [x] 2.1 RED: `internal/mcp/preemption_invariant_test.go` (new shared file) — table-driven test asserting sentinel (`Rank == exactSentinelRank`) and `SignatureMatch` rows are always present and always ordered before non-pre-empted rows, for `ApplyTokenBudget` under adversarial params (`MaxTokens=1`). Structure this file so PR4/PR5 extend the same table with their own passes.
- [x] 2.2 RED: `internal/mcp/token_budget_test.go` — `ApplyTokenBudget` trims a 20-item result set to fit `MaxTokens`, keeping top-ranked items complete and dropping the rest entirely (no partial truncation).
- [x] 2.3 RED: `internal/mcp/token_budget_test.go` — no-op byte-for-byte test: `cfg.Enabled=false` → output identical to input (golden equality).
- [x] 2.4 RED: `internal/mcp/token_budget_test.go` — sentinel/signature rows excluded from budget accounting: tiny budget smaller than smallest eligible item → pre-empted rows still returned complete, non-pre-empted rows dropped, no error.
- [x] 2.5 RED: `cmd/omnia/recall_fix_test.go` — token-parity test: recall-fix output using `token.TrimToBudget` with `maxRecallFixTokenBudget≈150` produces ~same size as the old 600-char cap.
- [x] 2.6 GREEN: implement `ApplyTokenBudget(results []store.SearchResult, cfg config.TokenBudgetConfig) []store.SearchResult` in `internal/mcp/token_budget.go` — gated no-op (`!cfg.Enabled || cfg.MaxTokens<=0 || len(results)==0`); partition `preempted`/`rest`; `previewTokens(r) = token.EstimateTokens(truncate(r.Content, 300))`; `token.TrimToBudget(rest, previewTokens, cfg.MaxTokens)`; `out = append(preempted, kept...)`.
- [x] 2.7 GREEN: add `InjectionConfig` + `TokenBudgetConfig` structs to `internal/config/config.go`, `Config.Injection InjectionConfig` field, `applyDefaults` sets `MaxTokens→1500` when zero (mirrors Ranking-weights idiom).
- [x] 2.8 Wiring: add `MCPConfig.Injection config.InjectionConfig` (value type) in `internal/mcp/mcp.go`; call `ApplyTokenBudget` as the LAST pass in the handleSearch pipeline (after `RankResults`/`ApplyStalenessDownrank`, before the ~1289 display/preview loop).
- [x] 2.9 Wiring: `cmd/omnia/main.go` passes `cfg.Injection` into `MCPConfig.Injection` at composition root.
- [x] 2.10 GREEN: migrate `cmd/omnia/recall_fix.go` from `maxRecallFixTotalChars=600` + `truncate(out,600)` to `token.TrimToBudget(hitLines, token.EstimateTokens, maxRecallFixTokenBudget)` with `maxRecallFixTokenBudget = 150`.
- [x] 2.11 Docs: update `internal/config` doc comment / README config reference noting the new `injection.budget` block, default-off, default `max_tokens=1500`.
- [x] 2.12 Verify: `CGO_ENABLED=0 go test ./...` + `go build ./...` + `go vet ./...` + `gofmt -l .` all clean.
- [ ] 2.13 PR: branch `feat/injection-token-budget`, reference approved issue, `type:feature` label, squash-merge to main (base: main, after PR1). (Left unchecked — sdd-apply does not create branches/commits/PRs per orchestrator instruction; orchestrator handles git.)

---

## PR3 — FormatContext token budget (Slice 1b, uncapped-bug fix)

**Requirement**: injection-budget domain REQ3 (spec obs #1642 — aggregate cap on FormatContext buckets).
**Estimated changed lines**: ~250
**Files touched**: `internal/store/store.go` (`Config.ContextTokenBudget`, `FormatContext` at line 3916, `DefaultConfig`/`FallbackConfig`), `internal/store/store_test.go`, `cmd/omnia/main.go` (wiring).
**Dependencies on previous PRs**: PR1 (`internal/token`). Independent of PR2's handleSearch wiring but shares the same `InjectionConfig` block already added in PR2.

- [x] 3.1 RED: `internal/store/store_test.go` — uncapped-bug regression test: large synthetic dataset (many sessions/pinned/recent-obs/prompts) with `ContextTokenBudget` set → today's code (pre-fix) produces unbounded output; post-fix output stays within budget.
- [x] 3.2 RED: `internal/store/store_test.go` — no-op-off test: `ContextTokenBudget=0` → `FormatContext` output byte-for-byte identical to pre-v0.3 behavior.
- [x] 3.3 RED: `internal/store/store_test.go` — priority allocation test: pinned bucket never starved when overall budget is tight and prompts bucket would otherwise consume it all.
- [x] 3.4 GREEN: add `ContextTokenBudget int` field to `store.Config` (0 = disabled); update `DefaultConfig()`/`FallbackConfig()` to leave it 0 by default.
- [x] 3.5 GREEN: modify `FormatContext` (`internal/store/store.go:3916`) to consume budget in priority order — pinned → recent observations → recent sessions → recent prompts — calling `token.TrimToBudget(items, itemTokens, remaining)` per bucket against a running remainder; keep existing per-item 200/300-char truncation unchanged (bounds each line; budget bounds the sum).
- [x] 3.6 Wiring: `cmd/omnia/main.go` sets `store.Config.ContextTokenBudget = cfg.Injection.ContextBudget.MaxTokens` only when `cfg.Injection.ContextBudget.Enabled`, else 0. (Threaded in all three FormatContext consumers — `cmdContext`, `cmdMCP`, `cmdServe` — with config load hoisted before `storeNew` since `s.cfg` is immutable after construction; covered by `cmd/omnia/context_budget_wiring_test.go`.)
- [x] 3.7 GREEN: add `ContextBudget TokenBudgetConfig` field to `InjectionConfig` in `internal/config/config.go` (added in PR2) with its own `applyDefaults` default `MaxTokens→1500`.
- [x] 3.8 Docs: update config reference documenting `injection.context_budget` block and the FormatContext uncapped-bug fix.
- [x] 3.9 Verify: `CGO_ENABLED=0 go test ./...` + `go build ./...` + `go vet ./...` + `gofmt -l .` all clean.
- [ ] 3.10 PR: branch `fix/formatcontext-token-budget`, reference approved issue, `type:bug` label (fixes the pre-existing uncapped-bucket defect), squash-merge to main (base: main, after PR2). (Left unchecked — sdd-apply does not create branches/commits/PRs per orchestrator instruction; orchestrator handles git.)

---

## PR4 — `ApplyMMR` diversity pass (Slice 2, Play H)

**Requirement**: injection-diversity domain, all 6 REQs (spec obs #1642).
**Estimated changed lines**: ~350
**Files touched**: `internal/mcp/mmr.go` (new), `internal/mcp/mmr_test.go` (new), `internal/mcp/preemption_invariant_test.go` (extend, from PR2), `internal/mcp/mcp.go` (wiring), `internal/config/config.go` (`DiversityConfig`, `applyDefaults`).
**Dependencies on previous PRs**: PR1 (`internal/token`), PR2 (pipeline + `InjectionConfig` + shared invariant test file to extend).

- [ ] 4.1 RED: extend `internal/mcp/preemption_invariant_test.go` — add `ApplyMMR` to the adversarial table (aggressive `lambda`/low `similarity_threshold`) asserting sentinel/signature rows always present and first.
- [ ] 4.2 RED: `internal/mcp/mmr_test.go` — two near-identical rows (Jaccard similarity ≥ `similarity_threshold`) → lower-ranked one dropped/suppressed.
- [ ] 4.3 RED: `internal/mcp/mmr_test.go` — all-distinct result set (similarity below threshold) → order unchanged pre/post pass.
- [ ] 4.4 RED: `internal/mcp/mmr_test.go` — no-op byte-for-byte test: `cfg.Enabled=false` → output identical to input.
- [ ] 4.5 RED: `internal/mcp/mmr_test.go` — λ-boundary ordering test: greedy `argmax[λ·rel(d) − (1−λ)·maxSim(d,selected)]` reselection produces expected order for a small fixture set.
- [ ] 4.6 GREEN: implement token-set Jaccard similarity helper (`jaccardSimilarity(a, b []string) float64` over lowercased preview word tokens) in `internal/mcp/mmr.go`.
- [ ] 4.7 GREEN: implement `ApplyMMR(results []store.SearchResult, relevance map[int64]float64, cfg config.DiversityConfig) []store.SearchResult` — gated no-op (`!cfg.Enabled || len(results)<2`); partition pre-empted out first; greedy MMR reselection over `rest` reusing `MinMaxNormalizeRelevance`; hard-drop candidates with `maxSim ≥ cfg.SimilarityThreshold`.
- [ ] 4.8 GREEN: add `DiversityConfig{Enabled, Lambda, SimilarityThreshold}` to `InjectionConfig` in `internal/config/config.go`; `applyDefaults` sets `Lambda→0.7`, `SimilarityThreshold→0.9` when zero.
- [ ] 4.9 Wiring: insert `ApplyMMR` call in `internal/mcp/mcp.go` handleSearch pipeline after type-lens (PR5 dependency note: if PR5 lands after PR4, insert MMR immediately before the token budget call from PR2; adjust order when PR5 merges), before `ApplyTokenBudget`.
- [ ] 4.10 Docs: update config reference documenting `injection.diversity` block, defaults `lambda=0.7`, `similarity_threshold=0.9`.
- [ ] 4.11 Verify: `CGO_ENABLED=0 go test ./...` + `go build ./...` + `go vet ./...` + `gofmt -l .` all clean.
- [ ] 4.12 PR: branch `feat/injection-mmr-diversity`, reference approved issue, `type:feature` label, squash-merge to main (base: main, after PR3).

---

## PR5 — `InferLensType` + `ApplyTypeLens` (Slice 4, Play A)

**Requirement**: type-as-lens domain, all 6 REQs (spec obs #1642).
**Estimated changed lines**: ~300
**Files touched**: `internal/mcp/type_lens.go` (new), `internal/mcp/type_lens_test.go` (new), `internal/mcp/preemption_invariant_test.go` (extend), `internal/mcp/mcp.go` (wiring), `internal/config/config.go` (`TypeLensConfig`).
**Dependencies on previous PRs**: PR1, PR2 (pipeline + shared invariant test file). Independent of PR4's MMR logic but shares pipeline insertion point.

- [ ] 5.1 RED: extend `internal/mcp/preemption_invariant_test.go` — add `ApplyTypeLens` to the adversarial table (hostile `lensType` matching a sentinel row's type) asserting sentinel/signature rows always present and first.
- [ ] 5.2 RED: `internal/mcp/type_lens_test.go` — `InferLensType`: error/panic/exception/crash/stacktrace/falla query signals → `"bugfix"`; decide/tradeoff/elegir → `"decision"`; architecture/patrón/diseño → `"architecture"`; how-to/pasos/procedimiento → `"pattern"`; no match → `""`.
- [ ] 5.3 RED: `internal/mcp/type_lens_test.go` — explicit-filter-wins: `InferLensType(query, explicitType)` with `explicitType != ""` → always returns `""` regardless of query signal.
- [ ] 5.4 RED: `internal/mcp/type_lens_test.go` — `ApplyTypeLens`: error-signal query → `bugfix`-type rows lifted above non-matching rows (stable partition `[preempted, matchesLens, rest]`), relative order preserved within each partition.
- [ ] 5.5 RED: `internal/mcp/type_lens_test.go` — no-op byte-for-byte test: `cfg.Enabled=false` OR `lensType==""` → output identical to input.
- [ ] 5.6 RED: `internal/mcp/type_lens_test.go` — neutral context test: no situational signal detected → ranking unchanged from baseline.
- [ ] 5.7 GREEN: implement `InferLensType(query, explicitType string) string` in `internal/mcp/type_lens.go` — returns `""` when `explicitType != ""`; else first-match ordered regex table (EN+ES) mapping query signals to `bugfix`/`decision`/`architecture`/`pattern`.
- [ ] 5.8 GREEN: implement `ApplyTypeLens(results []store.SearchResult, lensType string, cfg config.TypeLensConfig) []store.SearchResult` — gated no-op (`!cfg.Enabled || lensType=="" || len(results)==0`); stable partition `[preempted, matchesLens, rest]`, mirroring `ApplyStalenessDownrank`'s sink shape but lifting instead of sinking.
- [ ] 5.9 GREEN: add `TypeLensConfig{Enabled bool}` to `InjectionConfig` in `internal/config/config.go`.
- [ ] 5.10 Wiring: insert `ApplyTypeLens` call in `internal/mcp/mcp.go` handleSearch pipeline BEFORE `ApplyMMR` (order: RankResults → ApplyStalenessDownrank → ApplyTypeLens → ApplyMMR → ApplyTokenBudget), reconciling PR4's provisional insertion point.
- [ ] 5.11 Docs: update config reference documenting `injection.type_lens` block, default-off.
- [ ] 5.12 Verify: `CGO_ENABLED=0 go test ./...` + `go build ./...` + `go vet ./...` + `gofmt -l .` all clean; confirm final pipeline order matches design section 2.
- [ ] 5.13 PR: branch `feat/injection-type-lens`, reference approved issue, `type:feature` label, squash-merge to main (base: main, after PR4).

---

## PR6 — signal-gated recall nudge, bash (Slice 3, Play O)

**Requirement**: signal-gated-recall domain, all 6 REQs (spec obs #1642).
**Estimated changed lines**: ~200
**Files touched**: `plugin/claude-code/scripts/user-prompt-submit.sh` (extend), new shell test file (e.g. `plugin/claude-code/scripts/user-prompt-submit_test.sh` or existing test harness pattern for this script).
**Dependencies on previous PRs**: None — isolated bash, zero Go coupling. Sequenced last per design (D "O last — bash, fully isolated").

- [ ] 6.1 RED: shell test — new-topic prompt (imperative verb start EN/ES: implement/add/fix/haceme/arreglá…) with `OMNIA_SIGNAL_RECALL=1` → emits nudge instruction exactly once.
- [ ] 6.2 RED: shell test — repeat same-topic prompt in same session → deduped, silent (no second nudge), using dedup-marker idiom from `post-tool-error-recall.sh`.
- [ ] 6.3 RED: shell test — uncertainty-signal prompt (how/why/failing/no sé/cómo/ends-with-`?`) → emits nudge instruction.
- [ ] 6.4 RED: shell test — benign/no-signal prompt → outputs `{}`, no nudge, no added latency.
- [ ] 6.5 RED: shell test — malformed/empty stdin → `echo '{}'; exit 0` (never blocks prompt submission).
- [ ] 6.6 RED: shell test — `OMNIA_SIGNAL_RECALL` unset or `0` → always `{}` regardless of signal content (default-off, no-op).
- [ ] 6.7 GREEN: implement new-topic + uncertainty regex classifiers (EN+ES) in `plugin/claude-code/scripts/user-prompt-submit.sh`, gated by `OMNIA_SIGNAL_RECALL` env var.
- [ ] 6.8 GREEN: implement dedup marker write/check reusing `post-tool-error-recall.sh`'s hash-based marker idiom, scoped to `${TMPDIR:-/tmp}/omnia-signal-recall-<session>-<hash>`.
- [ ] 6.9 GREEN: emit INSTRUCTION output (systemMessage/additionalContext nudging `mem_search`), NOT auto-injected results, on trigger match.
- [ ] 6.10 GREEN: wrap all new logic so any failure path (missing env, no match, error) falls through to `echo '{}'; exit 0`.
- [ ] 6.11 Docs: update plugin README/hook comments documenting `OMNIA_SIGNAL_RECALL` env gate, default off, the one deliberate config.yaml exception (bash can't parse YAML).
- [ ] 6.12 Verify: run the shell test suite for this script; confirm exit code 0 and valid JSON output in every branch; manual self-check per design section 6.
- [ ] 6.13 PR: branch `feat/signal-gated-recall-nudge`, reference approved issue, `type:feature` label, squash-merge to main (base: main, after PR5).

---

## Cross-Cutting Notes

- Every PR runs `CGO_ENABLED=0 go test ./...` + `go build ./...` + `go vet ./...` + `gofmt -l .` before merge (project TDD gate, obs #1638).
- Every PR references its approved issue (`Closes #N`), carries exactly one `type:*` label, uses conventional commits, no `Co-Authored-By` trailers, squash-merge (branch-pr skill).
- All flags default OFF; each PR must include an explicit no-op-off byte-for-byte test proving zero behavior change pre-merge.
- The shared `preemption_invariant_test.go` (introduced PR2) is extended, never duplicated, by PR4 and PR5.
