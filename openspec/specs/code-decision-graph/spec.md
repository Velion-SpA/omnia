# Code Decision Graph Specification

## Change metadata

- Change: omnia-0.4-memory-frontier
- Capability: code-decision-graph
- Kind: ADDED (new capability, default-OFF)
- REQ range: REQ-400 through REQ-406

## Purpose

Complete the v0.2-partial code anchor machinery (`memory_anchors`, `internal/store/anchors.go`; git probe,
`internal/anchor/anchor.go`) into a queryable code↔decision graph. Today the only read is memory→anchors
(`GetAnchorsForObservations`, `internal/store/anchors.go:362`); this capability adds the missing reverse
direction — code→decision — via `mem_blame <file>:<line>` (MCP tool) and `omnia blame` (CLI), plus a graph
enumeration read over the same anchor rows as edges. Zero-LLM, deterministic, reuses existing git-blame/
content-hash machinery. No AST/tree-sitter parser, no new graph engine or table.

**Naming**: `mem_blame` / `omnia blame` follow the existing `mem_<verb>` (MCP) / `<verb>` (CLI) symmetry already
used by `mem_context`/`context`, `mem_stats`/`stats`. Verified against current conventions — no rename needed.

## Requirements

### Requirement: REQ-400 Default-Off Config Gate

A new `CodeGraphConfig` (`yaml: code_graph`, field `enabled`, default `false`) MUST gate every new surface in
this capability. When absent or `false`, `mem_blame`/`omnia blame` and the graph-enumeration read MUST NOT be
registered/callable, and no existing anchor write/read path (`UpsertAnchor`, `MarkAnchorStale`,
`GetAnchorsForObservations`) changes behavior.

#### Scenario: Disabled — new surfaces are a no-op

- GIVEN `code_graph.enabled` is absent or `false`
- WHEN `mem_blame` or `omnia blame` is invoked
- THEN the call returns a structured "capability disabled" response and touches no anchor table
- AND every pre-existing `mem_search`/`omnia forget-scan` behavior remains byte-for-byte unchanged

### Requirement: REQ-401 Reverse Query — `mem_blame` / `omnia blame`

Given `file:line` (and optional repo root), the system MUST find every anchor whose captured blame line range
covers that line and return the memory/memories attached via each anchor's `obs_sync_id`.

#### Scenario: Happy path — single anchor covers the line

- GIVEN an active anchor for `internal/store/anchors.go` lines 90-101 linked to a decision memory
- WHEN `mem_blame internal/store/anchors.go:95` is called
- THEN the response includes that memory, the anchor's status (`active`), and the blame SHA

### Requirement: REQ-402 Overlap Resolution — Multiple Anchors Cover One Line

When more than one anchor's line range covers the queried line, the system MUST include every covering anchor's
linked memory in the response — none may be silently dropped due to overlap.

#### Scenario: Edge case — two anchors cover the same line

- GIVEN two anchors on the same file whose line ranges both include line 50 (one narrower, one wider)
- WHEN `mem_blame <file>:50` is called
- THEN both anchors' linked memories appear in the response, each tagged with its own anchor status

### Requirement: REQ-403 Stale Anchors Surfaced, Never Hidden

Stale anchors (`anchor_status = stale`) MUST be included in `mem_blame` results, clearly marked, reusing the
existing active/stale/traveled vocabulary — never silently filtered out.

#### Scenario: Stale anchor still returned

- GIVEN an anchor marked `stale` via `MarkAnchorStale` still covers the queried line
- WHEN `mem_blame` is called for that line
- THEN the stale anchor's memory is returned with `anchor_status: "stale"` visible in the result

### Requirement: REQ-404 Code→Decision Graph Enumeration

The system MUST expose a read that enumerates all code→decision edges for a project/repo, treating each active
anchor row as one edge (file+symbol → memory) — no new graph storage engine or table.

#### Scenario: Enumerate edges for a repo

- GIVEN a repo with 5 active anchors linked to 3 distinct memories
- WHEN the graph-enumeration read is called for that repo
- THEN it returns 5 edges, each naming its source file:line/symbol and target memory

### Requirement: REQ-405 Zero-LLM, Deterministic, No AST Parsing

This capability MUST NOT invoke an LLM and MUST NOT introduce an AST/tree-sitter parser; all resolution reuses
the existing deterministic git-blame/content-hash anchor machinery (`internal/anchor/anchor.go`).

#### Scenario: No model call on the blame path

- GIVEN `mem_blame` is called
- WHEN the reverse query executes
- THEN no LLM/Ollama call occurs anywhere in the call path

### Requirement: REQ-406 Graceful Degradation Outside a Git Repo

When the queried file's directory is not inside a git repository, or git is unavailable, `mem_blame` MUST return
a clear "no anchors resolvable" result rather than an error that looks like a crash, mirroring
`anchor.Capture`'s existing `ErrNotAGitRepo`/`ErrGitNotInstalled` graceful-degradation contract.

#### Scenario: Failure mode — not a git repository

- GIVEN a `file:line` outside any git repository
- WHEN `mem_blame` is called
- THEN the response reports zero anchors found with a clear reason, and no panic/fatal error occurs

## Out of Scope (Non-Goals)

- Symbol call-graph edges (caller/callee resolution).
- Cross-file anchor relocation.
- Any LLM-assisted linking between code and memory.
- Replacing or altering `UpsertAnchor`/`MarkAnchorStale` write semantics.
