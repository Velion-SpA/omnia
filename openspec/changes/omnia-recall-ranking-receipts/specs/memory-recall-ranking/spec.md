# Memory Recall Ranking & Receipts Specification (NEW capability)

## Purpose

Extend recall fusion (`internal/recall.Fuse`, RRF over FTS5 lexical + semantic cosine hits, `internal/mcp/recall_adapter.go` wiring) with an optional recency × importance × relevance ranking pass (Generative Agents formula), and expose a per-hit score breakdown ("receipt") for transparency. `internal/recall` stays a pure, dependency-free leaf (design D6); ranking and receipt composition happen at the wiring boundary (`internal/mcp`, `internal/store`), same seam as `HydrateFusedResults`/`RecallScopeFilter`.

## Requirements

### Requirement: Ranking Combines Relevance, Recency, and Importance
When `recall.ranking.enabled` is `true`, the system MUST compute a final ranking score per result from three normalized [0,1] components — relevance (`recall.Result.Score` / RRF, or FTS5 rank when semantic is off), recency (decay of `Observation.UpdatedAt`), importance (heuristic weight by `Observation.Type`) — and MUST re-sort `mem_search`/`omnia search` output by this score.

#### Scenario: Strong relevance gap still wins
- GIVEN two candidates with a large relevance-score gap
- WHEN ranking is enabled with default weights
- THEN the notably more relevant result MUST still rank first

#### Scenario: Recency breaks a near-tie
- GIVEN two candidates with near-identical relevance, one `UpdatedAt` far more recent
- WHEN ranking is enabled
- THEN the more recent candidate MUST rank higher

### Requirement: Importance Heuristic By Observation Type
The system MUST assign a default importance weight per `Observation.Type`, with `decision`/`architecture` weighted higher than session-chatter types (`tool_use`, `file_read`, `search`), and MUST allow per-type overrides via config.

#### Scenario: Decision outranks chatter at equal relevance/recency
- GIVEN a `decision` and a `tool_use` observation with equal relevance and `UpdatedAt`
- WHEN ranking is enabled with default weights
- THEN the `decision` observation MUST rank first

### Requirement: Recency Decay Never Hard-Filters
The system MUST compute recency as a monotonically decreasing function of elapsed time since `UpdatedAt`, controlled by a configurable half-life (`recall.ranking.recency_half_life_days`), and MUST NOT let recency alone exclude a highly relevant, highly important old memory.

#### Scenario: Old high-importance decision is not excluded
- GIVEN a `decision` older than the recency half-life with high relevance
- WHEN ranking is enabled
- THEN it MUST still appear in results and MAY rank below a fresher, lower-importance match

### Requirement: Configurable Weights With Safe Defaults
The system MUST expose `recall.ranking.enabled` (bool, default `false`) and per-component weights (`recall.ranking.weights.recency|importance|relevance`, default `1.0` each) in `internal/config.RecallConfig`, following the same `applyDefaults` pattern as `rrf_k`/`dense_k`/`strong_floor`/`base_floor`.

#### Scenario: Config omits ranking section
- GIVEN a `config.yaml` with no `recall.ranking` section
- WHEN Omnia loads config
- THEN `recall.ranking.enabled` MUST default to `false` and weights MUST default to `1.0`

### Requirement: Backward-Compatible Default Behavior
When `recall.ranking.enabled` is `false` (default), `mem_search`/`omnia search` order and scores MUST be byte-for-byte identical to today's relevance-only `recall.Fuse` output. This requirement gates every other requirement in this spec.

#### Scenario: Ranking disabled preserves existing order
- GIVEN `recall.ranking.enabled` is unset or `false`
- WHEN `mem_search` runs a query
- THEN result order and scores MUST match pre-change `recall.Fuse` output exactly

### Requirement: Per-Hit Score Breakdown ("Receipt")
The system MUST support an explain mode (`mem_search` boolean arg `explain`; CLI `omnia search --explain`) that attaches to each hit: lexical (FTS5 rank / exact-match flag), semantic (cosine score or null), fusion (RRF score), recency term, importance term, final score, and a `staleness_penalty` field defaulting to `null`/`0` — reserved for `memory-structural-forgetting`'s downrank (obs #1595, Requirement 6) to populate later without a schema change.

#### Scenario: Explain mode returns full breakdown
- GIVEN `explain=true` with ranking enabled
- WHEN results are returned
- THEN each hit MUST include lexical, semantic, fusion, recency, importance, final score, and `staleness_penalty`

#### Scenario: Explain omitted by default
- GIVEN a `mem_search` call without `explain`
- WHEN results are returned
- THEN no breakdown fields MUST be present (unchanged response shape)

### Requirement: Ranking and Explain Degrade Gracefully
If any ranking or breakdown component cannot be computed (e.g. semantic recall disabled, missing `UpdatedAt`), the system MUST null/omit that specific component rather than failing the search or ranking pass.

#### Scenario: Semantic disabled, explain still returns partial breakdown
- GIVEN semantic recall is disabled (lexical-only mode)
- WHEN `explain=true` is requested
- THEN the breakdown MUST still return with `semantic` null and other components populated

## Coverage
Happy paths, edge cases (near-tie recency, old-but-important, config omitted, semantic disabled), backward compatibility (ranking-off byte-identical), and error states (graceful degradation, never a hard failure) are all covered above.

## Notes / Assumptions
No dedicated `proposal.md` exists for this slice: per locked 0.2 scope (obs #1592, #1598) this is a "light" slice going spec → tasks directly, no design phase. No pre-existing `openspec/specs/` baseline in this repo — this is a FULL new spec, matching the `memory-structural-forgetting` precedent (#1595). Exact formula shape (weighted sum vs. multiplicative) is a design decision, not specified here.

Keywords: recall ranking recency importance relevance, recall.ranking.enabled, mem_search explain, omnia search --explain, score breakdown receipt, staleness_penalty structural forgetting forward compat, backward compatible ranking default off, recall.Fuse RRF internal/recall internal/mcp recall_adapter
