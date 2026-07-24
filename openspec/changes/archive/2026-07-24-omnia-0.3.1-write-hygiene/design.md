# Design: Omnia v0.3.1 — Write Hygiene

## Technical Approach

Extend the ONE shared save core (`store.AddObservation`) with a deterministic pre-insert
write-gate, so every entry point (MCP `handleSave`, CLI `save`, `import`, backfill, sync) gets
identical dedup behavior with zero drift (the #140 lesson). Promote v0.3's Jaccard/tokenize
primitives out of `internal/mcp/mmr.go` into a new pure `internal/similarity` leaf that store, mcp
and cmd can all import (store MUST NOT import mcp). `dedupe` and `import claude-memory` are additive
offline CLI subcommands reusing the same primitives + FTS candidate blocking. Everything gates
behind `write_hygiene.enabled` (default-TRUE, D7 deviation): disabled == byte-for-byte v0.3.

## Architecture Decisions

### D1 — Shared similarity leaf
**Choice**: New `internal/similarity` package exporting `Tokenize(s) []string` and `Jaccard(a,b) float64`,
moved verbatim from mmr.go (regexp `[\p{L}\p{N}]+`, empty-set→0 rule preserved). `mmr.go` deletes its
copies and calls `similarity.*`. **Alternatives**: add to `internal/token` (rejected: token pkg is scoped
to count-estimation, cohesion); leave in mcp (rejected: store can't import mcp → cycle). **Rationale**:
pure stdlib leaf, importable by store/mcp/cmd without cycles; matches the "export shared primitives so
callers don't drift" convention. Existing MMR tests are the regression net (byte-identical behavior).

### D2 — Write-gate placement
**Choice**: Gate runs INSIDE `store.AddObservation`'s core (after the existing topic_key upsert and the
15-min hash window, before plain insert). Config flows via new `store.Config` fields (duplicated from
`config.WriteHygiene`, populated by cmd/omnia — same pattern as `ProceduralTrustThreshold`/
`ContextTokenBudget`, since store must not import config). A new `store.SaveObservation(p) (SaveResult,
error)` returns the decision; `AddObservation(p) (int64, error)` stays as a thin wrapper for callers that
don't need the envelope. **Alternatives**: gate in `handleSave` (rejected: CLI/import/HTTP paths would
drift — the #140 failure mode). **Rationale**: single decision point = uniform behavior across all save
entry points. Candidate retrieval reuses `FindCandidates`' shape: FTS `MATCH` on title (OR-of-terms),
same project+scope, BM25 floor, top-`candidate_limit` (default 10) — never all-pairs.

### D3 — Decision ladder (deterministic)
Order inside the core (gate only runs when `enabled` AND steps 1–2 didn't already dedupe):

| Step | Condition | Action |
|------|-----------|--------|
| 1 topic_key (existing) | topic_key matches a row | in-place UPDATE, `revision_count++` (UNCHANGED; explicit intent) |
| 2 hash window (existing) | exact normalized-hash + same proj/scope/type/title, ≤15 min | `duplicate_count++` (UNCHANGED) |
| 3 NOOP | `Jaccard ≥ noop_threshold` (0.98) vs a candidate | skip insert, `duplicate_count++` on target, return existing ID |
| 4 AUTO-UPDATE | `Jaccard > update_threshold` (strict `>` 0.9) AND passes shrink-guard | in-place UPDATE of the matched candidate, `revision_count++` |
| 5 SAVE+RELATE | candidate surfaced but `Jaccard ≤ 0.9` | insert new row + existing `FindCandidates`→`judgment_required` flow |
| 6 SAVE | no candidate | plain insert |

**Superset/shrink test** (step 4, similarity-triggered only): accept the new content as canonical iff
`len(normNew) ≥ shrink_guard × len(normOld)` (default 0.9). If shorter, downgrade step 4 → NOOP (keep the
richer existing row, `duplicate_count++`). The topic_key path (step 1) keeps its unconditional overwrite —
the guard applies ONLY to the new similarity branch, so the gate EXTENDS, never duplicates, existing
logic. **0.9 boundary**: `Jaccard == 0.9` is NOT an update (falls to step 5), per business rule ">0.9".

### D4 — Envelope contract
`handleSave` adds one object to the response `extra` map, consistent with existing `id`/`candidates`:
```
write_gate: { decision: "noop"|"update"|"relate"|"save", target_id: <int?>, similarity: <float>, reason: <string> }
```
NOOP/UPDATE set `target_id` to the existing #N (business rule: NOOP returns existing ID; `extra["id"]`
also = target). RELATE keeps the current `judgment_required`+`candidates` array unchanged (write_gate is
informational). SAVE omits `target_id`.

### D5 — Config `write_hygiene` block
```yaml
write_hygiene:
  enabled: true          # DEFAULT-TRUE
  noop_threshold: 0.98
  update_threshold: 0.9
  shrink_guard: 0.9
  candidate_limit: 10
```
Default-TRUE via a `writeHygieneEnabledKeyPresent(data)` probe (mirror `recallEnabledKeyPresent`, inverted):
`if !cfg.WriteHygiene.Enabled && !keyPresent { cfg.WriteHygiene.Enabled = true }` — so an explicit
`enabled: false` sticks. Thresholds use the simple zero-check default idiom (like `Diversity.Lambda`).

### D6 — injection.budget default 1500 → 400
**Choice**: change the applyDefaults literal (config.go:681) to 400. **Rationale**: eval obs #1659 —
budget=300 gave −70% tokens / 3.3× quality-per-1k / no accuracy regression; 400 keeps that win with margin
under the ~750 structural ceiling. **Migration**: `injectionBudgetMaxTokensKeyPresent` means any operator
who explicitly wrote `1500` keeps it (byte-for-byte); only never-set installs get 400. `budget.enabled`
stays default-false, so this only bites once opted in. `context_budget` stays 1500 (unchanged).

### D7 — FTS 0-hit relaxation
**Choice**: bounded, deterministic ladder inside `store.Search`'s lexical lane (single entry point →
composes with the recall/hybrid path automatically). Fires ONLY when strict-AND returns 0 rows (strictly
additive — never removes/reorders existing hits):

