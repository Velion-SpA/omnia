package mcp

// rank_pipeline_test.go — P0 (docs/conversational-retrieval-plan.md section
// 0.1/P0): RankPipeline is the extracted, exported composition of the six
// post-fusion stages that used to be inlined directly in mcp.go's
// handleSearch. Each stage already has its own exhaustive unit tests
// (recall_ranking_test.go, learned_ranker_test.go, anchor_downrank_test.go,
// type_lens_test.go, mmr_test.go, token_budget_test.go) — this file tests
// what only RankPipeline itself can regress: that it calls the six stages in
// the documented fixed order, that RankPipelineOptions' fields reach the
// stage they claim to feed, that PreLensSnapshot fires at the documented
// point, and that BudgetTrimmed accounting survives the extraction.

import (
	"testing"
	"time"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/store"
)

// rpSR is a minimal store.SearchResult fixture, mirroring anchor_downrank_
// test.go's own drSR helper.
func rpSR(id int64, syncID, typ, content string) store.SearchResult {
	return store.SearchResult{Observation: store.Observation{ID: id, SyncID: syncID, Type: typ, Content: content}}
}

func rpIDs(results []store.SearchResult) []int64 {
	ids := make([]int64, len(results))
	for i, r := range results {
		ids[i] = r.ID
	}
	return ids
}

