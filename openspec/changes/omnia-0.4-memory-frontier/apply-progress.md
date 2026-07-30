# Apply Progress: Omnia v0.4 Memory Frontier
## PR 1 — Config Scaffolding
- Completed tasks: 1.1, 1.2, 1.3, 1.4
- Remaining tasks: 2.1–11.12 remain unchecked in `tasks.md`.
- Boundary: seven default-OFF configuration blocks and parameter defaults only; no capability wiring or runtime behavior was added.
### TDD Cycle Evidence
| Task | Test File | Layer | Safety Net | RED | GREEN | TRIANGULATE | REFACTOR |
|---|---|---|---|---|---|---|---|
| 1.1 | `internal/config/v04_config_test.go` | Unit | `go test ./internal/config/...` passed before change | Test referenced the seven absent `Config` fields; compile failed | Added the config fields; test passed | v0.3.2 fixture snapshot covers non-default legacy values | N/A |
| 1.2 | `internal/config/v04_config_test.go` | Unit | Covered by 1.1 baseline | Existing RED test could not compile until the seven types existed | Added structs and parameter-only defaults; targeted test passed | Opt-in-only config test verifies documented defaults | N/A |
| 1.3 | `internal/config/v04_config_test.go` | Unit | Covered by config suite | N/A (style-only) | Config tests passed after doc/style cleanup | Skipped: structural/documentation-only task | Confirmed no v0.4 `*KeyPresent` probe is present |
| 1.4 | `internal/config/v04_config_test.go` | Unit | Covered by config suite | N/A (verification-only) | Config suite, cgo-free build, and vet passed | Skipped: verification-only task | `git diff --check` passed |
### Verification
- `go test ./internal/config/...` — passed
- `CGO_ENABLED=0 go build ./...` — passed
- `go vet ./...` — passed
- `git diff --check` — passed
- The v0.3.2 fixture has no v0.4 keys and its legacy config snapshot is byte-for-byte equal to `v0.3.2-config.golden.yaml`.
### Design Deviations
None. The design, tasks, code, and security specification now consistently use `EncryptionConfig` with the `encryption` key; no `security` alias is provided.


## PR 6 — Code-decision Graph
- Completed tasks: 6.1–6.10
- Remaining tasks: 2.1–5.8 and 7.1–11.12 remain unchecked in `tasks.md`.
- Boundary: deterministic, default-OFF reverse anchor reads plus `mem_blame`/`omnia blame`; no enforcement-gate behavior (PR 7/8) was implemented.
### TDD Cycle Evidence
| Task | Test File | Layer | Safety Net | RED | GREEN | TRIANGULATE | REFACTOR |
|---|---|---|---|---|---|---|---|
| 6.1–6.4 | `internal/store/anchors_test.go` | Unit | existing store suite | `BlameLine` references failed to compile | overlap and stale tests passed | narrow/wide + stale cases | deterministic SQL ordering |
| 6.5–6.6 | `internal/store/anchors_test.go` | Unit | existing store suite | `CodeDecisionGraph` reference failed to compile | graph projection test passed | 5 edges/2 nodes | projection reuses active-anchor read |
| 6.7–6.9 | `cmd/omnia/blame_test.go`, `internal/mcp/mcp_blame_test.go` | Unit | cmd/mcp suites | absent CLI/config seam failed to compile | disabled and non-repo contracts passed | CLI + MCP surfaces | shared `internal/codegraph` normalization |
| 6.10 | relevant suites | Verification | targeted suites | N/A | full verification passed | disabled path tested | `git diff --check` passed |
### Verification
- `CGO_ENABLED=0 go build ./...` — passed
- `go vet ./...` — passed
- `go test ./...` — passed
- `go test -cover ./...` — passed
- `go test -tags e2e ./internal/server/...` — passed
- Disabled path: `omnia blame` returns `capability disabled` before opening the store; disabled MCP config does not register `mem_blame`.
### Design Deviations
None.

