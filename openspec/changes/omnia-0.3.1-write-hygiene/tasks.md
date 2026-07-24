# Tasks: Omnia v0.3.1 — Write Hygiene

Decision needed before apply: No
Chained PRs recommended: Yes
Chain strategy: stacked-to-main
400-line budget risk: Low (post-split — see full forecast at bottom)

Delivery: 11 chained PRs, stacked-to-main, squash-merge, issue-first governance
(approved issue + `Closes #N` + one `type:*` label per PR, conventional commits,
no AI attribution). STRICT TDD throughout: every group opens with RED tasks
(`CGO_ENABLED=0 go test ./...` must fail first, for the right reason) before
GREEN/REFACTOR. Runner: `CGO_ENABLED=0 go test ./...`.

Design slices 3 (store-core) and 7 (dedupe) were flagged as budget risks and
are each split into two chained PRs (3→PR3/PR4, 7→PR8/PR9) — 9 design slices
→ 11 PRs.

**Spec clarification carried into PR9**: design's open question on dedupe
`--apply` scope is RESOLVED (user decision, obs #1663/#1665 context): `--apply`
takes an explicit cluster id ONLY. The spec's parenthetical "(or explicit
'all')" is superseded — no `--apply all` flag is implemented. sdd-verify
should check this against the narrower behavior, not the original spec text.

---

## PR1 — `internal/similarity` leaf (refactor, no behavior change)

Design slice 1 · Branch `refactor/similarity-leaf` · Base: `main` ·
`type:refactor` · Depends on: none · Capability: infra for write-gate

- [x] 1.1 RED: add `internal/similarity/similarity_test.go` porting the exact
      Jaccard/Tokenize cases currently in `internal/mcp/mmr_test.go` (empty-set→0,
      punctuation/case handling) against not-yet-existing `similarity.Tokenize`/
      `similarity.Jaccard` — confirm compile failure.
- [x] 1.2 GREEN: create `internal/similarity/similarity.go` exporting
      `Tokenize(s string) []string` and `Jaccard(a, b []string) float64`, moved
      verbatim from `internal/mcp/mmr.go` (`tokenizeForSimilarity`/`jaccardSimilarity`,
      regexp `[\p{L}\p{N}]+`, empty-set→0 rule preserved byte-for-byte).
- [x] 1.3 REFACTOR: modify `internal/mcp/mmr.go` to delete the local
      `tokenizeForSimilarity`/`jaccardSimilarity` copies and call
      `similarity.Tokenize`/`similarity.Jaccard` instead.
- [x] 1.4 Kill-switch/regression check: run existing `internal/mcp/mmr_test.go`
      unchanged — MUST still pass byte-for-byte (this file IS the regression
      net for D1; no MMR test edits in this PR).

Files: `internal/similarity/similarity.go` (new), `internal/similarity/similarity_test.go` (new), `internal/mcp/mmr.go` (modify).
Est. lines: ~180 (± regression net `mmr_test.go` untouched).

---

## PR2 — config: `write_hygiene` block + budget default 400

Design slice 2 · Branch `feat/write-hygiene-config` · Base: `main` (after PR1) ·
`type:feature` · Depends on: PR1 (none functionally, sequential base only) ·
Capability: write-gate (config surface), injection-budget

- [x] 2.1 RED: add `internal/config/config_test.go` cases: (a) unset
      `write_hygiene` block → `Enabled=true`, `NoopThreshold=0.98`,
      `UpdateThreshold=0.9`, `ShrinkGuard=0.9`, `CandidateLimit=10`; (b) explicit
      `enabled: false` sticks; (c) unset `injection.budget.max_tokens` →
      `400`; (d) explicit `1500` sticks (kill-switch/byte-for-byte case).
- [x] 2.2 GREEN: add `WriteHygieneConfig` struct + `WriteHygiene` field on
      `Config` in `internal/config/config.go`.
- [x] 2.3 GREEN: add `writeHygieneEnabledKeyPresent(data []byte) bool`
      mirroring `recallEnabledKeyPresent` (inverted: default-true unless an
      explicit `enabled: false` key is present in raw YAML).
- [x] 2.4 GREEN: wire defaults in `applyDefaults`: `if !cfg.WriteHygiene.Enabled
      && !writeHygieneEnabledKeyPresent(data) { cfg.WriteHygiene.Enabled = true
      }`; zero-check defaults for `NoopThreshold`/`UpdateThreshold`/
      `ShrinkGuard`/`CandidateLimit` (same idiom as `Diversity.Lambda`).
- [x] 2.5 GREEN: change the `cfg.Injection.Budget.MaxTokens = 1500` literal at
      `config.go:681` to `400`; leave `injectionBudgetMaxTokensKeyPresent` guard
      and `ContextBudget.MaxTokens = 1500` (unchanged) untouched.
- [x] 2.6 Docs: update config reference (README/DOCS.md config section) with
      the new `write_hygiene` block and the 400 default + migration note
      (explicit `1500` configs keep 1500).

Files: `internal/config/config.go` (modify), `internal/config/config_test.go` (modify), docs (modify).
Est. lines: ~180.

---

## PR3 — store: write-gate decision ladder core (SaveObservation/SaveResult)

Design slice 3a · Branch `feat/write-gate-core` · Base: `main` (after PR2) ·
`type:feature` · Depends on: PR1 (similarity), PR2 (config) · Capability: write-gate, save-normalization

- [x] 3.1 RED: add `internal/store/write_gate_test.go`. First case: kill-switch
      off (`write_hygiene.enabled:false`) with identical content saved twice →
      TWO rows exist, byte-for-byte v0.3 hash-window/topic_key behavior only —
      confirm this fails against not-yet-existing gate wiring.
- [x] 3.2 RED (same file): add failing cases for the full ladder ahead of
      implementation: NOOP (Jaccard ≥0.98 → skip insert, `duplicate_count++`,
      returns existing ID); AUTO-UPDATE (Jaccard >0.9 → in-place UPDATE,
      `revision_count++`); 0.9 boundary (Jaccard == exactly 0.9 → falls to
      SAVE+RELATE, NOT update); shrink-guard (`len(normNew) <
      shrink_guard*len(normOld)` on a similarity-triggered step-4 match →
      downgrades to NOOP, keeps richer existing row); SAVE+RELATE (candidate
      ≤0.9 → insert + existing `FindCandidates`/`judgment_required` flow
      unchanged); SAVE (no candidate → plain insert); determinism (same input
      twice with gate on, non-hash/non-topic_key path → same classification).
      Also added a `candidate_limit` edge-case test (BM25-top-ranked-but-
      unrelated candidate excludes a lower-ranked, higher-Jaccard one when the
      limit is 1, but reaches it once the limit widens) and an
      `AddObservation` thin-wrapper regression test (task 3.6).
- [x] 3.3 GREEN: add `store.Config` fields (`WriteHygieneEnabled`,
      `NoopThreshold`, `UpdateThreshold`, `ShrinkGuard`, `CandidateLimit`) next
      to `ProceduralTrustThreshold`/`ContextTokenBudget` in
      `internal/store/store.go`.
- [x] 3.4 GREEN: add `SaveResult` type (`ID int64`, `Decision string`,
      `TargetID *int64`, `Similarity float64`, `Reason string`) and decision
      constants (`noop`/`update`/`relate`/`save`).
- [x] 3.5 GREEN: implement `SaveObservation(p AddObservationParams) (SaveResult,
      error)` inside the existing `AddObservation` core — gate runs AFTER the
      existing topic_key upsert (step 1, unchanged) and 15-min hash window
      (step 2, unchanged), ONLY when `WriteHygieneEnabled`. Candidate retrieval
      reuses `FindCandidates`'s shape (FTS `MATCH` on title, same
      project+scope, BM25 floor, top-`CandidateLimit`, never all-pairs), scored
      via `similarity.Tokenize`/`similarity.Jaccard` on normalized content.
- [x] 3.6 GREEN: keep `AddObservation(p) (int64, error)` as a thin wrapper
      calling `SaveObservation` and returning `.ID` — no existing caller
      signature breaks.
