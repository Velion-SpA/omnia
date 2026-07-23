# Tasks: omnia-structural-forgetting (living memory, cheap code anchor)

## Apply Progress

- **PR1 (write/anchor side) — DONE** on branch `feat/structural-forgetting-anchor`: Phase 1 (`internal/anchor` leaf), Phase 2 (`memory_anchors` schema + CRUD + reserved `AnchorProvider` port), Phase 3 (`mem_save` `code_anchors` capture). All 25 PR1 tasks below are checked off. `go test ./internal/anchor/... ./internal/store/... ./internal/mcp/... -race` is GREEN modulo the pre-existing `internal/mcp` engram-project-detection flake (baseline `8a60e71`, unchanged — see apply-progress engram artifact for the exact diff evidence). `CGO_ENABLED=0 go build ./...` GREEN. Not committed (per apply instructions).
- **PR2 (reconcile/read side) — Phase 6-7 DONE, Phase 4-5 implemented but not fully task-checked** on branch `feat/structural-forgetting-scan`: Phase 4 (`ScanProject{Source:anchor}` travel/staleness) and Phase 5 (`omnia forget-scan` CLI) are implemented and exercised by `internal/store/scan_anchor_test.go`, but Phase 5's own dedicated CLI test file (`cmd/omnia/forget_scan_test.go`, tasks 5.1/5.4/5.5) was never written — left unchecked deliberately rather than marking tasks done without their named test coverage. Phase 6 (recall stale downrank + receipt) and Phase 7 (flag/regression/docs) are complete and checked off below. Fixed a mid-edit compile break left by a prior cut-off session (`BuildResultReceipt`/`BuildReceipt` callers in `mcp.go` and `cmd/omnia/main.go` hadn't been updated for the new trailing `stalenessPenalty float64` parameter) plus a `go vet` failure in `anchor_downrank_test.go`/`recall_ranking_test.go` (promoted-field struct literals / stale call arity). `go test ./internal/store/... ./internal/anchor/... ./internal/mcp/... ./cmd/omnia/... -race` GREEN except the pre-existing `internal/mcp` `unknown_project("engram")` flake (confirmed identical on baseline `7b52380`). `CGO_ENABLED=0 go build ./...` GREEN, `gofmt`/`go vet` clean. Not committed (per apply instructions).

## Review Workload Forecast

| Field | Value |
|-------|-------|
| Estimated changed lines | ~2200-2900 (new pkg + schema + 3 wiring points + CLI + tests) |
| 400-line budget risk | High |
| Chained PRs recommended | Yes |
| Suggested split | PR1 anchor leaf -> PR2 store schema/CRUD -> PR3 mem_save capture -> PR4 ScanProject anchor source (travel/stale) -> PR5 forget-scan CLI -> PR6 recall downrank/receipt |
| Delivery strategy | ask-on-risk |
| Chain strategy | pending |

Decision needed before apply: Yes
Chained PRs recommended: Yes
Chain strategy: pending
400-line budget risk: High

### Suggested Work Units

| Unit | Goal | Likely PR | Notes |
|------|------|-----------|-------|
| 1 | `internal/anchor` leaf: injectable git shell-out, parsing, degradation | PR1 | No wiring; standalone, fully unit-testable |
| 2 | `memory_anchors` schema + `internal/store/anchors.go` CRUD | PR2 | Depends on PR1 (port shape only) |
| 3 | `mem_save` `code_anchors` capture | PR3 | Depends on PR1+PR2 |
| 4 | `ScanProject{Source:anchor}` travel/staleness (highest risk+value) | PR4 | Depends on PR2; AnchorProvider port |
| 5 | `omnia forget-scan` CLI | PR5 | Depends on PR4 |
| 6 | recall stale downrank + receipt at mcp boundary | PR6 | Depends on PR2 only; can parallel PR4/5 |

## Phase 1: internal/anchor leaf (git shell-out, TDD) — PR1 DONE

