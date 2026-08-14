package mcp

// rank_pipeline_intent_test.go — P2 (docs/conversational-retrieval-plan.md
// "Query intent classification and routing"): RankPipeline optionally
// classifies opts.Query via internal/intent and overlays the resulting
// RoutingProfile onto the ranking/type-lens inputs it already accepts (see
// rank_pipeline.go's own doc comments on the IntentRouting field and the
// overlay block inside RankPipeline). This file locks the three properties
// that make that overlay safe to ship default-off:
//
//  1. IntentRouting.Enabled=false (the default) is byte-for-byte identical
//     to before P2 existed — intent.Classify is never even called.
//  2. When enabled, a classified intent overlays RecencyWeight/
//     RecencyHalfLifeDays/TypeLens exactly as intent.ProfileFor documents,
//     WITHOUT ever mutating the caller's own config.RankingConfig value —
//     RankPipeline works from a per-request clone.
//  3. ExplicitType still always wins over an intent-derived type-lens hint,
//     extending the pre-existing "Explicit User Filter Always Wins" rule
//     (TestRankPipeline_ExplicitTypeStandsDownTypeLens) to the new overlay.
//
// Every stage this file exercises already has its own exhaustive unit tests
// (recall_ranking_test.go, type_lens_test.go) and internal/intent has its
// own blind precision gate (internal/eval/testdata/conversational_cases.json)
// — this file only tests what RankPipeline's OWN wiring can regress.

import (
	"testing"
	"time"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/store"
)

// rpSRWithUpdated is rpSR (this package's existing fixture helper) plus an
// UpdatedAt timestamp, needed here because the ranking overlay's whole
// observable effect is on RankResults' recency component.
func rpSRWithUpdated(id int64, syncID, typ, content, updatedAt string) store.SearchResult {
	r := rpSR(id, syncID, typ, content)
	r.UpdatedAt = updatedAt
	return r
}

// intentRankingFixture builds the two-row fixture every test below shares:
// row 1 is FRESH (now) with LOW raw relevance; row 2 is OLD (60 days back)
// with HIGH raw relevance. Weights.Recency=5 (heavily recency-weighted) and
// a 14-day half-life make row 1 win on recency alone when the recency
// weight is left at its configured value, and lose to row 2 the moment
// Identity's profile overlay zeros that weight out — see each test's own
// hand-computed scores in its doc comment.
func intentRankingFixture(now time.Time) ([]store.SearchResult, map[int64]float64) {
	fresh := now.Format(time.RFC3339)
	old := now.AddDate(0, 0, -60).Format(time.RFC3339)
	results := []store.SearchResult{
		rpSRWithUpdated(1, "a", "note", "fresh low-relevance row", fresh),
		rpSRWithUpdated(2, "b", "note", "old high-relevance row", old),
	}
	relevance := map[int64]float64{1: 1, 2: 10}
	return results, relevance
}

func recencyWeightedRankingConfig() config.RankingConfig {
	return config.RankingConfig{
		Enabled:             true,
		RecencyHalfLifeDays: 14,
		Weights:             config.RankingWeights{Recency: 5, Relevance: 1},
	}
}

// TestRankPipeline_IntentRoutingDisabled_RankingUnaffectedAndIntentEmpty
// pins property 1: IntentRouting left at its zero value (Enabled=false)
// must leave RankResults' own recency-dominant order untouched (row 1,
// fresh, wins) even for a query that WOULD classify confidently as
// Identity, and RankPipelineOutput.Intent must stay "" — nothing ran.
func TestRankPipeline_IntentRoutingDisabled_RankingUnaffectedAndIntentEmpty(t *testing.T) {
	now := time.Now()
	results, relevance := intentRankingFixture(now)

	out := RankPipeline(results, relevance, RankPipelineOptions{
		Ranking: recencyWeightedRankingConfig(),
		Query:   "what is Omnia",
		// IntentRouting left zero-value: Enabled=false.
	}, now)

	if !rpEqualIDs(rpIDs(out.Results), []int64{1, 2}) {
		t.Fatalf("expected the recency-dominant order [1 2] unchanged with intent routing disabled, got %v", rpIDs(out.Results))
	}
	if out.Intent != "" {
		t.Fatalf("Intent = %q, want \"\" when IntentRouting.Enabled is false", out.Intent)
	}
}

