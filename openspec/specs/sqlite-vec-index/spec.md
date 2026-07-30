# SQLite Vec1 Index Specification

## Change metadata

- Change: omnia-0.4-memory-frontier
- Capability: sqlite-vec-index
- Kind: ADDED (new capability, default-OFF)
- REQ range: REQ-460 through REQ-469

## Purpose

Provide opt-in bundled Vec1 v0.35.2 flat/cos exact float32 KNN. Modernc brute force remains default/fallback.

## Requirements

### Requirement: REQ-460 Default-Off Config Gate

`vector_index.enabled` MUST default to `false`. When absent or false, all covered reads MUST remain byte-for-byte identical to pre-v0.4 brute force.

#### Scenario: Disabled parity
- GIVEN the flag is absent or false
- WHEN any covered read surface is called
- THEN it returns the pre-v0.4 brute-force result bytes

### Requirement: REQ-461 Non-Destructive Index Lifecycle

The index MUST use additive dual-write, backfill, and reindex steps without changing `embeddings` rows.

#### Scenario: Backfill and reindex preserve rows
- GIVEN an existing embeddings store
- WHEN the index is enabled, backfilled, or reindexed
- THEN the original rows and row count remain intact

### Requirement: REQ-462 Exact Read-Surface and Project Parity

Enabled Vec1 reads MUST match brute-force float32 flat/cos top-k, except ties, for all covered reads. Scoped reads MUST NOT return another project's vectors.

#### Scenario: Scoped exact KNN
- GIVEN indexed vectors in two projects
- WHEN a project-scoped read is executed
- THEN it returns only that project's brute-force-equivalent top-k

### Requirement: REQ-463 Float32 Flat/Cos Only

v0.4 MUST index float32 vectors with exact Vec1 flat/cos KNN. It MUST NOT introduce HNSW, ANN, int8, or binary formats.

#### Scenario: Unsupported format is not indexed
- GIVEN a non-float32 vector format
- WHEN index maintenance runs
- THEN it does not create an int8 or binary index entry

### Requirement: REQ-464 CGO-Free Approved Vec1 Path

The capability MUST use bundled Vec1 registration from `github.com/ncruces/go-sqlite3@v0.35.2` and build with `CGO_ENABLED=0`.

#### Scenario: Registered CGO-free Vec1
- GIVEN vector indexing is enabled on a supported host
- WHEN the vector store opens
- THEN bundled Vec1 is registered and available without cgo

### Requirement: REQ-465 Fallback on Unavailable or Corrupt Index

If Vec1 cannot open, is unavailable, corrupt, or query-fails, reads MUST use brute force and MUST NOT fail solely for the index.

#### Scenario: Corrupt index fallback
- GIVEN a corrupt or unavailable index
- WHEN a covered read is called
- THEN it returns the brute-force result

### Requirement: REQ-466 Active Dimension Lock and Mismatch Fallback

The first indexed float32 vector MUST establish the active dimension. Different-dimension vectors MUST be skipped with counts/reason during lifecycle work; different-dimension queries MUST use brute force.

#### Scenario: Dimension mismatch
- GIVEN an active index dimension and a different-dimension vector
- WHEN it is indexed or queried
- THEN lifecycle reporting records the skip and reads use brute force

### Requirement: REQ-467 Existing Data Intact Post-Migration

Lifecycle operations MUST verify no embeddings row was lost.

#### Scenario: Integrity verification
- GIVEN N embeddings before lifecycle work
- WHEN lifecycle work completes
- THEN verification confirms N embeddings remain

### Requirement: REQ-468 Native Vec1 BLOB and Target Safety

The canonical Omnia float32 source MUST remain little-endian. Each Vec1 BLOB MUST be explicitly re-encoded in machine-native byte order; it MUST NOT be treated as a generic little-endian Vec1 representation. v0.4 MUST support only little-endian targets.

#### Scenario: Unsupported host fallback
- GIVEN a non-little-endian host or failed native re-encoding
- WHEN vector indexing is requested
- THEN Vec1 is unavailable and brute force serves reads

### Requirement: REQ-469 Pinned Score Parity

For bundled Vec1 v0.35.2 flat/cos, normalized score MUST equal `1 - distance`. The contract MUST be tested for self, orthogonal, and antipodal vectors and MUST NOT use newer trunk score semantics.

#### Scenario: Score anchors
- GIVEN normalized self, orthogonal, and antipodal vectors
- WHEN Vec1 distance is converted to score
- THEN scores are respectively 1, 0, and -1

## Out of Scope (Non-Goals)

- HNSW or other ANN indexes.
- int8 or binary vectors in v0.4.
- Changing the embedding model, active dimension, or `engram.db`.
