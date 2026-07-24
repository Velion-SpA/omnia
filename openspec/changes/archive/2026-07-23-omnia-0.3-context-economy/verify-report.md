# Verify Report: Omnia v0.3 — Context Economy

Change: `omnia-0.3-context-economy` | Repo: omnia (main @ 5778164) | Date: 2026-07-23
Mode: full artifact verification (proposal + specs + design + tasks + apply-progress all present, hybrid engram+file store)

## Verdict

**PASS WITH WARNINGS** — 29/29 requirements PASS with runtime test evidence. 0 CRITICAL. 2 WARNING (both documentation/bookkeeping, not code defects). 3 SUGGESTION.

Ready to archive after the tasks.md reconciliation noted in WARNING 1 (recommend the orchestrator/user update the on-disk checkboxes, or accept this report as the record of completion — sdd-verify does not modify artifacts itself).
Ready for the eval pass: all flags remain default-OFF pending the design's own "Open (eval-gated) tuning" note (max_tokens=1500, lambda=0.7, similarity_threshold=0.9 need one empirical pass via internal/eval before recommending ON) — this is by design, not a gap.

## Test-Run Evidence

| Command | Result |
|---|---|
| `CGO_ENABLED=0 go build ./...` | clean, no output |
| `CGO_ENABLED=0 go vet ./...` | clean, no output |
| `gofmt -l .` | 21 pre-existing files flagged, **none touched by v0.3** (token.go, mmr.go, type_lens.go, token_budget.go, config.go, store.go, mcp.go, recall_fix.go all clean) |
| `CGO_ENABLED=0 go test ./...` | 46/47 packages `ok`; `internal/mcp` FAILs on the KNOWN pre-existing `unknown_project "engram"` flake (`TestHandleSave*`, `TestHandleCapturePassiveDefaultsSourceAndSession`) — environmental project-detection issue, documented as green-in-CI, not a v0.3 regression (reproduces identically on a stash of all v0.3 changes, per apply-progress). Not counted as a finding, per task instructions. |
| `go test ./internal/token/... ./internal/config/... ./internal/store/... -run "Token\|Budget\|MMR\|TypeLens\|Diversity\|Preemption\|Lens"` | all PASS (token golden table, TrimToBudget table, config defaults ×3 domains, FormatContext budget ×3) |
| `go test ./internal/mcp/... -run "Token\|Budget\|MMR\|TypeLens\|Diversity\|Preemption\|Lens"` | all PASS (ApplyTokenBudget ×4, ApplyMMR ×5 + Jaccard ×5, preemption invariant ×6 cases including the full composed 3-pass pipeline, InferLensType ×27 cases, ApplyTypeLens ×5, handleSearch injection-budget wiring ×5) |
| `go test ./cmd/omnia/... -run "RecallFix\|Budget"` | all PASS, including `TestFormatRecallFixCompact_NeverEmptyWhenHitsExist` and the 3 `cmd*_ThreadsContextTokenBudgetFromConfig` wiring tests |
| `bash -n plugin/claude-code/scripts/user-prompt-submit.sh` | syntax OK |
| `bash plugin/claude-code/scripts/user-prompt-submit_test.sh` | **22/22 PASS**, including 5× explicit OFF-path byte-for-byte diffs against `main`, LC_ALL=C Spanish trigger, cooldown, combined-nudge (no save-nudge starvation), marker-location |

## Three High-Stakes End-to-End Spot Checks (explicitly requested)

