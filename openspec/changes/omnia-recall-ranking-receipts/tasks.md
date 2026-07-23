# Tasks: Memory Recall Ranking & Receipts

## Review Workload Forecast

| Field | Value |
|-------|-------|
| Estimated changed lines | ~550-650 (whole change); ~150-300 per suggested PR |
| 400-line budget risk | High |
| Chained PRs recommended | Yes |
| Suggested split | PR1 config → PR2 ranking core+wiring → PR3 explain receipt → PR4 CLI+regression |
| Delivery strategy | ask-on-risk |
| Chain strategy | pending |

Decision needed before apply: Yes
Chained PRs recommended: Yes
Chain strategy: pending
400-line budget risk: High

### Suggested Work Units

| Unit | Goal | Likely PR | Notes |
|------|------|-----------|-------|
| 1 | Phase 1: `RankingConfig` + defaults + importance table | PR 1 | Inert (nothing reads it yet); ~150-200 lines |
| 2 | Phases 2-3: score primitives + `RankResults` wired into `handleSearch` | PR 2 | Gated by `enabled=false` default; ~250-300 lines; depends on PR 1 |
| 3 | Phase 4: `explain`/receipt surface on `mem_search` | PR 3 | ~150-200 lines; depends on PR 2 |
| 4 | Phase 5-6: `omnia search --explain` + regression gate | PR 4 | ~120-150 lines; depends on PR 3 |

## Phase 1: Config Foundation (`internal/config`)
- [x] 1.1 TEST `internal/config/recall_ranking_config_test.go`: `TestRankingConfig_DefaultsDisabled` — no `recall.ranking` in yaml → `Enabled=false`, `Weights.{Recency,Importance,Relevance}=1.0`, `RecencyHalfLifeDays=14`.
- [x] 1.2 IMPL `config.go`: add `RankingConfig{Enabled bool; RecencyHalfLifeDays float64; Weights RankingWeights; ImportanceOverrides map[string]float32}` + `RankingWeights{Recency,Importance,Relevance float32}`; add `RecallConfig.Ranking` (~L101, matches `recall.ranking.*` keys from spec Req 4).
- [x] 1.3 IMPL `applyDefaults()` (~L350): default weights to 1.0, half-life to 14 when zero; `Ranking.Enabled` stays zero-value default (mirrors `Recall.Enabled` doc).
- [x] 1.4 TEST `TestRankingConfig_ParsesOverrides` — full `recall.ranking.*` yaml roundtrip.
- [x] 1.5 TEST `TestDefaultImportanceWeight_DecisionAboveChatter` — `decision`/`architecture` > `tool_use`/`file_read`/`search`.
- [x] 1.6 IMPL `DefaultImportanceWeight(obsType string) float32` in `config.go`: decision/architecture=3, bugfix/pattern/manual=2, else=1.

