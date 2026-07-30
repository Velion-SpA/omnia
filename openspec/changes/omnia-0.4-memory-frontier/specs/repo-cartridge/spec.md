# Repo Cartridge Specification

## Change metadata

- Change: omnia-0.4-memory-frontier
- Capability: repo-cartridge
- Kind: ADDED (new capability, default-OFF)
- REQ range: REQ-450 through REQ-456

## Purpose

Precompute a versioned, per-repo digest — top/most-relevant memories plus code-graph/anchor state (capability
1) — keyed to a repo and commit SHA, so a fresh agent session can load "warm" instead of cold-querying.
Invalidation is commit/content-hash based (`HeadSHA`, `internal/anchor/anchor.go:192`), matching the existing
anchor/forget-scan incremental pattern, never time-based.

**Naming**: `cartridge build` / `cartridge load` follow the existing nested-subcommand-namespace convention
already used by `cloud`, `embed`, `migrate`, and `eval` (each parses its own `os.Args[2:]`,
`cmd/omnia/main.go:775,785,789,791`). Verified against current conventions — no rename needed.

## Requirements

### Requirement: REQ-450 Default-Off Config Gate

A new `CartridgeConfig` (`yaml: cartridge`, field `enabled`, default `false`) MUST gate this capability. When
disabled, `omnia cartridge build`/`omnia cartridge load` MUST be no-ops and no session-startup path changes.

#### Scenario: Disabled — cartridge commands are a no-op

- GIVEN `cartridge.enabled` is absent or `false`
- WHEN `omnia cartridge build` or `omnia cartridge load` is run
- THEN it exits reporting the capability disabled, and session startup behaves exactly as pre-v0.4

### Requirement: REQ-451 Cartridge Build Contract

`omnia cartridge build` MUST produce a versioned digest, keyed to the repo and its current commit SHA
(`HeadSHA`), containing the repo's most-relevant memories plus code-graph/anchor state (capability 1).

#### Scenario: Happy path — cartridge built at current HEAD

- GIVEN a repo at commit `abc123` with existing memories and anchors
- WHEN `omnia cartridge build` runs
- THEN a cartridge artifact is produced tagged with commit `abc123` and a format version number

### Requirement: REQ-452 Commit/Content-Hash Invalidation

A cartridge built at commit X MUST be detected as stale when the repo's current HEAD differs from X. Staleness
MUST be surfaced to the loader — never silently served as fresh.

#### Scenario: Edge case — cartridge used at a different HEAD

- GIVEN a cartridge built at commit `abc123`
- WHEN the repo's current HEAD is `def456` and `omnia cartridge load` is called
- THEN the load path reports the cartridge as stale (commit mismatch) rather than serving it as current

### Requirement: REQ-453 Load Path Contract

`omnia cartridge load` (or the equivalent session-startup hook) MUST use a valid, HEAD-matching cartridge to
start a session warm. A missing or stale cartridge MUST degrade to today's cold-start behavior, never an error.

#### Scenario: Happy path — session starts warm

- GIVEN a valid cartridge matching the current HEAD
- WHEN a new session starts
- THEN the session loads the cartridge's pre-digested memories/graph state instead of cold-querying

#### Scenario: Missing cartridge degrades to cold-start

- GIVEN no cartridge exists for the current repo
- WHEN a new session starts
- THEN the session falls back to normal cold-query behavior with no error surfaced

### Requirement: REQ-454 Local Artifact Only

Cartridges MUST remain a local, on-disk artifact for this slice — not shipped between machines or synced to
the cloud.

#### Scenario: Cartridge is not synced

- GIVEN a cartridge built on one machine
- WHEN the cloud sync path runs
- THEN the cartridge file is not transmitted or referenced by cloud sync

### Requirement: REQ-455 No Weight-Level Precompute

Cartridge contents MUST be limited to memory/code-graph digests — never model weights, KV-cache state, or any
parametric-memory artifact.

#### Scenario: Cartridge contains no model state

- GIVEN a built cartridge
- WHEN its contents are inspected
- THEN only memory/graph digest data is present, no model weights or cache tensors

### Requirement: REQ-456 Versioned On-Disk Format

The cartridge format MUST include an explicit version field so a future format change can detect and reject
an old cartridge rather than misreading it.

#### Scenario: Old-format cartridge is rejected cleanly

- GIVEN a cartridge written with an older format version than the running binary expects
- WHEN `omnia cartridge load` reads it
- THEN the load path detects the version mismatch and falls back to cold-start rather than misreading the file

## Out of Scope (Non-Goals)

- KV-cache/weight-level precomputation (parametric memory explicitly not adopted).
- Shipping cartridges between machines or to the cloud in this slice.
