# Verification Report: Omnia v0.3.2 — Time Travel

**Change**: `omnia-0.3.2-time-travel`
**Mode**: Full SDD re-verification, Strict TDD
**Verified worktree**: `/tmp/omnia-v032-integration-20260728`
**Verified HEAD**: `eb259ce12f3b6cbea1aee8a7bd74a30b9f306457`
**Source remediation**: `24c0e7325b604ae47f1abb72ea45425462060e38`
**Verification date**: 2026-07-29
**Verdict**: **PASS WITH WARNINGS**
**Archive readiness**: **READY**

All 21 tasks are complete and all 29 specification scenarios have fresh passing runtime evidence. The prior first-post-enable bisect blocker is remediated: both bounds now enforce the persisted recording boundary, pre-enable/backdated candidates are filtered, the second-precision compatibility waiver is limited to post-enable observation IDs at exact `.000Z`, and a fresh built CLI succeeds with the original same-second first observation.

## Completeness

| Metric | Value | Result |
|---|---:|---|
| Tasks | 21/21 complete | ✅ |
| Requirements | 22 | ✅ |
| Scenarios | 29/29 runtime-compliant | ✅ |
| Planning artifacts read | proposal, 3 specs, design, tasks, apply progress | ✅ |
| Native dispatcher before verify | `nextRecommended: verify`, 21/21 tasks | ✅ |
| Critical findings | 0 | ✅ |

## Blocker Remediation Evidence

The remediation adds an explicit availability predicate in `internal/store/timetravel.go`, distinguishes observation-ID bounds from timestamps, validates both good and bad bounds before reading candidates, restricts the query to IDs created after enable, and filters backdated post-enable rows.

| Required regression | Fresh evidence | Result |
|---|---|---|
| Original immediate first-post-enable same-second positive bound | `TestBisectEventsAcceptsImmediateFirstObservationAfterEnable`; pure built CLI metadata `09:00:46.961944Z` with observation `09:00:46` | ✅ |
| Good bound availability | good timestamp and good pre-enable ID subtests | ✅ |
| Bad bound availability | bad timestamp, pre-enable ID, and backdated fractional-ID subtests | ✅ |
| Pre-boundary timestamp rejection | store and CLI tests return `history unavailable before` | ✅ |
| Pre-enable ID/candidate rejection | ID gate plus `WHERE id > initial_max_observation_id` | ✅ |
| No pre-enable/backdated secret leak | `TestBisectRejectsUnavailableBadBoundsWithoutLeakingPreEnableContent` | ✅ |
| Exact `.000Z` post-enable ID waiver | `TestBisectEventsUsesInitialIDToDisambiguateBoundarySecond` | ✅ |
| Fractional `.100Z` before a nanosecond boundary | rejected as a bound, filtered as a candidate, no content leak | ✅ |
| Fractional `.950Z` after boundary | accepted as a valid post-enable bound/candidate | ✅ |
| Valid range returns only available post-enable candidates | store filtering test and CLI valid-range case | ✅ |
| Exact/offset timestamp boundary semantics | exact and equivalent-offset timestamp subtests pass; before-boundary offset rejects | ✅ |

Evidence: `/tmp/omnia-v032-final-reverify-focused-store.log`, `/tmp/omnia-v032-final-reverify-focused-cmd.log`, and `/tmp/omnia-v032-final-reverify-cli-immediate-boundary.log`.

## Build and Test Evidence

