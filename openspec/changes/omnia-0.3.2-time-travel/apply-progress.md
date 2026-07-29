# Apply Progress: Omnia v0.3.2 — Time Travel

## Completed Work

- [x] 1.1–1.3 — deterministic portable v2 export envelope and verification.
- [x] 2.1–2.3 — transactional, idempotent core import and verification.
- [x] 3.1–3.3 — validated, atomic relations and anchors import and verification.
- [x] 4.1–4.3 — procedures and portable archive delivery across store, CLI, server, and sync boundaries.
- [x] 5.1–5.3 — local history substrate, retention/purge behavior, and bulk/pulled revision capture.
- [x] 6.1–6.3 — recorded-state queries, recorded-time search/context, and CLI exposure.
- [x] 7.1–7.3 — exact recorded-event bounds, resumable bisect CLI/state machine, durable process-safe state, and verification.

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
Final validated HEAD: `2ac186e1362b22efc40ad615c8b9d215a183b088`

## Validation Evidence

- Focused recorded-time and bisect tests, store race tests, focused MCP/CLI race tests, `go vet`, formatting, module tidy diff, whitespace checks, and commit-hygiene checks passed at the final integration HEAD.
- Affected-package coverage passed with 81.8% total; profile: `/tmp/omnia-v032-final-coverage.out`.
- Exact validated arm64 binary: `/tmp/omnia-v032-final-checkpoint-bin`.
- Real isolated recorded-time and bisect CLI smoke passed (`SMOKE_RESULT=PASS`); transcript: `/tmp/omnia-v032-final-smoke.log`.
- Smoke workspace: `/tmp/omnia-v032-final-smoke.9EyUc1` (also recorded in `/tmp/omnia-v032-final-smoke-dir.txt`). The smoke proved live versus `--as-of` state, bisect start/status/convergence/reset, and database mode `0600`.
- Full `go test ./...` remains affected only by 10 known unrelated `internal/mcp` fixture failures that hardcode project `engram` while the checkout is detected as project `omnia`; these predate and are outside this change. Exact failures: `TestHandleSaveSuggestsTopicKeyWhenMissing`, `TestHandleSaveFallsBackToManualSaveWhenNoActiveSession`, `TestHandleSaveWithNilActivityStillSucceeds`, `TestHandleSavePromptCaptureFailureIsNonFatal`, `TestHandleSavePromptFeedsAutoCaptureContext`, `TestHandleSaveCapturePromptFalseSkipsCurrentPrompt`, `TestHandleSaveNoCurrentPromptStillSucceeds`, `TestHandleSaveDoesNotSuggestWhenTopicKeyProvided`, `TestHandleCapturePassiveDefaultsSourceAndSession`, and `TestHandleSaveReturnsLifecycleState`. Reproduction log: `/tmp/omnia-v032-final-mcp-known-failures.log`.
- Full `cmd/omnia` package coverage exits 1 even though all 571 tests report passing. The same package-level exit reproduces at baseline `b13d359`, so it is pre-existing. Evidence: `/tmp/omnia-v032-cmd-cover.log`, `/tmp/omnia-v032-baseline-cmd-cover.log`, and the enumerated passing tests in `/tmp/omnia-v032-cmd-cover-terminal.txt`.

## Remaining Tasks

None in apply: all tasks 1.1–7.3 are complete.

## Status

21 of 21 apply tasks complete. The change is ready for the separate SDD verify phase. Verification and archive phases have not been claimed or marked complete.
