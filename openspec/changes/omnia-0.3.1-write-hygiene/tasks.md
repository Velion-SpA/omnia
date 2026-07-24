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

- [ ] 3.1 RED: add `internal/store/write_gate_test.go`. First case: kill-switch
      off (`write_hygiene.enabled:false`) with identical content saved twice →
      TWO rows exist, byte-for-byte v0.3 hash-window/topic_key behavior only —
      confirm this fails against not-yet-existing gate wiring.
- [ ] 3.2 RED (same file): add failing cases for the full ladder ahead of
      implementation: NOOP (Jaccard ≥0.98 → skip insert, `duplicate_count++`,
      returns existing ID); AUTO-UPDATE (Jaccard >0.9 → in-place UPDATE,
      `revision_count++`); 0.9 boundary (Jaccard == exactly 0.9 → falls to
      SAVE+RELATE, NOT update); shrink-guard (`len(normNew) <
      shrink_guard*len(normOld)` on a similarity-triggered step-4 match →
      downgrades to NOOP, keeps richer existing row); SAVE+RELATE (candidate
      ≤0.9 → insert + existing `FindCandidates`/`judgment_required` flow
      unchanged); SAVE (no candidate → plain insert); determinism (same input
      twice with gate on, non-hash/non-topic_key path → same classification).
- [ ] 3.3 GREEN: add `store.Config` fields (`WriteHygieneEnabled`,
      `NoopThreshold`, `UpdateThreshold`, `ShrinkGuard`, `CandidateLimit`) next
      to `ProceduralTrustThreshold`/`ContextTokenBudget` in
      `internal/store/store.go`.
- [ ] 3.4 GREEN: add `SaveResult` type (`ID int64`, `Decision string`,
      `TargetID *int64`, `Similarity float64`, `Reason string`) and decision
      constants (`noop`/`update`/`relate`/`save`).
- [ ] 3.5 GREEN: implement `SaveObservation(p AddObservationParams) (SaveResult,
      error)` inside the existing `AddObservation` core — gate runs AFTER the
      existing topic_key upsert (step 1, unchanged) and 15-min hash window
      (step 2, unchanged), ONLY when `WriteHygieneEnabled`. Candidate retrieval
      reuses `FindCandidates`'s shape (FTS `MATCH` on title, same
      project+scope, BM25 floor, top-`CandidateLimit`, never all-pairs), scored
      via `similarity.Tokenize`/`similarity.Jaccard` on normalized content.
- [ ] 3.6 GREEN: keep `AddObservation(p) (int64, error)` as a thin wrapper
      calling `SaveObservation` and returning `.ID` — no existing caller
      signature breaks.