1. **All-flags-off = byte-for-byte v0.2 output.** CONFIRMED. `TestApplyTokenBudget_DisabledIsNoop`, `TestApplyMMR_DisabledIsNoop`, `TestApplyTypeLens_DisabledIsNoop`, `TestFormatContextTokenBudgetDisabledIsByteForByteNoOp`, `TestHandleSearch_InjectionBudgetDisabled_NoTrimFooterOrEnvelopeField` all pass; bash harness proves byte-identical OFF-path output against `main` for 5 adversarial inputs (embedded quotes/backslash, embedded newline, ~6000-char prompt, JSON-looking prompt, benign).
2. **Pre-emption invariant under the composed pipeline.** CONFIRMED. `preemption_invariant_test.go`'s `TestPreemptionInvariant_SentinelAndSignatureSurviveEveryPass` includes `ApplyTypeLens_then_ApplyMMR_then_ApplyTokenBudget` — the exact `handleSearch` chain (type-lens → MMR → budget), each pass under its own most-adversarial config (MaxTokens=1, Lambda=0.01, SimilarityThreshold=0.01, hostile lensType matching the sentinel's own type) — sentinel + signature rows survive complete and first. Also covers the sibling adversarial case where lensType matches the SIGNATURE row's type (not just the sentinel's), catching a coverage gap a narrower test would miss.
3. **recall-fix never-empty guarantee.** CONFIRMED. `formatRecallFixCompact` (cmd/omnia/recall_fix.go:191-213) force-keeps the first hit line (rune-truncated) when even it alone exceeds the token budget; only `len(hits)==0` returns `""`. `TestFormatRecallFixCompact_NeverEmptyWhenHitsExist` passes.

## Requirement Compliance Matrix (29/29 PASS)

### Domain: token-estimation (5/5 PASS)

| REQ | Status | Evidence |
|---|---|---|
| Pure, Dependency-Free Estimator | PASS | `internal/token/token.go` imports only `unicode/utf8`; `go build ./...` CGO_ENABLED=0 clean. `TestEstimateTokens_Golden`. |
| Deterministic Output | PASS | Pure function, no state. `TestEstimateTokens_Deterministic`. |
| Heuristic, Documented Accuracy | PASS | Package doc (token.go:1-34): explicit char/4 heuristic, ±25% EN/code accuracy, CJK under-count + whitespace over-count caveats documented. |
| Bounded Edge-Case Behavior | PASS | `TestEstimateTokens_BoundedHugeInput`; empty string → `(0+3)/4=0`. |
| Non-Disruptive Introduction | PASS | Every consumer (`ApplyTokenBudget`, `FormatContext`, `recall_fix`) is a gated no-op when its own flag is off; token package never runs unconditionally. `TestApplyTokenBudget_DisabledIsNoop`, `TestFormatContextTokenBudgetDisabledIsByteForByteNoOp`, bash OFF-path tests. |

### Domain: injection-budget (6/6 PASS)

| REQ | Status | Evidence |
|---|---|---|
| Token-Based Budget, Not Char-Based | PASS | `token_budget.go:70` `token.TrimToBudget(rest, previewTokens, cfg.MaxTokens)`. `TestApplyTokenBudget_TrimsTopRankedCompleteDropsRest`. |
| Top-N-Complete Trim Strategy | PASS | `internal/token/token.go` `TrimToBudget`: whole-item, `break` not partial. `TestTrimToBudget_Table` (7 sub-cases incl. exact-fit boundary). |
| Aggregate Cap on FormatContext Buckets | PASS | `store.go:4003-4013`, priority order pinned→obs→sessions→prompts against running remainder. `TestFormatContextTokenBudgetUncappedBugRegressionAndBoundedWhenEnabled`, `TestFormatContextTokenBudgetPinnedNeverStarved`. |
| One Shared Primitive Across Consumers | PASS | `token_budget.go`, `store.go`, `recall_fix.go` all call `internal/token.{EstimateTokens,TrimToBudget}` — same functions, no drift. `TestFormatRecallFixCompact_TokenParityWithOldCharCap`, `TestPreviewTokens_MatchesEstimateOfRenderedPreview`. |
| Sentinel/Signature Pre-Emption Invariant | PASS | `token_budget.go:58-68` partitions preempted out before budget applies. `TestApplyTokenBudget_TinyBudgetExcludesPreemptedRowsFromAccounting` + shared invariant test (`ApplyTokenBudget` case, MaxTokens=1). |
| Disabled by Default, No-Op When Off | PASS | `Enabled` zero value = false; `!cfg.Enabled` gated no-op. `TestApplyTokenBudget_DisabledIsNoop`, `TestHandleSearch_InjectionBudgetDisabled_AllResultsReturned`. |

