# SQLite Vector Index Specification

## Change metadata

- Change: omnia-0.4-memory-frontier
- Capability: sqlite-vec-index
- Kind: ADDED (new capability, default-OFF)
- REQ range: REQ-460 through REQ-467

## Purpose

Replace/augment the brute-force O(N) cosine scan (`Store.Search`/`search`, `internal/embed/store.go:164,186`,
over the `embeddings` table's `vector BLOB` column, `:52`) with a vector-native index inside SQLite, still
`CGO_ENABLED=0`. Covers the same read surfaces brute-force serves today: `Search`, `SearchScoped`, and the
`Graph`/`GraphScoped` k-NN used by consolidation (capability 3). KNN-flat only — no HNSW/ANN in this slice.
Migration MUST be non-destructive to existing `embeddings.db` data.

## Requirements

### Requirement: REQ-460 Default-Off Config Gate

A new `VectorIndexConfig` (`yaml: vector_index`, field `enabled`, default `false`) MUST gate this capability.
When disabled, `Search`/`SearchScoped`/`Graph`/`GraphScoped` MUST remain byte-for-byte identical to the current
brute-force implementation.

#### Scenario: Disabled — brute-force path is unchanged

- GIVEN `vector_index.enabled` is absent or `false`
- WHEN `Search`/`SearchScoped` is called
- THEN results are produced by the existing brute-force cosine scan, byte-for-byte identical to pre-v0.4

### Requirement: REQ-461 Non-Destructive Migration

Enabling the vector index MUST NOT delete, truncate, or mutate existing `embeddings` rows. Migration MUST be
additive (a new index structure built alongside/over existing vectors) or a safe dual-write — never a
destructive rewrite.

#### Scenario: Edge case — migrating an existing embeddings.db

- GIVEN an `embeddings.db` with 1,000 existing vectors and `vector_index.enabled` set to `true` for the first
  time
- WHEN the migration runs
- THEN all 1,000 original rows remain present and unmodified in the `embeddings` table afterward

### Requirement: REQ-462 Read-Surface Parity

`Search`, `SearchScoped`, `Graph`, and `GraphScoped` MUST return equivalent top-k results (same top matches,
ties aside) whether served by the brute-force path or the new vector index.

#### Scenario: Happy path — same top-k via new index

- GIVEN a query vector and a corpus of 500 embeddings, indexed by the new vector index
- WHEN `Search` is called with the index enabled and again with it disabled
- THEN both calls return the same top-k `sync_id`s in the same order

### Requirement: REQ-463 KNN-Flat Only

This capability MUST implement KNN-flat retrieval only. No HNSW or other approximate-nearest-neighbor index
MUST be introduced in this slice.

#### Scenario: No ANN structure present

- GIVEN the vector index is enabled
- WHEN its on-disk structures are inspected
- THEN no HNSW graph or other ANN index artifact exists

### Requirement: REQ-464 CGO-Free Constraint

The vector index implementation MUST NOT introduce a cgo dependency. The binary MUST still build successfully
with `CGO_ENABLED=0` across the existing goreleaser target platforms.

#### Scenario: Build stays cgo-free

- GIVEN the vector index capability is present in the codebase (enabled or not)
- WHEN `CGO_ENABLED=0 go build ./...` is run
- THEN the build succeeds with no cgo dependency introduced

### Requirement: REQ-465 Fallback On Index Failure

If the vector index fails to open or query, the system MUST fall back to the brute-force path rather than
failing the caller.

#### Scenario: Index open failure falls back

- GIVEN the vector index file is corrupted or fails to open
- WHEN `Search` is called
- THEN the call succeeds via the brute-force fallback path, with no error surfaced to the caller

### Requirement: REQ-466 Migration Verification Reporting

The migration path MUST report how many vectors were indexed versus skipped (e.g. dimension mismatches),
mirroring `Search`'s existing decode-skip semantics (`internal/embed/store.go:207-210`).

#### Scenario: Migration reports skip counts

- GIVEN an `embeddings.db` with 998 valid vectors and 2 rows with a mismatched dimension
- WHEN migration runs
- THEN the migration report shows 998 indexed and 2 skipped, naming the skip reason

### Requirement: REQ-467 Existing Data Intact Post-Migration

After migration completes, a row-count (or equivalent integrity) check MUST confirm no `embeddings` row was
lost relative to the pre-migration count.

#### Scenario: Row count matches before and after

- GIVEN an `embeddings.db` with N rows before migration
- WHEN migration completes
- THEN a post-migration row count also equals N

## Out of Scope (Non-Goals)

- HNSW or other approximate-nearest-neighbor indexes.
- Changing the embedding model or its vector dimension.
- The separate memory `engram.db` store (this capability covers the embeddings store only,
  `internal/embed/store.go`).