| Step | Query shape | Fires when |
|------|-------------|-----------|
| 0 | `"w1" "w2"` (strict AND, today) | baseline |
| 1 | drop EN+ES stopwords from the AND | step 0 = 0 rows AND ≥1 term removed & ≥1 remains |
| 2 | OR-mode of surviving content terms | step 1 = 0 rows |

Transparency: `SearchOptions` gains an optional `Diag *SearchDiag` (nil-safe); store fills
`{Relaxed, Step}`, `handleSearch` surfaces `extra["fts_relaxed"]`. Rollback flag `recall.fts_relax_on_zero`
(default true). **Rationale**: reproduced killing real queries twice (obs #1659/#1662); additive + bounded
is safe default-on.

### D8 — #147 session hooks
**Choice**: mirror #146/#403/#413 exactly — replace bare `resolveWriteProject()` in `handleSessionEnd`
and `resolveSessionStartProject` (empty-dir branch) with `resolveWriteProjectWithProcessOverride(cfg.DefaultProject)`;
both handlers already hold `cfg`. Explicit-directory and basename-fallback branches unchanged. Test pattern
reuses `update_project_resolution_test.go`'s cfg.DefaultProject assertions.

### D9 — dedupe-scan
**Choice**: FTS-blocked candidate generation (reuse `ScanProject`/`FindCandidates` FTS MATCH, never
all-pairs) → union-find clustering over pairs with content `Jaccard ≥ 0.9` → per component: canonical =
NEWEST (max created_at, tie-break max id). `--dry-run` (default) prints deterministic proposal (cluster id,
canonical #N, losers, scores; `--json` optional). `--apply` per cluster: for each loser insert a JUDGED
`memory_relations` row `canonical supersedes loser` (marked_by system:dedupe) + soft-delete the loser
(`deleted_at`) = "referenced history" (queryable/restorable, out of active recall). **Performance @1660**:
~n indexed FTS queries × ≤10 candidates (not n²≈2.75M) — sub-few-seconds offline.

### D10 — import claude-memory
**Choice**: discover `<dir>/*.md`, skip `MEMORY.md`; parse frontmatter with the existing `yaml.v3` dep
(split leading `---`…`---`, unmarshal, body = remainder — no new deps). Mapping:

| Claude field | Omnia |
|--------------|-------|
| `description` (fallback `name`) | title |
| body markdown | content |
| `metadata.type` | type via table: reference→discovery, note→discovery, default→manual |
| `name` slug | `topic_key = "claude-memory/"+name` |
| — | `source = "claude-memory"` (→ trust_tag via provenance foundation) |

**Idempotency**: the `topic_key` = slug drives the EXISTING topic_key upsert — re-import = in-place update
(`revision_count++`), never a new row (0 dups by construction), and it routes through the gate anyway.

### D11 — Spaced review resurfacing (Play G)
**Choice**: EXTEND the existing review machinery — DO NOT duplicate it. Omnia already has `review_after`
on observations (set at insert for `decayReviewAfterMonths` types: decision/policy/preference), a
`mem_review` MCP tool (`action=list`/`mark_reviewed`), `store.ObservationsNeedingReview` and
`store.MarkReviewed`. Play G adds only three thin surfaces:

1. **CLI `omnia review-due [--project X] [--json]`** — the missing CLI counterpart to `mem_review list`.
   Read-only wrapper over `ObservationsNeedingReview`. Compact output, NEVER contents: a count line, then
   IDs+titles grouped by project then type:
   ```
   5 memories due for review
   proj-a
     decision: #123 Title one, #145 Title two
     policy: #150 Title three
   Resolve: mem_review mark_reviewed <id>  (or delete if obsolete)
   ```
2. **Due-count nudge in `mem_context`** — gated DEFAULT-OFF via `review.due_nudge` (D7: it changes an
   existing tool's output). When on, `handleContext` adds `extra["review_due_count"]=N` + one text line
   from a new cheap `store.CountObservationsNeedingReview(project)` (COUNT query, no row hydration, never
   contents). Zero value false IS the default (no key-present probe needed, like `TypeLensConfig`).
3. **Resolution via existing paths only** — confirm = `MarkReviewed` (bumps `review_after` by type decay,
   already exists); obsolete = existing soft-delete / state change. NO new mutation surface.

**Alternatives**: a new `spaced_review` table + scheduler (rejected: `review_after` + decay map already
model the schedule); a new resolve tool (rejected: `mark_reviewed`/delete already cover it). **Rationale**:
deterministic, no LLM, zero schema change; independent slice that touches none of the write-gate files.

## Data Flow

    save call ──► store.AddObservation core
                    │ topic_key upsert? ── yes ─► UPDATE (revision++)
                    │ hash+15min? ──────── yes ─► duplicate_count++
                    │ [write_hygiene.enabled]
                    │   FTS candidates (similarity leaf) ─► Jaccard ladder
                    │     ≥0.98 NOOP · >0.9 UPDATE · ≤0.9 RELATE · none SAVE
                    └─► SaveResult{ID,decision,target,similarity} ─► handleSave envelope write_gate{}

## File Changes

| File | Action | Description |
|------|--------|-------------|
| `internal/similarity/similarity.go` | Create | exported Tokenize/Jaccard (moved from mmr.go) |
| `internal/mcp/mmr.go` | Modify | call `similarity.*`; delete local copies |
| `internal/store/store.go` | Modify | write-gate in AddObservation core; SaveObservation/SaveResult; Config fields; FTS relax ladder in Search + SearchDiag |
| `internal/config/config.go` | Modify | WriteHygieneConfig + probe; budget default 400; relax flag |
| `internal/mcp/mcp.go` | Modify | handleSave write_gate envelope; #147 session-hook process override; fts_relaxed |
| `cmd/omnia/dedupe.go` | Create | offline cluster/propose/apply subcommand |
| `cmd/omnia/import_claude_memory.go` | Create | frontmatter import routed through gate |
| `cmd/omnia/review_due.go` | Create | `omnia review-due` compact due-list (no contents) |
| `internal/store/store.go` (review) | Modify | add `CountObservationsNeedingReview(project)` |
| `internal/config/config.go` (review) | Modify | `review.due_nudge` flag (default-off) |
| `internal/mcp/mcp.go` (context) | Modify | gated due-count nudge in `handleContext` |
| `cmd/omnia/main.go` | Modify | dispatch `dedupe`, `import claude-memory`, `review-due` |

## Testing Strategy

| Layer | What | Approach (strict TDD, RED first) |
|-------|------|-----------|
| Unit | similarity leaf | Jaccard values, empty→0, tokenize; MMR tests = regression net |
| Unit | config | default enabled=true; explicit false sticks; budget=400; explicit 1500 sticks |
| Unit | gate | kill-switch OFF = byte-for-byte; NOOP/UPDATE/RELATE/SAVE; shrink-guard; 0.9 boundary; determinism |
| Unit | FTS relax | 0-hit→relaxed; stopword-only; diag transparency; flag off = no-op |
| Unit | #147 | cfg.DefaultProject honored on start/end (mirror #403 tests) |
| Integration | dedupe | dry-run golden output; --apply supersede+soft-delete; canonical=newest; idempotent re-apply |
| Integration | import | re-run = 0 dups (topic_key upsert); skip MEMORY.md; type map; provenance tag |
| Unit/Integration | spaced-review (G) | nothing-due quiet path; due listing grouped (no contents); nudge gate-off = byte-identical mem_context; resolution round-trip (mark_reviewed bumps review_after) |

## Migration / Rollout

`write_hygiene.enabled:false` reverts to v0.3's 15-min hash dedup. dedupe/import are additive offline
subcommands. injection.budget default change is key-present-guarded (explicit configs untouched). No schema
migration (reuses observations/memory_relations/deleted_at).

## Delivery Order (stacked-to-main, ≤400 lines/PR — 9 slices)

1. `internal/similarity` leaf (refactor, no behavior change).
2. config: `write_hygiene` block + budget 400.
3. store: write-gate core + SaveResult.
4. mcp: envelope transparency + RELATE wiring.
5. fix: #147 session-hook process override (independent, can ship early).
6. fix: FTS 0-hit relaxation ladder (independent).
7. cmd: `omnia dedupe`.
8. cmd: `omnia import claude-memory`.
9. spaced-review / Play G: `omnia review-due` + gated mem_context nudge (independent — touches no gate files).

## Open Questions

- [ ] `noop_threshold` 0.98 vs 0.95 — validate on real-save replay (obs #1662 clusters) during tasks/apply.
- [ ] dedupe `--apply` scope: all-clusters vs per-cluster-id gating — confirm CLI ergonomics in sdd-tasks.