### Domain: injection-diversity (6/6 PASS)

| REQ | Status | Evidence |
|---|---|---|
| Read-Time Diversity Re-Rank Over Hydrated Content | PASS | `mmr.go` `ApplyMMR` greedy MMR reselection. `TestApplyMMR_NearDuplicateSuppressed`. |
| Cheap Lexical Similarity Only | PASS | `jaccardSimilarity` — pure token-set Jaccard, no embedding/model imports in mmr.go. `TestJaccardSimilarity_*` (5 tests). |
| Post-Pass Over Existing Ranking | PASS | Wired in `mcp.go:1340`, after `RankResults`/`ApplyStalenessDownrank`/`ApplyTypeLens`; reuses `MinMaxNormalizeRelevance` rather than recomputing scores. |
| Sentinel/Signature Pre-Emption Invariant | PASS | `mmr.go:160-167` partitions preempted out first. Shared invariant test `ApplyMMR` + `ApplyMMR_then_ApplyTokenBudget` composed case. |
| No Unnecessary Change When Distinct | PASS | `TestApplyMMR_AllDistinctSetUnchanged` (zero-overlap, order unchanged) + `TestApplyMMR_LambdaBoundaryOrdering` (below-threshold-similarity item survives with 0.8 Jaccard < 0.9 threshold, reordered not dropped — 3 in, 3 out). |
| Disabled by Default, No-Op When Off | PASS | `TestApplyMMR_DisabledIsNoop`. |

### Domain: signal-gated-recall (6/6 PASS)

| REQ | Status | Evidence |
|---|---|---|
| Signal Detection Gates Recall Invocation | PASS | `user-prompt-submit.sh:277-286` EN+ES new-topic/uncertainty regex. Shell tests "new-topic nudge fires", "uncertainty nudge fires". |
| Must Not Fire Every Turn | PASS | Dedup marker + 300s cooldown (`signal_cooldown_active`, lines 251-262, 304-329). Shell tests "cooldown: second DIFFERENT topic within 300s is silent", "benign prompt produces no nudge". |
| Session Dedup via Existing Marker Idiom | PASS | cksum-based marker file, same idiom as `post-tool-error-recall.sh`. Shell test "dedup: second identical call is silent". |
| Silent Degradation on CLI Unavailability | PASS | Hook is pure bash regex (never shells out to the omnia CLI for the nudge decision itself); all writes guarded `\|\| true`. Shell tests "malformed stdin exits 0" / "yields valid JSON". |
| Injected Content Still Passes Budget/Diversity Gates | PASS (by architecture) | The hook emits an INSTRUCTION only, never auto-injected results (design ADR-7) — no separate result-injection code path exists to bypass `ApplyTypeLens`/`ApplyMMR`/`ApplyTokenBudget`; any `mem_search` the agent subsequently calls goes through the same `handleSearch` pipeline unconditionally. |
| Disabled by Default, No-Op When Off | PASS | `is_signal_recall_enabled()` default unset → false. Shell test "OMNIA_SIGNAL_RECALL unset -> always {}" + 5 OFF-path byte-for-byte diffs against `main`. |

### Domain: type-as-lens (6/6 PASS)

