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

## PR 7 — Memory Enforcement Gate A: Matcher + Command Runner
- Completed tasks: 7.1–7.8.
- Boundary: `internal/enforce` package only (`matcher.go`, `runner.go`, `gate.go`) — trusted-procedure
  matching, a sandboxed command runner covering all four `postcondition_kind` values, and the pure
  pass/flag/block verdict decision. No audit logging, no override handling, and nothing reachable from
  MCP/CLI yet (both deferred to PR 8 by design, per tasks.md's own phase split).
### TDD Cycle Evidence
| Task | Test File | RED | GREEN | REFACTOR |
|---|---|---|---|---|
| 7.1–7.2 | `matcher_test.go` | `MatchTrustedProcedures` undefined, package did not compile | `ListProcedures{State:trusted,Project}` candidate set narrowed by a per-file `SearchProcedures` query, intersected by sync_id; only the trusted procedure among matching trusted/candidate/retired fixtures is selected | Per-touched-file FTS query (not one long AND-of-fragments query) so a real match is never spuriously suppressed by FTS5 phrase-adjacency semantics |
| 7.3–7.4 | `runner_test.go` | `RunCommand` undefined | `exec.CommandContext` via `sh -c`/`cmd /C`, hard timeout, exit code captured; non-zero exit is a FAILURE not an Err, TimedOut and Err are distinct outcomes | `CommandResult.Passed()` helper; output capped via `truncateOutput` |
| 7.5–7.6 | `gate_test.go` | `Decide`/`DecideOptions`/`Verdict*` undefined | All-pass → `pass`; one failure + `Mode` unset → `flag`; `Mode: "block"` + failure → `block`; verified against all four postcondition kinds independently | `decideVerdict` isolated from `evaluatePostcondition` so the verdict rule is unit-testable without spawning a process |
| 7.7 | `gate_test.go` (unconfigured-command + custom-gating cases) | A missing command config or `AllowCustomCommands=false` had no dedicated skip path | `resolveCommand` extracted as its own helper returning `(command, note, ok)`; unconfigured/gated kinds produce a `skipped` outcome that never escalates `decideVerdict`, even under `mode: "block"` | N/A — extracted directly during GREEN since the skip-path was designed as its own function from the start |
| 7.8 | full `internal/enforce` suite | N/A (verification-only) | `CGO_ENABLED=0 go build ./...`, `go vet ./...`, `go test ./...` (full repo, not just this package) all passed | N/A |
### Verification
- `CGO_ENABLED=0 go build ./...` — passed
- `go vet ./...` — passed
- `go test ./...` — passed (full repo)
### Design Deviations
- Task 7.8 says "unit suite green with injected fake command runner." The runner tests instead exercise the
  real `exec.CommandContext` path directly (`exit 0`/`exit 1`/`sleep 5` via the real shell, plus a genuine
  unstartable-process case) rather than injecting a fake/mock runner. This is a deliberate strengthening, not
  a shortcut: `RunCommand` has no external dependency to fake (no network, no LLM, no filesystem write) — it
  only shells out — so exercising the real implementation gives equal-or-better coverage with no added
  flakiness risk, and keeps `Decide`/`evaluatePostcondition` (the parts that DO need isolation from process
  spawning for fast, deterministic unit tests) fully covered via `gate_test.go`'s configured-command fixtures.
- The matcher narrows via one `SearchProcedures` call **per touched file** (unioned by sync_id) rather than a
  single combined query built from all touched paths. `SearchProcedures`'s `sanitizeFTS` quotes every
  whitespace-separated word and ANDs them; a single query built by concatenating multiple file-path fragments
  would require ALL fragments to literally co-occur in one procedure's trigger text, which is far more
  fragile than the design's intent ("narrow via SearchProcedures ... using the touched file paths"). Read
  literally per-path, this still uses `SearchProcedures` exactly as documented, just once per path instead of
  once total.

## PR 8 — Memory Enforcement Gate B: MCP/CLI + Override + Audit
- Completed tasks: 8.1–8.10. Combined with PR 7 (7.1–7.8), all of Phase 7/8 (`memory-enforcement-gate`,
  the v0.4 flagship) is now complete.
- Boundary: `mem_enforce` MCP tool (`internal/mcp/mcp.go`, `handleEnforce`) + `omnia enforce` CLI
  (`cmd/omnia/enforce.go`), both calling the SAME new `enforce.Evaluate` orchestration function
  (`internal/enforce/evaluate.go`) so the pass/flag/block/override contract and the audit-entry mapping can
  never drift between surfaces (REQ-418, task 8.9 satisfied by construction rather than a later extraction
  pass — see Design Deviations). `internal/audit` gains `ActionEnforce` plus five additive `omitempty` Entry
  fields (`Verdict`, `ProcedureSyncIDs`, `PostconditionKind`, `ExitCode`, `OverrideReason`) per ADR-7.
### TDD Cycle Evidence
| Task | Test File | RED | GREEN | REFACTOR |
|---|---|---|---|---|
| 8.1–8.2 | `internal/enforce/evaluate_test.go` | `Evaluate`/`EvalOptions` undefined | `DecideOptions.Override` added; `decideVerdict` returns the distinct `VerdictOverride` (never silently `pass`) only when there is an actual violation to override | N/A |
| 8.3–8.4 | `internal/audit/audit_test.go`, `internal/enforce/evaluate_test.go` | `audit.ActionEnforce` and the five new `Entry` fields did not exist; `Evaluate` did not audit at all | `ActionEnforce` + `Verdict`/`ProcedureSyncIDs`/`PostconditionKind`/`ExitCode`/`OverrideReason` added to `Entry`; `appendAuditEntry` called for every verdict (pass/flag/block/override), including the zero-match `pass` path (REQ-411 fail-safe) | N/A |
| 8.5–8.6 | `internal/enforce/evaluate_test.go` (`TestEvaluate_NeverWritesToTouchedFiles`) | N/A — this is an invariant check, not a driving requirement; the gate never had a file-write API to begin with | A seeded touched file's content is byte-identical before/after a failing `Evaluate` call | N/A — nothing to extract; `Decide`/`RunCommand` only read/execute by construction |
| 8.7–8.8 | `internal/mcp/mcp_enforce_test.go`, `cmd/omnia/enforce_test.go` | `mem_enforce` unregistered/`handleEnforce` undefined; `loadEnforcementConfig`/`cmdEnforce` undefined | `mem_enforce` gated behind `cfg.Enforcement.Enabled` in `registerTools` (mirrors `mem_blame`/`CodeGraph`); `omnia enforce` dispatch case added, `loadEnforcementConfig` degrades to disabled on ANY `config.Load` error (fresh-install-safe from the start — see Design Deviations) | N/A |
| 8.9 | — | — | `enforce.Evaluate` designed as the single shared function from the start: `handleEnforce` and `cmdEnforce` both build an `enforce.EvalOptions` and call it directly, so there was no duplicated per-surface audit-mapping code to consolidate | N/A — see Design Deviations |
| 8.10 | full repo | N/A (verification-only) | `CGO_ENABLED=0 go build ./...`, `go vet ./...`, `go test ./...` (58 packages) all passed; disabled-path verified byte-for-byte (`mem_enforce` absent from `ListTools()`, `omnia enforce` prints `capability disabled` and never opens the store) | N/A |
### Verification
- `CGO_ENABLED=0 go build ./...` — passed
- `go vet ./...` — passed
- `go test ./...` — passed (full repo, 58 packages)
- `gofmt -l` on every touched/created file — clean
- Manual check: `omnia enforce --files foo.go` with no `config.yaml` on `$PATH`/`$HOME` prints `capability
  disabled` and exits 0 (never opens a store, never a fatal exit) — the exact anti-pattern already fixed 3x
  elsewhere in this codebase (blame/consolidate/rank-train) was avoided from the start here.
### Design Deviations
- Task 8.9 ("Consolidate the pass/flag/block/override → audit-entry mapping into one function shared by MCP
  and CLI entry points") describes a REFACTOR step that implies the mapping existed in duplicated form first.
  Since PR7 and PR8 were implemented in one continuous session (not as literally separate incremental
  sub-PRs with intermediate duplication), `enforce.Evaluate` was written as the single shared function from
  its first GREEN commit — `handleEnforce` (MCP) and `cmdEnforce` (CLI) were never given their own
  independent audit-mapping code to later merge. The end state matches 8.9's intent exactly (one function,
  identical contract, REQ-418 satisfied); there was simply no separate extraction commit needed.
- `omnia enforce --block` is an added CLI convenience (`design.md`'s own CLI shape: "`omnia enforce [--files
  ...] [--block] [--override --reason ...]`") that forces `Mode: "block"` for that invocation and exits
  non-zero on a `block` verdict, so a pre-commit hook/CI step can act on the exit code directly — this isn't
  in the REQ text verbatim but is required for the CLI to actually be usable "for hooks/CI use" per REQ-418's
  own scenario.

## PR 11 — Repo Cartridge
- Completed tasks: 11.1–11.12 (all of Phase 11).
- Boundary: `internal/cartridge` (Build/Save/Load/ResolveRepo) plus `omnia cartridge build`/`omnia cartridge
  load` CLI, gated behind default-OFF `cartridge.enabled`. No MCP tool (`mem_cartridge`) was added — design.md
  explicitly marks it "Optional" and tasks.md's Phase 11 checklist only requires the CLI surface; a future
  increment can add the MCP tool without changing this contract.
### TDD Cycle Evidence
| Task | Test File | Layer | Safety Net | RED | GREEN | TRIANGULATE | REFACTOR |
|---|---|---|---|---|---|---|---|
| 11.1–11.2 | `internal/cartridge/build_test.go` | Unit | none (new package) | `cartridge.Build`/`BuildParams`/`Cartridge`/`SchemaVersion` undefined — compile failure | `Build` assembles `{schema_version, repo_root, head_sha, built_at, top_memories[], anchors[], trusted_procedures[], ranker_model_version?}`; `Save` writes the versioned JSON artifact | happy path, trusted-only procedure filter, top-N truncation | shared `rankedTopMemories` helper keeps `Build` itself linear |
| 11.3–11.4 | `internal/cartridge/load_test.go` | Unit | build_test.go suite | `cartridge.Load`/`ReasonStaleCommit` undefined — compile failure | `Load` globs the repo-id prefix, picks the most-recently-built file, compares `HeadSHA` | stale-commit vs. fresh-commit vs. picks-latest-of-two-builds | `LoadResult{Fresh, Reason}` keeps every degradation path a plain value, never an error |
| 11.5–11.6 | `internal/cartridge/load_test.go` | Unit | build_test.go suite | missing-file and corrupt-file cases both undefined pre-`Load` | missing directory/file and corrupt JSON both degrade to `LoadResult{Reason: ReasonMissing/ReasonCorrupt}` | missing vs. corrupt distinguished by `Reason`, never by a caller-visible error | single `Load` function, no separate corrupt-handling path to drift |
| 11.7–11.8 | `internal/cartridge/load_test.go` | Unit | build_test.go suite | old-schema-version case undefined pre-`Load` | `SchemaVersion` mismatch degrades to `ReasonOldSchemaVersion` before the `HeadSHA` comparison even runs | fixture written with `SchemaVersion - 1` | version check precedes commit check so a stale AND old-format file reports the more fundamental reason |
| 11.9–11.10 | `cmd/omnia/cartridge_test.go` | Unit | cmd/omnia suite | `cmdCartridge` undefined — compile failure | disabled (build/load), missing-config-file, outside-git-repo, full build→load round trip, stale-commit-after-new-commit, and missing-cartridge-inside-a-real-repo all pass | build+load round trip against a real temp git repo with a seeded observation | shared `cartridgeFlags`/`loadCartridgeConfig`/`cartridgeDataDir`/`loadCartridgeRankerModel` helpers keep both subcommands' wiring in lockstep |
| 11.11 | `internal/cartridge/build_test.go` | Unit | build_test.go suite | N/A (assertion-only, added after GREEN) | `TestBuildContentShapeAssertion` marshals a built `Cartridge` and rejects any JSON key outside the documented allowlist (REQ-455); `TestBuildNeverWritesSyncMutations` confirms `ListPendingSyncMutations` count is unchanged by `Build`+`Save` (REQ-454) | N/A | N/A |
| 11.12 | full repo | Verification | targeted `internal/cartridge`/`cmd/omnia` suites first, then repo-wide | N/A | full verification passed; `gofmt`/`git diff --check` clean | disabled path (build+load) tested at both the CLI-flag level and the config-file level (explicit `false` and missing file) | N/A |
### Design Deviations
- Repo-root resolution shells directly to `git -C <dir> rev-parse --show-toplevel` inside `internal/cartridge`
  (mirroring `internal/anchor.Probe`'s own unexported `repoRoot` method and `internal/codegraph.Normalize`'s
  identical probe) rather than exporting `internal/anchor.Probe.repoRoot` or reusing `codegraph.Normalize`
  (whose file-relative-path contract doesn't fit a bare directory lookup cleanly). `HeadSHA` itself reuses the
  already-exported `internal/anchor.Probe.HeadSHA` directly, per design's explicit pointer to that method.
- `top_memories` ranking reuses `internal/mcp.RankResults`/`ApplyLearnedRanker` directly from
  `internal/cartridge` (not duplicated) — the same reuse pattern `cmd/omnia/recall.go`/`eval.go` already use to
  pull ranking helpers from `internal/mcp` at the CLI layer. With no live query, relevance is uniform across
  candidates (nil map), so ranking degrades to recency × importance when enabled, or `AllObservations`' own
  natural recency-DESC order when ranking is disabled too — never an error, never an empty digest.
- `internal/cartridge` normalizes the `--project` filter once via `store.NormalizeProject` before calling
  `AllObservations` (which does not normalize its own project filter internally, unlike
  `ListActiveAnchors`/`ListProcedures`), so `top_memories` and `anchors`/`trusted_procedures` never
  silently disagree on project casing. This is a local normalization inside the new package only — no existing
  store method's behavior was changed.
### Verification
- `CGO_ENABLED=0 go build ./...` — passed
- `go vet ./...` — passed
- `go test ./...` — passed (full repo, all packages, all v0.4 flags default-OFF)
- `gofmt -l` on all new/changed files — clean
- `git diff --check` — passed
- Disabled path: `omnia cartridge build`/`omnia cartridge load` both print `capability disabled` and touch
  neither the store nor the filesystem cartridges directory; a missing `config.yaml` degrades identically
  (matches the PR6B/PR9B/PR10B anti-pattern fix — never a fatal exit for a missing config file).

## PR 11 — Repo Cartridge: Review Remediation
An independent adversarial review of PR 11 found one blocker and two should-fix issues before this could
merge. All three are fixed with new RED→GREEN test coverage, on top of the original 4 commits (not amended).
### Findings fixed
1. **BLOCKER — default invocation leaked memories across all projects.** `omnia cartridge build`/`load` with
   no `--project` flag passed an empty string straight through to `Build`/`Load`, and
   `store.NormalizeProject("")` is treated by `AllObservations`/`CodeDecisionGraph`/`ListProcedures` as "no
   filter — every project." The bare, most-likely-to-be-run invocation therefore silently digested every
   project's memories into one file instead of just the current repo's. Fixed with a new
   `resolveCartridgeProject` helper (`cmd/omnia/cartridge.go`) that falls back to `detectProject(repoRoot)`
   when `--project` is empty, and errors loudly with a `--project` hint if detection itself fails — mirroring
   `resolveConflictsProject`'s existing detect-or-error-loudly convention rather than silently defaulting to
   "everything." Test: `TestCmdCartridgeBuildDefaultsToDetectedProjectNotEveryProject`.
2. **SHOULD-FIX — on-disk cartridge key ignored project, and `load`'s `--project` flag was parsed and
   discarded.** Two different projects sharing one repo+commit (a supported, tested scenario per
   `internal/project/detect_test.go`'s monorepo-subproject tests) would silently overwrite each other's
   cartridge file. Fixed: `Cartridge` gained a `Project` field, `FileName` (renamed from unexported `fileName`)
   now keys the on-disk artifact as `<repo-id>-<project>-<head-sha>.json`, and `Load` takes a `project`
   parameter, scopes its glob by project, and re-verifies the loaded file's embedded `Project` against the
   request as a defense-in-depth check (new `ReasonProjectMismatch` degradation reason — never a caller-
   visible error). Tests: `TestSaveKeysCartridgeByProjectAvoidingCrossProjectCollision`,
   `TestCmdCartridgeLoadFiltersByProjectAvoidingCrossProjectLeak`,
   `TestLoadReportsProjectMismatchForTamperedCartridge`.
3. **SHOULD-FIX — plaintext cartridge bypassed at-rest encryption.** `Save` wrote an unencrypted JSON file
   regardless of `EncryptionConfig.Enabled`. Since the memory-at-rest-security capability itself (PR4/PR5)
   hasn't landed yet, there is no encrypt-on-write helper to call — so rather than implement encryption out of
   scope, `Save` now fails closed: it refuses the write and returns a clear error when
   `encCfg.Enabled` is true, explaining cartridge export doesn't yet support encrypted output. This is a known
   limitation to revisit once PR4/PR5 lands. Test: `TestCmdCartridgeBuildRefusesPlaintextWhenEncryptionEnabled`.
### Verification
- `CGO_ENABLED=0 go build ./...` — passed
- `go vet ./...` — passed
- `go test ./...` — passed (full repo, all packages)
### Design Deviations
None beyond what's described above.

## 2026-07-30 — PR 2A + PR 2B + Phase 3 — sqlite-vec-index: Review Remediation
An independent adversarial review of the sqlite-vec-index capability found three SHOULD-FIX issues (plus two
findings explicitly deferred, see "Deliberately not fixed" below). All three fixed findings are addressed with
new/updated test coverage, on top of the original 5 commits (not amended).
### Findings fixed
1. **SHOULD-FIX — `GraphScoped`'s brute-force logic had been extracted into a helper shared with the Vec1
   path.** `store.go`'s `Graph`/`GraphScoped` used to have their own self-contained edge-dedup/degree/sort
   logic; a prior pass extracted it into a `finishGraph`/`neighbor` helper called by BOTH the brute-force path
   (`store.go`) and the Vec1 path (`vec1_search.go`'s `tryVecGraph`). The two paths were behaviorally identical
   either way (map-accumulation + explicit final sort is order-independent), so there was no live bug — but the
   coupling meant a future Vec1-only tuning change to the shared helper could silently regress the brute-force
   fallback, defeating the point of "never edit the brute-force path, only wrap it in a new branch." Fixed:
   `store.go`'s `GraphScoped` now has its own private, self-contained `neighbor`/`pair`/`edgeScore` logic again
   — restored verbatim from `origin/main`'s pre-v0.4 version (the only addition is the pre-existing
   `tryVecGraph` wrapper call at the top, which was already a correctly-shaped new branch). `vec1_search.go`
   keeps its OWN private copy, renamed `vec1Neighbor`/`vec1FinishGraph` so it can never be confused with or
   accidentally shared by the brute-force path again. Verification: manually diffed the restored `GraphScoped`
   function body (everything after the `tryVecGraph` wrapper) against `git show origin/main:internal/embed/
   store.go` — byte-for-byte identical (`diff` on the extracted function bodies produced zero lines of
   difference besides the wrapper). `Graph` itself was never touched (already a one-line delegate to
   `GraphScoped`, unchanged). Existing brute-force/Vec1-parity tests
   (`TestVecGraph_MatchesBruteForceEdgeSetAndRespectsProjectScope`, `TestStore_Prune*`, and the rest of
   `store_test.go`) pass unmodified — no test changes were needed for this finding since it was a pure
   decoupling refactor, not a behavior change. Since some duplication is the intended trade-off here (the
   design's "never edit the brute-force path" constraint outweighs DRY for this one helper), no shared-helper
   regression test was added; the manual diff above is the confirmation artifact for this finding.
2. **SHOULD-FIX — `Prune()` rerouted through `DeleteBySyncID` even when Vec1 is disabled, changing
   disabled-path behavior.** `Prune`'s per-id delete loop used to issue the original inline `DELETE FROM
   embeddings WHERE sync_id = ?` statement directly; it had been changed to call `DeleteBySyncID`
   unconditionally (to reuse the Vec1 dual-write logic when enabled). This silently changed the disabled path
   in two ways: (a) the error text became double-wrapped (`"embed: Prune delete %s: embed: DeleteBySyncID %s:
   %w"` instead of the original single `"embed: Prune delete %s: %w"`), and (b) it introduced a new
   `res.RowsAffected()` failure surface that never existed in `Prune`'s original disabled-path code — a real
   "byte-for-byte identical when disabled" violation. Fixed with a new `if !s.vec.healthyOK() { ... } else {
   ... }` branch inside the delete loop: the disabled branch is the untouched original single statement +
   single error wrap (byte-for-byte restored from `origin/main`); a new branch below it reuses `DeleteBySyncID`
   only when Vec1 IS healthy, which is new capability behavior, not a disabled-path regression. TDD: wrote
   `TestStore_Prune_DisabledPathErrorTextIsSingleWrapped` first — it forces the underlying per-id `DELETE` to
   fail deterministically via a SQL trigger (`RAISE(ABORT, ...)` scoped to one sync_id, so the listing `SELECT`
   still succeeds) and asserts the resulting error text has the single-wrap prefix and contains neither
   `"DeleteBySyncID"` nor `"rows affected"`. Confirmed RED against the pre-fix code (`got "embed: Prune delete
   a: embed: DeleteBySyncID a: constraint failed: forced prune failure (1811)"` — the double-wrap smoking gun,
   caught exactly as expected), then GREEN after the fix (single wrap, no `DeleteBySyncID`/`rows affected`
   substrings, and the row survives the failed delete). `TestStore_Prune`/`TestStore_Prune_EmptyLiveSetRemovesAll`
   (pre-existing, unmodified) and the Vec1 dual-write Prune coverage
   (`TestVecIndex_UpsertUpdateDeletePrune_MirrorDerivedTable`) still pass, confirming the Vec1-enabled path is
   unaffected.
3. **SHOULD-FIX — the crowding-out-under-Vec1 test didn't verify Vec1 was actually engaged.**
   `TestVecSearch_CrowdingOutFixHoldsUnderVec1` asserted the crowding-out fix holds under Vec1, but — unlike its
   sibling `TestVecScore_SelfOrthogonalAntipodal` — never confirmed Vec1 was actually used for the query. If
   Vec1 had silently fallen back to brute force for any reason, the test would have still passed trivially via
   the brute-force path, without guarding the Vec1-specific pushdown behavior it's named for. Fixed: added `if
   !store.vec.usable() { t.Fatal(...) }` immediately after the `VecBackfill` call, mirroring the exact pattern
   already established by `TestVecScore_SelfOrthogonalAntipodal` (and the `build(true)` helpers in the
   parity/graph tests). Confirmed the test still passes with this assertion in place — Vec1 is genuinely
   engaged for this scenario, not silently falling back.
### Deliberately not fixed (explicitly out of scope for this pass)
- **Finding #4 — dimension-change silently disables backfill until `--reindex`.** Left as a documented known
  limitation; not addressed in this remediation pass.
- **Finding #5 — in-memory Vec1 state is set before `tx.Commit()` is confirmed.** Left as a documented known
  limitation; not addressed in this remediation pass.
### Verification
- `CGO_ENABLED=0 go build ./...` — passed
- `go vet ./...` — passed
- `go test ./...` — passed (full repo, all packages)
- `gofmt -l` on every changed file — clean
- `git diff --check` — clean
### Design Deviations
None beyond what's described above.

## 2026-07-30 — PR 7/8 Remediation: Adversarial Review Findings (SHOULD-FIX #1, #2 + nit)

- Fixes two independent-review SHOULD-FIX findings against PR 7/8 (`internal/enforce`), plus a same-file nit
  the reviewer also flagged. No task numbers change — this is a remediation pass on already-`[x]` Phase 7/8
  work, not new scope. All fail-safe verdict rules are unchanged: a runner timeout still can only ever
  produce `flag` (never `block`), and a matcher lookup error still can only ever produce `pass` (never
  `block`/`flag`).

### Finding #1 — timeout only killed the direct `sh -c` child, not the whole process tree
- **Root cause**: `RunCommand` (`internal/enforce/runner.go`) had no `SysProcAttr{Setpgid: true}`, so
  `exec.CommandContext`'s default cancellation (`cmd.Process.Kill()`) only ever reached the direct shell
  process. A command that forks/backgrounds a child (`sleep 30 & wait`, standing in for how real test
  runners/linters/build tools fan out per-package/per-worker subprocesses) left that child running past the
  "timeout" verdict. The existing `TestRunCommand_TimeoutIsNotAFailure` test used a bare `sleep 5` (no fork),
  which is why this gap wasn't caught.
- **Deeper discovery during RED**: the bug is worse than "an orphan leaks in the background." Because the
  backgrounded child inherits the SAME stdout/stderr pipe Go creates to capture `RunCommand`'s output,
  `cmd.Wait()` itself cannot reach EOF on that pipe until every process holding the write end closes it — so
  the FIRST version of the RED test (polling `processExists` after `RunCommand` returned) accidentally
  PASSED: `RunCommand` silently blocked for the full ~30s until the backgrounded `sleep` exited on its own,
  at which point the child was, of course, already dead. The real, observable symptom is that a 1-second
  configured timeout does not actually bound `RunCommand`'s wall-clock time at all when a postcondition
  command forks. The RED test was strengthened with an elapsed-time assertion (`elapsed > 10s` fails) to
  actually discriminate broken vs. fixed behavior, in addition to the `processExists` liveness check.
- **Fix**: `internal/enforce/runner.go` now calls `setProcessGroup(cmd)` before `cmd.Run()` and overrides
  `cmd.Cancel` to call `killProcessGroup(cmd)` instead of relying on `exec.CommandContext`'s default
  single-process kill. Both functions are OS-specific (`syscall.SysProcAttr`'s fields and `syscall.Kill`
  differ per platform), split via build tags following this repo's existing precedent
  (`cmd/omnia/bisect_nofollow_unix.go` / `_windows.go`):
  - `internal/enforce/runner_unix.go` (`//go:build !windows`): `Setpgid: true` makes the shell its own
    process-group leader; `killProcessGroup` sends `SIGKILL` to `-cmd.Process.Pid` (negative PID = whole
    group), falling back to a direct `cmd.Process.Kill()` if the group signal fails for any reason.
  - `internal/enforce/runner_windows.go` (`//go:build windows`): documented gap — `setProcessGroup` is a
    no-op and `killProcessGroup` falls back to killing only the direct process. A full Windows process-tree
    kill needs Job Objects (`CreateJobObject`/`AssignProcessToJobObject`), a materially larger feature than
    this fix's scope; this capability's dev/CI targets are macOS/Linux. Documented here rather than silently
    left unaddressed.
- **Test**: `TestRunCommand_TimeoutKillsBackgroundedGrandchild` (`internal/enforce/runner_test.go`) —
  RED confirmed (30.01s elapsed, failed the 10s bound) against the unfixed runner; GREEN confirmed (1.00s
  elapsed, backgrounded child pid gone) after the fix. Skips on `windows` (POSIX shell syntax used in the
  repro — `$!`, `wait` — does not translate to `cmd /C` either, independent of the process-group gap above).

### Finding #2 — matcher lookup errors were silently swallowed as a bare `pass`
- **Root cause**: `Evaluate` (`internal/enforce/evaluate.go`) discarded `MatchTrustedProcedures`'s error
  entirely, returning a `pass` indistinguishable from a genuine "nothing matched." design.md's own contract
  ("cannot scope → pass with note") was not honored — if the procedure store started failing (DB corruption,
  disk I/O error), the gate would silently report clean passes forever with zero signal that enforcement
  itself was broken.
- **Fix**: fail-safe bias is unchanged — a matcher error still only ever yields `VerdictPass`, never
  `block`/`flag`, even under `mode: block` (asserted directly in the new test). Added:
  - `Result.Note` (`internal/enforce/gate.go`) — an additive `omitempty` JSON field, empty for every ordinary
    pass/flag/block/override outcome; populated only by `Evaluate`'s matcher-error path with
    `fmt.Sprintf("procedure lookup failed, returning unscoped pass: %v", err)`.
  - `audit.Entry.Note` (`internal/audit/audit.go`) — a sixth additive `omitempty` ADR-7 field (alongside the
    five PR8 added: `Verdict`, `ProcedureSyncIDs`, `PostconditionKind`, `ExitCode`, `OverrideReason`), wired
    through in `appendAuditEntry` so the note reaches the audit trail, not just the in-memory `Result`.
  - `Result` is marshaled directly to JSON by `cmd/omnia/enforce.go`'s CLI output, so `note` surfaces there
    automatically — no CLI/MCP handler changes were needed.
- **Test**: `TestEvaluate_MatcherErrorPassesWithNoteAndAudits` (`internal/enforce/evaluate_test.go`), using a
  new `fakeErroringProcedureSource` test double implementing `ProcedureSource` (`ListProcedures`/
  `SearchProcedures` both return a forced error) — the seam the reviewer confirmed existed specifically for
  this kind of error injection but had zero tests exercising it before this one. RED confirmed (`Note` empty)
  against the unfixed `Evaluate`; GREEN confirmed (non-empty `Note` on both the `Result` and the audit entry,
  verdict still `pass` under `mode: block`) after the fix.

### Nit — `gate.go`'s `Decide` produced `"violations":null`, `evaluate.go`'s error path produced `"violations":[]`
- One-line fix in `internal/enforce/gate.go`'s `Decide`: `var violations []Violation` → `violations :=
  []Violation{}`, so both code paths in this package now serialize the same JSON shape for "no violations"
  (`omnia enforce`'s JSON output is consumed by hooks/CI per REQ-418, where `null` vs. `[]` is a real
  difference for a naive consumer).

### Verification
- `CGO_ENABLED=0 go build ./...` — passed
- `go vet ./...` — passed
- `CGO_ENABLED=0 go test ./...` — passed (58 packages, full repo)
- `internal/enforce` full package suite — all pre-existing tests plus the 2 new ones pass; no regressions.
### Design Deviations
- None beyond the documented Windows process-tree-kill gap above (Finding #1): fail-safe verdict bias is
  unchanged for both findings, and the `Note`/`processExists` additions are purely additive (new
  `omitempty` fields, new unexported helper functions) — no existing field, JSON shape, or exported signature
  was removed or changed incompatibly.

## 2026-07-30 — PR 4/5: `memory-at-rest-security` (Phase 4 + Phase 5, final v0.4 capability)

Implements the memory-at-rest-security capability end-to-end on branch `codex/v04-security-impl` in worktree
`/private/tmp/omnia-v04-security`, based on `main` (all 6 other v0.4 capabilities already merged, including
sqlite-vec-index which owns the dual-driver seam this capability reuses). Two commits, matching tasks.md's
PR4/PR5 split: `a8e75c2` (Phase 4: keychain + adiantum VFS wiring) and `6923715` (Phase 5: migration + CLI +
audit/receipt coverage). Strict TDD throughout — every new behavior has a RED test confirmed failing for the
stated reason before the GREEN implementation landed.

### Phase 4 (PR4) — `internal/keychain` + driver-selection wiring

- **`internal/keychain` (new package)**: shells to macOS `/usr/bin/security` (`find-generic-password`/
  `add-generic-password -U`) or Linux `secret-tool` (`lookup`/`store`), mirroring `internal/anchor`'s
  shell-to-git de-risk pattern exactly — never a linked keychain library (rejected `github.com/keybase/
  go-keychain`/`99designs/keyring`: both link `Security.framework` via cgo). Injectable `run` func (mirrors
  `Probe.runGit`) plus an injectable `goos` field so tests exercise BOTH the macOS and Linux argument shapes
  deterministically regardless of the host actually running the suite. `ErrUnavailable` (CLI missing) vs
  `ErrNotFound` (CLI works, item absent — classified via macOS exit code 44 / Linux exit code 1 + empty
  stdout) are distinct sentinels; `GetOrCreateHexKey` generates via `crypto/rand` (32 bytes) only on
  `ErrNotFound`, never on `ErrUnavailable`. `keychain.Resolve` (task 4.7 REFACTOR) centralizes the
  keychain-or-fail DECISION (fail-closed by default; `allowPlaintextFallback=true` degrades explicitly) so
  `internal/store` and `internal/embed` — which must never import each other (existing architecture
  guardrail) — share ONE implementation via a `Resolver` interface instead of duplicating the logic.
- **`internal/store/encryption.go`** (omnia.db) and **`internal/embed/encryption.go`** (embeddings.db): both
  add an `EncryptionConfig`-gated branch in `New`/`newWithoutRepair`/`OpenStore` that opens via
  `github.com/ncruces/go-sqlite3`'s `adiantum` encrypting VFS (`?vfs=adiantum` + per-connection `PRAGMA
  hexkey=...` issued in the init callback — NOT the URI, since the adiantum package's own docs warn a
  URI-embedded key is visible via `vfs.Filename.URIParameters`) instead of `modernc.org/sqlite`. `internal/
  embed`'s version composes with the existing Vec1 seam (`vec1.Register` in the SAME init callback,
  encryption key first) when both capabilities are enabled — verified by
  `TestOpenStore_EncryptionAndVecIndex_BothEnabled_ComposeInOneConnector`. Disabled (the default) never
  consults the keychain seam at all (pinned by `TestNew_EncryptionDisabled_IsByteForByteUnaffected`/
  `TestOpenStore_EncryptionDisabled_NeverConsultsKeychain`).
- **Degradation**: `allow_plaintext_fallback=false` (default) → refuse to open, `ErrEncryptionKeyUnavailable`/
  `keychain.ErrKeyUnavailable`. `=true` → stderr warning + one `audit.ActionEncryptionFallback` entry (new
  `Action` constant) + open unencrypted. Never silent either way.
- **Empirical discovery (this capability's own "Open Questions" flag, now resolved)**: `internal/store`'s
  schema depends on FTS5 (`observations_fts`), which is NOT compiled into the ncruces driver by default —
  opening without registering it produced `no such module: fts5` on the very first migration. Fixed by
  registering `github.com/ncruces/go-sqlite3/ext/fts5` alongside the hexkey PRAGMA in every `internal/store`
  init callback. `internal/embed`'s schema has no virtual tables outside the optional Vec1 table, so it needed
  no equivalent registration.
- **Composition-root wiring** (necessary for the capability to be reachable in production, beyond tasks.md's
  literal checklist but required for functional correctness): `applyEncryptionConfig` (mirrors
  `applyTimeTravelConfig`) threads `config.yaml`'s `encryption.*` into the shared `store.Config` ONCE at
  `run()`'s composition root, covering every CLI subcommand that receives `cfg` by value. `embedStoreOptions`
  (renamed from `vecIndexStoreOptions`) now also threads `EncryptionConfig` into every direct
  `embed.OpenStore` production call site (`cmd/omnia/embed.go`, `autoembed.go` ×2, `recall.go`,
  `consolidate.go`, `internal/dashboard/local_datasource.go` via new `dashboard.Config` fields) — without
  this, an operator who migrates `embeddings.db` to an encrypted file would find every subsequent `omnia
  embed`/auto-embed/recall/consolidate/dashboard run failing to reopen it.

### Phase 5 (PR5) — migration, CLI, provenance receipts, threat model

- **`internal/store/migrate_encryption.go`** and **`internal/embed/migrate_encryption.go`**
  (`MigrateToEncrypted`/`MigrateToPlaintext`/`RotateKey`, independent per-package implementations — not
  shared code, since the two schemas differ (FTS5 vs Vec1) and the packages must never import each other):
  `VACUUM INTO` a sibling temp file (encrypted target via `?vfs=adiantum&hexkey=...`, plaintext target via
  `?vfs=os` — see empirical note below), verify the row count matches via a fresh connection against the
  temp file, atomically rename the original aside as a timestamped `.bak-<UTC-timestamp>` (kept indefinitely;
  cleanup is an explicit separate operator action, never automatic), then rename the migrated copy into
  place. Any failure aborts BEFORE the rename — the original is never touched or left half-migrated. Missing
  source file and already-in-target-format are both no-ops, not errors (idempotent, safe to call on every
  `omnia security encrypt`/`decrypt`/`rotate-key` invocation).
- **Empirical discovery**: `VACUUM INTO` run from a connection whose CURRENT vfs is `adiantum` tries to open
  a plaintext target under that SAME encrypting VFS unless the target URI explicitly overrides it with
  `?vfs=os` (`os` is ncruces' reserved default-VFS name, confirmed via `vfs.Find("")`/`vfs.Find("os")` both
  resolving to it) — without this, decrypt failed with a generic `unable to open database file`. Verified via
  a throwaway experiment test (deleted before commit) before writing the real implementation, given design.md
  flagged the combined adiantum-VFS mechanics as needing empirical verification.
- **`RotateKey`**: re-encrypts directly from an old-key-encrypted source to a new-key-encrypted target
  (never round-trips through plaintext on disk, unlike a naive decrypt-then-encrypt composition, which would
  leave the primary file briefly/permanently plaintext if a crash landed between the two steps). A missing
  file is a no-op (a capability that was never enabled must never block rotating the OTHER file's key); an
  existing-but-plaintext file is a real error (rotating a key only makes sense on an already-encrypted file).
- **`omnia security encrypt`/`decrypt`/`rotate-key`** (`cmd/omnia/security.go`, dispatched from `main.go`'s
  switch): `encrypt` is gated on `encryption.enabled=true` — a correctness guard, not just style, since the
  normal open path only selects the encrypted driver when that flag is set; encrypting while it's false would
  produce a file nothing could reopen. `decrypt`/`rotate-key` are deliberately NOT gated on that flag —
  REQ-435's own scenario is "set `encryption.enabled` back to false, THEN decrypt," so gating decrypt on the
  same flag would make it refuse to run exactly when needed. All three follow the mandated config.Load
  anti-pattern fix (`err != nil || !appCfg.Encryption.Enabled` degrades to a printed message, never a bare
  `fatal()`) — decrypt/rotate-key check only load success, not `.Enabled`, per the reasoning above.
  `rotate-key` re-encrypts BOTH files with the new key BEFORE ever calling `keychain.Set` — if either file's
  rotation fails, the keychain is left completely untouched (still the OLD key), so both on-disk files remain
  readable with it; only after both succeed does the keychain get updated to the new key.
- **REQ-436 (trust_tag in read receipts)**: found and fixed a real gap — `TrustTag` was already surfaced on
  `mem_save`'s write-time echo and `mem_delete`'s pre-delete snapshot (prior provenance-foundation work), but
  NOT on the actual RETRIEVAL paths (`mem_search`'s structured `results[]` entries, `mem_get_observation`'s
  text output) that spec REQ-436's own scenario describes ("an observation... WHEN retrieved, THEN...
  trust_tag"). Added `entry["trust_tag"]` to `handleSearch`'s structured envelope (`internal/mcp/mcp.go`) and
  a `Trust: <tag>` line to `handleGetObservation`'s text output, both nullable/additive (omitted for
  pre-provenance-foundation rows with a nil `TrustTag`, never a misleading empty string).
- **REQ-437 (enforcement/consolidation audit coverage) cross-phase check**: `internal/enforce/evaluate_test.go`
  already had `TestEvaluate_EveryOutcomeWritesExactlyOneAuditEntry` covering all 4 verdicts — reconfirmed
  passing, no change needed. `internal/consolidate` had NO test asserting its existing `audit.Append(...,
  audit.ActionConsolidate, ...)` call actually reaches the audit log — added
  `TestRun_AppendsActionConsolidateAuditEntry` (isolates `$HOME` to exercise the REAL `audit.Append`, not a
  test seam, mirroring `internal/mcp`'s own isolation convention) to close that gap. Passed immediately
  (the wiring was already correct from PR9B), confirming REQ-437 end-to-end.
- **REQ-433 (threat model in `omnia doctor`)**: `cmdDoctor` already receives `cfg.EncryptionEnabled` via the
  shared `store.Config` composition root, so no second `config.Load` was needed. `writeDoctorJSON`/
  `renderDoctorText` both take a new `encryptionEnabled bool` param: disabled (default) is byte-for-byte the
  pre-v0.4 output (same discipline extended to a CLI report, not just a DB format); enabled adds one text
  line and one additive `encryption_threat_model` JSON field stating what's protected (disk theft/lost
  laptop, process stopped) and what explicitly is NOT (live-process memory dump, unlocked keychain).
- **Verification**: `CGO_ENABLED=0 go build ./...`, `go vet ./...`, `go test ./...` (full repo, all ~60
  packages) all clean after both commits. Added a 10k-row encrypt+decrypt round-trip fixture test (task 5.8),
  ~15s, passes. `gofmt -l` and `git diff --check` clean on every changed file.

### Deviations from tasks.md / design.md
- **Production composition-root wiring** (threading `encryption.*`/`EncryptionConfig` through
  `embedStoreOptions` and every `embed.OpenStore` call site, plus `dashboard.Config`) was not literally itemized
  in tasks.md's Phase 4/5 checklist the way sqlite-vec-index's PR2B dedicated explicit work-unit scope to the
  equivalent wiring. Did it anyway: without it, the capability would be only half-reachable (omnia.db fully
  wired via the shared `store.Config`, embeddings.db not), which would silently break `omnia embed`/
  auto-embed/recall/consolidate/dashboard the moment an operator ran `omnia security encrypt`.
- **`RotateKey`'s missing-file semantics changed mid-implementation**: originally returned an error for a
  missing file (matching the "already plaintext" error case), but the CLI integration test caught that this
  breaks `omnia security rotate-key` whenever only ONE of the two capabilities (embeddings) was ever enabled —
  changed to a no-op, mirroring `MigrateToEncrypted`/`MigrateToPlaintext`'s existing convention, before this
  ever reached a real user.
- No deviation from the ADR-1 dual-driver strategy, ADR-2 full-database adiantum choice, or ADR-3 shell-to-CLI
  keychain choice — all three were followed exactly as specified.

### Open questions / known limitations (flagging per the task's own request — highest-data-safety-risk
capability of the whole v0.4 release)
1. **`rotate-key`'s two-file-plus-keychain update is not fully atomic.** If `omnia.db`'s rotation succeeds but
   `embeddings.db`'s fails, `omnia.db` is already re-encrypted with the NEW key while the keychain still holds
   the OLD one — the CLI prints explicit recovery instructions (restore `omnia.db`'s `.bak-*` or manually
   complete the rotation) rather than silently leaving an inconsistent state, but this is manual recovery, not
   automatic. A true two-phase-commit across 3 resources (2 files + 1 keychain entry) would need a
   write-ahead intent log; out of scope for this pass given the size budget, and not required by tasks.md's
   literal checklist.
2. **Linux keychain coverage is exit-code-heuristic, not empirically verified against a real `secret-tool`.**
   `secret-tool lookup`'s "not found" is classified as exit code 1 + empty stdout (a reasonable interpretation
   of its documented behavior) but was never run against a real Linux host with libsecret installed — flagged
   as an open item in design.md itself ("Linux keychain coverage beyond secret-tool... degrade per ADR-3"),
   unchanged by this pass.
3. **`omnia security decrypt`/`rotate-key` require a resolvable keychain key even to detect "nothing to
   decrypt/rotate."** If a user runs `decrypt` on a store that was NEVER encrypted and has no keychain entry
   at all, they get "no key found" rather than a friendlier "nothing to do" — the underlying migration
   functions DO short-circuit on "already in target format" before touching the key, but the CLI resolves the
   key first for a clearer degradation story on the common case (genuinely-encrypted-but-keychain-broken).
   Minor UX rough edge, not a correctness or safety issue.
4. **Backup files (`.bak-<timestamp>`) accumulate indefinitely** — by design (never delete data the user might
   need to recover), but there is no `omnia security` subcommand to list/prune them yet. Left as a documented
   follow-up, not blocking this capability's REQ-430–437 contract.
