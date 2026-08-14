package mcp

import (
	"time"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/intent"
	"github.com/velion/omnia/internal/ranker"
	"github.com/velion/omnia/internal/store"
)

// RankPipelineOptions bundles every config knob and precomputed input the
// six-stage post-fusion ranking pipeline needs (P0,
// docs/conversational-retrieval-plan.md section 0.1/P0): RankResults ->
// ApplyLearnedRanker -> ApplyStalenessDownrank -> ApplyTypeLens -> ApplyMMR
// -> ApplyTokenBudget. Every field mirrors an existing MCPConfig knob 1:1 —
// this struct exists so RankPipeline can be called from a package that has
// no MCPConfig (cmd/omnia's GET /search wiring), not to introduce a second
// source of truth for any of these values.
type RankPipelineOptions struct {
	// Ranking feeds RankResults AND ApplyLearnedRanker (the learned ranker
	// takes its own Ranking argument so its pre-model relevance
	// normalization stays consistent with RankResults' — see
	// ApplyLearnedRanker's own doc). Zero value (Enabled=false) is a pure
	// no-op for both stages.
	Ranking config.RankingConfig

	// LearnedRanker/LearnedRankerModel gate ApplyLearnedRanker. A nil model
	// (Enabled=true but no trained model loaded/promoted yet) is also a
	// pure no-op.
	LearnedRanker      config.RankerConfig
	LearnedRankerModel *ranker.Model

	// AnchorsByObs feeds ApplyStalenessDownrank, keyed by
	// Observation.SyncID. A nil/empty map is a pure no-op
	// (ApplyStalenessDownrank's own len(anchorsByObs)==0 short-circuit) —
	// callers that never load anchors (structural_forgetting.enabled=false,
	// or a caller that has no anchor lookup wired at all, e.g. `omnia
	// search --explain`) simply leave this nil.
	AnchorsByObs map[string][]store.MemoryAnchor

	// Query/ExplicitType feed InferLensType for ApplyTypeLens.
	// ExplicitType is the caller's own type filter (handleSearch's `type`
	// arg / GET /search's `type` query param) — InferLensType always
	// returns "" when it is non-empty (spec: Explicit User Filter Always
	// Wins), so a type-scoped search makes ApplyTypeLens a true no-op
	// regardless of TypeLens.Enabled.
	Query        string
	ExplicitType string
	TypeLens     config.TypeLensConfig

	Diversity config.DiversityConfig
	Budget    config.TokenBudgetConfig

	// IntentRouting gates P2's query-intent classification and routing
	// (docs/conversational-retrieval-plan.md: "Query intent classification
	// and routing"), layered ONTO the ranking/type-lens inputs above rather
	// than replacing them — see RankPipeline's own doc for exactly which two
	// stages it can influence and the "explicit filter always wins"/
	// "never mutate the shared config" invariants it must preserve. The
	// zero value (Enabled=false) is a pure no-op: intent.Classify is never
	// even called, so RankPipeline's behavior (and RankPipelineOutput.Intent,
	// which stays "") is byte-for-byte identical to before this field
	// existed.
	IntentRouting config.IntentRoutingConfig

	// PreLensSnapshot, when non-nil, is invoked once with the result set
	// exactly as it stands after RankResults -> ApplyLearnedRanker ->
	// ApplyStalenessDownrank have run, and BEFORE ApplyTypeLens/ApplyMMR/
	// ApplyTokenBudget reorder or trim it further.
	//
	// mem_search's `explain` arg (and GET /search's own explain=1, P0 item
	// 3) use this hook to snapshot the SAME batch RankResults scored
	// against for MinMaxNormalizeRelevance: that normalization must reflect
	// the full pre-trim batch, not one already narrowed by the lens/MMR/
	// budget stages — trimming before normalizing would let a single
	// surviving row pin its own min==max and read as "final":1.0
	// regardless of its real relevance (see the original inline comment
	// this hook replaces, preserved in mcp.go's handleSearch). Callers that
	// did not request explain pass nil and pay nothing for it — RankPipeline
	// only allocates/copies the snapshot slice when a hook is present.
	PreLensSnapshot func(results []store.SearchResult)
}