| Command / evidence | Result | Log |
|---|---|---|
| Focused portable/history/context/bisect store suite | ✅ PASS | `/tmp/omnia-v032-final-reverify-focused-store.log` |
| Focused recorded-time MCP suite | ✅ PASS | `/tmp/omnia-v032-final-reverify-focused-mcp.log` |
| Focused CLI/bisect/update-policy/release-proxy suite | ✅ PASS | `/tmp/omnia-v032-final-reverify-focused-cmd.log` |
| Focused server/sync/config boundaries | ✅ PASS | `/tmp/omnia-v032-final-reverify-focused-boundaries.log` |
| Isolated `CGO_ENABLED=0 go test -count=1 ./...` | ✅ PASS | `/tmp/omnia-v032-final-reverify-full-test-isolated.log` |
| `GOGC=1 CGO_ENABLED=0 go test -count=1 -cover ./...` | ✅ PASS | `/tmp/omnia-v032-final-reverify-full-cover.log` |
| Fresh package coverage profiles | ✅ PASS | `/tmp/omnia-v032-final-reverify-{store,cmd,mcp,server,config,sync}-cover.log` |
| `go test -race` — store | ✅ PASS | `/tmp/omnia-v032-final-reverify-race-store.log` |
| `go test -race` — MCP | ✅ PASS | `/tmp/omnia-v032-final-reverify-race-mcp.log` |
| `go test -race` — relevant CLI | ✅ PASS | `/tmp/omnia-v032-final-reverify-race-cmd.log` |
| `go test -race` — server/sync | ✅ PASS | `/tmp/omnia-v032-final-reverify-race-boundaries.log` |
| `go vet ./...` | ✅ PASS | `/tmp/omnia-v032-final-reverify-vet.log` |
| `CGO_ENABLED=0 go build ./...` | ✅ PASS | `/tmp/omnia-v032-final-reverify-build-all.log` |
| Native CLI build | ✅ PASS | `/tmp/omnia-v032-final-reverify-build-native.log` |
| Linux/Windows/Darwin amd64 CLI builds | ✅ PASS | `/tmp/omnia-v032-final-reverify-build-{linux,windows,darwin}.log` |
| `go mod verify` | ✅ PASS | `/tmp/omnia-v032-final-reverify-mod-verify.log` |
| `go mod tidy -diff` | ✅ PASS | `/tmp/omnia-v032-final-reverify-mod-tidy-diff.log` |
| `git diff --check 41d8348..HEAD` | ✅ PASS | `/tmp/omnia-v032-final-reverify-diff-check.log` |
| Changed-file `gofmt -l` | ✅ PASS, empty | `/tmp/omnia-v032-final-reverify-format.log` |
| Full-tree `gofmt -l` | ⚠️ 21 unrelated warnings, zero changed-file overlap | `/tmp/omnia-v032-final-reverify-format.log` |
| Release-like 10-case offline/update-policy proxy matrix | ✅ PASS | `/tmp/omnia-v032-final-reverify-release-proxy-matrix.log` |
| Explicit release-like bisect proxy plus live control | ✅ PASS | `/tmp/omnia-v032-final-reverify-bisect-proxy.log` |
| Fresh pure CLI immediate-boundary lifecycle | ✅ PASS | `/tmp/omnia-v032-final-reverify-cli-immediate-boundary.log` |
| Fresh isolated CLI recorded reads/export/import/bisect smoke | ✅ PASS | `/tmp/omnia-v032-final-reverify-cli-smoke.log` |

### Verification Harness Timing Note

An initial non-isolated full-normal run was deliberately overlapped with full coverage, four race suites, builds, and focused tests. Its 350 ms near-future proxy case elapsed under heavy load and became a recorded-time request, so `TestReleaseLikeOfflineUpdateCheckPolicy` failed once. The same test passed in the focused suite, the standalone release-like matrix, package coverage, and the subsequent isolated full-normal run. This is classified as a test-timing warning, not a product failure; no failure is hidden or waived.

## Specification Compliance Matrix

### Portable Export — 9/9

| Scenario | Passing runtime evidence | Result |
|---|---|---|
| Full-graph export includes every entity | deterministic graph and procedure round-trip tests | ✅ |
| Export on an empty store | empty/invalid-input test | ✅ |
| Export identifies schema version | v2 envelope test and real CLI export | ✅ |
| Re-import creates no duplicates | core/graph/procedure idempotency tests; repeated CLI import and byte-identical destination export | ✅ |
| Round-trip preserves relations and provenance | full-graph and relation/anchor validation tests | ✅ |
| Repeated export is byte-identical | deterministic test and real CLI `cmp` | ✅ |
| Pre-0.3.2 import | explicit legacy import test | ✅ |
| Future-versioned import rejected | preflight and HTTP invalid-input tests | ✅ |
| Fully offline export/import | release-like proxy matrix | ✅ |

