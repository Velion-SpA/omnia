package mcp

import (
	"time"

	"github.com/velion/omnia/internal/config"
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
	results = RankResults(results, relevance, opts.Ranking, now)
	results = ApplyLearnedRanker(results, relevance, opts.LearnedRanker, opts.LearnedRankerModel, opts.Ranking, now)
	results = ApplyStalenessDownrank(results, opts.AnchorsByObs)

	if opts.PreLensSnapshot != nil {
		opts.PreLensSnapshot(results)
	}

	if opts.TypeLens.Enabled {
		lensType := InferLensType(opts.Query, opts.ExplicitType)
		results = ApplyTypeLens(results, lensType, opts.TypeLens)
	}

	results = ApplyMMR(results, relevance, opts.Diversity)

	preTrimCount := len(results)
	results = ApplyTokenBudget(results, opts.Budget)
	budgetTrimmed := 0
	if opts.Budget.Enabled {
		budgetTrimmed = preTrimCount - len(results)
	}

	return RankPipelineOutput{Results: results, BudgetTrimmed: budgetTrimmed}
}