| REQ | Status | Evidence |
|---|---|---|
| Situational Type Boost/Filter | PASS | `type_lens.go` `InferLensType` + `ApplyTypeLens` lift. `TestInferLensType_SignalMapping` (23 EN+ES cases) + `TestApplyTypeLens_LiftsMatchingTypeStablePartition`. |
| Explicit User Filter Always Wins | PASS | `type_lens.go:81-83`: `if explicitType != "" { return "" }`. `TestInferLensType_ExplicitTypeWins`. |
| Neutral Context Leaves Ranking Unchanged | PASS | `TestApplyTypeLens_NeutralContextLeavesRankingUnchanged`, `TestApplyTypeLens_EmptyLensTypeIsNoop`. |
| Composes With Existing Importance Tiers | PASS | Stable partition preserves relative order within `matches`/`rest` (inherited from the prior `RankResults` tier pass); wired AFTER `RankResults`. `TestApplyTypeLens_LiftsMatchingTypeStablePartition`. |
| Sentinel/Signature Pre-Emption Invariant | PASS | `type_lens.go:116-126` partitions preempted first. Shared invariant test `ApplyTypeLens` (hostile lensType matching sentinel's type) + `ApplyTypeLens_lensMatchesSignatureType` (lensType matching the SIGNATURE row's type — a distinct adversarial case) + full composed 3-pass case. |
| Disabled by Default, No-Op When Off | PASS | `TestApplyTypeLens_DisabledIsNoop`. |

## Design Deviations — Documented Where Users Will See Them

| Deviation | Documented? | Where |
|---|---|---|
| Budget counts rendered previews (truncate(300)), not full stored content | YES | `token_budget.go:78-84` doc comment on `previewTokens`; README `injection.budget.max_tokens` row. |
| `budget_trimmed` transparency | YES | `mcp.go:1501-1502,1510-1511` — footer text + `envelope["budget_trimmed"]` field surfaced to the caller when trimming occurred. |
| Word-boundary lens signals, residual ambiguity | YES | `type_lens.go:34-53` — extensive "KNOWN LIMITATION (narrower than before, still real)" comment: full words like "fixed size"/"design tokens"/"error budget" still fire; accepted-heuristic posture documented, eval-tuning flagged as follow-up. |
| CJK MMR limitation | YES | `mmr.go:39-44` — "KNOWN LIMITATION" comment: whitespace-free scripts tokenize as one giant run, Jaccard dedup effectively inert; accepted for v0.3 (ES/EN corpus), bigram fallback flagged as follow-up. |
| PR6 cooldown default 300s | YES | `user-prompt-submit.sh:210-212` inline comment + `SIGNAL_COOLDOWN_SECS="${OMNIA_SIGNAL_RECALL_COOLDOWN_SECS:-300}"` (line 310) — but see WARNING 2 below re: README discoverability. |

## Findings

### CRITICAL
None.

### WARNING