- [x] 1.1 [TEST] `internal/anchor/anchor_test.go`: fake `runGit` parses `git blame -L --porcelain` -> BlameRange{SHA,Lines}
- [x] 1.2 `internal/anchor/anchor.go`: `Probe` struct, injectable `runGit(ctx,dir,args)([]byte,error)` field, `defaultRunGit` via `os/exec` (mirrors `internal/llm/claude.go` runCLI pattern)
- [x] 1.3 [TEST] `HeadSHA()` (`git rev-parse HEAD`) + `RangeHash()` (normalized content sha256) fixtures
- [x] 1.4 `anchor.go`: implement `Blame`, `RangeHash`, `HeadSHA`, repo root via `git rev-parse --show-toplevel`
- [x] 1.5 [TEST] no-git-binary / not-a-repo: `Capture` returns sentinel error, never panics
- [x] 1.6 `anchor.go`: `Capture(file,symbol,lineStart,lineEnd)` orchestrates Blame+RangeHash+HeadSHA; graceful degradation
- [x] 1.7 [TEST] `Locate(symbol)` relocation fixture: same body, new line range -> new BlameRange
- [x] 1.8 `anchor.go`: `Locate(symbol)` via git shell-out; symbol-not-found path
- [x] 1.9 [TEST] materiality helper: hash-equality + non-whitespace line-delta >= threshold (pure, table-driven)

## Phase 2: internal/store schema + anchor CRUD (TDD) — PR1 DONE

- [x] 2.1 `internal/store/store.go` `migrate()`: additive `CREATE TABLE IF NOT EXISTS memory_anchors` (id, sync_id UNIQUE, obs_sync_id, repo_root, file_path, symbol, line_start, line_end, blame_sha, blame_at, content_hash, anchor_status, created_at, checked_at, staled_at) + `idx_anchor_obs`, `idx_anchor_status` (appended at the end of `migrate()`, mirrors recall-reliability/provenance additive convention)
- [x] 2.2 [TEST] `internal/store/anchors_test.go`: `UpsertAnchor` inserts active row keyed by obs sync_id
- [x] 2.3 `internal/store/anchors.go`: `UpsertAnchor` (`newSyncID("anc")`)
- [x] 2.4 [TEST] `ListActiveAnchors(project)` returns only `anchor_status='active'`
- [x] 2.5 `anchors.go`: `ListActiveAnchors`
- [x] 2.6 [TEST] `UpdateAnchorRange` (travel) updates line_start/line_end/blame_sha/content_hash/checked_at
- [x] 2.7 `anchors.go`: `UpdateAnchorRange`
- [x] 2.8 [TEST] `MarkAnchorStale` idempotency: sets `anchor_status='stale'`, `staled_at`, `obs.review_after=now()`
- [x] 2.9 [TEST] `MarkAnchorStale` writes `supersedes` relation row ONLY when a newer memory exists; no row otherwise
- [x] 2.10 `anchors.go`: `MarkAnchorStale(syncID, newerObsSyncID *string)` — mirrors `JudgeBySemantic` provenance (`marked_by_kind='system'`, `marked_by_actor='engram'`)
- [x] 2.11 `internal/store/relations.go`: add `AnchorProvider` port (duck-typed, mirrors `SemanticRunner` in `runner.go`) — no import of `internal/anchor`. Declared now as a reserved seam (`AnchorRecheckResult` + `AnchorProvider`), NOT yet wired into `ScanOptions`/`ScanProject` — that wiring is PR2 (Phase 4).

## Phase 3: mem_save code_anchors capture (TDD) — PR1 DONE

- [x] 3.1 [TEST] `internal/mcp/mcp_anchor_capture_test.go`: `mem_save` with `code_anchors:[{file,symbol,line_start,line_end}]` persists linked `memory_anchors` row
- [x] 3.2 `internal/mcp/mcp.go` `handleSave`: parse `code_anchors` arg, call capture helper post-`AddObservation`; log+swallow errors (mirrors `FindCandidates` non-fatal pattern)
- [x] 3.3 [TEST] `mem_save` WITHOUT `code_anchors` unaffected (byte-identical existing behavior)
- [x] 3.4 [TEST] no-git / not-a-repo -> save succeeds, no anchor row, no error surfaced
- [x] 3.5 `internal/mcp/anchor_adapter.go` (new): `AnchorCaptureAdapter` wraps an `AnchorCapturer` port (satisfied by `*anchor.Probe`) -> `store.UpsertAnchor` params (keeps `internal/store` git-agnostic)

## Phase 4: ScanProject anchor source — travel before staling (TDD, HIGHEST VALUE/RISK)