- [ ] 3.7 Validation (write-gate calibration, obs #1662 real clusters): add a
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

Files: `internal/store/store.go` (modify), `internal/store/write_gate_test.go` (new), `internal/store/write_gate_replay_test.go` (new).
Est. lines: ~360.

---

## PR4 — store.Config wiring + write-gate integration validation

Design slice 3b · Branch `feat/write-gate-wiring` · Base: `main` (after PR3) ·
`type:feature` · Depends on: PR3 · Capability: write-gate

- [ ] 4.1 RED: add/extend a `cmd/omnia` integration test (or
      `internal/store` test using a config-loaded `Store`) asserting that a
      `write_hygiene:` block in `config.yaml` reaches `store.Config` fields at
      every `storeNew` call site — fails until wiring lands.
- [ ] 4.2 GREEN: at each of the 3 `storeNew` call sites in
      `cmd/omnia/main.go` (mirroring the existing `cfg.ContextTokenBudget =
      appCfg.Injection.ContextBudget.MaxTokens` pattern), add
      `cfg.WriteHygieneEnabled = appCfg.WriteHygiene.Enabled` and the four
      threshold/limit fields.
- [ ] 4.3 Kill-switch byte-for-byte check: with `write_hygiene.enabled:false`
      set explicitly in `config.yaml`, run the full existing dedup/save test
      suite (hash-window + topic_key tests) and confirm zero behavior change
      vs pre-PR3 baseline.
- [ ] 4.4 Docs: note in `DOCS.md`/config reference that `write_hygiene` is
      default-on and how to opt out.

Files: `cmd/omnia/main.go` (modify, 3 call sites), test file (modify/new), docs (modify).
Est. lines: ~200.

---

## PR5 — mcp: `write_gate` envelope + RELATE wiring

Design slice 4 · Branch `feat/write-gate-envelope` · Base: `main` (after PR4) ·
`type:feature` · Depends on: PR3, PR4 · Capability: write-gate

- [ ] 5.1 RED: add `internal/mcp/mcp_test.go` cases asserting `handleSave`'s
      response `extra` map gains `write_gate: {decision, target_id, similarity,
      reason}` for each of NOOP/UPDATE/RELATE/SAVE, with `target_id` set for
      NOOP/UPDATE and omitted for SAVE; NOOP/UPDATE also set `extra["id"]` to
      the target.
- [ ] 5.2 RED: assert RELATE keeps the existing `judgment_required` +
      `candidates` array byte-identical to pre-PR5 behavior (write_gate is
      additive/informational only on this path).
- [ ] 5.3 GREEN: modify `handleSave` in `internal/mcp/mcp.go` to call
      `store.SaveObservation` instead of `store.AddObservation`, and build the
      `write_gate` envelope object from the returned `SaveResult`.
- [ ] 5.4 Kill-switch check: with gate disabled (PR2/PR4 flag off), confirm
      `handleSave`'s envelope has NO `write_gate` key change vs current
      behavior (or an explicit `decision:"save"` no-op shape — pick one and
      lock it in a test).

Files: `internal/mcp/mcp.go` (modify), `internal/mcp/mcp_test.go` (modify).
Est. lines: ~220.

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

- [ ] 7.1 RED: add `internal/store` tests: strict-AND baseline with ≥1 hit →
      relaxation ladder MUST NOT fire (non-zero-hit-path kill-switch case);
      0 strict hits + stopword removal yields hits → `Diag.Relaxed=true,
      Step=1`; step 1 also 0 hits → OR-mode (step 2) tried, `Step=2`; all
      levels exhausted → empty result, `Diag` reflects exhaustion, no infinite
      retry.
- [ ] 7.2 RED: add config test — `recall.fts_relax_on_zero: false` → `Search`
      returns the pre-PR7 zero-hit behavior byte-for-byte (explicit gate-off
      case).
- [ ] 7.3 GREEN: add `recall.fts_relax_on_zero` (default true) to
      `internal/config/config.go` (zero-value-default idiom, default true via
      key-present probe since default differs from Go zero value).
- [ ] 7.4 GREEN: add `SearchDiag` struct (`Relaxed bool`, `Step int`) and an
      optional `Diag *SearchDiag` field on `SearchOptions`; implement the
      bounded ladder (strict AND → drop EN+ES stopwords → OR-of-terms) inside
      `Store.Search`, additive-only (never reorders/removes existing hits).
- [ ] 7.5 GREEN: surface `extra["fts_relaxed"]`/`extra["fts_relax_step"]` in
      `handleSearch` (`internal/mcp/mcp.go`) when `Diag.Relaxed`.

Files: `internal/store/store.go` (modify), `internal/config/config.go` (modify), `internal/mcp/mcp.go` (modify), test files (modify/new).
Est. lines: ~260.

---

## PR8 — `omnia dedupe` propose engine (dry-run)

Design slice 7a · Branch `feat/dedupe-scan` · Base: `main` (after PR7) ·
`type:feature` · Depends on: PR1 (similarity) · Capability: dedupe-scan

- [ ] 8.1 RED: add `cmd/omnia/dedupe_test.go`: bare `omnia dedupe` invocation
      leaves DB unchanged (propose-only default); FTS-blocked candidate
      pre-filter used (not O(n²) all-pairs) at a synthetic 1600+-row scale;
      union-find clusters pairs with Jaccard ≥0.9; canonical = newest
      (`created_at` max, tie-break max id) within each cluster; deterministic
      dry-run output (cluster id, canonical #N, losers, scores) and stable
      `--json` shape.
- [ ] 8.2 GREEN: create `cmd/omnia/dedupe.go`: reuse `Store.ScanProject`/
      `Store.FindCandidates` (FTS `MATCH`, never all-pairs) for candidate
      generation; union-find clustering over pairs with content
      `similarity.Jaccard ≥ 0.9`; per-cluster canonical selection; deterministic
      `--dry-run` (default) text + optional `--json` proposal output.
- [ ] 8.3 GREEN: wire `case "dedupe":` dispatch in `cmd/omnia/main.go`.
- [ ] 8.4 Docs: add `omnia dedupe` usage to `DOCS.md`/CLI help.

Files: `cmd/omnia/dedupe.go` (new), `cmd/omnia/dedupe_test.go` (new), `cmd/omnia/main.go` (modify), docs (modify).
Est. lines: ~300.

---

## PR9 — `omnia dedupe --apply <cluster-id>` (mutation)

Design slice 7b · Branch `feat/dedupe-apply` · Base: `main` (after PR8) ·
`type:feature` · Depends on: PR8 · Capability: dedupe-scan

- [ ] 9.1 RED: add cases to `cmd/omnia/dedupe_test.go`: `--apply <id>` on
      cluster A leaves cluster B fully untouched (explicit per-cluster
      isolation); **no `--apply all` flag exists — invoking `--apply` without
      a concrete cluster id errors with usage, per the resolved scope
      decision** (spec's original "(or explicit 'all')" wording is
      superseded); `--apply` on a cluster id invalidated since proposal
      (already applied, or an underlying observation changed) fails cleanly
      with no partial/silent mutation; idempotent re-apply of an already-applied
      cluster is a safe no-op/clear error, never a double-supersede.
- [ ] 9.2 RED: assert a successful `--apply <id>` inserts a JUDGED
      `memory_relations` row (`canonical supersedes loser`, `marked_by
      "system:dedupe"`) per loser, and soft-deletes each loser via
      `Store.DeleteObservationWithActor(loserID, false, "system:dedupe")`
      (referenced history: queryable/restorable, out of active recall) — while
      the canonical row is untouched.
- [ ] 9.3 GREEN: extend `cmd/omnia/dedupe.go` with `--apply <cluster-id>`
      flag parsing (reject bare `--apply`/`--apply all`), stale-cluster
      re-validation against current DB state before mutating, and the
      supersede+soft-delete mutation using the existing relation-insert +
      `DeleteObservationWithActor` primitives.
- [ ] 9.4 Docs: update `omnia dedupe` usage to document `--apply <cluster-id>`
      as the ONLY mutation path (no all-clusters shortcut).

Files: `cmd/omnia/dedupe.go` (modify), `cmd/omnia/dedupe_test.go` (modify), docs (modify).
Est. lines: ~260.

---

## PR10 — `omnia import claude-memory <dir>`

Design slice 8 · Branch `feat/import-claude-memory` · Base: `main` (after PR9) ·
`type:feature` · Depends on: PR3/PR4 (routes through the gate) · Capability: claude-memory-import

- [ ] 10.1 RED: add `cmd/omnia/import_claude_memory_test.go`: dir containing
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
- [ ] 10.2 GREEN: create `cmd/omnia/import_claude_memory.go`: discover
      `<dir>/*.md` skipping `MEMORY.md`; parse frontmatter with the existing
      `yaml.v3` dep (split leading `---`…`---`, unmarshal, body = remainder);
      map `description` (fallback `name`) → title, body → content,
      `metadata.type` via table (reference→discovery, note→discovery,
      default→manual), `name` slug → `topic_key = "claude-memory/"+name`,
      `source = "claude-memory"`; route every import through
      `store.SaveObservation`.
- [ ] 10.3 GREEN: wire `import claude-memory <dir>` dispatch — branch inside
      `cmd/omnia/main.go`'s existing `case "import":`/`cmdImport` on
      `os.Args[2]=="claude-memory"` before falling through to the existing
      `omnia import <file.json>` path.
- [ ] 10.4 Kill-switch/backward-compat check: `omnia import <file.json>`
      (existing JSON export/import) behaves byte-for-byte unchanged — add a
      regression test asserting the pre-existing `cmdImport` JSON path is
      untouched by the new dispatch branch.
- [ ] 10.5 Docs: add `omnia import claude-memory <dir>` usage + provenance
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