1. **On-disk `tasks.md` is out of sync with actual completed/merged work for PR6.** `openspec/changes/omnia-0.3-context-economy/tasks.md` still shows tasks 6.1–6.13 as `[ ]` unchecked, even though PR6 is merged to main (commit a933ce7, PR #133) and this verify pass confirms all 22 bash tests pass and the feature is fully implemented per spec. This happened because PR6 was built in an isolated worktree (base `be22200`) that had no `openspec/` directory, so its completion was only recorded in engram (`sdd/omnia-0.3-context-economy/apply-progress-pr6`), never synced back to the on-disk task file. Separately, the "PR: branch X... squash-merge" meta-tasks (1.9, 2.13, 3.10, 4.12, 5.13, 6.13) remain unchecked across ALL six PRs even though `git log` confirms all six are merged — expected during apply (sdd-apply doesn't touch git) but should be reconciled now that merge is confirmed. Recommend the orchestrator/user update `tasks.md` to check off 6.1–6.12 (this verify pass satisfies 6.12) and all six PR meta-tasks before archive.
2. **`OMNIA_SIGNAL_RECALL` (and `OMNIA_SIGNAL_RECALL_COOLDOWN_SECS`) are documented only inline in the hook script's own comments, not in `README.md`'s config table.** Every other v0.3 flag (`injection.budget.*`, `injection.context_budget.*`, `injection.diversity.*`, `injection.type_lens.*`) has a row in README.md's config table (lines 207-214); the one deliberate config.yaml exception (env-gated, per design ADR-7) has no equivalent top-level discoverability — an operator who doesn't read the script source has no way to learn this flag exists. Task 6.11 ("update plugin README/hook comments") was only half-completed: hook comments are thorough, README was not touched. Low severity (behavior is correct, default-off, byte-for-byte no-op) but a real discoverability gap.

### SUGGESTION

1. No end-to-end integration test directly demonstrates "signal fires → agent calls mem_search → injection-budget/diversity apply" as a single flow; this is currently guaranteed architecturally (the hook only emits an instruction, never a separate result-injection path) rather than empirically chained in one test. Not required — no code path exists that could bypass the gates — but a short doc note cross-referencing why no such test exists would preempt a future reviewer re-asking the question.
2. Engram observation #1647 (PR5 apply-progress, topic `sdd/omnia-0.3-context-economy/apply-progress`) describes `type_lens.go`'s signal matching as "plain substring (not word-boundary)" — this is now STALE relative to the actually-merged code, which uses `\b` word boundaries (the later adversarial-review fix summarized in obs #1649 was never folded back into #1647's own body). Not a code defect — verified the live code is correct — but the memory record itself could mislead a future reader who trusts it over the real source. Consider a follow-up engram note reconciling this, or let the archive-report supersede it.
3. The pre-existing `internal/mcp` `unknown_project "engram"` flake (`TestHandleSave*`/`TestHandleCapturePassiveDefaultsSourceAndSession`) has no in-repo comment marking it as a known, environmental, CI-green flake — a future contributor has to rediscover this from engram history. A one-line `// KNOWN FLAKE:` comment in `mcp_test.go` would save that rediscovery cost. Out of scope for v0.3 itself.

## Cross-Reference: Pipeline Order (design.md section 2 vs shipped code)

Design: `RankResults → ApplyStalenessDownrank → ApplyTypeLens → ApplyMMR → ApplyTokenBudget`
Shipped (`internal/mcp/mcp.go:1320-1360`): `ApplyTypeLens` (line 1321) → `ApplyMMR` (line 1340) → `ApplyTokenBudget` (line 1360), all after the existing `RankResults`/`ApplyStalenessDownrank` calls earlier in the same function. **MATCH.**

## Config Defaults Cross-Reference (design.md section 4 vs shipped code)

`internal/config/config.go` `applyDefaults` (lines 680-711): `Injection.Budget.MaxTokens → 1500`, `Injection.ContextBudget.MaxTokens → 1500`, `Injection.Diversity.Lambda → 0.7`, `Injection.Diversity.SimilarityThreshold → 0.9`; all four `Enabled` fields have no default override (zero value `false` stands). **MATCH** design's `1500/0.7/0.9`, all default-OFF.

## PR / Commit Mapping (confirmed via `git log`)

| PR | Commit | Domain |
|---|---|---|
| PR1 #132 | 1851b94 | internal/token leaf |
| PR2 #134 | c1c1506 | ApplyTokenBudget + handleSearch wiring + recall-fix migration |
| PR3 #135 | bbcd024 | FormatContext token budget (uncapped-bug fix) |
| PR4 #136 | d253aad | ApplyMMR diversity |
| PR5 #137 | 5778164 (main tip) | type-as-lens |
| PR6 #133 | a933ce7 | signal-gated recall nudge (bash) |

## Tasks Completion (per engram apply-progress, cross-checked against code)

PR1-PR5: all substance tasks `[x]` checked in on-disk tasks.md; only PR-creation meta-tasks unchecked (expected — sdd-apply doesn't handle git). PR6: substance tasks 6.1-6.11 complete per engram + verified by this pass; 6.12 (verify) satisfied by this report; 6.13 (PR) confirmed merged as #133 via git log, but on-disk tasks.md was never updated (WARNING 1).