## PR 6 Remediation — Read Contract Hardening
- `mem_blame` now emits only grouped public anchor hits with `{anchor_status, range, blame_sha, memories[{sync_id,type,title,preview}]}`; it never serializes full `Observation.Content`.
- Valid no-match responses serialize `"hits":[]`; fractional MCP line values are rejected instead of truncated; explicit `repo_root` is verified by git before querying.
- Runtime coverage includes enabled MCP and CLI paths, disabled MCP registration, no-repo degradation, and the required five anchors to three memories graph projection.
### TDD Cycle Evidence
| Task | Test File | RED | GREEN | REFACTOR |
|---|---|---|---|---|
| Public MCP response | `internal/mcp/mcp_blame_test.go` | Full observation content leaked | Grouped preview-only response passed | Projection keeps store rows internal |
| Input/degradation contract | `internal/mcp/mcp_blame_test.go`, `internal/codegraph/path_test.go` | Empty hits were `null`; fractional lines truncated | Explicit empty array, integer validation, real git-root validation passed | Shared normalization remains CLI/MCP seam |
| Runtime surfaces | `cmd/omnia/blame_test.go`, `internal/mcp/mcp_blame_test.go`, `internal/store/anchors_test.go` | Existing graph fixture had only two memories | CLI/MCP enabled paths, disabled registration, and 5→3 graph passed | None needed |

## PR 9 (9A + 9B) — Sleep Consolidation
- Completed tasks: 9.1–9.14.
- Boundary: local, default-OFF clustering over the existing k-NN graph, local Ollama digest generation
  (no cloud API), retained source relations (never hard-deleted), `omnia consolidate` CLI, and audit.
  The optional idle-time worker (mirroring `buildAutoEmbedWorker`) was **not** implemented in this slice —
  spec REQ allows either explicit invocation or an idle worker; only explicit invocation ships here.
  A future increment can add the idle worker without changing this contract.
### TDD Cycle Evidence
| Task | RED | GREEN | REFACTOR |
|---|---|---|---|
| 9.1–9.4 | `cluster_test.go` failed with missing `Clusters`, then with an unsafe dropped remainder | Union-find clustering and balanced splitting pass | Deterministic degree/id ordering and no-drop split |
| 9.5–9.6 | `generate_test.go` failed because `Client.Generate` did not exist | Local `/api/chat` client passes | Fixed low-temperature request contract |
| 9.7–9.10 | `consolidate_test.go` initially failed due to missing session linkage | Digest, retained consolidates relations, and unreachable no-op pass | Single `Run` orchestration function |
| 9.11–9.13 | Disabled invocation test added before command completion; also found and fixed a real bug where a missing (not just disabled) config file caused `cmdConsolidate` to `fatal()`/exit instead of degrading to disabled | Disabled CLI and runner are no-ops for both the explicit-disabled and missing-config-file cases | CLI delegates to shared orchestration |
| 9.14 | Full suite was run after implementation | CGO-free build, vet, unit/coverage suites passed | `git diff --check` clean |
### Verification
- `CGO_ENABLED=0 go build ./...` — passed
- `go vet ./...` — passed
- `go test ./...` — passed
### Design Deviations
None. (Idle worker deferred as noted above — not a deviation, the spec permits explicit-invocation-only.)

## PR 10A — Learned Ranker: Feature + Model Foundation
- Completed tasks: 10.1–10.4. Tasks 10.5–10.6 (cold-start fallback) live at the MCP integration boundary,
  completed in PR 10B, not here — the feature/model package below has no caller-side cold-start decision to make.
- Boundary: `internal/ranker` package only — feature vector construction and a pure-Go L2-regularized
  logistic regression, trainable and serializable. No wiring into recall/search yet.
