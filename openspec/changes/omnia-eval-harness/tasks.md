# Tasks: Memory Eval Harness

## Review Workload Forecast

| Field | Value |
|-------|-------|
| Estimated changed lines | ~1800-2400 (code+tests+corpus data) across ~20 files |
| 400-line budget risk | High |
| Chained PRs recommended | Yes |
| Suggested split | PR 1 -> PR 2 -> PR 3 -> PR 4 (see Work Units) |
| Delivery strategy | ask-on-risk |
| Chain strategy | pending (ask user: stacked-to-main or feature-branch-chain) |

Decision needed before apply: Yes
Chained PRs recommended: Yes
Chain strategy: pending
400-line budget risk: High

### Suggested Work Units

| Unit | Goal | Likely PR | Notes |
|------|------|-----------|-------|
| 1 | Corpus schema/loader + token-cost metric (EVAL-1, EVAL-2) | PR 1 | Foundation; new `internal/eval` package; corpus data authoring |
| 2 | Scoring split + segmented reporting (EVAL-5, EVAL-3) | PR 2 | Depends on PR1 types; base = PR1 branch |
| 3 | Contradiction test + retrieval-only/end-task split (EVAL-4, EVAL-7) | PR 3 | Depends on PR1/PR2; touches `internal/store/relations.go` read paths + `internal/embed/model_ab.go` reuse |
| 4 | Repro runner + CLI + goreleaser gate + integration/docs (EVAL-6, EVAL-8) | PR 4 | Depends on PR1-3; touches `cmd/omnia/main.go`, `.goreleaser.yaml` |

All new code lives in `internal/eval` (new package, import path `github.com/velion/omnia/internal/eval`) plus `cmd/omnia/eval.go`. Extends, does not fork, `internal/embed` (`ABPair`/`RunModelAB`/`testdata/ab_pairs.json`) and `internal/store/relations.go` (`RelationSupersedes`, `GetRelationsForObservations`).

## Phase 1: Corpus Foundation (EVAL-2)

- [x] 1.1 [RED] `internal/eval/corpus_test.go`: `TestLoadCorpus_EnforcesSizeRange` (fails <50 or >150), `TestLoadCorpus_RequiresOneCapabilityTag`, `TestCase_TracesToObservationID`
- [x] 1.2 [GREEN] `internal/eval/corpus.go`: `EvalCase{ID, ObservationID, Capability, Query, Language, ExpectedFact, SupersedesOf *string}`, `Capability` enum (Recall/Causal/StateUpdate/StateAbstraction), `LoadCorpus(path string) ([]EvalCase, error)`
- [x] 1.3 (PARTIAL — starter seed only) Authored `internal/eval/testdata/cases.json` with 11 real cases sourced from actual dogfooded `mem_save` observations (obs-d0028b15003f693d, obs-ae9cbeb4adab2197, obs-800444bf5da4ec17, obs-69ffb0cfdecf7003, obs-a54f370762c5764e, obs-5f0f37d6c82db89a, obs-32a80db27f88276f, obs-ee26147f34365a09, obs-393ccb0f4b37fb93, obs-f20c63d783b1dceb, obs-6c018942da345c6b); all four capabilities + EN/ES present. Full 50-150 authoring per spec EVAL-2 remains a follow-up data-authoring task before Phase 9's integration test can run `LoadCorpus` against this file (it is below the 50-case floor by design, per PR1 apply-progress). EVAL-4 contradiction fixture deferred to PR3 — requires a real `supersedes` relation (none exist in the DB today); no starter case sets `SupersedesOf` (removed a fabricated `supersedes_of` claim that had no backing DB relation), but the field remains supported in the schema for PR3 to populate against a genuine relation.

## Phase 2: Token-Cost Metric + Pareto (EVAL-1)

- [x] 2.1 [RED] `internal/eval/metric_test.go`: `TestQualityPerToken_Formula`, `TestParetoPoint_IncludesTokenBreakdown`, `TestReport_RejectsAggregateOnly`
- [x] 2.2 [GREEN] `internal/eval/metric.go`: `TokenBreakdown{QueryEmbed, Retrieval, InjectedContext, Judge int}`, `QualityPerToken(accuracy float64, totalTokens int) float64`, `ParetoPoint{Model string; Tokens int; Accuracy float64; Breakdown TokenBreakdown}`. Also added `TokenBreakdown.Total()`, `NewParetoPoint`, `ParetoPoint.QualityPerToken()`, and a minimal `Frontier{Points []ParetoPoint}.Validate()` (the "not a single aggregate scalar" floor) — the full segmented `Report{ByCapability, ByLanguage}` type is Phase 4 / PR2 (EVAL-3), not implemented here.

