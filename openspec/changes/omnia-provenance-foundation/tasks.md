# Tasks: Memory Provenance Foundation (omnia-provenance-foundation)

Strict TDD (`CGO_ENABLED=0 go test ./...`). Encrypt-at-rest OUT OF SCOPE (0.3 fast-follow) — not tasked.

## Review Workload Forecast

| Field | Value |
|---|---|
| Estimated changed lines | ~750-950 (prod ~300-350, tests ~450-600 across 8+ files) |
| 400-line budget risk | High |
| Chained PRs recommended | Yes |
| Suggested split | PR1 → PR2 → PR3 |
| Delivery strategy | ask-on-risk |
| Chain strategy | pending (ask user) |

Decision needed before apply: Yes
Chained PRs recommended: Yes
Chain strategy: pending
400-line budget risk: High

### Suggested Work Units

| Unit | Goal | Likely PR | Notes |
|---|---|---|---|
| 1 | Phases 1-3: audit taxonomy + trust schema/classify + write-time capture in `handleSave` | PR 1 | Independent; green alone; ~250-300 lines |
| 2 | Phases 4-5: `deletion_tombstones` + hard-delete purge + `embed.DeleteBySyncID` + `handleDelete` fan-out | PR 2 | Depends on PR1's audit/schema; ~250-300 lines |
| 3 | Phases 6-8: pull-path tombstone replication (`applyObservationDeleteTx`) + perms hardening + doctor check + full regression | PR 3 | Depends on PR2's tombstone table; ~250-300 lines |

## Phase 1: Audit Taxonomy (Foundation)
- [x] 1.1 RED `internal/audit/audit_test.go`: `Entry` JSON round-trips `Source`,`TrustTag`,`SyncID`,`SessionID`; `ActionRead`/`ActionWrite` constants exist
- [x] 1.2 GREEN `internal/audit/audit.go`: add 4 fields to `Entry`; add `ActionRead`,`ActionWrite` consts. Constants only — no read call-site wiring (open question, per spec risk note)

## Phase 2: Trust Schema + Classification (Foundation)
- [x] 2.1 RED `internal/store/provenance_test.go` (new): table-test `classifyTrust(source)` → `user`/`agent`/`ingest:tool|web|doc`/unknown→`unverified`
- [x] 2.2 GREEN `internal/store/provenance.go` (new): pure `classifyTrust()` + trust-tag constants
- [x] 2.3 RED `internal/store/store_migration_test.go`: `migrate()` adds nullable `source`,`trust_tag` to `observations`; re-run idempotent
- [x] 2.4 GREEN `store.go` `migrate()`: `addColumnIfNotExists("observations","source","TEXT")` + `("observations","trust_tag","TEXT")`
- [x] 2.5 RED `store_test.go`: `AddObservation{Source,TrustTag}` persists; topic_key revision path preserves via `COALESCE(NULLIF(?, ''), col)` (mirrors `error_signature`); legacy/no-source row reads `trust_tag="unverified"`
- [x] 2.6 GREEN `store.go`: `AddObservationParams`+`Source`,`TrustTag`; `Observation`+`Source`,`TrustTag *string`; update INSERT (new-row path, `AddObservation` ~L2481), revision UPDATE (~L2405), `observationSelectColumns`, `scanObservationRow`

## Phase 3: Write-Time Capture + Save Audit (Core)
- [x] 3.1 RED `internal/mcp/mcp_test.go`: `handleSave` w/ `source` arg → `store.classifyTrust` → persisted `trust_tag`; missing `source`→`unverified`; one `audit.Append(ActionWrite, Source, TrustTag)` fires (new call-site — today only `internal/dashboard/handlers.go` calls `audit.Append`) — implemented in `internal/mcp/provenance_test.go` (new file, focused per project-structure convention)
- [x] 3.2 GREEN `mcp.go` `handleSave` (~L1358): read `source` arg, classify, pass to `AddObservationParams`, `audit.Append` after success — via new `auditAppend` seam (mirrors `suggestTopicKey`/`addPromptIfMissing` DI convention)
- [x] 3.3 RED/GREEN: audit-append failure (unwritable log dir) does not block/rollback the save — assert `AddObservation` still succeeds

## Phase 4: Tombstone + Hard Delete (Core)
- [x] 4.1 RED `store_migration_test.go`: `deletion_tombstones(sync_id PK, entity, project, actor, reason, content_hash, hard, deleted_at)` created
- [x] 4.2 GREEN `store.go` `migrate()`: `CREATE TABLE IF NOT EXISTS deletion_tombstones`
- [x] 4.3 RED `store_test.go` (implemented in `internal/store/provenance_test.go`): `DeleteObservation(id, true)` purges row+FTS AND inserts one `deletion_tombstones` row for that `sync_id`; `DeleteObservation(id, false)` writes neither vector-purge nor tombstone (regression on existing soft-delete branch)
- [x] 4.4 GREEN `store.go` `DeleteObservation` (~L3075): `INSERT INTO deletion_tombstones` inside the `hardDelete` branch, before `enqueueSyncMutationTx`

