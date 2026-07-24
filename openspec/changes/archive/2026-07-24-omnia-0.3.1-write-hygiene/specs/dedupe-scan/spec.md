# Dedupe-Scan Specification

## Purpose

`omnia dedupe`: an offline, candidate-filtered near-duplicate scan over the existing observation base. Proposes merge clusters by default; applies only per explicit, still-valid cluster on `--apply`.

## Requirements

### Requirement: Propose-Only By Default

`omnia dedupe` MUST default to propose-only (dry-run) behavior: it MUST NOT merge, delete, or otherwise mutate any observation unless `--apply` is explicitly passed.

#### Scenario: Bare invocation mutates nothing

- GIVEN an observation base with near-duplicate clusters
- WHEN `omnia dedupe` is run with no flags
- THEN proposed clusters are printed and the database is unchanged

### Requirement: Explicit Per-Cluster Apply

`--apply` MUST require an explicit cluster identifier (or an explicit "apply all proposed clusters" flag). It MUST NOT implicitly apply merges for clusters not named in the invocation.

#### Scenario: Apply targets one named cluster only

- GIVEN two proposed clusters A and B
- WHEN `omnia dedupe --apply A` is run
- THEN only cluster A is merged; cluster B remains unmerged and still proposable

### Requirement: Candidate Pre-Filter, Not All-Pairs

Candidate discovery MUST use a lexical/FTS pre-filter to narrow comparison pairs before any pairwise similarity check. The scan MUST NOT perform an all-pairs O(n²) comparison across the full observation set, including at ~1600+ observation scale.

#### Scenario: Scan completes at production scale via pre-filter

- GIVEN a base of 1600+ observations
- WHEN `omnia dedupe` runs
- THEN candidate pairs are narrowed by pre-filter before similarity scoring, not compared exhaustively

### Requirement: Canonical Survivor Is The Newest

Within an applied merge cluster, the canonical survivor MUST be the newest observation in the cluster by creation timestamp. All other cluster members become referenced history, not hard-deleted.

#### Scenario: Newest observation wins

- GIVEN a cluster of 3 near-duplicate observations with different creation times
- WHEN `omnia dedupe --apply <cluster-id>` is run
- THEN the observation with the latest creation timestamp becomes canonical; the other two remain retrievable as referenced history with their relations/provenance intact

### Requirement: Stale Cluster Apply Fails Safely

`--apply` against a cluster ID that no longer matches the current proposal (already applied, or invalidated by underlying changes) MUST fail with a clear error and MUST NOT partially or silently apply an unintended merge.

#### Scenario: Apply on a stale cluster id

- GIVEN a cluster id from a previous `dedupe` run whose underlying observations have since changed
- WHEN `omnia dedupe --apply <that-cluster-id>` is run
- THEN the command fails with a clear "stale cluster" error and no observation is mutated
