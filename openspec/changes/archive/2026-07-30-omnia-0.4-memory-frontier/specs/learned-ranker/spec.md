# Learned Ranker Specification

## Change metadata

- Change: omnia-0.4-memory-frontier
- Capability: learned-ranker
- Kind: ADDED (new capability, default-OFF)
- REQ range: REQ-440 through REQ-446

## Purpose

Replace the hand-tuned similarity floors (`StrongFloor`/`BaseFloor` = 0.35/0.25, jina-calibrated;
`DefaultFuseParams`/`AdaptiveFloor`, `internal/recall/recall.go:74,96`) with a locally-trained, lightweight
re-ranker over existing signal: `mem_judge` verdicts and bugfix `outcome` (worked/did_not_work), plus lexical
rank, embedding cosine, recency, type-match, and importance-by-type
(`DefaultImportanceWeight`, `internal/config/config.go:398`). Training runs entirely on the user's own local
corpus via `omnia rank-train`; enabling is gated on the existing token-cost-normalized eval harness
(`eval.RunOnce`, `internal/eval/harness.go:37`) proving no regression. Cold-start behavior is byte-for-byte
identical to today.

## Requirements

### Requirement: REQ-440 Default-Off Config Gate

A new `RankerConfig` (`yaml: learned_ranker`, field `enabled`, default `false`) MUST gate live use of a
trained model. When disabled, ranking MUST use `DefaultFuseParams`/`AdaptiveFloor` exactly as today.

#### Scenario: Disabled — today's floors are used unchanged

- GIVEN `learned_ranker.enabled` is absent or `false`
- WHEN a search/recall query runs
- THEN ranking output is produced by `DefaultFuseParams`/`AdaptiveFloor`, byte-for-byte identical to pre-v0.4

### Requirement: REQ-441 Cold-Start Is Byte-For-Byte Current Floors

With zero judgments/outcomes recorded, or with fewer than the configured minimum training signal, ranking
output MUST be identical to today's hand-tuned floors — enabling the flag alone MUST NOT change results until
enough signal exists.

#### Scenario: Edge case — cold-start fallback with a trained flag but no signal

- GIVEN `learned_ranker.enabled = true` but the corpus has zero `mem_judge` verdicts and zero bugfix outcomes
- WHEN a search/recall query runs
- THEN ranking output is identical to the `DefaultFuseParams`/`AdaptiveFloor` path

### Requirement: REQ-442 Local-Only, Pure-Go, No-CGO Training

`omnia rank-train` MUST train entirely on the local corpus with no network call to a cloud service, and MUST
NOT introduce any cgo dependency.

#### Scenario: Happy path — training completes locally

- GIVEN a corpus with sufficient judgment/outcome signal
- WHEN `omnia rank-train` is run
- THEN a trained model artifact is produced without any cloud API call, and the binary still builds with
  `CGO_ENABLED=0`

### Requirement: REQ-443 Feature Vector Sourced From Existing Signals Only

The trained model's feature vector MUST be built only from already-existing signals: lexical rank, embedding
cosine, recency, type-match, importance-by-type, and judgment/outcome history — no new signal source is
introduced by this capability.

#### Scenario: Feature vector uses existing fields only

- GIVEN a training run over the local corpus
- WHEN the feature vector for one candidate is built
- THEN every feature traces to an existing recall/config/store field, none newly invented

### Requirement: REQ-444 Eval-Gated Enablement

A trained model MUST NOT be usable for live ranking until it has been evaluated against the existing
token-cost-normalized eval harness (`eval.RunOnce`) and shown no accuracy regression versus the current
hand-tuned floors.

#### Scenario: Happy path — trained model passes the eval gate

- GIVEN a freshly trained model and the eval harness run against it
- WHEN the harness reports no regression versus `DefaultFuseParams`
- THEN the model becomes eligible to be enabled for live ranking

### Requirement: REQ-445 Model Versioning and Invalidation

Trained model artifacts MUST be versioned. A corpus or feature-shape change that invalidates a stored model
MUST be detected — the system MUST NOT silently keep using a stale model against a changed feature shape.

#### Scenario: Stale model is invalidated

- GIVEN a stored model trained against an older feature-vector shape
- WHEN the feature vector definition changes
- THEN the stored model is detected as invalid and is not used for live ranking

### Requirement: REQ-446 Fallback On Missing or Invalid Model

If the configured model file is absent, corrupt, or fails to load, ranking MUST fall back to
`DefaultFuseParams`/`AdaptiveFloor` without surfacing an error to the search/recall caller.

#### Scenario: Corrupt model file falls back silently

- GIVEN `learned_ranker.enabled = true` and a corrupted model file on disk
- WHEN a search/recall query runs
- THEN the query still returns results using `DefaultFuseParams`/`AdaptiveFloor`, with no error returned to
  the caller

## Out of Scope (Non-Goals)

- Replacing RRF fusion or the FTS5/embedding retrieval legs.
- Online/continuous learning during a live session.
- Cross-user or global models.
