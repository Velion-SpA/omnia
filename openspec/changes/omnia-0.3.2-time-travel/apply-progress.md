# Apply Progress: Omnia v0.3.2 — Time Travel

## Completed Work

- [x] 1.1–1.3 — deterministic portable v2 export envelope and verification.
- [x] 2.1–2.3 — transactional, idempotent core import and verification.
- [x] 3.1–3.3 — validated, atomic relations and anchors import and verification.
- [x] 4.1–4.3 — procedures and portable archive delivery across store, CLI, server, and sync boundaries.
- [x] 5.1–5.3 — local history substrate, retention/purge behavior, and bulk/pulled revision capture.
- [x] 6.1–6.3 — recorded-state queries, recorded-time search/context, and CLI exposure.
- [x] 7.1–7.3 — exact recorded-event bounds, resumable bisect CLI/state machine, durable process-safe state, and verification.

## TDD Cycle Evidence

Strict TDD was active for this change. The original executor phase reports recorded
the failing-first behavior summarized below, but the raw RED command transcripts
were not retained in the final filesystem. RED entries are therefore labeled as
**executor-report evidence**, not as claims that a raw failure log is still
available. GREEN and later triangulation are independently auditable from the
reviewed commits and final-integration logs.

| Work Unit / Tasks | Safety Net | RED | GREEN | REFACTOR / Triangulation | Evidence |
|---|---|---|---|---|---|
| **PR1A / 1.1–1.3** — deterministic portable v2 envelope (`af61ecc`) | Per-task pre-change output is not retained; the final tree only preserves the task instruction to run the focused store suite. | **Executor-report evidence:** the new envelope tests failed before portable schema-v2 metadata, canonical payload hashing, deterministic ordering, empty export, pinned state, and malformed/future-version rejection existed. No raw RED transcript remains. | `TestPortableExportV2DeterministicFullGraph` and `TestPortableExportV2EmptyAndRejectsInvalidInput` pass after integration. | Full and empty stores triangulate output shape; malformed, negative, legacy-v1, future-version, and checksum-mismatch inputs exercise distinct paths. Canonical hashing was isolated in `portable_export.go`. | Reviewed commit `af61ecc`; `/tmp/omnia-v032-verify-focused-store.log`; `/tmp/omnia-v032-verify-store-cover.out`. |
| **PR1B / 2.1–2.3** — safe core importer (`356bedf`) | Raw pre-change package baseline is not retained. | **Executor-report evidence:** importer tests were initially compile/behavior RED because v2 restore did not exist; after the first GREEN attempt, SQLite's ASCII-space trimming produced an additional identity/tombstone mismatch RED. No raw RED transcript remains. | Atomic/idempotent core restore, imported-wins semantics, explicit legacy handling, and duplicate-self-export convergence pass. | Observation and prompt tombstones are tested with ASCII-space and tab identities; future/count/checksum preflight failures and legacy duplicate convergence prevent a narrow hard-coded GREEN. Import dispatch/errors were centralized in `portable_import.go`. | Reviewed commit `356bedf`; `/tmp/omnia-v032-verify-focused-store.log`; `/tmp/omnia-v032-verify-store-cover.out`. |
| **PR1C / 3.1–3.3** — relations and anchors (`2517a6e`) | Raw pre-change package baseline is not retained. | **Executor-report evidence:** graph import first failed with `sql: no rows`; padded references then exposed an additional RED before canonical reference validation was complete. No raw RED transcript remains. | Validated, dependency-ordered relation/anchor import is atomic and idempotent. | Triangulation covers dangling endpoints/owners, invalid relation and judgment types, dangling supersedes references, invalid anchor status/field type, and padded references. Reference validation is shared. | Reviewed commit `2517a6e`; `/tmp/omnia-v032-verify-focused-store.log`. |
| **PR1D / 4.1–4.3** — procedures and portable delivery (`eb8bf13`, integrated checkpoint `b13d359`) | Raw pre-change store/server/sync/CLI baselines are not retained. | **Executor-report evidence:** procedure restore and portable CLI/HTTP delivery were absent; HTTP semantic import failures incorrectly returned 500 before error classification was added. No raw RED transcript remains. | Procedure round-trip/scale/atomicity, portable HTTP delivery/errors, CLI file mode, and unchanged sync wire all pass. | Invalid procedure and dangling relation cases triangulate HTTP semantics; scale and repeated import exercise transactional behavior; the sync regression test proves portable delivery remains decoupled from cloud wire format. | Reviewed commit `eb8bf13`; `/tmp/omnia-v032-verify-focused-{store,server,sync}.log`; `/tmp/omnia-v032-verify-affected-normal.log`. |
| **PR2A–PR2B / 5.1–5.3** — history substrate, retention, purge, bulk/pulled capture (`4096ae3`, `6897df7`; integrated `73f34bc`, `073362d`) | Raw per-slice pre-change config/store baselines are not retained. | **Executor-report evidence:** time-travel tests failed before gated before-image capture, retention, purge, and tombstones existed; timestamp parsing blocked monotonic boundaries. Bulk/pulled capture was then missing, followed by NULL-hash and disabled-mode timestamp-drift REDs. No raw RED transcript remains. | Disabled/insert no-op, before-image capture, unlimited/capped retention, hard-delete proof, rollback, project purge, and pulled update/delete behavior pass. | Rapid same-instant updates, success/rollback project purge, pulled update/soft-delete/hard-delete, disabled retention, capture failure, NULL-related paths, and race execution cover independent branches. | Reviewed commits `4096ae3`, `6897df7`; `/tmp/omnia-v032-verify-focused-store.log`; `/tmp/omnia-v032-verify-race-store.log`; `/tmp/omnia-v032-verify-{config,store}-cover.out`. |
| **PR3A–PR3D / 6.1–6.3** — recorded state/search/context/MCP/CLI (`80d6ece`, `3d89184`, `249cc75`, `c1dce0f`; integrated `761bbd6` through `148a97c`) | Raw per-slice pre-change store/MCP/CLI baselines are not retained. | **Executor-report evidence:** `StateAsOf`/recorded-search APIs were first compile/behavior RED. Subsequent cycles exposed relaxation-after-filtering, recording-boundary, bounded-context, feature-gating, and wall-clock-drift REDs before the four read slices were complete. No raw RED transcript remains. | State-at-time, recorded search/context, MCP schema/handlers, CLI disclosure, and unchanged live behavior pass; isolated smoke proves live v3 versus recorded v1 without current-state leakage. | Before/exact/future boundaries, retention gaps, hard delete, legacy rows, mixed-case projects, search relaxation, zero-hit boundaries, future=live context, adjunct omission, ordering, feature gate, and CLI/MCP paths triangulate the behavior. | Reviewed commits `80d6ece`, `3d89184`, `249cc75`, `c1dce0f`; `/tmp/omnia-v032-verify-focused-{store,mcp,cmd}.log`; `/tmp/omnia-v032-verify-race-{store,mcp,cmd}.log`; `/tmp/omnia-v032-final-smoke.log`. |
| **PR4A–PR4C / 7.1–7.3** — exact bounds, resumable bisect, durable process-safe state (`506dcd6`, `226659a`, `e72b2f8`; integrated `6c543fd` through `2ac186e`) | Raw per-slice pre-change store/CLI baselines are not retained. | **Executor-report evidence:** CLI tests first failed because `runBisect` was undefined. Later RED cycles covered composite-ID collisions, concurrent/safety transitions, ABA, restrictive umask, absent data directory, and coverage-subprocess regressions. No raw RED transcript remains. | Exact bounds, deterministic midpoint/marks, resume/reset, tombstone handling, corrupt/stale-state rejection, locking, ABA protection, absent-directory handling, and `0600` durability pass. | Same-timestamp adjacent/reversed IDs, ID-versus-timestamp bounds, availability boundary, zero/single candidates, invalid/reversed bounds, no-mark stability, tombstones, symlink/non-regular state, crash preservation, concurrent marks, reset/start ABA, restrictive umask, race, smoke, and cross-builds provide triangulation. | Reviewed commits `506dcd6`, `226659a`, `e72b2f8`; `/tmp/omnia-v032-verify-focused-{store,cmd}.log`; `/tmp/omnia-v032-verify-race-cmd.log`; `/tmp/omnia-v032-final-smoke.log`; `/tmp/omnia-v032-verify-cross-build.log`. |