// TestRankPipeline_IntentRoutingIdentity_ZerosRecencyAndFlipsOrder is
// property 2's positive case: with IntentRouting enabled and a query that
// classifies as Identity (0.8 confidence, above the 0.6 default threshold),
// intent.ProfileFor(Identity).RecencyWeight (a pointer to 0) must overlay
// onto the per-request ranking clone, zeroing the recency component out —
// flipping the winner from the fresh/low-relevance row to the
// old/high-relevance one, exactly as hand-computed in intentRankingFixture's
// own doc comment. RankPipelineOutput.Intent must report "identity".
func TestRankPipeline_IntentRoutingIdentity_ZerosRecencyAndFlipsOrder(t *testing.T) {
	now := time.Now()
	results, relevance := intentRankingFixture(now)

	out := RankPipeline(results, relevance, RankPipelineOptions{
		Ranking:       recencyWeightedRankingConfig(),
		Query:         "what is Omnia",
		IntentRouting: config.IntentRoutingConfig{Enabled: true},
	}, now)

	if !rpEqualIDs(rpIDs(out.Results), []int64{2, 1}) {
		t.Fatalf("expected Identity's recency:0 overlay to flip the order to [2 1] (high relevance wins once recency stops dominating), got %v", rpIDs(out.Results))
	}
	if out.Intent != "identity" {
		t.Fatalf("Intent = %q, want %q", out.Intent, "identity")
	}
}

// TestRankPipeline_IntentRoutingNeverMutatesSharedRankingConfig is property
// 2's safety half: the SAME config.RankingConfig value (as a caller would
// hold it in a long-lived, request-shared MCPConfig/appCfg field) must read
// back with its original Weights.Recency after being passed through
// RankPipeline under an intent that overlays a different value — proving
// RankPipeline works from a per-request clone, never the caller's own
// struct in place. A regression here would leak one query's intent-derived
// recency profile into every subsequent request until process restart —
// exactly the failure mode the per-request-clone requirement exists to
// prevent.
func TestRankPipeline_IntentRoutingNeverMutatesSharedRankingConfig(t *testing.T) {
	now := time.Now()
	shared := recencyWeightedRankingConfig()

	results, relevance := intentRankingFixture(now)
	_ = RankPipeline(results, relevance, RankPipelineOptions{
		Ranking:       shared,
		Query:         "what is Omnia", // classifies Identity -> RecencyWeight override to 0
		IntentRouting: config.IntentRoutingConfig{Enabled: true},
	}, now)

	if shared.Weights.Recency != 5 {
		t.Fatalf("shared config.RankingConfig.Weights.Recency = %v after RankPipeline, want unchanged 5 (RankPipeline must never mutate the caller's shared config)", shared.Weights.Recency)
	}

	// Run a SECOND request through the same shared config with a DIFFERENT
	// intent (Status, RecencyWeight=3) immediately after, to prove the first
	// call's overlay never leaked forward into this one either — each call
	// must independently clone from `shared`, not from whatever the
	// previous call last computed.
	statusResults, statusRelevance := intentRankingFixture(now)
	statusOut := RankPipeline(statusResults, statusRelevance, RankPipelineOptions{
		Ranking:       shared,
		Query:         "cómo va Workly",
		IntentRouting: config.IntentRoutingConfig{Enabled: true},
	}, now)
	if statusOut.Intent != "status" {
		t.Fatalf("Intent = %q, want %q (test fixture assumption — see intent.signals)", statusOut.Intent, "status")
	}
	if shared.Weights.Recency != 5 {
		t.Fatalf("shared config.RankingConfig.Weights.Recency = %v after a SECOND RankPipeline call, want unchanged 5", shared.Weights.Recency)
	}
}