- [x] 3.7 Validation (write-gate calibration, obs #1662 real clusters): add a
      replay-style fixture test (`internal/store/write_gate_replay_test.go`)
      using representative pairs modeled on the real duplicate/near-duplicate
      clusters found in obs #1662: (a) the #1360/#1359 same-incident,
      different-wording pair — confirm `noop_threshold=0.98` does NOT
      misclassify this as NOOP/AUTO-UPDATE (documented as "dedupe by wording,
      not by fact" — Jaccard on this pair must land below 0.9 or the
      calibration is wrong); (b) a near-verbatim repeat (session-summary style,
      whitespace/punctuation-only diff) DOES fire NOOP; (c) the 4
      near-identical "embeddinggemma" SDD-artifact cluster — confirm at least
      the closest pair triggers AUTO-UPDATE, not silent duplication. If
      `noop_threshold=0.98` misfires on any fixture, adjust and document the
      final value here (resolves design's open question 1) before merge.
      RESULT: defaults (0.98/0.9/0.9) classify all three fixtures correctly
      with real margin — no threshold change needed. Observed scores: (a)
      0.2458 (well below 0.9, correctly falls to relate); (b) 1.0000 (fires
      NOOP); (c) closest pair 0.9535 (fires AUTO-UPDATE). See
      `write_gate_replay_test.go` doc comments for the full pairwise matrix.

Files: `internal/store/store.go` (modify), `internal/store/write_gate_test.go` (new), `internal/store/write_gate_replay_test.go` (new).
Est. lines: ~360. ACTUAL: 387 (store.go: +387/-7, net new logic) + 453
(write_gate_test.go) + 149 (write_gate_replay_test.go) = ~989 changed lines —
significantly over the ~360 estimate (flagged as a review-workload risk, same
pattern PR11 hit: 545L vs 280L estimate). Per explicit instruction, tests were
NOT cut to fit the estimate — the calibration test (3.7) and the full ladder
edge-case matrix (3.2, incl. candidate_limit) are the highest-value coverage
in the whole change and the store-layer implementation itself is fully
production code (no filler). Delivery-step owner should decide whether to
split `write_gate_test.go`/`write_gate_replay_test.go` into a follow-up
commit within the same PR, or accept `size:exception` given this is the
"highest single-PR risk" slice the tasks doc already called out in advance.

---

## PR4 — store.Config wiring + write-gate integration validation

Design slice 3b · Branch `feat/write-gate-wiring` · Base: `main` (after PR3) ·
`type:feature` · Depends on: PR3 · Capability: write-gate

- [x] 4.1 RED: add/extend a `cmd/omnia` integration test (or
      `internal/store` test using a config-loaded `Store`) asserting that a
      `write_hygiene:` block in `config.yaml` reaches `store.Config` fields at
      every `storeNew` call site — fails until wiring lands.
      DONE: `cmd/omnia/write_hygiene_wiring_test.go` (new), mirroring
      `context_budget_wiring_test.go`'s `withCapturedStoreConfig`/`withArgs`
      pattern. Confirmed RED against unwired main.go: all 4 "enabled
      propagates all thresholds" cases (cmdContext/cmdMCP/cmdServe/cmdSave)
      failed with captured `WriteHygieneEnabled=false`/thresholds=0 (the
      store.Config zero value), plus the end-to-end "gate on" case failed
      with 2 rows instead of 1 — all for the right reason (wiring absent),
      before any production-code edit landed.
- [x] 4.2 GREEN: at each of the 3 `storeNew` call sites in
      `cmd/omnia/main.go` (mirroring the existing `cfg.ContextTokenBudget =
      appCfg.Injection.ContextBudget.MaxTokens` pattern), add
      `cfg.WriteHygieneEnabled = appCfg.WriteHygiene.Enabled` and the four
      threshold/limit fields.
      DONE + SCOPE CORRECTION (per explicit apply-time instruction to find
      EVERY production storeNew call site, not just the 3 ContextTokenBudget
      sites): wired at cmdServe, cmdMCP, cmdContext (the 3 ContextTokenBudget
      sites — cmdContext itself never saves, wired for consistency/zero-drift
      per design D2) PLUS cmdSave (`omnia save` CLI — a genuine save path
      named explicitly in design D2's "MCP handleSave, CLI save, import,
      backfill, sync" entry-point list; had ZERO prior appCfg loading, so a
      new `loadAppConfigWithRecallAutodetect()` call was added there).
      Investigated and explicitly EXCLUDED: `cmdImport` (routes through
      `Store.Import`, a raw-restore path that bypasses
      AddObservation/SaveObservation entirely by existing v0.3 design) and
      `cmdSync` (never calls AddObservation) — wiring store.Config fields on
      either would be dead code with no behavioral effect. All other
      `storeNew(cfg)` call sites in `cmd/omnia/main.go` (delete/timeline/
      stats/export/search/TUI/conflicts/projects-*/obsidian-export) are
      read-only or mutate-existing-rows-only and never reach the write-gate
      either. cmdContext's inline `if appCfg, cfgErr := ...; cond` was
      hoisted to a statement (`appCfg, cfgErr := ...` then a separate `if`)
      so the same load can be reused for both ContextTokenBudget and
      Write Hygiene wiring without a second `config.Load` call.
- [x] 4.3 Kill-switch byte-for-byte check: with `write_hygiene.enabled:false`
      set explicitly in `config.yaml`, run the full existing dedup/save test
      suite (hash-window + topic_key tests) and confirm zero behavior change
      vs pre-PR3 baseline.
      DONE via `TestWriteHygieneWiring_EndToEnd_ProductionSavePath` (new,
      `cmd/omnia`): drives the REAL `omnia save` CLI dispatch twice through
      the real config-wiring code path (no hand-built store.Config), reusing
      `internal/store`'s own `TestSaveObservation_LadderNoop` fixture
      (case/punctuation-only variant, Jaccard 1.0, different titles so the
      pre-existing exact-title hash-window never fires either way — isolates
      the write-gate steps 3-6 specifically). Default config (gate ON): 2nd
      near-duplicate save NOOPs, 1 row total. `write_hygiene.enabled:false`:
      2nd save duplicates exactly like pre-v0.3.1, 2 rows total. Full
      `internal/store` suite (all pre-existing hash-window/topic_key/
      write-gate/replay tests from PR3 + PR3-review-fixes) re-run green,
      zero regressions.
- [x] 4.4 Docs: note in `DOCS.md`/config reference that `write_hygiene` is
      default-on and how to opt out.
      DONE: added a bullet under `DOCS.md`'s `### Observations` /
      `POST /observations` entry naming every production save surface
      (`mem_save`, `omnia save`, dashboard, `POST /observations`) as sharing
      one write-gate, plus the `write_hygiene.enabled: false` opt-out and
      what it restores (byte-for-byte pre-v0.3.1: every save is a brand-new
      row). `README.md`'s config reference table already documented the
      full `write_hygiene.*` field list + default-on/kill-switch semantics
      from PR2 — this DOCS.md bullet is the narrative/wiring-complete
      cross-reference, not a duplicate of the table.

Files: `cmd/omnia/main.go` (modify, 3 call sites), test file (modify/new), docs (modify).
Est. lines: ~200.

---

## PR5 — mcp: `write_gate` envelope + RELATE wiring

Design slice 4 · Branch `feat/write-gate-envelope` · Base: `main` (after PR4) ·
`type:feature` · Depends on: PR3, PR4 · Capability: write-gate

- [x] 5.1 RED: add `internal/mcp/mcp_test.go` cases asserting `handleSave`'s
      response `extra` map gains `write_gate: {decision, target_id, similarity,
      reason}` for each of NOOP/UPDATE/RELATE/SAVE, with `target_id` set for
      NOOP/UPDATE and omitted for SAVE; NOOP/UPDATE also set `extra["id"]` to
      the target.
      DONE: written as new file `internal/mcp/write_gate_envelope_test.go`
      (kept separate from the already-large `mcp_test.go`, mirroring PR11's
      `review_due_nudge_test.go` precedent). Confirmed RED via `git stash` of
      the mcp.go/main.go production edits + `CGO_ENABLED=0 go vet
      ./internal/mcp/...` → `unknown field WriteHygieneEnabled in struct
      literal of type MCPConfig` — before any production code landed.
      `TestHandleSaveWriteGateEnvelope_NoopNamesExistingID` covers NOOP + the
      top-level `id`=target assertion; `TestHandleSaveWriteGateEnvelope_
      UpdateNamesTarget` covers similarity-triggered AUTO-UPDATE;
      `TestHandleSaveWriteGateEnvelope_SaveOmitsTargetID` covers plain SAVE
      (no candidate) with `target_id` absent from the map entirely.
- [x] 5.2 RED: assert RELATE keeps the existing `judgment_required` +
      `candidates` array byte-identical to pre-PR5 behavior (write_gate is
      additive/informational only on this path).
      DONE: `TestHandleSaveWriteGateEnvelope_RelateKeepsJudgmentRequired
      ByteIdentical` runs the same RELATE-triggering fixture with the gate
      off vs on (in separate projects, so the gate-on run's own FTS candidate
      search never sees the gate-off run's row as a self-match) and asserts
      `judgment_required`/`candidates` count are identical either way, with
      `write_gate` present ONLY on the gate-on side.
- [x] 5.3 GREEN: modify `handleSave` in `internal/mcp/mcp.go` to call
      `store.SaveObservation` instead of `store.AddObservation`, and build the
      `write_gate` envelope object from the returned `SaveResult`.
      DONE. Also added `MCPConfig.WriteHygieneEnabled bool` (threaded from
      `cmd/omnia/main.go`'s cmdMCP, alongside the existing
      `mcpCfg.Review = appCfg.Review` line) so the envelope's own kill-switch
      reads the SAME `write_hygiene.enabled` value already wired into
      `store.Config` — this small addition to `cmd/omnia/main.go` was
      necessary to satisfy 5.4's byte-for-byte requirement (see below); not
      originally listed in this task's file list but required plumbing, not
      scope creep.
- [x] 5.4 Kill-switch check: with gate disabled (PR2/PR4 flag off), confirm
      `handleSave`'s envelope has NO `write_gate` key change vs current
      behavior (or an explicit `decision:"save"` no-op shape — pick one and
      lock it in a test).
      DECISION (locked at apply time): option (a) — NO `write_gate` key at
      all when `MCPConfig.WriteHygieneEnabled` is false, envelope
      byte-for-byte identical to pre-write-hygiene output. DONE:
      `TestHandleSaveWriteGateKillSwitchByteIdentical` proves this
      specifically via a `topic_key`-upsert scenario (Decision=`update` fires
      REGARDLESS of the gate, since that pre-existing step is unconditional)
      — the envelope must still omit `write_gate` and keep the original
      "Memory saved: ..." message, not just for the trivial plain-SAVE case.
      A naive implementation gating only on `Decision==WriteGateDecisionSave`
      would have leaked a `write_gate` key here; gating on the explicit
      `MCPConfig.WriteHygieneEnabled` flag instead avoids that.

Files: `internal/mcp/mcp.go` (modify), `internal/mcp/write_gate_envelope_test.go`
(new), `cmd/omnia/main.go` (modify, +1 wiring line for
`mcpCfg.WriteHygieneEnabled`), `DOCS.md` (modify, `mem_save` envelope docs).
Est. lines: ~220. Actual: `mcp.go` +62/-1, `write_gate_envelope_test.go` 362
(new), `main.go` +8, `DOCS.md` +2 — total ~434L (over estimate, same
over-estimate pattern PR3/PR4/PR11 all hit, driven by thorough per-decision
RED/GREEN coverage — 5 tests covering all four decisions plus the
kill-switch's non-obvious topic_key-upsert edge case — not scope creep).

---

## PR6 — fix #147: session-hook project-override parity

Design slice 5 (independent) · Branch `fix/session-hook-project-override` ·
Base: `main` (after PR5) · `type:bug` · Depends on: none (can ship anytime) ·
Capability: session-hooks

- [ ] 6.1 RED: add tests mirroring `update_project_resolution_test.go`'s
      `cfg.DefaultProject` assertions for (a) `resolveSessionStartProject`'s
      empty-directory branch and (b) `handleSessionEnd` — both currently call
      bare `resolveWriteProject()` (confirmed at `internal/mcp/mcp.go:2596` and
      `:2611`), so with a process-level `cfg.DefaultProject` override set, the
      resolved project MUST match `handleSave`'s resolution — this fails today.
- [ ] 6.2 GREEN: replace `resolveWriteProject()` with
      `resolveWriteProjectWithProcessOverride(cfg.DefaultProject)` in both
      `resolveSessionStartProject`'s empty-dir branch and `handleSessionEnd`
      (`internal/mcp/mcp.go`), matching the `#403`/`#413`/`handleCapturePassive`
      precedent exactly. Explicit-directory and basename-fallback branches stay
      unchanged.
- [ ] 6.3 Regression check: existing `resolveSessionStartProject`/
      `handleSessionEnd` tests for explicit-directory and basename-fallback
      paths still pass unchanged.

Files: `internal/mcp/mcp.go` (modify, 2 call sites), test file (modify).
Est. lines: ~90.

---

## PR7 — fix: FTS 0-hit relaxation ladder

Design slice 6 (independent) · Branch `fix/fts-zero-hit-relaxation` ·
Base: `main` (after PR6) · `type:bug` · Depends on: PR2 (config flag pattern) ·
Capability: fts-recall

- [x] 7.1 RED: add `internal/store` tests: strict-AND baseline with ≥1 hit →
      relaxation ladder MUST NOT fire (non-zero-hit-path kill-switch case);
      0 strict hits + stopword removal yields hits → `Diag.Relaxed=true,
      Step=1`; step 1 also 0 hits → OR-mode (step 2) tried, `Step=2`; all
      levels exhausted → empty result, `Diag` reflects exhaustion, no infinite
      retry. — COMPLETE (see PR7 Apply Notes below; also added all-stopword,
      single-term-duplicate-retry, and topic_key/signature composition pins
      beyond the task's literal list).
- [x] 7.2 RED: add config test — `recall.fts_relax_on_zero: false` → `Search`
      returns the pre-PR7 zero-hit behavior byte-for-byte (explicit gate-off
      case). — COMPLETE. Initially landed as a config-package-only test with
      no runtime consumer (flagged by sdd-verify as an unacceptable "config
      key that does nothing"); FOLLOW-UP FIX (same session) wired it end to
      end via `store.Config.DisableFTSRelax` + cmd/omnia threading — see
      "PR7 Follow-up Fix" below. `Search` now genuinely returns the pre-PR7
      zero-hit behavior byte-for-byte when the flag is explicitly false.
- [x] 7.3 GREEN: add `recall.fts_relax_on_zero` (default true) to
      `internal/config/config.go` (zero-value-default idiom, default true via
      key-present probe since default differs from Go zero value). — COMPLETE.
- [x] 7.4 GREEN: add `SearchDiag` struct (`Relaxed bool`, `Step int`) and an
      optional `Diag *SearchDiag` field on `SearchOptions`; implement the
      bounded ladder (strict AND → drop EN+ES stopwords → OR-of-terms) inside
      `Store.Search`, additive-only (never reorders/removes existing hits). —
      COMPLETE; `SearchDiag` also gained an `Exhausted bool` field beyond the
      task's literal 2-field list (see PR7 Apply Notes).
- [x] 7.5 GREEN: surface `extra["fts_relaxed"]`/`extra["fts_relax_step"]` in
      `handleSearch` (`internal/mcp/mcp.go`) when `Diag.Relaxed`. — COMPLETE
      (FTS-only path only; hybrid recall path gets the ladder's underlying
      fix for free via Store.Search but not this transparency surfacing yet —
      see PR7 Apply Notes).

Files: `internal/store/store.go` (modify), `internal/config/config.go` (modify), `internal/mcp/mcp.go` (modify), test files (modify/new).
Est. lines: ~260.

## PR7 Apply Notes (2026-07-24)

STATUS: COMPLETE. Worktree `wt-fts`, branch `feat/v031-fts-fallback`, off
main `224a68b` (PR1-PR6+PR11 already merged). NOT committed (apply step
only; commit/PR is a separate delivery step). Strict TDD throughout: every
RED test confirmed failing (`go vet`/`go test`) against the pre-PR7 baseline
before any production code landed, then GREEN after.

Sub-tasks delivered (5, matching design D7 / tasks.md 7.1-7.5):
- Store.Search's strict AND-of-every-term FTS5 pass was extracted into a new
  `runFTSQuery(ftsQuery, opts, limit)` helper (byte-for-byte the same SQL/
  filter logic as before, just parameterized by the MATCH argument) so the
  strict pass and the ladder's step 1/step 2 retries share one code path.
  `zeroHitRelax(words, opts, limit)` implements the bounded 2-step ladder,
  gated in `Search` on `len(ftsResults) == 0` from the strict pass ONLY —
  the pre-existing topic_key sentinel and error-signature lanes are
  untouched, unconditional, and run earlier in `Search` against the
  ORIGINAL query, exactly as before; pinned via
  `TestSearch_TopicKeySentinelUnaffectedWhenLadderNeverFires` and
  `TestSearch_RelaxedTermsNeverLeakIntoTopicKeyLane`.
- `SearchDiag{Relaxed bool, Step int, Exhausted bool}` — `Exhausted` is an
  addition beyond the task's literal 2-field description, needed to
  distinguish "strict pass already succeeded, ladder never ran" (Diag stays
  zero-value either way) from "the ladder ran through every level and still
  found zero rows" (Exhausted=true) — both leave `Relaxed=false`, and
  `handleSearch`/callers need to tell them apart for anything beyond the
  binary "did relaxation help" signal (e.g. deciding whether to suggest
  simpler keywords only when relaxation genuinely exhausted itself).
- Two duplicate-query guards keep the ladder bounded/deterministic: (1) if
  dropping stopwords changes nothing (no stopword present), step 1 is
  skipped outright — it would be byte-identical to the strict pass — but
  step 2 (a real AND→OR relaxation) still runs when >1 term remains; (2) if
  exactly one non-stopword term remains, step 2 is skipped too, since
  AND-of-one-term and OR-of-one-term are the same query (already tried
  either at step 1 or the strict pass). An all-stopword query has no
  relaxed query left to construct at all and exhausts immediately with
  neither step attempted.
- `internal/config`: `RecallConfig.FTSRelaxOnZero bool` (yaml
  `fts_relax_on_zero`), default TRUE via `ftsRelaxOnZeroKeyPresent`'s
  explicit-vs-absent probe (mirrors `writeHygieneEnabledKeyPresent` exactly,
  inverted from `recallEnabledKeyPresent`). RESOLVED (was a scope gap in the
  first apply pass, fixed same session per sdd-coordinator instruction — see
  "PR7 Follow-up Fix" below): this flag is now genuinely wired end to end
  via `store.Config.DisableFTSRelax` + `cmd/omnia` threading at
  cmdServe/cmdMCP/cmdContext/cmdSave, so an operator's explicit
  `fts_relax_on_zero: false` actually reaches `Store.Search` instead of
  being silently ignored.
- `internal/mcp`: `handleSearch`'s FTS-only path (`cfg.Recall == nil`)
  passes `Diag: &ftsDiag` and, when `ftsDiag.Relaxed`, adds
  `extra["fts_relaxed"]=true` + `extra["fts_relax_step"]=N` — omitted
  entirely (not even `false`/`0`) on every other path, preserving byte-for-
  byte output there. SCOPE NOTE: the hybrid recall path
  (`cfg.Recall != nil`, `StoreLexicalSearcher.Search` in
  `recall_adapter.go`) does NOT forward a `Diag` pointer through
  `recall.LexicalSearchOptions` in this PR, so it gets the ladder's
  underlying fix for free (the ladder lives inside the one `Store.Search`
  entry point `StoreLexicalSearcher` itself calls) but never surfaces the
  `fts_relaxed` transparency fields — spec fts-recall's REQ "Fallback
  Transparency" doesn't scope hybrid vs. FTS-only, so wiring `Diag` through
  `recall_adapter.go` too is a reasonable, low-risk follow-up, just not
  required to satisfy PR7's own task list (`internal/mcp/mcp.go` only,
  not `internal/mcp/recall_adapter.go` or `internal/recall`).
- REAL-BATTERY-SHAPE regression fix (found via full-suite verification, not
  a task-list item): two PRE-EXISTING tests,
  `TestMemSave_ExplicitBackedProjectRejectsAmbiguousNormalizationCollision`
  and `TestMemSave_ExplicitProjectRejectsCollapsedStoreBucket`
  (`internal/mcp/mcp_test.go`), asserted "a rejected save wrote nothing" via
  `s.Search(<rejected title text>, ...)` returning 0 rows. PR7's OR-mode
  step 2 now legitimately surfaces an unrelated, already-seeded observation
  in each test via a single shared non-stopword term (e.g. "explicit"),
  making that Search()-based non-persistence proxy a false positive for
  "nothing new was written" — confirmed by reverting to clean `224a68b`
  (`git stash`) and re-running both tests in isolation: they pass on clean
  main and fail only with PR7's changes applied. Fixed by switching both
  assertions to an exact `Store.Stats().TotalObservations` before/after
  comparison — a check entirely independent of `Search`'s own matching
  algorithm — rather than weakening the OR-mode relaxation itself (the
  loose match is intended, designed-for behavior, not a defect).

RED→GREEN evidence: `internal/store/fts_relax_test.go` (new, 8 tests)
confirmed RED via `CGO_ENABLED=0 go vet ./internal/store/...` →
`vet: internal/store/fts_relax_test.go:37:11: undefined: SearchDiag` before
any production code existed; GREEN after `SearchDiag`/`Diag`/`runFTSQuery`/
`zeroHitRelax`/`ftsStopwords`/`sanitizeFTSTerms`/`sanitizeFTSTermsOR` landed
in `store.go` — all 8 new tests + the full pre-existing `internal/store`
suite pass (6.661s). `internal/config/fts_relax_config_test.go` (new, 4
tests) confirmed RED via `go vet ./internal/config/...` →
`cfg.Recall.FTSRelaxOnZero undefined`; GREEN after `RecallConfig.
FTSRelaxOnZero` + `ftsRelaxOnZeroKeyPresent` + the `applyDefaults` fill-in
landed — full `internal/config` suite green. `internal/mcp/
fts_relax_envelope_test.go` (new, 3 tests) confirmed RED via `go test -run`
(handler ran and found the result via the store-level ladder already, but
the envelope carried no `fts_relaxed` key yet); GREEN after the `handleSearch`
wiring landed.

Full verification: `CGO_ENABLED=0 go build ./...` clean. `CGO_ENABLED=0 go
vet ./...` clean. `gofmt -l` on all touched/new files empty (no formatting
diffs). `CGO_ENABLED=0 go test ./internal/store/... ./internal/config/...`
both fully green. `CGO_ENABLED=0 go test ./internal/mcp/...` — exactly the
SAME 10 pre-existing `unknown_project`/`TestHandleCapturePassiveDefaults
SourceAndSession` failures already confirmed pre-existing across every
prior PR in this chain (PR1-PR4/PR11), re-confirmed pre-existing HERE TOO
via `git stash` back to clean `224a68b` and re-running — same 10 failures,
0 PR7 files present. Full-repo `CGO_ENABLED=0 go test ./...`: every other
package green; `internal/mcp` shows only those same 10 pre-existing
failures.

Line count actuals vs ~260L estimate (FIRST apply pass, before the follow-up
fix below): production code — `internal/store/store.go` +258/-33 (net ~225:
`runFTSQuery` extraction + `SearchDiag`/`zeroHitRelax`/stopword list/
term-builders), `internal/config/config.go` +47, `internal/mcp/mcp.go` +22.
Test code — `internal/store/fts_relax_test.go` 376L (new, 8 tests),
`internal/config/fts_relax_config_test.go` 88L (new, 4 tests),
`internal/mcp/fts_relax_envelope_test.go` 147L (new, 3 tests),
`internal/mcp/mcp_test.go` +41 (2 pre-existing tests hardened against the
new OR-mode relaxation, see above). Total ≈946 insertions/33 deletions —
well over the ~260L estimate, same over-estimate pattern PR3 (989 vs
360)/PR4 (343 vs 200)/PR11 (545 vs 280) all hit, driven by the same cause
(thorough RED/GREEN coverage per edge case, not scope creep into unassigned
features).

## PR7 Follow-up Fix (2026-07-24, same session) — wire `recall.fts_relax_on_zero` end to end

STATUS: COMPLETE. Same worktree `wt-fts`, branch `feat/v031-fts-fallback`,
still uncommitted. Triggered by an sdd-coordinator review that correctly
rejected the first apply pass's scope decision: a config key that parses
and defaults but has no runtime consumer is "an API lie" — an operator
setting `fts_relax_on_zero: false` was silently ignored, and (unlike
write-hygiene's PR2→PR3/PR4 staging) no later PR was planned to wire this
one in. Fixed via strict TDD in the same session, not deferred further.

Field shape chosen: `store.Config.DisableFTSRelax bool` — INVERTED relative
to `WriteHygieneEnabled`/`ContextTokenBudget`'s own positive-named,
off-by-default convention. Rationale: the zero-hit relaxation ladder is
strictly additive (fires ONLY on exactly zero strict hits, never reorders
or removes an existing hit), so it should protect every install out of the
box — including a bare `store.Config{}` literal and any composition root
that hasn't wired this field — without requiring explicit opt-in. Making
the Go zero value (`false`) mean "ladder ACTIVE" achieves that for free;
only an explicit `recall.fts_relax_on_zero: false` in a successfully-loaded
config.yaml, threaded by the composition root, sets `DisableFTSRelax=true`
and restores pre-PR7 zero-hit behavior byte-for-byte. This is the opposite
polarity from write-hygiene's "missing config degrades to off" convention
by design: here, missing config.yaml (or a zero-value `store.Config{}` in
any caller/test) must degrade to "ladder ON", not off.

Sub-tasks delivered:
- `internal/store/store.go`: added `Config.DisableFTSRelax bool` (doc
  comment explains the inversion). `Search`'s ladder gate changed from
  `len(ftsResults) == 0` to `len(ftsResults) == 0 && !s.cfg.DisableFTSRelax`
  — the ONLY change to the gate condition; `zeroHitRelax`/`runFTSQuery`
  themselves are untouched.
- `internal/store/fts_relax_test.go`: new golden test
  `TestSearch_DisableFTSRelaxKillSwitchRestoresPrePR7BehaviorByteForByte`
  (+ `newTestStoreWithDisableFTSRelax` helper) — reuses the EXACT SAME
  fixture/query as `TestSearch_ZeroHitStopwordRelaxationFindsResult` (which
  finds the doc via step 1 with the ladder active) and asserts that with
  `DisableFTSRelax=true`, results stay empty and `Diag` stays entirely
  zero-value (not even `Exhausted=true` — the ladder never runs at all, it
  doesn't "exhaust").
- `cmd/omnia/main.go`: one line added inside each of the 4 EXISTING
  write-hygiene wiring blocks (cmdServe, cmdMCP, cmdSave, cmdContext) —
  `cfg.DisableFTSRelax = !appCfg.Recall.FTSRelaxOnZero` — reusing the same
  `appCfg`/`appCfgErr` already loaded there for `write_hygiene.*`/
  `ContextTokenBudget`, no second `config.Load` call. `cmdSearch` (the
  `omnia search` CLI) was intentionally NOT wired, mirroring write-hygiene's
  own precedent of only wiring the 4 named sites.
- `cmd/omnia/fts_relax_wiring_test.go` (new): mirrors
  `write_hygiene_wiring_test.go`'s pattern exactly, reusing its
  `withCapturedStoreConfig`/`testConfig`/`withArgs`/`stubRuntimeHooks`
  helpers. 4 wiring test functions (one per site) each with an
  "enabled:true (default) keeps DisableFTSRelax false" + "enabled:false
  flows through to DisableFTSRelax true" pair; `cmdContext`'s also gets a
  third "missing config.yaml keeps the ladder ON" subtest (the
  inverted-polarity case that most needed pinning). Plus
  `TestFTSRelaxWiring_EndToEnd_ProductionSearchPath`: drives the REAL
  `loadAppConfigWithRecallAutodetect → cmdContext → storeNew` production
  path (no hand-built `store.Config` literal), reopens the resulting store,
  and issues a real `Store.Search` call — proving the wiring produces 1
  result with the flag on (default) and 0 results + zero-value `Diag` with
  the flag off (the golden case, end to end through production wiring).

RED→GREEN evidence: `cmd/omnia/fts_relax_wiring_test.go` confirmed RED via
`CGO_ENABLED=0 go vet ./cmd/omnia/...` →
`captured.DisableFTSRelax undefined (type *store.Config has no field or
method DisableFTSRelax)` before any production code changed; the
"enabled:false" subtests at each of the 4 sites additionally failed
functionally even after the type existed but before the wiring line
landed (`expected recall.fts_relax_on_zero:false to set
store.Config.DisableFTSRelax true, got false`), confirming the RED wasn't
just a compile error. `internal/store/fts_relax_test.go`'s new golden test
confirmed RED via `go vet` → `cfg.DisableFTSRelax undefined (type Config
has no field or method DisableFTSRelax)`. GREEN after the `Config` field +
gate condition + 4 wiring lines landed — all new tests pass, plus the full
pre-existing `internal/store`/`cmd/omnia` suites.

Full verification (after the fix): `CGO_ENABLED=0 go build ./...` clean.
`CGO_ENABLED=0 go vet ./...` clean. `gofmt -l` empty on all touched/new
files (`store.go`, `fts_relax_test.go`, `main.go`,
`fts_relax_wiring_test.go`). `CGO_ENABLED=0 go test ./internal/store/...
./internal/config/... ./cmd/omnia/...` all green. Full-repo
`CGO_ENABLED=0 go test ./...`: every package green except `internal/mcp`'s
SAME 10 pre-existing `unknown_project`/
`TestHandleCapturePassiveDefaultsSourceAndSession` failures already
confirmed pre-existing (re-verified against clean `224a68b` in the first
apply pass, unaffected by this follow-up fix since it touches no
`internal/mcp` files).

Updated line count actuals (git diff --stat, exact): `internal/store/
store.go` now +280/-33 total (follow-up added ~22 lines: `Config` field +
doc comment + gate condition change), `internal/store/fts_relax_test.go`
now 445L total (follow-up added 69 lines: golden test + helper),
`cmd/omnia/main.go` +26 (4× wiring blocks, one line of code + a short
comment each), `cmd/omnia/fts_relax_wiring_test.go` 273L (new, 4 wiring
test functions + 1 end-to-end test). PR7 GRAND TOTAL (first pass + this
follow-up fix, all 5 touched/new production+test files): 383 insertions/33
deletions across tracked files (`store.go`/`config.go`/`mcp.go`/
`mcp_test.go`/`main.go`) + 953 lines across 4 new test files
(`fts_relax_test.go` 445, `fts_relax_config_test.go` 88,
`fts_relax_envelope_test.go` 147, `fts_relax_wiring_test.go` 273) = 1336
insertions/33 deletions vs the original ~260L estimate. The
config-key-with-no-consumer gap that made the first pass already look
"done" at ~946L is exactly why this follow-up was necessary; the extra
~390L is the genuine cost of making the rollback flag actually roll
something back, not scope creep.

REMAINING ACCEPTED FOLLOW-UP (no code change, explicitly deferred per
sdd-coordinator instruction): the hybrid recall path's `fts_relaxed`
transparency fields (`internal/mcp/recall_adapter.go`'s
`StoreLexicalSearcher` doesn't forward a `Diag` pointer through
`recall.LexicalSearchOptions`) remain unwired. This is accepted as fine to
defer — UNLIKE the config-flag gap just fixed — because the underlying fix
(the ladder itself) already applies on the hybrid path for free (it shares
the one `Store.Search` entry point `StoreLexicalSearcher` calls); only the
optional transparency surfacing is missing there, not the fix.

---

## PR8 — `omnia dedupe` propose engine (dry-run)

Design slice 7a · Branch `feat/dedupe-scan` · Base: `main` (after PR7) ·
`type:feature` · Depends on: PR1 (similarity) · Capability: dedupe-scan

- [x] 8.1 RED: add `cmd/omnia/dedupe_test.go`: bare `omnia dedupe` invocation
      leaves DB unchanged (propose-only default); FTS-blocked candidate
      pre-filter used (not O(n²) all-pairs) at a synthetic 1600+-row scale;
      union-find clusters pairs with Jaccard ≥0.9; canonical = newest
      (`created_at` max, tie-break max id) within each cluster; deterministic
      dry-run output (cluster id, canonical #N, losers, scores) and stable
      `--json` shape.
      DONE: confirmed RED via `go vet ./cmd/omnia/...` → `undefined:
      cmdDedupe` before any production code landed. Also added explicit
      empty-DB, all-distinct-content, cross-type-pair, and soft-deleted-row
      exclusion cases beyond the ones listed above.
- [x] 8.2 GREEN: create `cmd/omnia/dedupe.go`: reuse `Store.ScanProject`/
      `Store.FindCandidates` (FTS `MATCH`, never all-pairs) for candidate
      generation; union-find clustering over pairs with content
      `similarity.Jaccard ≥ 0.9`; per-cluster canonical selection; deterministic
      `--dry-run` (default) text + optional `--json` proposal output.
      DONE via `runDedupeScan` (testable core) + `cmdDedupe` (flag parsing +
      human/`--json` print, mirroring `cmdReviewDue`'s shape). Uses
      `Store.AllObservations` (not `ScanProject`, which is single-project-only
      and can't serve the "no `--project` = all projects" case) +
      `Store.FindCandidates(..., SkipInsert:true)` per observation for the
      FTS-blocked pre-filter. Cross-type pairs are never clustered (mirrors
      the write-gate's own type-gating precedent — not stated explicitly by
      the dedupe-scan spec/design, a deliberate consistency choice, noted in
      code comments). Candidate retrieval uses an explicit, more permissive
      `BM25Floor` than `FindCandidates`' own default (-2.0): at 1600+-row
      scale with genuinely rare title terms, BM25's IDF component can push a
      true near-duplicate pair's score below a tight floor even though the
      per-observation candidate LIMIT (not the floor) is what bounds
      complexity; false positives from a wider floor are cheap since they
      still must clear the separate, stricter content-Jaccard≥0.9 test.
      Cluster ids are derived from each cluster's smallest member id (not
      union-find's internal root, not scan order) so the same DB always
      proposes the same cluster id across runs — pinned by a determinism
      test (`TestRunDedupeScanDeterministicAcrossRuns`) and a 1650-filler-row
      scale test (`TestRunDedupeScanCandidatePreFilterBoundedAtScale`)
      asserting `PairsScored <= scanned*candidateLimit`.
- [x] 8.3 GREEN: wire `case "dedupe":` dispatch in `cmd/omnia/main.go`.
      DONE, plus a `printUsage()` entry.
- [x] 8.4 Docs: add `omnia dedupe` usage to `DOCS.md`/CLI help.
      DONE: new "Dedupe Scan CLI (admin)" section in `DOCS.md` (mirrors the
      "Forget Scan CLI (admin)" section's structure) + `printUsage()` entry
      in `cmd/omnia/main.go`.

Files: `cmd/omnia/dedupe.go` (new, 391 lines), `cmd/omnia/dedupe_test.go`
(new, 421 lines), `cmd/omnia/main.go` (modify, +8 lines: dispatch + usage),
`DOCS.md` (modify, +17 lines). Est. lines: ~300. ACTUAL: ~416 net new
production + docs lines (excluding the test file, which the tasks.md
estimate convention treats separately per PR3/PR4/PR5/PR11 precedent) —
somewhat over estimate, driven by the same thorough-doc-comment pattern
those PRs hit (candidate-limit/BM25-floor rationale, cross-type-clustering
precedent, cluster-id-determinism rationale — all load-bearing for a future
PR9 `--apply`, not filler); no `size:exception` needed, still comfortably
under the 400-line/PR budget on its own.

---

## PR9 — `omnia dedupe --apply <cluster-id>` (mutation)

Design slice 7b · Branch `feat/dedupe-apply` · Base: `main` (after PR8) ·
`type:feature` · Depends on: PR8 · Capability: dedupe-scan

- [x] 9.1 RED: add cases to `cmd/omnia/dedupe_test.go`: `--apply <id>` on
      cluster A leaves cluster B fully untouched (explicit per-cluster
      isolation); **no `--apply all` flag exists — invoking `--apply` without
      a concrete cluster id errors with usage, per the resolved scope
      decision** (spec's original "(or explicit 'all')" wording is
      superseded); `--apply` on a cluster id invalidated since proposal
      (already applied, or an underlying observation changed) fails cleanly
      with no partial/silent mutation; idempotent re-apply of an already-applied
      cluster is a safe no-op/clear error, never a double-supersede.
      DONE: 6 tests (bare `--apply`, `--apply all`, `--apply`+`--dry-run`
      combo, stale/never-existed cluster id, cluster-A-vs-cluster-B
      isolation, idempotent re-apply refusal + single-supersede assertion).
- [x] 9.2 RED: assert a successful `--apply <id>` inserts a JUDGED
      `memory_relations` row (`canonical supersedes loser`, `marked_by
      "system:dedupe"`) per loser, and soft-deletes each loser via
      `Store.DeleteObservationWithActor(loserID, false, "system:dedupe")`
      (referenced history: queryable/restorable, out of active recall) — while
      the canonical row is untouched.
      DONE: 2 tests (relation+soft-delete shape assertions via
      `GetRelationsForObservations`/raw `deleted_at`+`content` query,
      `--json` apply-result round-trip).
- [x] 9.3 GREEN: extend `cmd/omnia/dedupe.go` with `--apply <cluster-id>`
      flag parsing (reject bare `--apply`/`--apply all`), stale-cluster
      re-validation against current DB state before mutating, and the
      supersede+soft-delete mutation using the existing relation-insert +
      `DeleteObservationWithActor` primitives.
      DONE (see PR9 Apply Notes below).
- [x] 9.4 Docs: update `omnia dedupe` usage to document `--apply <cluster-id>`
      as the ONLY mutation path (no all-clusters shortcut).
      DONE: `DOCS.md`'s "Dedupe Scan CLI (admin)" section + `main.go`'s
      `printUsage()` entry both updated with the `--apply`/`--dry-run` shape.

Files: `cmd/omnia/dedupe.go` (modify, +251/-21 net), `cmd/omnia/dedupe_test.go`
(modify, +389 new test lines), `cmd/omnia/main.go` (modify, usage text),
`DOCS.md` (modify, docs). Est. lines: ~260. ACTUAL: ~281 net shipped
production+docs lines (dedupe.go+main.go+DOCS.md, excl. test file per
PR3/PR4/PR8/PR11 convention) — somewhat over estimate, same thorough-doc-
comment pattern every prior PR in this chain hit; comfortably under the
400-line/PR budget on shipped code alone, no `size:exception` needed.

## PR9 Apply Notes (2026-07-24)

STATUS: COMPLETE — implemented in worktree wt-apply, branch
feat/v031-dedupe-apply, off main ff86676 (PR1+PR2+PR11+PR3+PR4+PR5+PR8 all
merged). NOT committed (apply step only; commit/PR left to the delivery
step). Strict TDD: RED confirmed via `git stash push -- cmd/omnia/dedupe.go`
(reverting to the pre-PR9, PR8-only baseline) then
`CGO_ENABLED=0 go vet ./cmd/omnia/...` → `vet: cmd/omnia/dedupe_test.go:746:59:
undefined: dedupeApplyActor`; restored via `git stash pop`, then GREEN.

Sub-tasks delivered (4, matching design D9/tasks 9.1-9.4):
- Flag parsing: `--apply <cluster-id>` requires an explicit value (bare
  `--apply` errors with usage before the store opens); `--apply all` is
  explicitly rejected (the resolved scope decision — no all-clusters
  shortcut); `--apply` + `--dry-run` together is rejected (mutually
  exclusive safety check, also before the store opens).
- Staleness handling (`applyDedupeCluster`): re-runs `runDedupeScan` FRESH
  against current DB state and looks up the named cluster id in that fresh
  result — no separate "proposal snapshot" is ever stored between `omnia
  dedupe` and `omnia dedupe --apply`, so a fresh scan IS the staleness
  check. Both staleness cases (already-applied; underlying data changed)
  collapse onto the same "cluster id not found in a fresh scan" error path
  with no special-casing needed, since cluster ids are derived entirely
  from current DB state (see runDedupeScan's own doc comment). This also
  makes idempotent re-apply a natural refusal: once a cluster is applied,
  its losers are soft-deleted and excluded from every future
  `AllObservations`-backed scan, so the same cluster id can never
  recompute again.
- Mutation (per loser, canonical untouched): `Store.SaveRelation` (insert
  pending) + `Store.JudgeRelation` (mark judged, `relation=supersedes`,
  explicit `MarkedByActor="system:dedupe"`/`MarkedByKind="system"`) — this
  two-step path was chosen over the simpler one-call `Store.JudgeBySemantic`
  specifically because `JudgeBySemantic` hardcodes
  `marked_by_actor="engram"`/`marked_by_kind="system"` with no override,
  and design D9 requires `marked_by "system:dedupe"` verbatim;
  `SaveRelation`+`JudgeRelation` is the exact same two-step path an
  ordinary `mem_judge` verdict already takes, so this reuses the existing
  relation machinery rather than inventing a new one. Then
  `Store.DeleteObservationWithActor(loserID, false, "system:dedupe")`
  (soft-delete, confirmed reused verbatim — same actor string on both
  halves of the mutation).
- Docs: `DOCS.md`'s existing "Dedupe Scan CLI (admin)" section (from PR8)
  extended with the `--apply`/`--dry-run` usage line + a 5-point mutation
  behavior list; `main.go`'s `printUsage()` `dedupe` entry updated to the
  same shape.

**Implementation decisions not spelled out verbatim in design/tasks,
resolved during apply** (all documented in code comments in `dedupe.go`):
1. `memory_relations.sync_id` for the new supersede relation is generated
   locally in `cmd/omnia` (`newDedupeRelationSyncID`, same `"rel-<16hex>"`
   shape as `internal/store`'s own unexported `newSyncID` generator) rather
   than calling `internal/store`'s private helper directly — `SaveRelation`'s
   `SyncID` field is an exported, caller-supplied contract (the DB's UNIQUE
   constraint is the only real requirement), so this is using that contract
   as designed, not inventing a new ID scheme.
2. Relation created BEFORE soft-delete (not after) — order doesn't affect
   correctness here (soft-delete never touches `sync_id` or triggers the
   hard-delete-only orphaning branch), but creating the attribution record
   first means a mutation that fails partway through never leaves a
   soft-deleted loser with no supersede record explaining why.
3. Confirmed via a dedicated test
   (`TestCmdDedupeApplyCreatesSupersedeRelationAndSoftDeletesLoser`) that
   `GetRelationsForObservations`' `TargetMissing` flag is `true` for a
   soft-deleted (not hard-deleted) loser — its own doc comment says
   "Missing OR soft-deleted... set the flag to true"; this is a distinct
   signal from `judgment_status='orphaned'` (hard-delete-only, asserted
   separately as staying `'judged'`). Flagging this so sdd-verify doesn't
   mistake `TargetMissing=true` for the relation having been orphaned or
   the loser being unreachable — the loser's content and the relation row
   are both still directly queryable, exactly the "referenced history"
   contract design D9 requires.
4. Sequential, stop-on-first-error apply across a cluster's losers (no
   cross-loser transaction spanning multiple `Store` calls, since `Store`
   does not expose a public cross-method transaction primitive to
   `cmd/omnia`): documented as a known limitation, not silently glossed
   over. The spec's own "no partial/silent mutation" requirement is scoped
   to the STALE-cluster-apply case specifically (verified: the fresh-scan
   staleness check runs BEFORE any mutation call, so a stale/unknown
   cluster id is always a full no-op) — it is not a cross-loser atomicity
   guarantee within an otherwise-valid multi-loser apply, which no existing
   `cmd/omnia` command with multi-row mutation currently provides either.

RED→GREEN evidence: all 8 new PR9 tests
(`TestCmdDedupeApplyBareFlagRequiresExplicitClusterID`,
`TestCmdDedupeApplyAllIsNotSupported`,
`TestCmdDedupeApplyAndDryRunAreMutuallyExclusive`,
`TestCmdDedupeApplyStaleClusterIDFailsCleanlyNoMutation`,
`TestCmdDedupeApplyIsolatesNamedClusterOnly`,
`TestCmdDedupeApplyIdempotentReApplyRefusesCleanly`,
`TestCmdDedupeApplyCreatesSupersedeRelationAndSoftDeletesLoser`,
`TestCmdDedupeApplyJSONReportsClusterCanonicalAndLosers`) written first,
confirmed RED via `git stash` of the pre-PR9 `dedupe.go` (see STATUS above),
then GREEN after the implementation landed — one iterative fix needed
during GREEN (not a production bug): the `TargetMissing` test assertion
initially expected `false` for a soft-deleted target; corrected to `true`
per `GetRelationsForObservations`' own documented contract (see decision #3
above).

Full verification: `CGO_ENABLED=0 go build ./...` clean. `go vet ./...`
clean. `gofmt -l cmd/omnia/dedupe.go cmd/omnia/dedupe_test.go cmd/omnia/main.go`
empty (no formatting diffs). `CGO_ENABLED=0 go test ./cmd/omnia/...
./internal/store/...` — both green (cmd/omnia 13.5s incl. all 17 dedupe
tests + the 1650-row scale test, internal/store 7.8s untouched/no
regression). Full-repo `CGO_ENABLED=0 go test ./...`: only failures are the
SAME 10 pre-existing `internal/mcp` `unknown_project` test failures already
confirmed pre-existing/unrelated across every prior PR in this chain
(TestHandleSaveSuggestsTopicKeyWhenMissing,
TestHandleSaveFallsBackToManualSaveWhenNoActiveSession,
TestHandleSaveWithNilActivityStillSucceeds,
TestHandleSavePromptCaptureFailureIsNonFatal,
TestHandleSavePromptFeedsAutoCaptureContext,
TestHandleSaveCapturePromptFalseSkipsCurrentPrompt,
TestHandleSaveNoCurrentPromptStillSucceeds,
TestHandleSaveDoesNotSuggestWhenTopicKeyProvided,
TestHandleCapturePassiveDefaultsSourceAndSession,
TestHandleSaveReturnsLifecycleState) — PR9 never touches `internal/mcp`.

---

## PR10 — `omnia import claude-memory <dir>`

Design slice 8 · Branch `feat/import-claude-memory` · Base: `main` (after PR9) ·
`type:feature` · Depends on: PR3/PR4 (routes through the gate) · Capability: claude-memory-import

- [x] 10.1 RED: add `cmd/omnia/import_claude_memory_test.go`: dir containing
      only `MEMORY.md` → zero observations, no error; dir with `MEMORY.md` +
      N memory files → only the N files considered, MEMORY.md skipped; each
      imported observation carries a `source="claude-memory"` provenance tag;
      re-running import over an UNCHANGED dir with `write_hygiene.enabled:true`
      (default) creates ZERO new observations (NOOP/AUTO-UPDATE only, routed
      through the gate via `topic_key = "claude-memory/"+name`); re-running
      after one file is edited → AUTO-UPDATE, not a duplicate; explicit
      documentation case: `write_hygiene.enabled:false` → plain v0.3 save
      semantics apply and idempotency does NOT hold (duplicates possible) —
      assert this is the documented, non-silent behavior, not a bug.
- [x] 10.2 GREEN: create `cmd/omnia/import_claude_memory.go`: discover
      `<dir>/*.md` skipping `MEMORY.md`; parse frontmatter with the existing
      `yaml.v3` dep (split leading `---`…`---`, unmarshal, body = remainder);
      map `description` (fallback `name`) → title, body → content,
      `metadata.type` via table (reference→discovery, note→discovery,
      default→manual), `name` slug → `topic_key = "claude-memory/"+name`,
      `source = "claude-memory"`; route every import through
      `store.SaveObservation`.
- [x] 10.3 GREEN: wire `import claude-memory <dir>` dispatch — branch inside
      `cmd/omnia/main.go`'s existing `case "import":`/`cmdImport` on
      `os.Args[2]=="claude-memory"` before falling through to the existing
      `omnia import <file.json>` path.
- [x] 10.4 Kill-switch/backward-compat check: `omnia import <file.json>`
      (existing JSON export/import) behaves byte-for-byte unchanged — add a
      regression test asserting the pre-existing `cmdImport` JSON path is
      untouched by the new dispatch branch.
- [x] 10.5 Docs: add `omnia import claude-memory <dir>` usage + provenance
      mapping table to `DOCS.md`.

Files: `cmd/omnia/import_claude_memory.go` (new), `cmd/omnia/import_claude_memory_test.go` (new), `cmd/omnia/main.go` (modify), docs (modify).
Est. lines: ~280.

---

## PR11 — Play G: `omnia review-due` + gated `mem_context` nudge

Design slice 9 (independent — touches no write-gate files) · Branch
`feat/spaced-review-due` · Base: `main` (after PR10) · `type:feature` ·
Depends on: none · Capability: spaced-review

- [ ] 11.1 RED: add `cmd/omnia/review_due_test.go`: nothing due → quiet "0
      memories due for review" clean-exit summary; some due → compact grouped
      output (project → type → `#ID Title, #ID Title`), NEVER dumps any
      observation's `content` field.
- [ ] 11.2 RED: add `internal/store` test for a new
      `CountObservationsNeedingReview(project string) (int, error)` — cheap
      `COUNT` query, no row hydration.
- [ ] 11.3 RED: add `internal/mcp` test for `handleContext`: with
      `review.due_nudge` at its zero-value default (off), `mem_context`
      response is byte-identical to pre-PR11 output (no `review_due_count`
      key at all); with the flag on, `extra["review_due_count"]=N` plus one
      text line, no other existing field altered.
- [ ] 11.4 GREEN: add `Store.CountObservationsNeedingReview` in
      `internal/store/store.go` reusing the existing `decayReviewAfterMonths`/
      `ObservationsNeedingReview` query shape.
- [ ] 11.5 GREEN: create `cmd/omnia/review_due.go` — read-only wrapper over
      `ObservationsNeedingReview`, grouped by project then type, compact
      ID+title output, resolution hint line pointing at existing
      `mem_review mark_reviewed <id>` (no new mutation surface).
- [ ] 11.6 GREEN: wire `case "review-due":` dispatch in `cmd/omnia/main.go`.
- [ ] 11.7 GREEN: add `review.due_nudge` (bool, zero-value-false default, no
      key-present probe needed — mirrors `TypeLensConfig`) to
      `internal/config/config.go`; gate the nudge addition in `handleContext`
      (`internal/mcp/mcp.go`) behind it.
- [ ] 11.8 Resolution round-trip check: confirm existing `mem_review
      action=mark_reviewed` (`Store.MarkReviewed`) still bumps `review_after`
      per the (unchanged) type-decay map — no new mutation path introduced by
      this PR.
- [ ] 11.9 Docs: add `omnia review-due` usage + `review.due_nudge` flag to
      `DOCS.md`.

Files: `cmd/omnia/review_due.go` (new), `cmd/omnia/review_due_test.go` (new), `internal/store/store.go` (modify), `internal/config/config.go` (modify), `internal/mcp/mcp.go` (modify), `cmd/omnia/main.go` (modify), docs (modify).
Est. lines: ~280.

---

## Review Workload Forecast

| Field | Value |
|-------|-------|
| Estimated changed lines | ~2,610 total across 11 PRs (see per-PR breakdown) |
| 400-line budget risk | Low (post-split) — see note |
| Chained PRs recommended | Yes |
| Suggested split | PR1 → PR2 → PR3 → PR4 → PR5 → PR6 → PR7 → PR8 → PR9 → PR10 → PR11 |
| Delivery strategy | (as cached by orchestrator for this session) |
| Chain strategy | stacked-to-main |

Decision needed before apply: No
Chained PRs recommended: Yes
Chain strategy: stacked-to-main
400-line budget risk: Low

### Per-PR estimate vs 400-line budget

| PR | Slice | Est. lines | Risk | Depends on |
|----|-------|-----------|------|------------|
| PR1 | 1 similarity leaf | ~180 | Low | none |
| PR2 | 2 config | ~180 | Low | PR1 (sequential base) |
| PR3 | 3a write-gate core | ~360 | Medium (closest to budget) | PR1, PR2 |
| PR4 | 3b store.Config wiring + validation | ~200 | Low | PR3 |
| PR5 | 4 mcp envelope | ~220 | Low | PR3, PR4 |
| PR6 | 5 fix #147 | ~90 | Low | none (independent) |
| PR7 | 6 fix FTS relax | ~260 | Low | PR2 |
| PR8 | 7a dedupe propose | ~300 | Low-Medium | PR1 |
| PR9 | 7b dedupe apply | ~260 | Low | PR8 |
| PR10 | 8 import claude-memory | ~280 | Low | PR3, PR4 |
| PR11 | 9 spaced-review (Play G) | ~280 | Low | none (independent) |

Note: design's original 9-slice plan flagged slice 3 (store-core, pre-split
estimate ~560 lines) and slice 7 (dedupe, pre-split estimate ~560 lines) as
High risk. Splitting each into two PRs (3→PR3+PR4, 7→PR8+PR9) brings every
individual PR to ≤360 lines with margin under the 400-line budget — this is
why the resulting per-PR risk is Low even though the underlying work was
originally High risk before the split.

### Suggested Work Units

| Unit | Goal | PR | Notes |
|------|------|----|-------|
| 1 | Shared similarity primitives, zero behavior change | PR1 | Regression net = existing mmr_test.go |
| 2 | write_hygiene config + budget=400 default | PR2 | Kill-switch: explicit 1500/false stick |
| 3 | Write-gate decision ladder + noop_threshold calibration | PR3 | Validation task 3.7 against obs #1662 real clusters |
| 4 | Wire store.Config across all storeNew call sites | PR4 | Kill-switch: gate-off byte-for-byte |
| 5 | write_gate envelope on handleSave | PR5 | RELATE path stays byte-identical |
| 6 | Fix #147 session-hook project override | PR6 | Independent, can reorder earlier |
| 7 | FTS 0-hit relaxation ladder | PR7 | Kill-switch: non-zero-hit path untouched |
| 8 | omnia dedupe dry-run/propose | PR8 | FTS pre-filter, never all-pairs |
| 9 | omnia dedupe --apply <cluster-id> | PR9 | No --apply all; stale-cluster-fails-safely |
| 10 | omnia import claude-memory | PR10 | Idempotent via gate; JSON import untouched |
| 11 | Play G review-due + gated context nudge | PR11 | Nudge default-OFF, byte-identical when off |