### Post-Apply Verification Remediation Evidence

These fixes were discovered during the separate verification phase. They are
**not part of the 21 original apply tasks** and do not change the task count
above.

| Remediation | RED | GREEN / Triangulation | Evidence |
|---|---|---|---|
| **MCP checkout-independent fixtures** (`d7ceed3`) | The baseline and verified checkpoint reproduced exactly ten failures: `TestHandleSaveSuggestsTopicKeyWhenMissing`, `TestHandleSaveFallsBackToManualSaveWhenNoActiveSession`, `TestHandleSaveWithNilActivityStillSucceeds`, `TestHandleSavePromptCaptureFailureIsNonFatal`, `TestHandleSavePromptFeedsAutoCaptureContext`, `TestHandleSaveCapturePromptFalseSkipsCurrentPrompt`, `TestHandleSaveNoCurrentPromptStillSucceeds`, `TestHandleSaveDoesNotSuggestWhenTopicKeyProvided`, `TestHandleCapturePassiveDefaultsSourceAndSession`, and `TestHandleSaveReturnsLifecycleState`. | Explicit fixture enrollment/default-project setup removed checkout-basename dependence. The focused ten, full MCP package, full normal suite, and MCP/CLI race review passed. | Commit `d7ceed3`; RED `/tmp/omnia-remediation-baseline-mcp.log`; GREEN `/tmp/omnia-remediation-mcp-ten-green.log`, `/tmp/omnia-remediation-mcp-full-green.log`, `/tmp/omnia-remediation-full-test.log`, `/tmp/omnia-remediation-race.log`. |
| **Offline update policy for portable/recorded-time commands** (`fd64e63`) | **Executor-report evidence:** initial release-proxy tests proved `export`, `import`, and recorded-time reads contacted the update endpoint. Strict-TDD follow-up cycles caught parser-parity cases and a 350ms-future slow-proxy case before the policy/parser was complete. Raw per-cycle RED transcripts are not retained as standalone final artifacts. | Twenty unit cases and ten release-proxy scenarios passed, covering split/equal options, command-specific parsing, malformed/missing values, live-command policy, and the slow future response. Full `cmd/omnia` and race runs passed. | Commit `fd64e63`; `/tmp/omnia-v032-remediation-A-cmd-full-final2.log`; `/tmp/omnia-v032-remediation-A-cmd-race-final2.log`. |
| **Coverage subprocess/output harness** (`bd3d586`) | Regression RED: `captureOutput did not restore os.Stdout after panic`; the base coverage-instrumented `cmd/omnia` package also exited non-zero. | Panic-safe restoration was added and directly tested. Reviewed GREEN evidence reports `cmd/omnia` coverage at **69.9%**, plus full normal and race passes. | Commit `bd3d586`; baseline coverage evidence `/tmp/omnia-remediation-cover-cmd-baseline.log`; full/race evidence `/tmp/omnia-remediation-full-test.log`, `/tmp/omnia-remediation-race.log`. |
| **Bisect recording-boundary enforcement** (`24c0e73`) | Fresh first-post-enable ID bounds were rejected; a bad pre-boundary timestamp was accepted; pre-enable bad IDs/candidates leaked their secret content; and an over-broad fractional `.100Z` waiver was accepted and leaked. Review RED cycles reproduced the store and CLI failures. | Both good and bad bounds are now validated. Timestamp references use strict nanosecond boundaries; pre-enable and backdated candidates are filtered. Only the exact `.000Z` first-post-enable ID boundary receives the compatibility waiver, while `.950Z` passes naturally. Fresh CLI positive, negative, and valid-range cases pass, together with full store/CLI/full-normal, focused race, and coverage runs. | Commit `24c0e73`; `/tmp/omnia-v032-final-verify-cli-boundary-repro.log`; `/tmp/omnia-v032-boundary-red.log`; `/tmp/omnia-v032-boundary-review-red-{store,cmd}.log`; `/tmp/omnia-v032-boundary-v2-red-{store,cmd}.log`; `/tmp/omnia-v032-boundary-v2-cli-final.log`. |