// RankPipelineOutput is RankPipeline's return value: the final ranked
// results plus the one piece of bookkeeping handleSearch's response
// text/envelope needs but that RankPipeline itself does not print or
// serialize (this package's I/O-shaping stays in mcp.go/cmd/omnia, not
// here).
type RankPipelineOutput struct {
	Results []store.SearchResult
	// BudgetTrimmed counts rows ApplyTokenBudget removed (0 when Budget is
	// disabled, or enabled but nothing was cut) — handleSearch's
	// trim-transparency footer/envelope field (PR2 review fix) reads this
	// instead of re-deriving it from a before/after length diff at the call
	// site.
	BudgetTrimmed int
	// Intent is P2's classified query intent (internal/intent.Intent, as a
	// plain string — this package does not import intent's type into its own
	// exported surface beyond this string, so callers outside internal/mcp
	// never need an internal/intent import just to read the response field).
	// Empty ("") whenever opts.IntentRouting.Enabled is false — routing never
	// ran, so there is nothing informational to report, matching the
	// present-only-when-notable convention this codebase already uses for
	// fts_relaxed/budget_trimmed/recall_degraded. When routing IS enabled,
	// Intent is always one of internal/intent's Intent constants as a
	// string, including "unknown" for a query no signal matched confidently
	// — "unknown" is still informational (it tells a caller routing ran and
	// found nothing to route on), unlike the enabled/disabled distinction
	// above. Purely informational: nothing in this package or its callers
	// re-parses this string to make a decision — the routing DECISION
	// already happened inside RankPipeline itself, before this value is
	// returned.
	Intent string
}