## Phase 2: Ranking Primitives (new `internal/mcp/recall_ranking.go`)
- [x] 2.1 TEST `internal/mcp/recall_ranking_test.go`: `TestRankScore_WeightedSum` — pins `final = wR*relevance + wC*recency + wI*importance` (Generative Agents shape; resolves spec's open weighted-sum-vs-multiplicative note).
- [x] 2.2 IMPL `RankScore(relevance, recency, importance float64, w config.RankingWeights) float64`.
- [x] 2.3 TEST `TestComputeRecency_HalfLifeDecay` — 1.0 at t=0, 0.5 at t=halfLife, monotonic, never 0 (Req: recency never hard-filters).
- [x] 2.4 IMPL `ComputeRecency(updatedAt string, now time.Time, halfLifeDays float64) (float64, bool)` — `0.5^(elapsedDays/halfLife)`; `ok=false` on unparseable `UpdatedAt`.
- [x] 2.5 TEST `TestImportanceScore_NormalizedToUnitRange` — per-type weight ÷ max configured weight.
- [x] 2.6 IMPL `ImportanceScore(obsType string, cfg config.RankingConfig) float64`.

## Phase 3: Ranking Pass (wiring boundary, `internal/mcp` — `internal/recall` stays untouched/pure per D6)
- [x] 3.1 TEST `TestRankResults_DisabledIsNoop` — `Ranking.Enabled=false` → order/scores byte-identical (backward-compat gate).
- [x] 3.2 TEST `TestRankResults_RelevanceGapWins` — large gap survives ranking.
- [x] 3.3 TEST `TestRankResults_RecencyBreaksNearTie`.
- [x] 3.4 TEST `TestRankResults_DecisionOutranksToolUseAtParity`.
- [x] 3.5 TEST `TestRankResults_OldImportantNotExcluded` — stays in results, may rank lower.
- [x] 3.6 TEST `TestRankResults_ExactSentinelStaysFirst` — `Rank==-1000` rows pre-empt ranking.
- [x] 3.7 IMPL `RankResults(results []store.SearchResult, relevance map[int64]float64, cfg config.RankingConfig, now time.Time) []store.SearchResult` — min-max normalize `relevance` across batch; keep exact rows first; stable-sort rest by `RankScore` desc.
- [x] 3.8 IMPL `internal/mcp/mcp.go` `handleSearch` (~L1073-1105): build `relevance` map (RRF `Score` for recall path, negated `Rank` for FTS5-only path); call `RankResults` before response loop (~L1107); add `RecallRanking config.RankingConfig` to `MCPConfig` (~L70).

## Phase 4: Score-Breakdown Receipt (`explain`)
- [x] 4.1 TEST `TestBuildReceipt_FullBreakdown` — explain+ranking on → lexical/semantic/fusion/recency/importance/final/`staleness_penalty` all present.
- [x] 4.2 TEST `TestBuildReceipt_OmittedByDefault` — no `explain` arg → no breakdown fields (unchanged shape).
- [x] 4.3 TEST `TestBuildReceipt_SemanticNullWhenDisabled` — FTS5-only path → `semantic` null, rest populated.
- [x] 4.4 TEST `TestBuildReceipt_StalenessPenaltyReservedZero` — always `0`/null this slice (forward-compat for `memory-structural-forgetting` obs #1595 Req 6, no schema change later).
- [x] 4.5 IMPL `BuildReceipt(...) map[string]any` in `recall_ranking.go`, nil/omit unavailable components.
- [x] 4.6 IMPL `mcp.go`: add `mcp.WithBoolean("explain", ...)` to `mem_search` tool (~L340); `handleSearch` parses `explain` bool, attaches `entry["score_breakdown"]` per hit (~L1149-1173) only when true.

## Phase 5: CLI `omnia search --explain`
- [x] 5.1 TEST `cmd/omnia/main_extra_test.go`: `TestCmdSearch_ExplainFlag_PrintsBreakdown`.
- [x] 5.2 TEST `TestCmdSearch_NoExplainFlag_UnchangedOutput` — byte-identical to today's output.
- [x] 5.3 IMPL `cmd/omnia/main.go` `cmdSearch` (~L1164-1201): parse `--explain`; print `mcp.BuildReceipt` output per result after existing block (~L1234-1243).
- [x] 5.4 IMPL `cmd/omnia/recall.go` `buildRecallService`/`cmdMCP` (~L214): thread `appCfg.Recall.Ranking` into `MCPConfig.RecallRanking`.

## Phase 6: Regression Gate
- [x] 6.1 TEST `internal/mcp/recall_wiring_test.go`: `TestHandleSearch_RankingUnsetByteIdentical` — zero-value `RecallRanking` → response identical to pre-change fixture (gates every requirement in this spec).
- [x] 6.2 Run `go test ./...`; confirm zero regressions in `internal/recall`, `internal/mcp`, `internal/config`, `cmd/omnia`.