### Time-Travel Query — 10/10

| Scenario | Passing runtime evidence | Result |
|---|---|---|
| No `--as-of` supplied | live-path MCP/CLI and future/live tests | ✅ |
| Edited memory returns prior content | state, MCP, CLI, and real CLI smoke | ✅ |
| Soft-deleted memory renders as it existed at T | state and capture tests | ✅ |
| Query before hard delete still hides content | retention/hard-delete proof tests | ✅ |
| Hard delete mid-bisect purges history | tombstone skip/no-leak CLI test | ✅ |
| Timestamp predates substrate | recording-boundary and zero-hit search tests | ✅ |
| Future timestamp uses current state | state/context/MCP/update-policy tests | ✅ |
| Default retention keeps all history | unlimited retention test | ✅ |
| Configured cap prunes older history | capped retention and gap tests | ✅ |
| Fully offline recorded-time read | release-like proxy matrix and real CLI smoke | ✅ |

### Memory Bisect — 10/10

| Scenario | Passing runtime evidence | Result |
|---|---|---|
| Bisect starts with both bounds | immediate same-second unit and pure CLI regressions; both-bound availability matrix | ✅ |
| Repeatable bisect sequence | deterministic midpoint and composite-bound tests | ✅ |
| Step output is bounded | midpoint output excludes long/private content | ✅ |
| Step without a mark does not advance | repeated status equality in tests and CLI smoke | ✅ |
| Zero revisions in range | edge-case test | ✅ |
| Single revision in range | edge-case test | ✅ |
| Resume after interruption | durable-state test and CLI smoke | ✅ |
| Explicit restart clears state | reset test and CLI smoke | ✅ |
| Candidate hard-deleted mid-bisect | tombstone skip/no-content-leak test | ✅ |
| Fully offline bisect session | update-policy test and explicit proxy probe | ✅ |

**Compliance summary**: **29/29 scenarios pass.**

## Correctness and Design Coherence

| Area | Result | Evidence |
|---|---|---|
| Dedicated history ownership | ✅ | `observation_revisions` remains independent of sync outbox state. |
| Transactional update/delete capture | ✅ | local, bulk, pulled, rollback, retention, and purge tests pass. |
| Hard-delete proof and content purge | ✅ | state/context/bisect tests verify tombstone proof without historical leakage. |
| Historical search limitation | ✅ | current FTS candidates are rewritten at T and the limitation is disclosed. |
| Portable v2 isolation | ✅ | deterministic full graph is separate from the unchanged sync wire. |
| Atomic/idempotent import | ✅ | core, graph, procedures, invalid references, tombstones, and repeated import pass. |
| Bisect boundary ownership | ✅ | persisted nanosecond time plus initial max ID distinguishes pre-enable, `.000Z` compatibility, and backdated rows. |
| Bisect state durability/security | ✅ | locks, ABA generation, permissions, symlink checks, crash-safe replacement, absent dir, umask, and races pass. |
| Default-off/unlimited retention | ✅ | config and store tests pass. |
| Local-only surfaces | ✅ | release-like proxy matrices and CLI smoke pass. |

## TDD Compliance

`apply-progress.md` contains the seven-work-unit TDD evidence table and post-apply remediation evidence, including boundary remediation `24c0e73`. Current GREEN and triangulation evidence is independently confirmed. Original primary-work-unit raw RED transcripts were not retained; the artifact correctly labels those entries as **executor-report evidence** rather than presenting them as raw logs.

