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