## Reviewed Work Units

All reviewed implementation slices remained within the 400 changed-line budget. The planned PR4 slice was split into independently buildable PR4A, PR4B, and PR4C review units.

| Work unit | Reviewed commit | Scope | Changed lines |
|---|---|---|---:|
| PR1A | `af61ecc` | Deterministic portable v2 envelope | 399 |
| PR1B | `356bedf` | Transactional portable core restore | 400 |
| PR1C | `2517a6e` | Portable relations and anchors | 368 |
| PR1D | `eb8bf13` | Complete portable archive delivery | 380 |
| PR2A | `4096ae3` | Local observation revision substrate | 397 |
| PR2B | `6897df7` | Bulk and pulled revision capture | 380 |
| PR3A | `80d6ece` | Recorded state queries | 237 |
| PR3B | `3d89184` | Recorded-time retrieval | 213 |
| PR3C | `249cc75` | Bounded historical memory context | 310 |
| PR3D | `c1dce0f` | Recorded-time CLI reads | 400 |
| PR4A | `506dcd6` | Exact composite recorded-event bounds | 237 |
| PR4B | `226659a` | Resumable bisect CLI and state machine | 354 |
| PR4C | `e72b2f8` | Process locking and durable state hardening | 400 |

## Final Integration Mapping

