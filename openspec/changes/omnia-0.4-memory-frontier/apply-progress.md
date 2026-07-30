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