func rpEqualIDs(got, want []int64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestRankPipeline_AllDisabled_ReturnsInputUnchanged pins the zero-value
// contract every individual Apply*/RankResults stage already guarantees on
// its own: RankPipelineOptions{} (every Enabled flag false, nil model, nil
// anchors) must leave the input completely untouched and report
// BudgetTrimmed=0 — the same "byte-for-byte identical when off" guarantee
// handleSearch relied on before this slice existed, now verified at the
// composed-pipeline level instead of only per-stage.
func TestRankPipeline_AllDisabled_ReturnsInputUnchanged(t *testing.T) {
	results := []store.SearchResult{
		rpSR(1, "a", "note", "alpha"),
		rpSR(2, "b", "decision", "beta"),
		rpSR(3, "c", "note", "gamma"),
	}
	relevance := map[int64]float64{1: 3, 2: 2, 3: 1}

	out := RankPipeline(results, relevance, RankPipelineOptions{}, time.Now())

	if !rpEqualIDs(rpIDs(out.Results), []int64{1, 2, 3}) {
		t.Fatalf("expected input order unchanged, got %v", rpIDs(out.Results))
	}
	if out.BudgetTrimmed != 0 {
		t.Fatalf("BudgetTrimmed = %d, want 0 when Budget.Enabled is false", out.BudgetTrimmed)
	}
}

// TestRankPipeline_TypeLensLiftsMatchingRow_ThenSurvivesIntoBudget proves two
// things about the fixed stage order at once: ApplyTypeLens actually runs
// inside RankPipeline (lifting the "decision"-type row above two "note" rows
// for a query InferLensType maps to "decision"), and it runs BEFORE
// ApplyTokenBudget — a budget tight enough to keep only 2 of 3 rows must keep
// the LENS-BOOSTED top 2 (the lifted decision row plus whichever note row
// was already ahead), not the pre-lens top 2, proving ApplyTypeLens's
// reordering is what ApplyTokenBudget actually sees (design section 2's
// documented order: ... -> ApplyTypeLens -> ApplyMMR -> ApplyTokenBudget).
func TestRankPipeline_TypeLensLiftsMatchingRow_ThenSurvivesIntoBudget(t *testing.T) {
	// Each Content is exactly 40 runes -> EstimateTokens = (40+3)/4 = 10.
	content := func(tag string) string {
		s := "0123456789012345678901234567890123456789"
		return tag + s[len(tag):]
	}
	results := []store.SearchResult{
		rpSR(1, "a", "note", content("r1")),     // originally ranked first
		rpSR(2, "b", "decision", content("r2")), // originally ranked second, but lens-eligible
		rpSR(3, "c", "note", content("r3")),     // originally ranked third
	}
	relevance := map[int64]float64{1: 3, 2: 2, 3: 1}

	out := RankPipeline(results, relevance, RankPipelineOptions{
		Query:        "why did we choose this database",
		ExplicitType: "",
		TypeLens:     config.TypeLensConfig{Enabled: true},
		Budget:       config.TokenBudgetConfig{Enabled: true, MaxTokens: 20}, // fits exactly 2 rows @10 tokens each
	}, time.Now())

	// Lens lifts id 2 (decision) above ids 1/3 (note): pre-budget order is
	// [2, 1, 3]. A 20-token budget then keeps the first 2 of THAT order.
	if !rpEqualIDs(rpIDs(out.Results), []int64{2, 1}) {
		t.Fatalf("expected the lens-lifted row (2) to survive the budget ahead of row 1, got %v", rpIDs(out.Results))
	}
	if out.BudgetTrimmed != 1 {
		t.Fatalf("BudgetTrimmed = %d, want 1 (row 3 dropped)", out.BudgetTrimmed)
	}
}

// TestRankPipeline_TypeLensDisabled_ClassifierNeverInfluencesOrder is the
// flag-OFF counterpart: the SAME query/content fixture as above, but with
// TypeLens.Enabled=false, must leave the original relevance order
// untouched — InferLensType's classifier must not even run, matching the
// "near-zero work when disabled" contract RankPipeline's own doc claims.
func TestRankPipeline_TypeLensDisabled_ClassifierNeverInfluencesOrder(t *testing.T) {
	results := []store.SearchResult{
		rpSR(1, "a", "note", "alpha"),
		rpSR(2, "b", "decision", "beta"),
		rpSR(3, "c", "note", "gamma"),
	}
	relevance := map[int64]float64{1: 3, 2: 2, 3: 1}

	out := RankPipeline(results, relevance, RankPipelineOptions{
		Query:    "why did we choose this database",
		TypeLens: config.TypeLensConfig{Enabled: false},
	}, time.Now())

	if !rpEqualIDs(rpIDs(out.Results), []int64{1, 2, 3}) {
		t.Fatalf("expected order unchanged with TypeLens disabled, got %v", rpIDs(out.Results))
	}
}

// TestRankPipeline_ExplicitTypeStandsDownTypeLens mirrors InferLensType's
// own "Explicit User Filter Always Wins" contract at the pipeline level: an
// ExplicitType makes the lens a no-op even though TypeLens.Enabled is true
// and the query text would otherwise match a lens signal.
func TestRankPipeline_ExplicitTypeStandsDownTypeLens(t *testing.T) {
	results := []store.SearchResult{
		rpSR(1, "a", "note", "alpha"),
		rpSR(2, "b", "decision", "beta"),
	}
	relevance := map[int64]float64{1: 2, 2: 1}

	out := RankPipeline(results, relevance, RankPipelineOptions{
		Query:        "why did we choose this database",
		ExplicitType: "note", // caller already filtered by type
		TypeLens:     config.TypeLensConfig{Enabled: true},
	}, time.Now())

	if !rpEqualIDs(rpIDs(out.Results), []int64{1, 2}) {
		t.Fatalf("expected order unchanged when ExplicitType stands the lens down, got %v", rpIDs(out.Results))
	}
}

// TestRankPipeline_PreLensSnapshot_FiresAfterStalenessDownrankBeforeTypeLens
// locks the ordering contract RankPipelineOptions.PreLensSnapshot's own doc
// promises: the snapshot must reflect RankResults -> ApplyLearnedRanker ->
// ApplyStalenessDownrank's output, but NOT yet ApplyTypeLens's reordering —
// this is what lets mem_search's explain / GET /search's explain=1 normalize
// relevance over the full pre-trim batch (see BuildResultReceipt's own doc
// for why normalizing after trimming would be wrong).
func TestRankPipeline_PreLensSnapshot_FiresAfterStalenessDownrankBeforeTypeLens(t *testing.T) {
	results := []store.SearchResult{
		rpSR(1, "a", "note", "alpha"),
		rpSR(2, "b", "decision", "beta"),
	}
	relevance := map[int64]float64{1: 1, 2: 2}

	anchors := map[string][]store.MemoryAnchor{
		"a": {{AnchorStatus: store.AnchorStatusStale}},
	}

	var snapshotIDs []int64
	calls := 0
	out := RankPipeline(results, relevance, RankPipelineOptions{
		AnchorsByObs: anchors,
		Query:        "why did we choose this database",
		TypeLens:     config.TypeLensConfig{Enabled: true},
		PreLensSnapshot: func(snapshot []store.SearchResult) {
			calls++
			snapshotIDs = rpIDs(snapshot)
		},
	}, time.Now())

	if calls != 1 {
		t.Fatalf("expected PreLensSnapshot to fire exactly once, got %d calls", calls)
	}
	// ApplyStalenessDownrank sinks the stale row (id 1) behind the fresh one
	// (id 2) — the snapshot must already reflect that sink...
	if !rpEqualIDs(snapshotIDs, []int64{2, 1}) {
		t.Fatalf("expected snapshot to reflect post-staleness-downrank order [2 1], got %v", snapshotIDs)
	}
	// ...while the FINAL output additionally reflects ApplyTypeLens lifting
	// the decision-type row (id 2) — which in this fixture is already first,
	// so the assertion that actually distinguishes "lens ran after the
	// snapshot" is snapshotIDs above, not this line; this just confirms the
	// pipeline still completes normally with the hook wired.
	if !rpEqualIDs(rpIDs(out.Results), []int64{2, 1}) {
		t.Fatalf("expected final order [2 1], got %v", rpIDs(out.Results))
	}
}

// TestRankPipeline_PreLensSnapshotNil_NeverCalled proves callers that don't
// request explain (the overwhelming common case) pay nothing for the hook —
// a nil PreLensSnapshot must never be invoked.
func TestRankPipeline_PreLensSnapshotNil_NeverCalled(t *testing.T) {
	results := []store.SearchResult{rpSR(1, "a", "note", "alpha")}
	// A nil func field is the zero value; RankPipeline must guard the call
	// with a nil check rather than panicking.
	out := RankPipeline(results, map[int64]float64{1: 1}, RankPipelineOptions{}, time.Now())
	if len(out.Results) != 1 {
		t.Fatalf("expected pipeline to run normally with a nil PreLensSnapshot, got %v", out.Results)
	}
}

// TestRankPipeline_SentinelAndSignatureRowsPreemptedThroughout is the
// adversarial pin: a topic_key exact-match sentinel row and a
// SignatureMatch row must both survive at the FRONT of the result set
// regardless of which stages are enabled — RankPipeline must not
// re-implement or weaken any single stage's own pre-emption by composing
// them together.
func TestRankPipeline_SentinelAndSignatureRowsPreemptedThroughout(t *testing.T) {
	sentinel := rpSR(99, "s", "note", "sentinel content padded to be long enough to matter here")
	sentinel.Rank = exactSentinelRank
	sig := rpSR(50, "g", "note", "signature content padded to be long enough to matter here too")
	sig.SignatureMatch = true

	results := []store.SearchResult{
		sentinel,
		sig,
		rpSR(1, "a", "decision", "alpha content padded so the budget can trim it away"),
		rpSR(2, "b", "note", "beta content padded so the budget can trim it away too"),
	}
	relevance := map[int64]float64{99: 0, 50: 0, 1: 2, 2: 1}

	out := RankPipeline(results, relevance, RankPipelineOptions{
		Ranking:  config.RankingConfig{Enabled: true},
		Query:    "why did we choose this database",
		TypeLens: config.TypeLensConfig{Enabled: true},
		Budget:   config.TokenBudgetConfig{Enabled: true, MaxTokens: 1}, // squeezes every non-preempted row
	}, time.Now())

	if len(out.Results) < 2 || out.Results[0].ID != 99 || out.Results[1].ID != 50 {
		t.Fatalf("expected sentinel (99) then signature (50) row preempted at the front regardless of ranking/lens/budget, got %v", rpIDs(out.Results))
	}
}