## Phase 5: Embed Purge Fan-Out (Core)
- [x] 5.1 RED `internal/embed/store_test.go`: `DeleteBySyncID(ctx, syncID)` removes the row; `Count()==0` after
- [x] 5.2 GREEN `internal/embed/store.go`: add `DeleteBySyncID(ctx, syncID) (int, error)` — `DELETE FROM embeddings WHERE sync_id=?`
- [x] 5.3 RED `mcp_test.go` (implemented in `internal/mcp/provenance_test.go`): `handleDelete(hard_delete=true)` fans out to `embed.DeleteBySyncID` via a new embed handle on `MCPConfig` (via `Worker.Store()` accessor, mirrors `enqueueAutoEmbed`/`cfg.AutoEmbed`) AND appends one `audit.Append(ActionHardDelete)`; soft delete does not fan out (still audits `ActionSoftDelete` — see Deviations in final report re: reconciling this task line with the spec's "mem_delete soft and hard MUST append an entry")
- [x] 5.4 GREEN `mcp.go`: `MCPConfig.AutoEmbed.Store()` embed handle; `handleDelete` (~L1814) fan-out purge + `audit.Append`. Confirmed `internal/store` still has zero import of `internal/embed` (`go list -deps` check)

## Phase 6: Sync Pull Tombstone Replication (Integration)
- [x] 6.1 RED `internal/store/sync_apply_test.go` (implemented in `internal/store/provenance_test.go`): `applyObservationDeleteTx` w/ `payload.HardDelete=true` purges the row AND inserts its own `deletion_tombstones` row (proof replicates on the pull path independent of local push)
- [x] 6.2 GREEN `store.go` `applyObservationDeleteTx` (~L6509): add `INSERT INTO deletion_tombstones` alongside the existing `DELETE FROM observations` when `payload.HardDelete`
- [x] 6.3 Test: run the SAME tombstone-row assertion for local `DeleteObservation(hard=true)` (4.3) and pulled `applyObservationDeleteTx` (6.1) side by side to prove push/pull symmetry — `TestDeletionTombstone_PushPullSymmetry`

## Phase 7: Store Permissions + Doctor Warning (Hardening)
- [x] 7.1 RED `store_test.go` (implemented in `internal/store/provenance_test.go`): `New(cfg)` creates `cfg.DataDir` at `0700` (no group/world bits) and the db file at `0600`
- [x] 7.2 GREEN `store.go` `New`/`newWithoutRepair` (~L671, ~L712): `os.MkdirAll(cfg.DataDir, 0o700)`; `os.Chmod(dbPath, 0o600)` after `openDB`. Added `Store.DataDir()`/`Store.DBPath()` accessors for the diagnostic check below
- [x] 7.3 RED `internal/diagnostic/diagnostic_test.go` (implemented in `internal/diagnostic/provenance_test.go`): new check flags a store dir path containing `Mobile Documents/com~apple~CloudDocs`, `Dropbox`, or `OneDrive`, OR group/world-readable perms (dir AND db file). Updated `TestRegistryLookupAndOrdering` + `TestRunnerRunAllHealthyEvaluatesEveryMVPCheck` (5 checks now) and the shared `newDiagnosticTestStore` fixture (fresh subdir so `New()`'s owner-only `MkdirAll` actually applies — `t.TempDir()` itself is 0755 on this toolchain)
- [x] 7.4 GREEN `internal/diagnostic/checks.go`: added `StoreExposureCheck{}` (`Code()`+`Run(ctx, Scope)`, `CheckStoreExposure` const, warning severity); `registry.go` `DefaultRegistry()` += `StoreExposureCheck{}`. No `cmd/omnia/doctor.go` change needed — `runDiagnostics`/`RunAll` already iterates the registry

## Phase 8: Regression
- [x] 8.1 `CGO_ENABLED=0 go test ./...` green end-to-end; legacy/no-source saves and existing dashboard audit call-sites unchanged. Verified via `CGO_ENABLED=0 go test ./internal/store/... ./internal/audit/... ./internal/embed/... ./internal/mcp/... ./internal/diagnostic/... ./cmd/omnia/... -race`: all green except the confirmed-pre-existing 10 `internal/mcp` `unknown_project "engram"` failures (byte-identical to a clean baseline worktree at the pre-slice commit — zero new failures). `gofmt -l` and `go vet ./...` clean.

## Delivery Note

Implemented as ONE consolidated PR (`size:exception`), per explicit orchestrator/user instruction overriding the Review Workload Forecast's chained-PR recommendation above. All 8 phases landed together on `feat/provenance-foundation`; not committed (apply-only run).

## Docs Updated (docs-alignment)
- `docs/DOCTOR.md` — added `store_exposure` to the MVP check catalog.
- `internal/mcp/mcp.go` — added the `source` parameter to the `mem_save` tool schema (`mcp.WithString("source", ...)`), so MCP clients can discover it directly from the tool definition.
- `CHANGELOG.md` — added an `omnia-provenance-foundation` entry under `## Unreleased`.
