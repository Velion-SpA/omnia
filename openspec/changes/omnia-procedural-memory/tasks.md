# Tasks: omnia-procedural-memory (0.2 procedural slot)

## Review Workload Forecast

| Field | Value |
|-------|-------|
| Estimated changed lines | ~2000-2500 (additions+deletions) |
| 400-line budget risk | High |
| Chained PRs recommended | Yes |
| Suggested split | PR1→PR6 (see Work Units) |
| Delivery strategy | ask-on-risk |
| Chain strategy | pending |

Decision needed before apply: Yes
Chained PRs recommended: Yes
Chain strategy: pending
400-line budget risk: High

### Suggested Work Units

| Unit | Goal | Likely PR | Notes |
|------|------|-----------|-------|
| 1 | `procedures`/`procedures_fts` schema + store CRUD + governance (Phases 1-2) | PR1 | base=main; ~700-900 lines; independently GREEN |
| 2 | `internal/llm.ProcedureInducer` shell-out (Phase 3) | PR2 | base=PR1 branch; ~300-400 lines |
| 3 | Online induction on `mem_save` (Phase 4) | PR3 | base=PR2 branch; ~300-400 lines |
| 4 | SSGM `applied_procedure` feedback (Phase 5) | PR4 | base=PR3 branch; ~250-350 lines |
| 5 | Offline `procedure-induct` batch + CLI (Phase 6) | PR5 | base=PR4 branch; ~500-600 lines |
| 6 | Contrastive retrieval card + docs + full regression (Phase 7-8) | PR6 | base=PR5 branch; ~300-400 lines |

## Phase 1: Store Foundation (spec: procedures-store)

- [x] 1.1 [RED] `internal/store/procedures_test.go`: `UpsertProcedure` rejects unknown `polarity`/`postcondition_kind`
- [x] 1.2 `internal/store/store.go` `migrate()`: add `procedures` + `procedures_fts` `CREATE TABLE IF NOT EXISTS` (mirror `memory_relations`, L971-1002)
- [x] 1.3 `internal/store/procedures.go` (new): `Procedure`/`InducedProcedure` types, polarity/state/postcondition_kind consts + `isValid*` guards (mirror `validRelationVerbs`)
- [x] 1.4 `procedures.go`: `UpsertProcedure(p Procedure) (string, error)`, `sync_id` via `newSyncID("proc")` — GREEN 1.1
- [x] 1.5 [RED] `procedures_test.go`: `SearchProcedures` returns row by trigger match (real SQLite `:memory:`)
- [x] 1.6 `procedures.go`: `SearchProcedures(query, polarity, state string, limit int)` over `procedures_fts` (mirror `observations_fts` external-content pattern) — GREEN 1.5
- [x] 1.7 `internal/config/config.go`: `ProceduralConfig{Enabled, TrustThreshold, ConfidenceFloor, ReviewAfterDays}` + `applyDefaults` (mirror `RecallConfig`, L101-108/304-337; default `Enabled=false`, `TrustThreshold=3`)
- [x] 1.8 (added, not originally enumerated) `procedures.go`: `GetProcedure`/`ListProcedures` — the "list/get" half of the CRUD surface the PR1 assignment explicitly required, with dedicated tests

## Phase 2: Governance Gate (spec: governance-gate)

- [x] 2.1 [RED] `procedures_test.go`: 3rd `ConfirmReuse` on `reuse_confirmed=2` → `reuse_confirmed=3`, `state=trusted`
- [x] 2.2 `procedures.go`: `ConfirmReuse(syncID, actor string) (Procedure, error)` — UPVOTE, system provenance (mirror `JudgeBySemantic` actor="engram"/kind="system") — GREEN 2.1
- [x] 2.3 [RED] `procedures_test.go`: `Contradict` at floor → `state=retired`, row still queryable; idempotent re-retire
- [x] 2.4 `procedures.go`: `Contradict(syncID, actor string)` (DOWNVOTE) + `RetireProcedure` (never hard-delete) — GREEN 2.3
- [x] 2.5 [RED] `procedures_test.go`: unused `trusted` procedure past `review_after` flagged by `DecayProcedures`
- [x] 2.6 `procedures.go`: `DecayProcedures()` (reuse `review_after` spaced-repetition field) — GREEN 2.5