- [ ] 4.1 `relations.go`: `CandidateSource` type (`fts`|`embedding`|`anchor`), `ScanOptions.Source` (default `fts` preserves Phase3/4 exactly), `ScanResult` += `AnchorsChecked/AnchorsTraveled/AnchorsStaled`
- [ ] 4.2 [TEST] `internal/store/scan_anchor_test.go`: unchanged anchor (fake `AnchorProvider` same SHA/hash) -> skipped, stays active, `AnchorsChecked++`
- [ ] 4.3 `relations.go`: `ScanProject` early-branches when `Source=="anchor"`: `ListActiveAnchors` -> worker pool (reuse existing concurrency/wg machinery) calls `AnchorProvider.Recheck`
- [ ] 4.4 [TEST] **TRAVEL**: refactor-moved function (same content-hash, new range) -> `UpdateAnchorRange` called, NOT `MarkAnchorStale`, `AnchorsTraveled++`
- [ ] 4.5 `relations.go`: classify Recheck result Unchanged/Traveled/Changed; Traveled always attempted BEFORE staleness eval (REQ-004 ordering)
- [ ] 4.6 [TEST] symbol-not-found + changed + material -> `MarkAnchorStale`, `AnchorsStaled++`, `review_after` set
- [ ] 4.7 [TEST] changed but non-material (whitespace-only) -> skipped, stays active
- [ ] 4.8 [TEST] `ScanProject{Source:anchor}` counters consistent across a mixed batch (REQ-008)
- [ ] 4.9 [TEST] stale-with-newer-memory writes supersedes row; stale-without does not (delegates to 2.9)

## Phase 5: cmd/omnia forget-scan CLI (TDD)

- [ ] 5.1 [TEST] `cmd/omnia/forget_scan_test.go`: `omnia forget-scan --project P` dry-run reports checked/traveled/staled, no writes
- [ ] 5.2 `cmd/omnia/forget_scan.go` (new): `cmdForgetScan(cfg)` mirrors `cmdConflictsScan` flags (`--project --repo --semantic --apply`, dry-run default)
- [ ] 5.3 `cmd/omnia/main.go`: add `case "forget-scan": cmdForgetScan(cfg)` (~line 726, beside `"conflicts"`)
- [ ] 5.4 [TEST] not-a-repo/no-git -> reports skipped gracefully, exit 0
- [ ] 5.5 [TEST] `--semantic` gate reuses `resolveAgentRunner()` cost-confirm flow (mirrors `cmdConflictsScan`)
- [ ] 5.6 `forget_scan.go`: wire `anchor.NewProbe()` into `store.ScanOptions{Source:"anchor", AnchorProvider:...}` via adapter

## Phase 6: recall stale downrank + receipt (TDD, mcp wiring boundary only)

- [x] 6.1 [TEST] `internal/mcp/anchor_adapter_test.go`: penalty function reduces score deterministically (pure; `internal/recall.Fuse` untouched) — implemented as `internal/mcp/anchor_downrank_test.go`'s `TestStalenessPenaltyFor_*` table (StalenessPenaltyFor/DefaultStalenessPenalty live in `anchor_adapter.go`, next to PR1's capture code, rather than a separate file)
- [x] 6.2 `anchor_adapter.go`: downrank fn + `BuildStaleReceipt` -> "anchor <file>:<lines> changed <old->new sha>"
- [x] 6.3 [TEST] `mem_search`: stale-anchored memory ranks below equally-relevant fresh one; receipt line present (`TestHandleSearch_StructuralForgettingEnabled_StaleMemoryDownrankedWithReceipt`)
- [x] 6.4 `mcp.go` `handleSearch`: after `RankResults` and the plain `s.Search` branch, batch-load anchor status via `GetAnchorsForObservations`, re-sort via `ApplyStalenessDownrank`, append `anchor_receipt` to text+structured entry, gated behind `cfg.StructuralForgetting.Enabled`
- [x] 6.5 [TEST] memory with no anchor unaffected (regression) — `TestApplyStalenessDownrank_EmptyAnchorsMap_NoOp` + `TestHandleSearch_StructuralForgettingDisabled_NoDownrankNoReceipt`

## Phase 7: Regression / flag / docs

- [x] 7.1 `internal/config/config.go`: add `structural_forgetting.enabled` (default **false**, not true — backward-compatible default chosen over the original plan so a fresh install/upgrade sees zero behavior change; deviation noted) gating only recall downrank (capture and `omnia forget-scan` always run regardless of this flag)
- [x] 7.2 Full `go test ./internal/store/... ./internal/anchor/... ./internal/mcp/... ./cmd/omnia/... -race` — GREEN except the pre-existing `internal/mcp` `unknown_project("engram")` flake (10 tests; confirmed identical failure set on baseline `7b52380` via `git stash -u` A/B, zero new failures); all Phase 3/4 conflict-detection tests unchanged
- [x] 7.3 `CGO_ENABLED=0 go build ./...` GREEN (cgo-free verdict); `gofmt -l` / `go vet ./...` clean on all touched files