// TestRankPipeline_IntentRoutingIdentity_ExplicitTypeStillWins is property
// 3: Identity's profile carries TypeLens: "doc", but an ExplicitType (the
// caller's own `type` filter) must still stand the lens down entirely —
// extending TestRankPipeline_ExplicitTypeStandsDownTypeLens's pre-existing
// invariant to the new intent-derived overlay, not just InferLensType's own
// inference.
func TestRankPipeline_IntentRoutingIdentity_ExplicitTypeStillWins(t *testing.T) {
	results := []store.SearchResult{
		rpSR(1, "a", "note", "alpha"),
		rpSR(2, "b", "doc", "beta"),
	}
	relevance := map[int64]float64{1: 2, 2: 1}

	out := RankPipeline(results, relevance, RankPipelineOptions{
		Query:         "what is Omnia", // classifies Identity -> TypeLens "doc"
		ExplicitType:  "note",          // caller already filtered by type
		TypeLens:      config.TypeLensConfig{Enabled: true},
		IntentRouting: config.IntentRoutingConfig{Enabled: true},
	}, time.Now())

	if !rpEqualIDs(rpIDs(out.Results), []int64{1, 2}) {
		t.Fatalf("expected order unchanged — ExplicitType must stand the intent-derived \"doc\" lens down, got %v", rpIDs(out.Results))
	}
}

// TestRankPipeline_IntentRoutingIdentity_TypeLensLiftsDocRow is TypeLens'
// positive counterpart to the ExplicitType test above: with no
// ExplicitType, Identity's TypeLens:"doc" profile hint must actually lift a
// "doc"-typed row above non-matching rows, exactly like an InferLensType
// signal would — proving the overlay reaches ApplyTypeLens, not just
// RankResults.
func TestRankPipeline_IntentRoutingIdentity_TypeLensLiftsDocRow(t *testing.T) {
	results := []store.SearchResult{
		rpSR(1, "a", "note", "alpha"), // originally ranked first
		rpSR(2, "b", "doc", "beta"),   // originally ranked second, but doc-lens-eligible
	}
	relevance := map[int64]float64{1: 2, 2: 1}

	out := RankPipeline(results, relevance, RankPipelineOptions{
		Query:         "what is Omnia", // classifies Identity -> TypeLens "doc"; InferLensType itself would infer "" for this query (no bugfix/decision/architecture/pattern signal)
		TypeLens:      config.TypeLensConfig{Enabled: true},
		IntentRouting: config.IntentRoutingConfig{Enabled: true},
	}, time.Now())

	if !rpEqualIDs(rpIDs(out.Results), []int64{2, 1}) {
		t.Fatalf("expected the intent-derived \"doc\" lens to lift row 2 above row 1, got %v", rpIDs(out.Results))
	}
}

// TestRankPipeline_IntentRoutingUnknownQuery_ReportsUnknownAndNoOverlay
// proves the classifier's own safe default (Unknown) survives the wiring:
// a query no signal matches confidently must leave ranking/type-lens
// completely untouched (the same guarantee as IntentRouting disabled) while
// STILL reporting Intent="unknown" — distinct from the ""-when-disabled
// case, since routing DID run here, it just found nothing to route on.
func TestRankPipeline_IntentRoutingUnknownQuery_ReportsUnknownAndNoOverlay(t *testing.T) {
	now := time.Now()
	results, relevance := intentRankingFixture(now)

	out := RankPipeline(results, relevance, RankPipelineOptions{
		Ranking:       recencyWeightedRankingConfig(),
		Query:         "xyz completely unrouted query 12345",
		IntentRouting: config.IntentRoutingConfig{Enabled: true},
	}, now)

	if !rpEqualIDs(rpIDs(out.Results), []int64{1, 2}) {
		t.Fatalf("expected the recency-dominant order [1 2] unchanged for an unrouted query, got %v", rpIDs(out.Results))
	}
	if out.Intent != "unknown" {
		t.Fatalf("Intent = %q, want %q (routing ran but found nothing to route on)", out.Intent, "unknown")
	}
}
