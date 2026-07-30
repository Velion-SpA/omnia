# Sleep Consolidation Specification

## Change metadata

- Change: omnia-0.4-memory-frontier
- Capability: sleep-consolidation
- Kind: ADDED (new capability, default-OFF)
- REQ range: REQ-420 through REQ-426

## Purpose

Complete v0.3.1's spaced-review scaffolding (`review_after`, `internal/store/anchors.go:296`; `ReviewConfig`,
`internal/config/config.go:238`) with local, idle-time, opt-in consolidation: clustering related episodic
memories via the existing k-NN semantic graph (`embed.Graph`/`GraphScoped`, `internal/embed/store.go:264,282`)
and synthesizing a higher-level digest per cluster using the local Ollama client
(`internal/embed/embed.go:34,78`). Digests always retain pointers back to their source observations and never
delete or supersede them — local competitors' cloud consolidation cannot do this without a privacy/token cost.

## Requirements

### Requirement: REQ-420 Default-Off Config Gate

A new `ConsolidationConfig` (`yaml: consolidation`, field `enabled`, default `false`) MUST gate this entire
capability. When disabled, `omnia consolidate` (and any idle worker) MUST be a no-op, and no digest observation
or relation row is written.

#### Scenario: Disabled — consolidate is a no-op

- GIVEN `consolidation.enabled` is absent or `false`
- WHEN `omnia consolidate` is run
- THEN it exits without writing any digest observation or relation row
- AND the existing `embed.Graph`/`GraphScoped` behavior for other callers is unchanged

### Requirement: REQ-421 Cluster Discovery Reuses the Existing k-NN Graph

Cluster discovery MUST reuse `embed.Graph`/`GraphScoped` (`internal/embed/store.go:264,282`) — no new
clustering engine is introduced.

#### Scenario: Happy path — cluster found via existing graph

- GIVEN three related episodic memories connected in the k-NN graph above the configured `min_score`
- WHEN `omnia consolidate` runs
- THEN the same three memories are grouped into one cluster candidate, using `Graph`'s existing edge weights

### Requirement: REQ-422 Digest Creation With Mandatory Source Pointers

For each qualifying cluster, the system MUST create one digest observation and write relation rows pointing
from the digest to every source observation. Sources MUST NEVER be deleted, hidden, or marked superseded by
this process.

#### Scenario: Digest retains all source pointers

- GIVEN a qualifying 3-memory cluster
- WHEN a digest is produced for it
- THEN the digest has 3 relation rows, one per source, and all 3 sources remain independently retrievable
  via `mem_search`

### Requirement: REQ-423 Cluster Size Bounds

Clusters smaller than the configured `min_cluster_size` MUST be skipped (not consolidated). Clusters larger
than the configured `max_cluster_size` MUST be capped or split into multiple digests — never silently
truncated without the excess sources being recorded in a separate digest or explicitly reported as skipped.

#### Scenario: Edge case — cluster too small is skipped

- GIVEN a 2-memory cluster and `min_cluster_size = 3`
- WHEN `omnia consolidate` runs
- THEN no digest is created for that cluster and it is reported as "below minimum size"

#### Scenario: Edge case — cluster too large is capped, not dropped

- GIVEN a 40-memory cluster and `max_cluster_size = 20`
- WHEN `omnia consolidate` runs
- THEN every one of the 40 sources ends up referenced by at least one digest — none are silently discarded

### Requirement: REQ-424 Local Ollama Model Only

Summarization/synthesis MUST run through the existing local Ollama client pattern
(`internal/embed/embed.go:34,78`). No cloud API call MUST occur for this capability.

#### Scenario: No cloud call during consolidation

- GIVEN consolidation is enabled and Ollama is reachable locally
- WHEN a digest is synthesized
- THEN the only model call observed is to the local Ollama endpoint

### Requirement: REQ-425 Idle-Time / Opt-In Trigger

Consolidation MUST run only via explicit invocation (`omnia consolidate`) or an idle-detecting worker gated
by the same config flag — never as an unconditional background pass.

#### Scenario: Idle worker respects the gate

- GIVEN `consolidation.enabled = false`
- WHEN the process is otherwise idle
- THEN no idle consolidation worker starts

### Requirement: REQ-426 Sources Remain Independently Retrievable

A digest MUST augment retrieval, never replace it: normal `mem_search`/`mem_context` results MUST continue to
surface source observations exactly as before consolidation ran.

#### Scenario: Source memory still surfaces after digesting

- GIVEN a source memory that has since been included in a digest
- WHEN `mem_search` is called with a query matching that source
- THEN the source memory still appears in results, unaffected by its inclusion in a digest

## Out of Scope (Non-Goals)

- Automatic deletion or "forgetting" of consolidated sources.
- Cloud-side consolidation.
- Real-time (non-idle) synthesis.
