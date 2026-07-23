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

- [x] 3.1 [RED] `internal/eval/scoring_test.go`: `TestScoreRecall_SubstringMatch_NoJudgeTokens`, `TestScoreStateUpdate_ExactOrEmbeddingThreshold`, `TestScoreCausal_UsesAgentRunner`, `TestScoreStateAbstraction_CountsJudgeTokensInTotal` (plus miss/error/rejection edge cases)
- [x] 3.2 [GREEN] `internal/eval/scoring.go`: `Scorer` interface + `Score(ctx, c EvalCase, retrieved string, judge llm.AgentRunner) (hit bool, judgeTokens int, err error)` dispatcher — `JudgeFreeScorer` (substring/exact + optional embedding-threshold via `EmbeddingSimilarity`, no judge, always 0 tokens) for Recall+StateUpdate; `LLMJudgeScorer` (wraps `llm.AgentRunner.Compare` via the existing `llm.BuildPrompt`, hit = Relation `compatible`/`related`, tokens via existing `llm.EstimateScanCost(1)`) for Causal+StateAbstraction

## Phase 4: Segmented Reporting (EVAL-3)

- [x] 4.1 [RED] `internal/eval/report_test.go`: `TestReport_AllFourCapabilityRowsPresent`, `TestReport_ENvsESRowsUseExistingABPairs` (plus `TestSegment_Accuracy`, `TestSegment_QualityPer1kTokens`, `TestBuildReport_AggregatesHitsAndTokens`)
- [x] 4.2 [GREEN] `internal/eval/report.go`: `Segment{Label,Total,Hits,TotalTokens}` + `.Accuracy()`/`.QualityPer1kTokens()`, `CaseResult{Case,Hit,TotalTokens}`, `Report{ByCapability map[Capability]Segment; ByLanguage map[Language]Segment}`, `BuildReport([]CaseResult) Report` (always seeds all 4 capability + 2 language rows). Segmented via the existing corpus's own `Language` field (real dogfooded material, no new dataset authored); `internal/embed/testdata/ab_pairs.json` deliberately stays out of this report — its `ABPair` shape has no Capability/expected-fact fields to segment by, and Phase 6/PR3's retrieval-only recall@k section (EVAL-7) keeps that metric un-merged with this end-task view — see PR2 apply-progress.

## Phase 5: Adversarial Contradiction (EVAL-4) — COMPLETE (PR3)

- [x] 5.1 [RED] `internal/eval/contradiction_test.go`: `TestContradiction_SupersededObservationScoresMiss`, `TestContradiction_MissingSupersedesRelationFailsFast` (plus `TestContradiction_NoSupersedesOfFailsFast` edge case). Fixtures are self-contained: a temp-directory `*store.Store` (own `store.New`/`t.TempDir()` bootstrap in the test file, no shared/user DB) with two real `AddObservation` rows linked by a real, judged `store.SaveRelation`+`store.JudgeRelation(Relation: store.RelationSupersedes)` pair — resolves the fixture PR1 deferred (obs #1609: no `SupersedesOf` case existed because no real supersedes relation existed anywhere in the DB).
- [x] 5.2 [GREEN] `internal/eval/contradiction.go`: `RelationsGetter` interface (`GetRelationsForObservations([]string) (map[string]store.ObservationRelations, error)`, satisfied by `*store.Store`) + `ScoreContradiction(g RelationsGetter, c EvalCase, surfacedObservationID string) (hit bool, err error)`. Fails fast (not a silent Recall fallback) when `c.SupersedesOf` is nil/empty OR no judged `store.RelationSupersedes` relation links `c.ObservationID` (source) to `*c.SupersedesOf` (target). **Correction to this task's original wording**: hit is `surfacedObservationID == c.ObservationID` (the relation's **SourceID**, i.e. the CURRENT/superseding observation) — NOT the relation's TargetID as originally noted here. Verified against the codebase's own established Source=new/Target=old convention (`internal/mcp/mcp_test.go` `TestHandleSearch_SupersededAnnotation`: `SourceID: newObs.SyncID, TargetID: oldObs.SyncID`; `internal/mcp/mcp.go`'s `"supersedes: #<TargetIntID>"` / `"superseded_by: #<SourceIntID>"` annotation labels) and against spec EVAL-4's own scenario text ("only the newer one scores a hit"). Using TargetID as originally written would have inverted the hit/miss verdict.

## Phase 6: Retrieval-Only vs End-Task (EVAL-7) — COMPLETE (PR3)

- [x] 6.1 [RED] `internal/eval/retrieval_test.go`: `TestRetrievalSection_WrapsRunModelAB` (asserts byte-identical `embed.ABResult` vs calling `embed.RunModelAB` directly, plus error propagation), `TestReport_RetrievalAndEndTaskAreSeparateSections` (asserts `BuildReport` never populates `Report.Retrieval` and attaching it never mutates `ByCapability`/`ByLanguage`)
- [x] 6.2 [GREEN] `internal/eval/retrieval.go`: `RetrievalSection{Result embed.ABResult}` + `RunRetrievalSection(ctx, model, emb embed.Embedder, pairs []embed.ABPair, k int) (RetrievalSection, error)` — thin wrapper, zero new retrieval logic, calls `embed.RunModelAB` verbatim. `internal/eval/report.go`: added `Report.Retrieval *RetrievalSection` (nil until a caller explicitly attaches one) alongside the existing `ByCapability`/`ByLanguage` end-task fields — never derived from or merged into them (spec EVAL-7 no-merge rule).

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