// RankPipeline runs the post-fusion ranking pipeline shared by mem_search
// (internal/mcp's handleSearch) and GET /search (cmd/omnia's SetSearch
// wiring, P0): RankResults -> ApplyLearnedRanker -> ApplyStalenessDownrank
// -> ApplyTypeLens -> ApplyMMR -> ApplyTokenBudget, in that fixed order.
//
// Before this function existed, the six stages were inlined directly in
// mcp.go's handleSearch, and GET /search ran NONE of them — it called
// recall.Fuse (or plain FTS5), hydrated, and returned the bare array (P0's
// diagnosis, docs/conversational-retrieval-plan.md section 0.1: "GET
// /search bypasses Omnia's entire ranking pipeline"). A voice agent (Vel,
// via Hermes) was on a strictly weaker retrieval path than a coding agent,
// over the exact same store. Lifting the stages into one exported function
// that BOTH callers invoke is what makes that impossible to happen again by
// accident: there is now only one place this order can be defined, so the
// two consumers cannot drift — the same "avoid divergence" argument that
// already justified recallOrFTSSearch as a shared seam for `omnia
// search`/GET /search (issue #86), extended here past hydration into
// ranking itself.
//
// Every stage is independently gated by its own config's Enabled flag and
// is a pure no-op over its input when disabled (see each Apply*/RankResults
// function's own doc) — RankPipeline adds no additional gating logic of its
// own, with one deliberate exception: the ApplyTypeLens classifier call
// (InferLensType) is skipped entirely — not just reduced to a no-op re-rank
// — when opts.TypeLens.Enabled is false, matching the "near-zero work when
// disabled" idiom the pre-extraction inline code already established for
// this stage (an adversarial-review finding on the original v0.3 slice).
//
// Because every stage is independently gated and self-no-ops, RankPipeline
// is meant to be called UNCONDITIONALLY by both callers: behavior is
// controlled purely through the zero-value-is-off config passed in via
// RankPipelineOptions, never through an "if enabled, call the pipeline"
// branch at the call site — the same convention this codebase already uses
// for every other Context Economy gate (RecallRanking, StructuralForgetting,
// Injection.*).
//
// Every stage also independently pre-empts the topic_key exact-match
// sentinel (SearchResult.Rank == exactSentinelRank) and error-signature-
// match rows (SearchResult.SignatureMatch) ahead of its own re-rank/trim —
// see each Apply*/RankResults' own doc. RankPipeline preserves that
// pre-emption purely by calling the six stages in order and changing
// nothing about how any one of them partitions its input; it does not
// re-implement or duplicate the exclusion itself.
func RankPipeline(results []store.SearchResult, relevance map[int64]float64, opts RankPipelineOptions, now time.Time) RankPipelineOutput {
	// P2 (docs/conversational-retrieval-plan.md): classify the query ONCE,
	// up front, so both the ranking overlay below and the type-lens overlay
	// further down read the same Classification — Classify is a pure
	// regex-table scan (<1ms, see internal/intent's own bench_test.go), so
	// re-running it per stage would cost nothing correctness-wise, but a
	// single call keeps this function's control flow easy to audit against
	// its own "byte-for-byte when disabled" claim. Gated entirely behind
	// opts.IntentRouting.Enabled (default false, see that field's own doc):
	// when off, classifiedIntent stays intent.Unknown and profile stays the
	// zero-value RoutingProfile, so every overlay below is inert and this
	// function's output is identical to before P2 existed.
	classifiedIntent := intent.Unknown
	var profile intent.RoutingProfile
	intentLabel := ""
	if opts.IntentRouting.Enabled {
		cls := intent.Classify(opts.Query)
		intentLabel = string(cls.Intent)
		if cls.Intent != intent.Unknown {
			classifiedIntent = cls.Intent
			profile = intent.ProfileFor(classifiedIntent)
		}
	}

	// Per-request clone (explicit constraint): ranking is a COPY of
	// opts.Ranking, never opts.Ranking itself re-assigned in place. Even
	// though Go already copies RankingConfig by value into opts.Ranking at
	// the call site (RankPipelineOptions.Ranking is a value field, not a
	// pointer), this local variable makes that copy impossible to
	// accidentally lose in a future edit — opts.Ranking is a caller-owned
	// value that may be built directly from a long-lived, request-shared
	// config.RankingConfig (cfg.RecallRanking / appCfg.Recall.Ranking) that
	// every OTHER request reads too; mutating it in place would leak one
	// query's intent-derived recency profile into every subsequent query
	// until the process restarts. RankingWeights (the only nested field this
	// overlay touches) is a plain value struct with no pointers/maps of its
	// own, so this copy is a genuine, independent value, not a shared alias.
	ranking := opts.Ranking
	if classifiedIntent != intent.Unknown {
		if profile.RecencyWeight != nil {
			ranking.Weights.Recency = *profile.RecencyWeight
		}
		if profile.RecencyHalfLifeDays != nil {
			ranking.RecencyHalfLifeDays = *profile.RecencyHalfLifeDays
		}
	}

	results = RankResults(results, relevance, ranking, now)
	results = ApplyLearnedRanker(results, relevance, opts.LearnedRanker, opts.LearnedRankerModel, ranking, now)
	results = ApplyStalenessDownrank(results, opts.AnchorsByObs)

	if opts.PreLensSnapshot != nil {
		opts.PreLensSnapshot(results)
	}

	if opts.TypeLens.Enabled {
		lensType := InferLensType(opts.Query, opts.ExplicitType)
		// P2 overlay: RoutingProfile.TypeLens replaces InferLensType's own
		// inference — Classify already read the same query with intent-level
		// context (bilingual phrase cues) InferLensType's flatter table
		// lacks (see intent.RoutingProfile.TypeLens's own doc). Guarded by
		// opts.ExplicitType == "" so the pre-existing "Explicit User Filter
		// Always Wins" invariant is preserved unconditionally: InferLensType
		// itself already returns "" whenever ExplicitType is non-empty, and
		// this overlay only ever fires on the SAME condition, so a
		// type-scoped search stands the lens down regardless of whether
		// intent routing is enabled.
		if classifiedIntent != intent.Unknown && opts.ExplicitType == "" && profile.TypeLens != "" {
			lensType = profile.TypeLens
		}
		results = ApplyTypeLens(results, lensType, opts.TypeLens)
	}

	results = ApplyMMR(results, relevance, opts.Diversity)

	preTrimCount := len(results)
	results = ApplyTokenBudget(results, opts.Budget)
	budgetTrimmed := 0
	if opts.Budget.Enabled {
		budgetTrimmed = preTrimCount - len(results)
	}

	return RankPipelineOutput{Results: results, BudgetTrimmed: budgetTrimmed, Intent: intentLabel}
}