The reviewed history/recorded-time/bisect commits were cherry-picked onto the portable-history checkpoint without conflicts.

| Work unit | Reviewed commit | Integration commit |
|---|---|---|
| PR1D checkpoint | `eb8bf13` | `b13d359` |
| PR2A | `4096ae3` | `73f34bc` |
| PR2B | `6897df7` | `073362d` |
| PR3A | `80d6ece` | `761bbd6` |
| PR3B | `3d89184` | `232f49b` |
| PR3C | `249cc75` | `4aed018` |
| PR3D | `c1dce0f` | `148a97c` |
| PR4A | `506dcd6` | `6c543fd` |
| PR4B | `226659a` | `11dac4e` |
| PR4C | `e72b2f8` | `2ac186e` |

Final integration worktree: `/tmp/omnia-v032-integration-20260728`
Implementation checkpoint: `2ac186e1362b22efc40ad615c8b9d215a183b088`
Current remediated/apply-evidence HEAD: `24c0e7325b604ae47f1abb72ea45425462060e38`

## Validation Evidence

- At implementation checkpoint `2ac186e`, focused recorded-time and bisect tests, store race tests, focused MCP/CLI race tests, `go vet`, formatting, module tidy diff, whitespace checks, and commit-hygiene checks passed.
- Affected-package coverage passed with 81.8% total; profile: `/tmp/omnia-v032-final-coverage.out`.
- Exact validated arm64 binary: `/tmp/omnia-v032-final-checkpoint-bin`.
- Real isolated recorded-time and bisect CLI smoke passed at the implementation checkpoint (`SMOKE_RESULT=PASS`); transcript: `/tmp/omnia-v032-final-smoke.log`. This smoke remains valid checkpoint evidence and will be re-executed by final `sdd-verify`.
- Smoke workspace: `/tmp/omnia-v032-final-smoke.9EyUc1` (also recorded in `/tmp/omnia-v032-final-smoke-dir.txt`). The smoke proved live versus `--as-of` state, bisect start/status/convergence/reset, and database mode `0600`.
- Initial verification found ten checkout-dependent MCP fixture failures and a coverage-harness subprocess/output failure. They were remediated by `d7ceed3` and `bd3d586`; they are not current validation failures.
- Combined preverify at pre-boundary-remediation HEAD `0964d17` passed `CGO_ENABLED=0 go test -count=1 ./...`.
- Combined preverify at `0964d17` passed `GOGC=1 CGO_ENABLED=0 go test -count=1 -cover ./...`.
- Combined package coverage at `0964d17`: `cmd/omnia` 70.0%, `internal/mcp` 88.4%, `internal/store` 79.2%, `internal/sync` 90.0%, and `internal/config` 92.6%.
- Race runs for `cmd/omnia` and `internal/mcp` passed at `0964d17`. `go vet`, `go build`, `go mod tidy -diff`, `git diff --check`, and remediation-file `gofmt` checks passed.
- Combined `0964d17` evidence logs: `/tmp/omnia-v032-remediation-combined-focused.log`, `/tmp/omnia-v032-remediation-combined-test.log`, `/tmp/omnia-v032-remediation-combined-cover.log`, `/tmp/omnia-v032-remediation-combined-race.log`, `/tmp/omnia-v032-remediation-combined-quality.log`, and `/tmp/omnia-v032-remediation-combined-final-audit.log`.
- Boundary remediation `24c0e73` independently passed `CGO_ENABLED=0 go test -count=1 ./...`, full `internal/store` and `cmd/omnia` suites, focused store/CLI race runs, `go vet`, changed-file formatting, `git diff --check`, and module checks. Coverage reached 82% for `BisectEvents` and 100% for its boundary helper. Fresh real CLI positive, negative, valid-range, and adversarial recording-boundary cases passed.
- Full-tree `gofmt -l` still reports 21 unrelated pre-existing files; every changed/remediation Go file is format-clean.

## Remaining Tasks

None in apply: all tasks 1.1–7.3 are complete.

## Status

21 of 21 apply tasks complete. The change is ready for the separate SDD verify phase. Verification and archive phases have not been claimed or marked complete.