## Phase 3: Induction Shell-out (spec: induction, `internal/llm`)

- [x] 3.1 [RED] `internal/llm/procedure_test.go`: `Induce` parses valid JSON program via fake CLI (mirror `runner_test.go` `fakeRunner`)
- [x] 3.2 `internal/llm/procedure.go` (new): `ProcedureInducer` interface, `Induce(ctx, prompt) (InducedProcedure, error)`; implement on `ClaudeRunner`/`OpenCodeRunner` (mirror `Compare` in `claude.go`/`opencode.go`) — GREEN 3.1
- [x] 3.3 [RED] `procedure_test.go`: malformed JSON / model-set polarity → error (caller sets polarity, never the model, per design decision #2)
- [x] 3.4 `procedure.go`: deterministic template-extraction fallback when no runner/error — GREEN 3.3 (plus `SafeInduce`, the graceful-degradation wrapper PR2's mem_save wiring will call)

## Phase 4: Online Candidate Induction (spec: induction, `mem_save`)

- [x] 4.1 [RED] `internal/mcp/procedure_induction_test.go` (new): `outcome=worked` + fake inducer → `candidate` `playbook`, `source_obs_sync_ids` linked
- [x] 4.2 `internal/mcp/mcp.go` `MCPConfig` (L40-80): add `Procedural *ProceduralWiring` field (mirror `AutoEmbed *embed.Worker`)
- [x] 4.3 `mcp.go` `handleSave` (~L1447, after `Outcome` persisted): async best-effort induce call, log-and-swallow on error (mirror `FindCandidates` L1508-1512 / `enqueueAutoEmbed` non-blocking) — GREEN 4.1
- [x] 4.4 [RED] `procedure_induction_test.go`: no `Procedural`/no runner configured → save succeeds, no procedure, no error
- [x] 4.5 `cmd/omnia/main.go`: build `Procedural` wiring from `config.ProceduralConfig` when `enabled=true` — GREEN 4.4

## Phase 5: SSGM Reuse Attribution (spec: reuse-attribution)

- [x] 5.1 [RED] `procedure_induction_test.go`: `applied_procedure` + `outcome=worked` → `ConfirmReuse` invoked, counters update
- [x] 5.2 `mcp.go`: add `applied_procedure` arg to `mem_save`/`mem_update` schemas (~L417-421/458-459) + `handleSave`/`handleUpdate` wiring to `ConfirmReuse`/`Contradict` — GREEN 5.1
- [x] 5.3 [RED] `procedure_induction_test.go`: no `applied_procedure`, no session match → safe no-op, save unchanged
- [x] 5.4 `internal/mcp` (new helper): best-effort same-session `trigger`-vs-`error_signature` fallback match (mirror `resolveFallbackSessionID`) — GREEN 5.3

## Phase 6: Offline Batch Induction (spec: induction, `ScanProject` seam)

- [x] 6.1 [RED] `internal/store/scan_procedure_test.go` (new): fake `ProcedureInducer`; 3 `worked` obs sharing `error_signature` + 2 unrelated `did_not_work` → `--apply` yields exactly 2 candidates
- [x] 6.2 `internal/store/relations.go` or `procedures.go`: `InduceProject(opts InduceOptions) (InduceResult, error)` — cluster via `NormalizeErrorSignature`, reuse `ScanProject`'s worker-pool shape (L1340-1428 pairCh/wg/mu) — GREEN 6.1
- [x] 6.3 [RED] `scan_procedure_test.go`: no `--apply` → counts only, zero rows written
- [x] 6.4 `cmd/omnia/procedure.go` (new): `omnia procedure-induct [--project][--apply]` (mirror `cmdConflictsScan`, `conflicts.go` L302-476) — GREEN 6.3
- [x] 6.5 `cmd/omnia/procedure.go`: `procedure list/inspect/retire` subcommands (mirror `cmdConflictsList`/`Show`)
- [x] 6.6 `cmd/omnia/procedure_test.go` (new): flag parsing, `--apply` vs dry-run, no-LLM graceful skip (mirror `conflicts_test.go` `stubAgentRunnerFactory`)
- [x] 6.7 `cmd/omnia/main.go` (~L725): register `"procedure-induct"`/`"procedure"` dispatch case

## Phase 7: Contrastive Retrieval (spec: contrastive-retrieval)

- [x] 7.1 [RED] `internal/mcp/procedure_card_test.go` (new): matching query → one card, top trusted playbook + top trusted anti-playbook, confidence values
- [x] 7.2 [RED] `procedure_card_test.go`: only one polarity trusted → card has that polarity only, no fabricated pair
- [x] 7.3 `internal/mcp/recall_adapter.go`: `BuildProcedureCard(s, query, project, scope)` — picks top trusted each polarity via `SearchProcedures` (mirror `HydrateFusedResults`; `recall.Fuse` untouched/pure) — GREEN 7.1-7.2
- [x] 7.4 `mcp.go` `handleSearch` (~L1019) + `handleContext` (~L1810): attach card to envelope when `cfg.Procedural` enabled (mirror `extra["outcome"]` L1167)

## Phase 8: Backward-Compat, Sync Exclusion, Docs

- [x] 8.1 [RED] `mcp_test.go`: `procedural.enabled=false` + `outcome=worked` → response identical to pre-feature baseline
- [x] 8.2 [RED] `internal/store/store_test.go`: cloud sync payload builder never emits `procedures` rows (no `SyncEntityProcedure` const, no `enqueueSyncMutationTx` call from `procedures.go`)
- [x] 8.3 `docs/PROCEDURAL_MEMORY.md` (new): schema, polarity, governance lifecycle, retrieval card, forward-compat note to deferred compiler (#1592)
- [x] 8.4 Full regression: `CGO_ENABLED=0 go test ./...` GREEN, incl. signature-lane + conflict-semantic suites unchanged

## Also (PR1 review fix, applied in this PR2 batch)

- [x] `internal/store/procedures.go` `UpsertProcedure`: clamp the incoming `p.Confidence` via `clampConfidence` now that PR2's inducers feed real, caller-supplied values (regression test: `TestUpsertProcedure_ClampsOutOfRangeConfidence`)

## PR2 Status (this apply batch)

PR2 = Phases 4-8 (online induction, SSGM reuse feedback, offline batch + CLI, contrastive retrieval, backward-compat/docs/regression), COMPLETE. This closes out the omnia-procedural-memory change (0.2 procedural slot) and, per the delivery plan, the final of the six 0.2 slices.

New tests this batch: 4 (llm: `BuildInducePrompt`) + 1 (store: `clampConfidence` regression) + 4 (store: `InduceProject`) + 1 (store: sync-exclusion regression) + 12 (mcp: online induction + SSGM feedback + contrastive card + backward-compat) + 5 (cmd/omnia: `procedure-induct` + `procedure` CLI), all GREEN.

Verbatim: `CGO_ENABLED=0 go test ./internal/store/... ./internal/llm/... ./internal/mcp/... ./cmd/omnia/... -race` → `ok internal/store`, `ok internal/llm`, `ok cmd/omnia`; `internal/mcp` shows the SAME 10 pre-existing `unknown_project "engram"` failures verified against baseline commit `0e2fc6a` (zero new failures). `CGO_ENABLED=0 go build ./...` clean; `CGO_ENABLED=0 go test $(go list ./... | grep -v internal/mcp)` all `ok`. `gofmt -l` clean; `go vet` clean across all four touched packages.

Not committed (per instructions).