| Check | Result | Details |
|---|---|---|
| TDD evidence reported | ✅ | Seven work units plus remediation evidence are present. |
| All behavior tasks have tests | ✅ | 7/7 work units map to existing test files. |
| RED confirmed | ⚠️ | Tests and disclosed executor reports exist, but original primary-work-unit raw RED transcripts are unavailable. |
| Boundary-remediation RED/GREEN | ✅ | Apply evidence records the original, review, and tightened boundary RED cycles; fresh GREEN passes. |
| GREEN confirmed | ✅ | All mapped tests pass in focused, coverage, race, and isolated full execution. |
| Triangulation adequate | ✅ | Boundaries, invalidity, rollback, retention, tombstones, concurrency, privacy, and cross-surface cases exist. |
| Safety net | ✅ | Full, coverage, race, vet, build, cross-build, module, proxy, and CLI evidence is green. |

**TDD compliance**: **6/7 fully evidenced.** Missing original raw transcripts remain a process warning, not an archive blocker.

## Test Layers and Assertion Quality

| Layer | Evidence | Result |
|---|---|---|
| Unit | parsers, normalization, checksum, composite ordering, availability predicate | ✅ |
| Integration | SQLite store, MCP, HTTP, CLI state machine, sync-wire boundaries | ✅ |
| Runtime/E2E | built CLI same-second regression, recorded reads, portable round-trip, permissions, proxy behavior | ✅ |

Tests assert concrete values, errors, persistence, rollback, cardinality, ordering, output disclosure, permissions, network attempts, and absence of secret content. The immediate-boundary retry loop fails explicitly if it cannot construct its required same-second fixture; it cannot ghost-pass.

**Assertion quality**: ✅ All assertions verify real behavior.

## Changed-File Coverage

Fresh statement profiles were generated for each changed production package. Go does not report branch coverage.

| Changed file | Statement coverage | Rating |
|---|---:|---|
| `cmd/omnia/bisect.go` | 77.8% | ⚠️ Low |
| `cmd/omnia/main.go` | 80.7% | ⚠️ Acceptable |
| `internal/config/config.go` | 92.6% | ✅ Excellent |
| `internal/mcp/mcp.go` | 88.4% | ⚠️ Acceptable |
| `internal/server/server.go` | 70.1% | ⚠️ Low |
| `internal/store/portable_export.go` | 85.6% | ⚠️ Acceptable |
| `internal/store/portable_import.go` | 84.2% | ⚠️ Acceptable |
| `internal/store/store.go` | 78.0% | ⚠️ Low |
| `internal/store/timecontext.go` | 80.9% | ⚠️ Acceptable |
| `internal/store/timetravel.go` | 72.7% | ⚠️ Low |
| **Aggregate** | **80.0% (6422/8030)** | ⚠️ Acceptable |

The remediated `bisectBoundAvailable` helper is 100% covered and `BisectEvents` is 82.0% covered. Evidence: `/tmp/omnia-v032-final-reverify-changed-file-coverage.log`, `/tmp/omnia-v032-final-reverify-boundary-coverage.log`, and package profiles.

## Issues

### CRITICAL

None.

### WARNING

1. Original raw RED transcripts for the seven primary work units were not retained; apply progress truthfully provides executor-report evidence.
2. Four changed production files remain below 80% whole-file statement coverage, although the remediated boundary helper is 100% and `BisectEvents` is 82%.
3. The release-like near-future test is timing-sensitive under extreme verifier-induced CPU contention; isolated and repeated executions pass.
4. Full-tree formatting reports 21 pre-existing/unrelated Go files; changed Go files are clean and overlap is zero.
5. CLI help and runtime disclaimers exist, but external user documentation does not yet explain time-travel configuration, recorded reads, portable v2, or bisect.

### SUGGESTION

1. Replace the 350 ms wall-clock release-proxy assertion with a controllable clock or wider deterministic margin to eliminate load-dependent test behavior.

## Final Verdict

**PASS WITH WARNINGS — READY TO ARCHIVE.**

All 29 scenarios, current-state privacy boundaries, full coverage, isolated full-normal execution, relevant races, static checks, builds, cross-builds, module checks, offline proxy controls, and fresh real CLI flows pass at exact HEAD `eb259ce12f3b6cbea1aee8a7bd74a30b9f306457`.
