# Injection Diversity Specification

## Purpose

Suppresses near-duplicate content among injected results at read time using a cheap, read-time MMR (maximal marginal relevance) style re-rank over already-hydrated content — so a budget-limited result set is not clogged with repeats — without adding new embedding calls or model dependencies, and without disturbing sentinel/signature pre-emption.

## Requirements

### Requirement: Read-Time Diversity Re-Rank Over Hydrated Content

When enabled, the system MUST apply an MMR-style diversity pass over the hydrated (fully fetched) content of fused/ranked results, demoting rows highly similar to an already-selected higher-ranked row.

#### Scenario: Near-duplicate demoted
- GIVEN fused results contain two rows describing the same fact in near-identical wording
- WHEN injection-diversity is enabled
- THEN the lower-ranked near-duplicate is demoted or suppressed and a more diverse set is returned

### Requirement: Cheap Lexical Similarity Only

Similarity comparison MUST use a cheap lexical measure (e.g., token/n-gram overlap); it MUST NOT invoke new embedding generation and MUST NOT introduce a new ML model dependency.

#### Scenario: No new embedding call
- GIVEN injection-diversity is enabled
- WHEN similarity is computed between two hydrated results
- THEN no new embedding request is issued and no new model dependency is invoked

### Requirement: Post-Pass Over Existing Ranking

Diversity re-ranking MUST run as a pass after existing fusion/ranking (RRF, v0.2 recency×importance×relevance), not replace it.

#### Scenario: Base ranking still applies
- GIVEN results already ranked by RRF and v0.2 ranking
- WHEN injection-diversity runs
- THEN the diversity pass only adjusts order/inclusion among already-ranked results; it does not recompute base relevance/recency/importance scores

### Requirement: Sentinel and Signature Pre-Emption Invariant

Diversity re-ranking MUST NOT alter the always-first, complete status of sentinel (topic_key rank -1000) or signature-match rows, even if they are lexically similar to other rows.

#### Scenario: Sentinel row unaffected by similarity
- GIVEN a sentinel row is highly similar in content to a lower-ranked normal row
- WHEN injection-diversity runs
- THEN the sentinel row is still emitted first and in full, and is not suppressed, demoted, or reordered

### Requirement: No Unnecessary Change When Distinct

When all non-preempted results share zero lexical overlap (pairwise similarity 0), diversity re-ranking MUST NOT change their order. Results whose pairwise similarity is non-zero but below the similarity threshold MAY be legitimately re-ordered by the MMR objective (diversity trades off against relevance), but MUST NOT be dropped — the hard drop applies only at or above the threshold.

#### Scenario: Zero-overlap set unchanged
- GIVEN a result set where every pair has similarity 0
- WHEN injection-diversity is enabled
- THEN the result order is identical to the order before the diversity pass

#### Scenario: Below-threshold overlap never drops
- GIVEN a result set with pairs overlapping below the similarity threshold
- WHEN injection-diversity is enabled
- THEN every result survives the pass (re-ordering per the MMR objective is permitted)

### Requirement: Disabled by Default, No-Op When Off

The feature MUST be controlled by a dedicated config flag, default OFF. When OFF, result order and content MUST be byte-for-byte identical to pre-v0.3 behavior.

#### Scenario: Flag off preserves current behavior
- GIVEN the injection-diversity flag is OFF
- WHEN results are ranked and returned
- THEN output order and content are byte-for-byte identical to pre-v0.3 behavior (no MMR pass applied)
