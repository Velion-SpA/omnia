# Design: Omnia v0.3 — Context Economy

Status: proposed
Depends on: proposal.md (obs #1641)
Store: hybrid (engram topic_key `sdd/omnia-0.3-context-economy/design` + this file)

## 1. Context and Problem

Recall (FTS5 + semantic RRF fusion, v0.2 ranking, sentinel/signature lanes) is
mature. The entire gap is DOWNSTREAM, at INJECTION: everything that reaches the
agent is char/count-based, never token-aware.

- `internal/mcp/mcp.go handleSearch` selects a flat `limit=10` items and
  `truncate(content, 300)` each preview — no notion of an aggregate token cost.
- `internal/store/store.go FormatContext` assembles 4 buckets (sessions /
  prompts / pinned / recent observations) with per-item 200–300 char truncation
  and **ZERO aggregate cap** — a pre-existing unbounded-growth bug (many items,
  each individually bounded, sum without limit).
- `cmd/omnia/recall_fix.go` is the ONLY budget in the repo
  (`maxRecallFixTotalChars = 600`, char-based, forced-recall-on-error path).
- No token-estimation primitive exists anywhere (no tiktoken; CGO is banned).

v0.3 makes injection token-aware, fixes the uncapped-bucket bug, and adds three
economy plays (diversity, signal-gated activation, type-as-lens) — all gated,
default-OFF, byte-for-byte no-op when disabled (D7).

## 2. Architecture Approach (the load-bearing decision)

The v0.2 codebase already established the pattern this release extends: an
optional transformation over an already-fused/hydrated result set, living at the
**internal/mcp wiring layer**, expressed as a **pure, stable-partition re-sort
pass**, gated by a config flag, and a **byte-for-byte no-op when the flag is
off**. Two such passes already exist and are the template:

- `recall_ranking.go RankResults` — recency×importance×relevance re-sort.
- `anchor_adapter.go ApplyStalenessDownrank` — sinks stale-anchor rows.

Both partition pre-empted rows (`Rank == exactSentinelRank (-1000)` OR
`SignatureMatch`) OUT first, keep them first in original order, and never let
them compete. Both are `return results` no-ops when their gate is off or there's
nothing to do.

**v0.3 adds three more passes of the exact same shape, plus one shared leaf
primitive, plus one store-side budget, plus one bash classifier.** We do NOT
touch `internal/recall` (fusion) or `internal/store`'s FTS query — they stay
pure leaves (design D6). The new token math lives in a NEW pure leaf so every
injection path measures tokens identically and cannot drift.

### The injection pipeline after v0.3 (handleSearch, in order)

```
results = HydrateFusedResults(...) / store.Search(...)     // unchanged
results = RankResults(results, relevance, cfg.RecallRanking, now)      // v0.2
results = ApplyStalenessDownrank(results, anchorsByObs)               // v0.2 (gated)
results = ApplyTypeLens(results, lensType, cfg.Injection.TypeLens)    // v0.3 Play A
results = ApplyMMR(results, relevance, cfg.Injection.Diversity)       // v0.3 Play H
results = ApplyTokenBudget(results, cfg.Injection.Budget)            // v0.3 Play B  (LAST)
// ... existing display loop (300-char previews, lazy mem_get_observation footer)
```

Ordering rationale: ranking/staleness/type-lens decide **priority**; MMR removes
**redundancy**; the token budget does the **final trim to fit**. Budget MUST be
last so it trims the genuinely lowest-priority, non-redundant tail. MMR before
budget so the budget is never spent on near-duplicates. Every pass preserves
pre-emption identically, so their composition preserves it too.

## 3. Components

### 3.1 Slice 0 — token-estimation primitive (new leaf `internal/token`)

**Decision (location):** a NEW pure leaf package `internal/token`, NOT inside
`internal/recall`.
- Rationale: `internal/recall` has one job — RRF fusion. Token estimation is
  orthogonal. A dedicated leaf keeps both single-purpose AND — critically —
  lets `internal/store` import it (FormatContext needs it) without dragging in
  fusion. `internal/token` imports only the stdlib, so any package
  (`internal/store`, `internal/mcp`, `cmd/omnia`) can depend on it with zero
  cycle risk. This is the single anti-drift lever: one estimator, every path.

**API:**
```go
package token

// EstimateTokens approximates the BPE token count of text using the
// ~4-chars-per-token rule of thumb: ceil(runeCount / 4). Deterministic,
// stdlib-only, no CGO, no model download.
func EstimateTokens(text string) int

// TrimToBudget returns the longest prefix of items whose cumulative sizeOf
// stays <= budget (top-N complete, cut the rest — never a partial item),
// plus the tokens consumed. budget <= 0 returns (nil, 0). Generic and pure;
// knows nothing about pre-emption (callers handle that outside the budget).
func TrimToBudget[T any](items []T, sizeOf func(T) int, budget int) (kept []T, used int)
```

**Heuristic:** `(utf8.RuneCountInString(text) + 3) / 4` (ceil on rune count, not
byte count — UTF-8 safe). Chosen over word-based because char/4 is the
industry rule-of-thumb, is content-agnostic (works on code, prose, JSON), and
needs no tokenizer table.

**Accuracy — documented in package doc + a golden fixture test:** typically
within ±25% of true cl100k BPE for English/code; OVER-counts on
whitespace/punctuation-dense text; UNDER-counts on CJK (where BPE is ~1–2
chars/token but char/4 assumes 4). Documented explicitly so operators tuning the
budget know it is a conservative English/code estimate, not exact. A golden test
pins a small `(text → expectedTokens)` table so the heuristic can't silently
change.

### 3.2 Slice 1 — injection token budget (Play B)

**`internal/mcp` new pass** (mirrors RankResults):
```go
func ApplyTokenBudget(results []store.SearchResult, cfg config.TokenBudgetConfig) []store.SearchResult
```
- Gated no-op: `if !cfg.Enabled || cfg.MaxTokens <= 0 || len(results) == 0 { return results }`.
- **Pre-emption rides OUTSIDE the budget** (the mechanism): partition into
  `preempted` (`Rank == exactSentinelRank || SignatureMatch`) and `rest`.
  `preempted` rows are emitted in full and DO NOT consume budget. The budget
  applies only to `rest`:
  `kept, _ := token.TrimToBudget(rest, previewTokens, cfg.MaxTokens)`;
  `out = append(preempted, kept...)`. This satisfies the non-negotiable
  business rule ("sentinel + signature-match ALWAYS complete and OUTSIDE the
  budget") by construction and mirrors the existing partition passes.
- `previewTokens(r)` measures `token.EstimateTokens(truncate(r.Content, 300))`
  — the SAME preview string handleSearch renders — so the budget is honest
  about what actually reaches the agent (not the full untruncated content, which
  is lazy-loaded via `mem_get_observation`).
- Insertion: at the END of the handleSearch pass pipeline (after RankResults /
  ApplyStalenessDownrank / ApplyTypeLens / ApplyMMR), before the display loop.

**FormatContext budget** (`internal/store`, fixes the uncapped-bucket bug):
- `internal/store` imports `internal/token` (clean — token is a stdlib-only
  leaf). Add `ContextTokenBudget int` to `store.Config` (0 = disabled = today's
  uncapped behavior; the composition root sets it only when the flag is on).
- FormatContext gains a **priority allocation across its 4 buckets** from ONE
  shared budget, consumed in priority order (the pinned bucket = the
  pre-emption analog, never starved):
  1. **Pinned** (highest — the user explicitly kept these),
  2. Recent observations,
  3. Recent sessions,
  4. Recent prompts (lowest).
  Each bucket calls `token.TrimToBudget(items, itemTokens, remaining)` against
  the running remainder. Per-item 200/300-char truncation stays (bounds each
  line); the budget bounds the SUM — which is exactly the missing aggregate cap.
  When `ContextTokenBudget == 0`, FormatContext is byte-for-byte today's code.

**Shared primitive — no drift:** `cmd/omnia/recall_fix.go` migrates from
`maxRecallFixTotalChars = 600` + `truncate(out, 600)` to
`token.TrimToBudget(hitLines, token.EstimateTokens, budget)` with a
`maxRecallFixTokenBudget ≈ 150` (600/4, preserving today's size). All three
injection paths (mem_search, FormatContext, recall-fix) now measure tokens via
the ONE `token.EstimateTokens` — the anti-drift guarantee the proposal demands.

**Default budget value — 1500 tokens** (`injection.budget.max_tokens` and
`injection.context_budget.max_tokens`). Rationale: recall-fix's forced-injection
floor is ~150 tokens (600 chars / 4) — the minimum "proven fix" size. A
user-initiated search / a full-session context assembly can afford ~10× that
floor. 1500 tokens ≈ 6000 chars ≈ the current 10-item × 300-char preview payload
plus structure, so enabling the flag at the default barely changes today's
volume while making it token-BOUNDED (can't grow unbounded) and eval-tunable
DOWNWARD to prove economy. Flags default OFF, so 1500 only applies on opt-in.

### 3.3 Slice 2 — MMR diversity (Play H)

**`internal/mcp` new pass:**
```go
func ApplyMMR(results []store.SearchResult, relevance map[int64]float64, cfg config.DiversityConfig) []store.SearchResult
```
- Gated no-op when `!cfg.Enabled || len(results) < 2`.
- **Pre-empted rows bypass MMR** (partition out first, emit first, never
  dropped, never reordered) — identical mechanism to the other passes.
- **Similarity = token-set Jaccard** on lowercased word tokens:
  `|A∩B| / |A∪B|` over the two rows' preview token sets. Chosen over trigram
  because memory content is prose/structured text (not order-sensitive code
  fragments), Jaccard is O(n+m) cheap, deterministic, allocation-light, and its
  threshold is human-interpretable ("90% shared vocabulary ≈ duplicate"). No new
  embeddings — this is a read-time lexical pass only.
- **Greedy MMR reselection** over `rest`: start from the top-ranked row, then
  iteratively pick `argmax [ λ·rel(d) − (1−λ)·maxSim(d, selected) ]`, where
  `rel(d)` is the normalized relevance (reuse `MinMaxNormalizeRelevance`), or
  descending rank-position as the proxy when relevance is unavailable.
  Additionally **hard-drop** any candidate whose `maxSim(d, selected) ≥
  similarity_threshold` (near-identical → true duplicate; "no dupes").
- **Defaults:** `lambda = 0.7` (favor relevance, mild diversity — standard MMR
  default), `similarity_threshold = 0.9` (only near-verbatim rows are dropped;
  everything else is merely reordered). Both eval-tunable.
- Insertion: after type-lens, before the token budget (dedup BEFORE trim).

### 3.4 Slice 3 — signal-gated recall (Play O, bash)

Extends `plugin/claude-code/scripts/user-prompt-submit.sh`. This is bash, not
Go, and does NOT read config.yaml — so it is **gated by an env var
`OMNIA_SIGNAL_RECALL` (unset/`0` = OFF, the default)**, documented as the one
deliberate exception to the config.yaml surface (bash can't cheaply parse YAML,
and the existing hook idioms are env-driven).

- **Trigger heuristics (pure bash regex on the prompt text — no LLM):**
  - *New-topic:* not the first message AND the prompt opens an imperative work
    item — matches `^(implement|add|create|fix|refactor|build|migrate|write|
    haceme|implementá|agregá|arreglá|creá|refactorizá)\b` (EN + ES) → the agent
    likely needs prior context on this task.
  - *Uncertainty:* the prompt contains uncertainty/question markers —
    `\b(how do|why|what'?s|not sure|isn'?t working|failing|stuck|no sé|cómo|
    por qué|no funciona|falla)\b` or ends with `?` → the agent should SEARCH
    memory before answering.
- **What it emits:** an INSTRUCTION (systemMessage / additionalContext), NOT
  auto-injected results — "This looks like a new task / an uncertain question;
  call `mem_search` with the key terms before proceeding." Rationale: a generic
  free-text search on arbitrary prompt text is noisy (exactly why recall-fix is
  restricted to the signature lane). Nudging the agent to run a good search is
  safer than injecting a bad one. Mirrors the first-message ToolSearch nudge
  idiom already in this script.
- **Dedup-marker reuse:** reuse `post-tool-error-recall.sh`'s per-session marker
  idiom — hash `(trigger + normalized query terms)` into
  `${TMPDIR:-/tmp}/omnia-signal-recall-<session>-<hash>`; if the marker exists,
  stay silent (no repeat nudge for the same topic in a session).
- **Failure-silent:** any error, missing env, or non-match → `echo '{}'; exit 0`
  (never block the prompt), matching the script's existing MUST-exit-0 contract.

### 3.5 Slice 4 — type-as-lens (Play A)

**Decision (where the lens applies): RANKING LAYER (a boost pass), NOT the
options-building boundary (a hard `opts.Type` filter).**
- Rationale: the codebase's ranking philosophy is explicitly "downrank/boost,
  NEVER hard-filter" — `ComputeRecency` ("Recency Decay Never Hard-Filters"),
  `AdaptiveFloor` (widen so a sparse set isn't filtered to nothing),
  `StalenessPenaltyFor` (downrank, never exclude). A situational type inference
  is a heuristic guess; making it a hard `opts.Type` filter would EXCLUDE
  relevant non-matching-type memories on a guess — the wrong failure mode.
  A boost re-orders without excluding, matching every neighbouring primitive.

**`internal/mcp` new pass** (mirrors ApplyStalenessDownrank's lift/sink shape):
```go
func InferLensType(query, explicitType string) string      // "" = no lens
func ApplyTypeLens(results []store.SearchResult, lensType string, cfg config.TypeLensConfig) []store.SearchResult
```
- **Explicit-type-filter-wins:** `InferLensType` returns `""` whenever the
  caller passed an explicit `type` argument (`opts.Type != ""`) — the user
  already expressed intent, so the lens stands down. With `lensType == ""`,
  `ApplyTypeLens` is a no-op.
- Gated no-op when `!cfg.Enabled || lensType == "" || len(results) == 0`.
- **Boost = stable partition** `[preempted, matchesLens, rest]` → emit
  `preempted ++ matchesLens ++ rest`. Rows of the situational type are lifted
  above non-matching rows WITHOUT excluding anything and WITHOUT disturbing
  pre-emption — the exact mirror of `ApplyStalenessDownrank` (which sinks;
  this lifts). Relative order inside each partition is preserved.
- **Signal → type mapping (`InferLensType`, EN + ES regex on the query):**

  | Query signal (regex, case-insensitive)                         | Lens type      |
  |----------------------------------------------------------------|----------------|
  | `error, panic, exception, crash, stack ?trace, falla, fallo`   | `bugfix`       |
  | `fix, broken, not working, no funciona, arregl`                | `bugfix`       |
  | `decide, decision, tradeoff, choose, elegir, decidir`          | `decision`     |
  | `architecture, design, pattern, arquitectura, patrón, diseño`  | `architecture` |
  | `how to, steps, procedure, cómo, pasos, procedimiento`         | `pattern`      |
  | (no match)                                                     | `""` (no lens) |

  First match wins; the table is a small ordered rule list. This is a heuristic,
  eval-measured, opt-in — not a claim of correct classification.
- Insertion: before MMR/budget (it changes which rows are "top").

## 4. Config Surface

New parent block `injection` in config.yaml, sub-gates siblings to
`recall` / `structural_forgetting` / `procedural`, ALL default-OFF:

```yaml
injection:
  budget:                       # Play B — mem_search token budget
    enabled: false
    max_tokens: 1500
  context_budget:               # Play B — FormatContext / mem_context (fixes uncapped bug)
    enabled: false
    max_tokens: 1500
  diversity:                    # Play H — MMR
    enabled: false
    lambda: 0.7
    similarity_threshold: 0.9
  type_lens:                    # Play A — situational type boost
    enabled: false
# Play O (signal-gated recall) is a bash hook, gated by env OMNIA_SIGNAL_RECALL=1
# (default off) — NOT config.yaml (the hook does not parse YAML).
```

Go structs in `internal/config/config.go` (mirroring RankingConfig's shape):
```go
type InjectionConfig struct {
    Budget        TokenBudgetConfig `yaml:"budget"`
    ContextBudget TokenBudgetConfig `yaml:"context_budget"`
    Diversity     DiversityConfig   `yaml:"diversity"`
    TypeLens      TypeLensConfig    `yaml:"type_lens"`
}
type TokenBudgetConfig struct { Enabled bool `yaml:"enabled"`; MaxTokens int `yaml:"max_tokens"` }
type DiversityConfig struct {
    Enabled bool `yaml:"enabled"`
    Lambda float64 `yaml:"lambda"`
    SimilarityThreshold float64 `yaml:"similarity_threshold"`
}
type TypeLensConfig struct { Enabled bool `yaml:"enabled"` }
```
- `Config.Injection InjectionConfig yaml:"injection"`.
- `applyDefaults`: `MaxTokens → 1500`, `Lambda → 0.7`,
  `SimilarityThreshold → 0.9` when zero (mirroring the Ranking-weights default
  idiom — `Enabled` never gets a default; its zero value IS the default OFF).
- `MCPConfig` gains `Injection config.InjectionConfig` (value type, zero = all
  off — same convention as `RecallRanking` / `StructuralForgetting`).
- `store.Config` gains `ContextTokenBudget int`; `cmd/omnia` sets it to
  `cfg.Injection.ContextBudget.MaxTokens` only when
  `cfg.Injection.ContextBudget.Enabled`, else 0.

## 5. The pre-emption INVARIANT (cross-cutting, non-negotiable)

Every new pass — `ApplyTokenBudget`, `ApplyMMR`, `ApplyTypeLens` — MUST preserve
the guarantee that a `Rank == -1000` sentinel row and a `SignatureMatch` row are
(a) always present in the output and (b) always ordered before any
non-pre-empted row, under ANY parameters (tiny budget, aggressive λ, low
threshold, hostile lens type). This is enforced by a SINGLE shared table-driven
test (`preemption_invariant_test.go`) that runs each pass against an adversarial
input containing both pre-empted kinds and asserts the invariant — the same
guarantee `RankResults` and `ApplyStalenessDownrank` already uphold. The
mechanism is uniform: partition pre-empted OUT first, keep in full, budget/MMR/
lens operate only on `rest`.

## 6. Test Strategy per Slice (strict TDD — RED first)

- **Slice 0:** RED `EstimateTokens` golden table (`empty→0`; ascii; unicode/CJK
  documented tolerance); `TrimToBudget` (whole-item only, never partial;
  `budget<=0 → nil`; huge budget → all; exact-fit boundary).
- **Slice 1 (search):** RED budget trims a 20-item set to fit; **no-op
  byte-for-byte** golden equality when `Budget.Enabled=false`; **pre-emption
  invariant** (sentinel+signature kept & first under `max_tokens=1`); recall-fix
  token-parity (same estimator, ~same output size as the old 600-char cap).
- **Slice 1 (FormatContext):** RED aggregate stays ≤ budget when enabled;
  **uncapped-bug regression** (large dataset → bounded output; today's code
  fails this); pinned never dropped for prompts; **no-op-off** byte-for-byte
  (today's uncapped output when `ContextTokenBudget=0`).
- **Slice 2 (MMR):** RED two near-identical rows → one dropped/sunk; distinct
  rows untouched; λ-boundary ordering; pre-emption invariant; no-op-off.
- **Slice 4 (type-lens):** RED error-signal query lifts `bugfix` rows above
  others; explicit `opts.Type` → lens stands down (no reorder); pre-emption
  invariant; no-op-off.
- **Slice 3 (signal-gated, shell):** RED new-topic prompt → nudge once; repeat
  same topic → deduped silent; uncertainty prompt → nudge; benign prompt → `{}`;
  malformed stdin → `{}` exit 0; `OMNIA_SIGNAL_RECALL` unset → always `{}`.
- **Shared:** the pre-emption invariant test covers all three Go passes in one
  table.
- Every slice runs under `CGO_ENABLED=0 go test ./...` + `go build ./...` +
  `go vet ./...` + `gofmt -l` (project TDD gate, obs #1638).

## 7. Delivery Order and PR Boundaries (stacked-to-main, ≤400 changed lines)

1. **PR1 — Slice 0** `internal/token` leaf (EstimateTokens + TrimToBudget +
   golden tests + package doc). No wiring, no behavior change; everything
   depends on it. (~200 lines)
2. **PR2 — Slice 1a** `ApplyTokenBudget` + handleSearch wiring + `InjectionConfig`
   + defaults + `MCPConfig.Injection` + `cmd/omnia` wiring + recall-fix migrated
   to the shared token primitive + tests (budget, no-op-off, pre-emption
   invariant, recall-fix parity). (~350 lines)
3. **PR3 — Slice 1b** FormatContext token budget + `store.Config.ContextTokenBudget`
   + `cmd/omnia` wiring + tests (uncapped-bug regression, priority allocation,
   no-op-off). Depends on PR1. (~250 lines)
4. **PR4 — Slice 2 (Play H)** `ApplyMMR` + Jaccard similarity + wiring + config +
   tests. (~350 lines)
5. **PR5 — Slice 4 (Play A)** `InferLensType` + `ApplyTypeLens` + wiring + config
   + explicit-filter-wins + tests. (~300 lines)
6. **PR6 — Slice 3 (Play O)** `user-prompt-submit.sh` signal classifier + env
   gate + dedup markers + manual self-check + shell tests. Bash-only, isolated.
   (~200 lines)

Order rationale: 0 first (foundation everything imports). Then 1 (core value:
token budget + the uncapped-bucket bug fix). Then H and A as lower-risk add-ons
(proposal Q2). O last — bash, fully isolated, zero Go coupling. Each PR sits
behind its own flag, is independently revertable (D7), and stays ≤400 changed
lines (v0.2 guideline). Each carries a `type:*` label and references the
approved issue (obs #1638 governance); a fresh adversarial review runs on every
diff before PR.

## 8. ADR-style decisions

- **ADR-1 — Estimator location = new leaf `internal/token`, not `internal/recall`.**
  Keeps fusion single-purpose; lets `internal/store` import it without cycles;
  one estimator = zero drift. *Rejected:* folding into `internal/recall` (muddies
  fusion, and `internal/store` importing recall would be a layering smell).
- **ADR-2 — char/4 (runes/4) heuristic, no deps, accuracy documented.**
  *Rejected:* word-based (content-specific), vendored BPE tables (weight, CGO
  temptation, upkeep).
- **ADR-3 — Composable wiring-layer passes over touching recall/store cores.**
  Mirrors RankResults/ApplyStalenessDownrank; keeps the two leaves pure (D6).
  *Rejected:* mutating `recall.Fuse` or `store.Search` (breaks the leaf boundary
  and the byte-for-byte-off guarantee).
- **ADR-4 — Pre-emption via partition-OUTSIDE-budget.** Sentinel/signature kept
  in full and excluded from budget accounting; the uniform mechanism across all
  passes; enforced by one shared invariant test. *Rejected:* keeping pre-empted
  rows but counting their tokens (could starve ordinary rows on a tiny budget).
- **ADR-5 — MMR similarity = token-set Jaccard; λ=0.7 + hard θ=0.9 drop.**
  Cheap, deterministic, interpretable on prose; no new embeddings. *Rejected:*
  trigram (costlier, less interpretable), cosine on embeddings (needs vectors at
  read time — expensive, defeats the "no new embeddings" constraint).
- **ADR-6 — Type-lens as a ranking-layer BOOST pass, not an options-boundary
  hard filter; explicit-filter-wins.** Matches the codebase's "never
  hard-filter" philosophy; a heuristic guess must not exclude results.
  *Rejected:* setting `opts.Type` pre-search (excludes relevant memories on a
  guess); folding a term into `RankScore` (couples to RecallRanking's gate,
  breaks independent toggling).
- **ADR-7 — Signal-gated recall = env-gated bash NUDGE, not YAML, not
  auto-inject.** The hook can't cheaply read YAML; a generic free-text search is
  noisy, so nudge-to-search beats inject-bad-results. *Rejected:* running
  `omnia search` inline (noise, the reason recall-fix is signature-only).
- **ADR-8 — FormatContext priority allocation (pinned first) fixes the uncapped
  bug via `store.Config.ContextTokenBudget`.** Pinned = the pre-emption analog,
  never starved. *Rejected:* fixed per-bucket percentages (can starve pinned to
  feed low-value prompts).
- **ADR-9 — Default budget 1500 tokens (~10× the recall-fix 150-token floor);
  all flags default-off.** Bounded but non-disruptive at opt-in; eval-tunable
  down to prove economy.
- **ADR-10 — One shared estimator across mem_search, FormatContext, and
  recall-fix.** The explicit anti-drift guarantee the proposal requires.

## 9. Risks and Open Questions

- **char/4 under-counts CJK** — a Spanish/English corpus is fine; documented, and
  the budget is conservative (over-counts Latin text). *Mitigation:* documented
  tolerance + eval measures real per-token quality.
- **Type-lens partition boost is blunt** — it lifts ALL matching-type rows above
  non-matching regardless of relevance magnitude. Acceptable for an opt-in
  situational lens; if eval shows over-boosting, a follow-up can soften it to a
  scored term. *Tracked as the main tuning risk.*
- **MMR/lens/budget ordering interactions** — mitigated by each pass being an
  independent gated no-op and by the shared pre-emption invariant test; enabling
  a subset never breaks the others.
- **Open (eval-gated) tuning:** exact `max_tokens` (1500?), `lambda` (0.7?),
  `similarity_threshold` (0.9?), and the type-lens boost strength all need one
  empirical pass against the live corpus via `internal/eval` before any flag is
  recommended ON — same "seed values need one tuning pass" posture v0.2's
  procedural/ranking configs already carry.
- **Success measure:** v0.2's per-token quality eval must hold/improve at lower
  token cost with flags ON; all flags OFF must be byte-for-byte v0.3 == v0.2.
```
