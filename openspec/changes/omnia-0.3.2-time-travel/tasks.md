# Tasks: Omnia v0.3.2 — Time Travel

## Review Workload Forecast

| Field | Value |
|---|---|
| Estimated changed lines | 2,100–2,700 total; ≤400 per slice |
| 400-line budget risk | High |
| Chained PRs recommended | Yes |
| Suggested split | PR1A → PR1B → PR1C → PR1D → PR2A → PR2B → PR3A → PR3B → PR3C → PR3D → PR4A → PR4B → PR4C |
| Delivery strategy | auto-chain |
| Chain strategy | stacked-to-main |

Decision needed before apply: No
Chained PRs recommended: Yes
Chain strategy: stacked-to-main
400-line budget risk: High

### Suggested Work Units

| PR | Goal / base |
|---|---|
| PR1A | v2 envelope / `main` |
| PR1B | Core importer / PR1A |
| PR1C | Relations+anchors / PR1B |
| PR1D | Procedures+delivery / PR1C |
| PR2A | Local revision substrate / `main` |
| PR2B | Bulk and pulled revision capture / PR2A |
| PR3A | Recorded state queries / PR2B |
| PR3B | Recorded-time search / PR3A |
| PR3C | Bounded historical context / PR3B |
| PR3D | Recorded-time CLI reads / PR3C |
| PR4A | Exact recorded-event bounds / PR3D |
| PR4B | Resumable bisect CLI and state machine / PR4A |
| PR4C | Process locking and durable state hardening / PR4B |

Every PR includes behavior tests and targets ≤400 changed lines.

## PR1A: Deterministic v2 Export Envelope

- [x] 1.1 **RED:** In `internal/store/store_test.go`, prove deterministic v2 metadata, empty export, pinned round-trip, and malformed/future rejection.
- [x] 1.2 **GREEN:** Add canonical v2 envelope/read-side decoding in `internal/store/store.go`; preserve legacy fields and pinned state without enabling imports.
- [x] 1.3 **REFACTOR/VERIFY:** Isolate canonical hashing; run `CGO_ENABLED=0 go test ./internal/store`.

## PR1B: Safe Core Importer

- [x] 2.1 **RED:** In `internal/store/store_test.go`, prove atomic/idempotent core import and explicit legacy overwrite semantics.
- [x] 2.2 **GREEN:** Implement transactional v2 upsert-by-`sync_id` and separate untouched legacy importer in `internal/store/store.go`.
- [x] 2.3 **REFACTOR/VERIFY:** Centralize dispatch/errors; run `CGO_ENABLED=0 go test ./internal/store`.

## PR1C: Relations and Anchors

- [x] 3.1 **RED:** In `internal/store/store_test.go`, prove valid endpoints/owners/types, idempotency, and rollback on invalid graph references.
- [x] 3.2 **GREEN:** Add validated dependency-ordered relation/anchor import in `internal/store/store.go`.
- [x] 3.3 **REFACTOR/VERIFY:** Share reference validation; run `CGO_ENABLED=0 go test ./internal/store`.

## PR1D: Procedures and Delivery

- [x] 4.1 **RED:** Prove procedure round-trip/scale, atomic CLI mode `0600`, HTTP errors, and byte-stable sync in package tests.
- [x] 4.2 **GREEN:** Import procedures in `internal/store/store.go`; add portable-only export wiring in `cmd/omnia/main.go` and `internal/server/server.go`, decoupled from `internal/sync/sync.go`.
- [x] 4.3 **REFACTOR/VERIFY:** Share safe temp-file replacement; test `./internal/store ./internal/server ./internal/sync ./cmd/omnia`.

## PR2: History Substrate and Purge

- [x] 5.1 **RED:** In store/config tests, prove disabled/insert no-op, before-images, retention, revision purge, and tombstone.
- [x] 5.2 **GREEN:** Add config, migration, transactional capture/prune/purge in `internal/config/config.go`, `internal/store/store.go`, and `internal/store/timetravel.go`.
- [x] 5.3 **VERIFY:** Run `CGO_ENABLED=0 go test ./internal/config ./internal/store`.

## PR3: Recorded-Time Reads

- [x] 6.1 **RED:** In store/MCP/CLI tests, prove edit/delete visibility, disclaimers, future time, search limitation, and unchanged live output.
- [x] 6.2 **GREEN:** Implement `StateAsOf`; wire gated `as_of` through `internal/store/timetravel.go`, `internal/mcp/mcp.go`, and `cmd/omnia/main.go`.
- [x] 6.3 **VERIFY:** Run `CGO_ENABLED=0 go test ./internal/store ./internal/mcp ./cmd/omnia`.

## PR4A–PR4C: Resumable Bisect

- [x] 7.1 **RED:** In `cmd/omnia/bisect_test.go`, prove bounds, midpoint, marks, edges, resume/reset, tombstones, disabled mode, and convergence.
- [x] 7.2 **GREEN:** Add event queries and `cmd/omnia/bisect.go`; wire atomic `$OMNIA_DATA_DIR/bisect-state.json`.
- [x] 7.3 **VERIFY:** Run `CGO_ENABLED=0 go test ./...` and `CGO_ENABLED=0 go test -cover ./...`.