### TDD Cycle Evidence
| Task | RED | GREEN | REFACTOR |
|---|---|---|---|
| 10.1–10.2 | `features_test.go` failed, package/`BuildFeatures` did not exist | Feature vector traces only to existing recall/config/store fields | None needed |
| 10.3–10.4 | `model_test.go` failed, package/`Train` did not exist | Batch gradient descent separates positive/negative labels; versioned serialize/load round-trips | Feature-schema-hash versioning |
### Verification
- `CGO_ENABLED=0 go build ./...` — passed
- `go vet ./...` — passed
- `go test ./...` — passed
### Design Deviations
None.

## PR 10B — Learned Ranker: Train + Live Integration
- Completed tasks: 10.5–10.12 (cold-start fallback, `omnia rank-train`, model-invalidation, MCP-boundary
  re-rank wiring, verification). Combined with PR 10A (10.1–10.4), all of Phase 10 is now complete.
- Boundary: `omnia rank-train` (dispatch + CLI help), `internal/mcp.ApplyLearnedRanker` at the same wiring
  seam as `RankResults`, and `internal/store.ListRankerTrainingRows` (the real training-data source, used only
  by `omnia rank-train` — not part of PR10A's pure algorithm package).
### TDD Cycle Evidence
| Task | RED | GREEN | REFACTOR |
|---|---|---|---|
| 10.5–10.6 | Cold-start behavior lives in `ApplyLearnedRanker`'s caller contract, not `internal/ranker` | Disabled, nil-model, and cold-start callers all pass through unchanged — verified via `TestNewServerWithConfig...` style disabled-registration tests | N/A |
| 10.7–10.8 | `rank_train_test.go` did not exist prior (a real gap left by the original implementation, found and filled while splitting this PR) | `omnia rank-train`: insufficient-examples message, promotes on successful eval, refuses to promote on regression | Injectable `rankerEval` var keeps the promotion gate testable without a real eval run |
| 10.9–10.10 | `model_test.go` (PR10A) covers schema-mismatch/corruption rejection | `LoadCurrent` recovers to fallback on either | Reused from PR10A |
| 10.11 | — | `ApplyLearnedRanker` called immediately after `RankResults` in `handleSearch`, same seam | `internal/recall`/`internal/store` untouched |
| 10.12 | — | Full suite green, `CGO_ENABLED=0` build/vet clean | — |
### Bugs found and fixed while completing this PR
- `cmdRankTrain` had the same `config.Load` error → `fatal()` anti-pattern already fixed in PR6B (`omnia blame`)
  and PR9B (`omnia consolidate`): a missing config file (not just an explicit disable) now degrades to the
  same "learned ranker is disabled" message instead of exiting the process.
- `rank_train_test.go` did not exist in the original implementation — added RED/GREEN coverage for the
  disabled path, the missing-config-file path, insufficient-training-examples, successful promotion, and
  eval-regression refusal, using the pre-existing injectable `rankerEval` seam.
### Verification
- `CGO_ENABLED=0 go build ./...` — passed
- `go vet ./...` — passed
- `go test ./...` — passed
### Design Deviations
None.

## PR 2A + PR 2B + Phase 3 — sqlite-vec-index (bundled Vec1)

- Completed tasks: 2.1–2.21, 3.1–3.10 (all of Phase 2/Phase 2B/Phase 3).
- Boundary: opt-in, additive Vec1 flat/cos exact float32 KNN index over the existing `embeddings.db`
  (`github.com/ncruces/go-sqlite3@v0.35.2`, pinned). The `embeddings` table remains the sole authoritative
  source of truth; the brute-force scan is never removed and is the permanent fallback for disabled, absent,
  unhealthy, corrupt, or dimension-mismatched cases. Default-OFF: `vector_index.enabled` absent/false is
  byte-for-byte identical to pre-v0.4 behavior by construction (every new code path is gated behind
  `s.vec != nil` / `WithVecIndex`, added as new branches around the pre-existing statements, never replacing
  them). Also lands PR1's config correction: `VecIndexConfig.Quantization` (unreleased) removed — v0.4's
  contract is exactly `vector_index.enabled`, flat/cos float32 only, no quantization/int8/binary knob.
- Empirical validation before implementation (per design's "Open Questions"): wrote small throwaway Go
  programs against the real pinned dependency (`github.com/ncruces/go-sqlite3@v0.35.2` +
  `github.com/ncruces/go-sqlite3-wasm/v3@v3.2.35303`) to lock down behavior the design flagged as needing
  verification before implementation, all confirmed:
  - `driver.Open(dsn, vec1.Register)` + `CREATE VIRTUAL TABLE ... USING vec1(vector, project)` +
    `INSERT INTO ...(cmd, vector) VALUES ('rebuild', '{index:"flat", distance:"cos"}')` works under
    `CGO_ENABLED=0`, and the 'rebuild' config persists across process restarts without reissue.
  - Score anchors: self/orthogonal/antipodal distances are exactly 0/1/2, i.e. `score = 1 - distance` gives
    1/0/-1 — confirms the PINNED bundled version, not the newer Vec1 trunk's `2 - distance` formula (verified
    against the live `sqlite.org/vec1` trunk doc, which documents the newer formula explicitly).
  - Vec1's metadata-column WHERE pushdown (`v.project = ?` / `v.project IN (...)`) filters BEFORE top-K
    accumulation (not after) — reproduced the exact crowding-out scenario the brute-force `SearchScoped` fix
    already guards and confirmed Vec1 preserves the same guarantee.
  - The `SELECT e.sync_id, e.obs_id, v.distance FROM vec_embeddings(?, ?) AS v JOIN embeddings AS e ON
    e.rowid = v.rowid WHERE v.project = ?` query shape from design.md works exactly as specified.
  - Dimension-mismatched inserts/queries error cleanly (`unexpected vector blob size N bytes, expected M`);
    the source table and vec1 table state remain intact after an isolated failed statement.
### TDD Cycle Evidence
| Task | Test File | RED | GREEN | REFACTOR |
|---|---|---|---|---|
| 2.1–2.2 | `internal/embed/store_vec1_test.go` | `WithVecIndex`/private connector referenced before existing; compile failure | Options-based `OpenStore`, `driver.Open(dsn, vec1.Register)` connector selection, disabled path unchanged | Connector isolated to `internal/embed/vec1_connector.go` |
| 2.3–2.4 | same | `vec_embeddings`/`vec_index_state` assertions failed, tables didn't exist | Same-DB DDL + bookkeeping table; `embeddings` untouched | `vecStateDDL`/`vecTableDDL`/`vecRebuildFlatCos` constants |
| 2.5–2.6 | same | `active_dim`/byte-order assertions failed, no state tracked | `vecIndex` struct + `hostIsLittleEndian`/`encodeNativeVector` + persisted `native_little_endian` marker, checked against host on every open | Nil-receiver-safe `vecIndex` methods (`usable`/`healthyOK`/`dim`/`markUnhealthy`/`reset`) |
| 2.7–2.8 | same | `VecBackfill`/`VecReindex` didn't exist; 998+2-row report assertions failed | `vecPopulate` shared backfill/reindex core; readiness marker only written after count verification | **Found and fixed a real deadlock**: `vecPopulate`'s original implementation nested an `ExecContext` INSERT inside a still-open `Rows` loop on `MaxOpenConns(1)` — reproduced via an empirical 1000-row benchmark (hung past a 15s timeout), fixed by buffering source rows before issuing any derived write (mirrors `Search`/`GraphScoped`'s own pattern); verified fix: 1000-row `VecReindex` now completes in ~43ms |
| 2.9, 2.12 | — | N/A (refactor/verify) | Shared DDL/state/backfill helpers in one file; disabled byte parity confirmed | `CGO_ENABLED=0 go build`/`go vet` clean |
| 2.10–2.11 | `internal/config/v04_config_test.go` | New reflection-based contract test failed against the still-present `Quantization` field | Removed `VecIndexConfig.Quantization` + its `applyDefaults` value; `VecIndexConfig` now exposes only `Enabled` | N/A |
| 2.13–2.14 | `internal/embed/store_vec1_test.go` | Upsert/Delete/Prune dual-write assertions failed (no derived mirroring existed) | Transactional dual-write in `Upsert`/`DeleteBySyncID` (`Prune` reuses `DeleteBySyncID`); forced-failure test (drop `vec_embeddings` mid-run) proves source mutation survives and Vec1 is marked permanently unhealthy | `vecIndex.upsertRow` isolated in `internal/embed/vec1_dualwrite.go` |
| 2.15–2.16 | `cmd/omnia/autoembed_test.go`, `cmd/omnia/recall_test.go` | New composition tests failed (functions took no `vecIndexEnabled` parameter yet) | Threaded `Config.VecIndex.Enabled` through `buildAutoEmbedWorker`, `buildCLIEmbedPurgeStore`, `buildRecallService`/`buildRecallServiceForCLI` (shared by cmdMCP/cmdServe/`omnia search`/`omnia eval --injection`) | Extracted `vecIndexStoreOptions` shared helper (`cmd/omnia/vecindex.go`) |
| 2.17–2.18 | `cmd/omnia/dashboard_test.go`, `internal/dashboard/local_datasource_test.go` (new) | Failed to compile (`VecIndexEnabled` field/param didn't exist) | Added `dashboard.Config.VecIndexEnabled`; extracted `buildDashboardConfig` from `cmdDashboard` for testability without starting a real HTTP server | `newLocalDataSource` passes `embed.WithVecIndex(cfg.VecIndexEnabled)` |
| 2.19–2.20 | `cmd/omnia/embed_test.go` (new) | `--reindex` flag didn't exist; disabled-capability test failed | Added `--reindex`; `omnia embed` now also calls `VecBackfill`/`VecReindex` after every reconcile and prints the report | **Found and fixed a real pre-existing anti-pattern**: `cmdEmbed`'s `config.Load` failure called `fatal()` (hard process exit) instead of degrading to "capability disabled" like `omnia blame`/`consolidate`/`rank-train` already do — fixed and covered by `TestCmdEmbed_MissingConfigFileDegradesGracefully` |
| 2.21 | — | N/A (refactor/verify) | — | `vecIndexStoreOptions` is the single shared options builder for every direct opener |
| 3.1–3.2 | `internal/embed/store_vec1_test.go` | Score-anchor assertions failed (no Vec1 read routing existed) | `vecScore(d) = float32(1 - d)`, locked to self=1/orthogonal=0/antipodal=-1 | Isolated in `internal/embed/vec1_search.go` |
| 3.3–3.4 | same | Parity assertions failed against brute force on a 500-vector, 2-project fixture | `tryVecSearch` routes `Search`/`SearchScoped` through metadata-filtered KNN + JOIN, converts scores, falls through to unmodified brute-force body on `ok=false` | Fixed a test-fixture bug found while writing this: `oneHot` vectors collapse into two tied similarity buckets, making top-k comparison ambiguous — switched to `angledVector` (distinct cosine per row) |
| 3.5–3.6 | same | Forced-failure/dimension-mismatch assertions failed | Any genuine Vec1 error marks the Store instance permanently unhealthy and falls back; a query-dimension mismatch routes to brute force without touching health at all | Centralized in `tryVecSearch`/`tryVecGraph`'s shared `ok` return convention |
| 3.7–3.8 | same | `GraphScoped` parity assertions failed (no per-node Vec1 KNN routing existed) | `tryVecGraph` runs one Vec1 KNN query per node (K = full scope size, guaranteeing the same candidate universe as the O(N²) brute-force scan) | Extracted `neighbor`/`finishGraph` as the ONE shared top-k-per-node/edge-dedup/degree/sort helper used by BOTH the brute-force and Vec1 paths |
| 3.9–3.10 | — | N/A (refactor/verify) | — | `CGO_ENABLED=0 go build ./...`, `go vet ./...`, full `go test ./...` all green; disabled-path byte parity confirmed throughout |
### Verification
- `CGO_ENABLED=0 go build ./...` — passed
- `go vet ./...` — passed
- `go test ./...` — passed (full repo, all packages)
- `gofmt -l` on every changed/new file — clean
- `git diff --check` — clean
- Disabled path (`vector_index.enabled` absent or `false`): `Store.vec` stays nil; every new dual-write/read
  branch is `if !s.vec.healthyOK() { <original unmodified statement> }` / `if hits, ok := s.tryVecSearch(...);
  ok { ... }` — the pre-existing brute-force statements are never edited, only wrapped, so disabled-path bytes
  are identical by construction, not merely by testing.
### Design Deviations
- **Config correction ownership (PR1 → PR2A)**: `VecIndexConfig.Quantization` removed per design's explicit
  "Config correction ownership" note — not a deviation, but flagged here since it un-checks part of historical
  task 1.2's original scope (task 2.10–2.12 is the sole owner of this correction, as tasks.md specifies).
- **`ready` semantics**: design's wording ("first-enable backfill... writes a completion marker") could be read
  as requiring per-row `Upsert` calls to grant read-routing readiness immediately. Implemented instead so
  `ready` is granted ONLY by a verified whole-table `VecBackfill`/`VecReindex` pass (count-verified), while
  individual `Upsert`/`DeleteBySyncID`/`Prune` calls dual-write incrementally without unilaterally granting
  readiness. `cmdEmbed` calls `VecBackfill` after every reconcile run specifically so a normal `omnia embed`
  invocation (the natural first-enable workflow) still activates reads without requiring a separate explicit
  `--reindex`. Rationale: only a full-table pass can supply the row-count verification REQ-467 requires;
  granting readiness from a single write would let reads route through Vec1 before every existing row is
  confirmed indexed.
- **`Graph`/`GraphScoped` Vec1 routing bails to brute force on ANY mixed-dimension row in scope** rather than
  attempting a partial per-node fallback — simpler and provably correct (the brute-force path already handles
  mixed dimensions via its own per-pair skip), at the cost of not accelerating a scope that happens to contain
  even one off-dimension row. Not expected to matter in practice (a store's active dimension is fixed by its
  configured embedding model).
- **go.mod transitive bumps**: adding `github.com/ncruces/go-sqlite3@v0.35.2` raised the MVS-selected versions
  of `golang.org/x/crypto`, `golang.org/x/net`, `golang.org/x/mod`, `golang.org/x/sync`, `golang.org/x/sys`,
  `golang.org/x/text`, `golang.org/x/tools` (via `go mod tidy`). All are backward-compatible minor bumps; full
  `go test ./...` passed with no regressions, including `internal/cloud/*` packages that depend on
  `golang.org/x/crypto`/`golang.org/x/net`.
### Open Questions / Follow-ups for a Human Reviewer
- No throughput/benchmark claim was made (matches design: "No throughput claim is part of this design") —
  `tryVecGraph` issues one Vec1 KNN query per node (O(N) round trips), which is a different cost SHAPE than the
  brute-force O(N²) in-process scan, not necessarily faster in absolute terms for small stores. Correctness was
  the priority given the review-workload constraints on this batch; a follow-up could benchmark real-world
  store sizes if throughput becomes a concern.
- `cmd/omnia/embed_test.go`'s `--reindex` coverage is scoped to the disabled-capability and missing-config
  degrade paths (the underlying `VecBackfill`/`VecReindex` report mechanics are exhaustively covered at the
  `internal/embed` layer). A full CLI-level test with a real seeded embeddings row would additionally require
  faking the Ollama HTTP embedder inside `embed.Reconcile`'s call path — deferred as an acceptable scope
  boundary given the mechanism itself is already fully tested.
