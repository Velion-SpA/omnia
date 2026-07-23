# Memory Eval Harness Specification

## Purpose

Token-cost-normalized memory-quality evaluation harness — the H1 measuring stick that gates every other Omnia 0.2 slice. Extends the existing recall-eval (`internal/embed` `ABPair`/`RunModelAB`, `ab_pairs.json`) rather than replacing it. Scope: metric definition, eval corpus, adversarial/bilingual coverage, scoring strategy, reproducibility, and release-gate wiring.

## Requirements

### Requirement: EVAL-1 Token-Cost-Normalized Metric & Pareto Frontier

The system MUST define `quality-per-1k-tokens = accuracy / (total_tokens / 1000)`, where `total_tokens` sums query-embedding, retrieval, injected-context, and judge tokens. The system MUST report results as a Pareto frontier of (tokens, accuracy) points per configuration and MUST NOT report a single aggregate scalar as the sole result.

#### Scenario: Frontier point per configuration
- GIVEN a harness run against one embedding-model configuration
- WHEN the run completes
- THEN the report includes one (tokens, accuracy) point plus its token breakdown

#### Scenario: Aggregate-only report rejected
- GIVEN a generated report
- WHEN it is validated
- THEN it includes per-category figures, not only one aggregate ratio

### Requirement: EVAL-2 Eval Corpus Sourcing & Capability Tagging

The system MUST provide 50-150 eval cases sourced from real Omnia dev material (dogfooded `mem_save` observations, git log/PR history) and MUST NOT use LoCoMo-style long synthetic transcripts. Every case MUST be tagged with exactly one capability: Recall, Causal, State-Update/Staleness, or State-Abstraction.

#### Scenario: Case traces to a real observation
- GIVEN a dogfooded observation used as gold source
- WHEN a case references it
- THEN the case stores the observation ID and an expected fact drawn from its content

#### Scenario: Corpus size enforced at load
- GIVEN the case file is loaded
- WHEN the harness starts
- THEN it fails fast if case count is outside 50-150

### Requirement: EVAL-3 Segmented Reporting (Capability & Language)

The harness MUST report accuracy and quality-per-1k-tokens broken down per capability (all four buckets) AND separately for EN-query vs. bilingual ES-query slices, extending the existing `ab_pairs.json` bilingual set rather than re-authoring it.

#### Scenario: Per-capability and per-language rows present
- GIVEN a completed run
- WHEN the report is generated
- THEN it lists accuracy/tokens per capability AND separately for EN vs. ES queries

### Requirement: EVAL-4 Adversarial Contradiction Test

The system MUST include a contradiction subset built on the existing `mem_judge` `supersedes` relation: inject an older/newer observation pair, query for the fact, and score a hit only if the CURRENT (superseding) observation is what surfaces.

#### Scenario: Superseded fact scores as a miss
- GIVEN an older observation superseded by a newer one
- WHEN the harness queries for that fact
- THEN matching the older observation scores a miss; only the newer one scores a hit

#### Scenario: Missing supersedes relation fails fast
- GIVEN a contradiction pair with no recorded `supersedes` relation
- WHEN the harness loads the case
- THEN it fails fast rather than silently scoring the case as plain Recall

### Requirement: EVAL-5 Scoring Strategy & Token Accounting

For Recall and State-Update/Staleness cases, the system MUST score via substring/exact-match/embedding-threshold and MUST NOT require an LLM judge. For Causal and State-Abstraction cases, the system MAY use an LLM judge (via the existing `internal/llm` `AgentRunner`) and MUST count judge tokens in that case's `total_tokens`.

#### Scenario: Exact-fact case scored without judge cost
- GIVEN a Recall-category case with a defined expected substring
- WHEN it is scored
- THEN no judge token cost is recorded for that case

### Requirement: EVAL-6 Reproducibility via Repeated Runs

The system MUST execute every configuration 3-5 times and report mean and standard deviation of accuracy and quality-per-1k-tokens. A single-run number MUST NOT be used for any release-gate decision.

#### Scenario: Insufficient runs blocks gating
- GIVEN only 1 run exists for a configuration
- WHEN the release gate evaluates it
- THEN the gate refuses to decide and reports insufficient runs

#### Scenario: Mean and stddev both reported
- GIVEN 5 completed runs of one configuration
- WHEN the report is generated
- THEN both mean and stddev appear for accuracy and quality-per-1k-tokens

### Requirement: EVAL-7 Retrieval-Only vs. End-Task Metrics

The system MUST report an intrinsic retrieval-only metric (recall@k, extending the existing `RunModelAB`/`ABResult` mechanism) and an extrinsic end-task metric (case-level answer correctness) as two distinct figures, never merged into one number.

#### Scenario: Both metrics appear in one report
- GIVEN a completed harness run
- WHEN the report is generated
- THEN it contains a retrieval-only recall@k section and a separate end-task accuracy section

### Requirement: EVAL-8 Release-Gate Wiring

The harness MUST be invocable as a goreleaser pipeline step, configurable as `advisory` (logs, never blocks) or `blocking` (fails the step past a configured regression threshold). Default MUST be `advisory`.

#### Scenario: Advisory mode never blocks release
- GIVEN gate mode is `advisory` and a regression is detected
- WHEN the release pipeline runs
- THEN the pipeline continues and the regression is logged

#### Scenario: Blocking mode fails the release step
- GIVEN gate mode is `blocking` and the configured threshold is exceeded
- WHEN the release pipeline runs
- THEN the release step exits non-zero and the release does not proceed