## Phase 3: Scoring Strategy (EVAL-5)

- [ ] 3.1 [RED] `internal/eval/scoring_test.go`: `TestScoreRecall_SubstringMatch_NoJudgeTokens`, `TestScoreStateUpdate_ExactOrEmbeddingThreshold`, `TestScoreCausal_UsesAgentRunner`, `TestScoreStateAbstraction_CountsJudgeTokensInTotal`
- [ ] 3.2 [GREEN] `internal/eval/scoring.go`: `Score(ctx, case EvalCase, retrieved string, judge llm.AgentRunner) (hit bool, judgeTokens int, err error)` — substring/exact/embedding-threshold for Recall+StateUpdate (no judge); `judge.Compare` for Causal+StateAbstraction (judge tokens counted)

## Phase 4: Segmented Reporting (EVAL-3)

- [ ] 4.1 [RED] `internal/eval/report_test.go`: `TestReport_AllFourCapabilityRowsPresent`, `TestReport_ENvsESRowsUseExistingABPairs`
- [ ] 4.2 [GREEN] `internal/eval/report.go`: `Report{ByCapability map[Capability]Segment; ByLanguage map[string]Segment}`; reuses `internal/embed/testdata/ab_pairs.json` for the ES slice (no re-authoring)

## Phase 5: Adversarial Contradiction (EVAL-4)

- [ ] 5.1 [RED] `internal/eval/contradiction_test.go`: `TestContradiction_SupersededObservationScoresMiss`, `TestContradiction_MissingSupersedesRelationFailsFast`
- [ ] 5.2 [GREEN] `internal/eval/contradiction.go`: loads pairs via `store.Store.GetRelationsForObservations`, asserts a `store.RelationSupersedes` relation exists (fail-fast `error` if absent); hit only if surfaced observation == relation's `TargetID`

## Phase 6: Retrieval-Only vs End-Task (EVAL-7)

- [ ] 6.1 [RED] `internal/eval/retrieval_test.go`: `TestRetrievalSection_WrapsRunModelAB`, `TestReport_RetrievalAndEndTaskAreSeparateSections`
- [ ] 6.2 [GREEN] `internal/eval/retrieval.go`: thin wrapper calling `embed.RunModelAB(ctx, model, emb, pairs, k)` for the recall@k section; `report.go` emits it alongside, never merged with, the end-task accuracy section

## Phase 7: Reproducibility Runner (EVAL-6)

- [ ] 7.1 [RED] `internal/eval/runner_test.go`: `TestRunHarness_ExecutesThreeToFiveRuns`, `TestRunHarness_ReportsMeanAndStddev`, `TestGate_SingleRunRefusesDecision`
- [ ] 7.2 [GREEN] `internal/eval/runner.go`: `RunHarness(ctx, cfg Config, runs int) (RunSummary, error)` (validates `runs` in [3,5]); computes mean/stddev of accuracy and quality-per-1k-tokens per config

## Phase 8: CLI + Release-Gate Wiring (EVAL-8)

- [ ] 8.1 [RED] `cmd/omnia/eval_test.go`: `TestCmdEval_AdvisoryNeverBlocks`, `TestCmdEval_BlockingExitsNonZeroPastThreshold`
- [ ] 8.2 [GREEN] `cmd/omnia/eval.go`: `cmdEval(args []string)` following `cmdEmbed`'s `flag.NewFlagSet` pattern; flags `--mode advisory|blocking` (default `advisory`), `--threshold`, `--runs`; add `case "eval": cmdEval(args[1:])` to the switch in `cmd/omnia/main.go`
- [ ] 8.3 Wire `.goreleaser.yaml` `before.hooks`: add a step invoking the eval gate (advisory by default) before build; blocking-mode non-zero exit aborts goreleaser per EVAL-8

## Phase 9: Integration + Docs

- [ ] 9.1 `internal/eval/harness_integration_test.go`: end-to-end run against `testdata/cases.json` + `ab_pairs.json`; asserts report has Pareto, per-capability, per-language, retrieval-only, and end-task sections plus mean/stddev
- [ ] 9.2 Document `omnia eval` usage and gate config in `DOCS.md`
