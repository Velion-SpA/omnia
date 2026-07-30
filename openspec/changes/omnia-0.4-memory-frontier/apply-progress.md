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
