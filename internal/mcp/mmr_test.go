package mcp

// mmr_test.go — RED→GREEN tests for Omnia v0.3 "Context Economy" PR4
// (design obs #1643 section 3.3, spec obs #1642 injection-diversity domain,
// all 6 REQs). See preemption_invariant_test.go's extended
// preemptionInvariantCases table for the shared adversarial
// sentinel/signature-row invariant this pass must also satisfy — this file
// covers ApplyMMR's own dedup/no-op/ordering behavior plus the
// jaccardSimilarity primitive it's built on.

import (
	"testing"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/store"
)

// TestApplyMMR_DisabledIsNoop is the backward-compat gate (spec: Disabled by
// Default, No-Op When Off): cfg.Enabled=false must return results completely
// untouched, same order, same values — byte-for-byte identical output.
func TestApplyMMR_DisabledIsNoop(t *testing.T) {
	results := []store.SearchResult{
		tbSR(1, "alpha beta gamma", 1),
		tbSR(2, "alpha beta gamma", 2), // exact duplicate content
	}
	got := ApplyMMR(results, nil, config.DiversityConfig{Enabled: false, Lambda: 0.7, SimilarityThreshold: 0.9})
	if !equalInt64s(idsOfResults(got), idsOfResults(results)) {
		t.Fatalf("ApplyMMR(disabled) reordered/dropped results: got %v, want %v", idsOfResults(got), idsOfResults(results))
	}
	for i := range got {
		if got[i].Content != results[i].Content {
			t.Errorf("ApplyMMR(disabled) altered content at index %d: got %q, want %q", i, got[i].Content, results[i].Content)
		}
	}
}

// TestApplyMMR_FewerThanTwoResultsIsNoop: MMR needs at least 2 rows to have
// anything to compare — a single result (or none) must pass through
// untouched even when enabled.
func TestApplyMMR_FewerThanTwoResultsIsNoop(t *testing.T) {
	results := []store.SearchResult{tbSR(1, "solo row", 1)}
	got := ApplyMMR(results, nil, config.DiversityConfig{Enabled: true, Lambda: 0.7, SimilarityThreshold: 0.9})
	if !equalInt64s(idsOfResults(got), idsOfResults(results)) {
		t.Fatalf("ApplyMMR(<2 results) = %v, want untouched %v", idsOfResults(got), idsOfResults(results))
	}
}

// TestApplyMMR_NearDuplicateSuppressed (spec: Near-duplicate demoted; REQ1):
// two near-identical rows (Jaccard similarity >= similarity_threshold) ->
// the lower-ranked one is hard-dropped entirely, and the next-most-diverse
// row is promoted into its place instead of leaving a gap.
func TestApplyMMR_NearDuplicateSuppressed(t *testing.T) {
	// A and B share 9/10 tokens (Jaccard = 9/10 = 0.9, meets the default
	// threshold exactly); C shares nothing with either.
	a := tbSR(1, "w1 w2 w3 w4 w5 w6 w7 w8 w9 w10", 1)
	b := tbSR(2, "w1 w2 w3 w4 w5 w6 w7 w8 w9", 2)
	c := tbSR(3, "z1 z2 z3", 3)
	results := []store.SearchResult{a, b, c}

	got := ApplyMMR(results, nil, config.DiversityConfig{Enabled: true, Lambda: 0.7, SimilarityThreshold: 0.9})

	wantIDs := []int64{1, 3} // b (id 2) hard-dropped as a near-duplicate of a
	if !equalInt64s(idsOfResults(got), wantIDs) {
		t.Fatalf("ApplyMMR near-duplicate suppression: got %v, want %v (near-dup id=2 dropped, id=3 promoted)", idsOfResults(got), wantIDs)
	}
}

// TestApplyMMR_AllDistinctSetUnchanged (spec: All-distinct set unchanged):
// when every pair is below the similarity threshold (here: completely
// disjoint token sets), the diversity pass must not reorder or drop
// anything — output order is identical to input order.
func TestApplyMMR_AllDistinctSetUnchanged(t *testing.T) {
	results := []store.SearchResult{
		tbSR(1, "alpha beta gamma", 1),
		tbSR(2, "delta epsilon zeta", 2),
		tbSR(3, "eta theta iota", 3),
		tbSR(4, "kappa lambda mu", 4),
	}
	got := ApplyMMR(results, nil, config.DiversityConfig{Enabled: true, Lambda: 0.7, SimilarityThreshold: 0.9})
	wantIDs := []int64{1, 2, 3, 4}
	if !equalInt64s(idsOfResults(got), wantIDs) {
		t.Fatalf("ApplyMMR(all-distinct) reordered: got %v, want unchanged %v", idsOfResults(got), wantIDs)
	}
}

// TestApplyMMR_LambdaBoundaryOrdering locks the exact greedy reselection
// formula (design section 3.3): argmax [lambda*rel(d) - (1-lambda)*maxSim(d,
// selected)]. relevance is nil here, so rel(d) falls back to the documented
// "descending rank-position" proxy (1.0, 0.5, 0.0 for a 3-item rest in
// position order) — relevance is "unavailable" by design's own escape
// hatch. Fixture: A (top-ranked, rel=1.0) starts selected. B (rel=0.5) is
// fairly similar to A (Jaccard 4/5=0.8, below the 0.9 threshold — survives,
// not hard-dropped) but not near-identical. C (rel=0.0) is completely
// distinct from both. With lambda=0.3 (diversity-favoring), B's similarity
// penalty (0.7 weight) outweighs its relevance edge over C, so C is
// reselected BEFORE B even though B ranked higher going in.
func TestApplyMMR_LambdaBoundaryOrdering(t *testing.T) {
	a := tbSR(1, "a b c d x", 1)
	b := tbSR(2, "a b c d", 2)
	c := tbSR(3, "p q r s", 3)
	results := []store.SearchResult{a, b, c}

	got := ApplyMMR(results, nil, config.DiversityConfig{Enabled: true, Lambda: 0.3, SimilarityThreshold: 0.9})

	wantIDs := []int64{1, 3, 2} // A first (start row), then C (diverse), then B last
	if !equalInt64s(idsOfResults(got), wantIDs) {
		t.Fatalf("ApplyMMR lambda-boundary ordering: got %v, want %v", idsOfResults(got), wantIDs)
	}
}

// ─── jaccardSimilarity / tokenizeForSimilarity ──────────────────────────────

// TestJaccardSimilarity_Identical: identical token sets (even from
// differently-cased/punctuated source strings) must score exactly 1.0.
func TestJaccardSimilarity_Identical(t *testing.T) {
	got := jaccardSimilarity(tokenizeForSimilarity("Hello World"), tokenizeForSimilarity("hello world"))
	if got != 1.0 {
		t.Errorf("jaccardSimilarity(identical) = %v, want 1.0", got)
	}
}

// TestJaccardSimilarity_Disjoint: completely non-overlapping token sets must
// score exactly 0.0 (spec: Cheap Lexical Similarity Only, no partial credit
// for zero shared vocabulary).
func TestJaccardSimilarity_Disjoint(t *testing.T) {
	got := jaccardSimilarity(tokenizeForSimilarity("alpha beta"), tokenizeForSimilarity("gamma delta"))
	if got != 0.0 {
		t.Errorf("jaccardSimilarity(disjoint) = %v, want 0.0", got)
	}
}

// TestJaccardSimilarity_CaseAndPunctuationInsensitive: tokenization must
// lowercase and strip punctuation before comparing, so "Hello, World!" and
// "hello world" are recognized as the same two tokens, not four distinct
// ones.
func TestJaccardSimilarity_CaseAndPunctuationInsensitive(t *testing.T) {
	got := jaccardSimilarity(tokenizeForSimilarity("Hello, World!"), tokenizeForSimilarity("hello world"))
	if got != 1.0 {
		t.Errorf("jaccardSimilarity(case/punctuation-insensitive) = %v, want 1.0", got)
	}
}

// TestJaccardSimilarity_PartialOverlap locks the exact |A∩B|/|A∪B| formula
// with a hand-computed fixture: 4 shared tokens, 5 total in the union
// (1 unique to A) -> 4/5 = 0.8.
func TestJaccardSimilarity_PartialOverlap(t *testing.T) {
	got := jaccardSimilarity(tokenizeForSimilarity("a b c d x"), tokenizeForSimilarity("a b c d"))
	if got != 0.8 {
		t.Errorf("jaccardSimilarity(partial overlap) = %v, want 0.8", got)
	}
}

// TestJaccardSimilarity_BothEmpty pins the deliberate conservative choice
// documented on jaccardSimilarity: empty-vs-empty (and empty-vs-anything) is
// 0, NOT 1, so empty/near-empty content never looks like a spurious
// "duplicate" and gets hard-dropped by accident.
func TestJaccardSimilarity_BothEmpty(t *testing.T) {
	if got := jaccardSimilarity(nil, nil); got != 0.0 {
		t.Errorf("jaccardSimilarity(empty, empty) = %v, want 0.0", got)
	}
	if got := jaccardSimilarity(tokenizeForSimilarity(""), tokenizeForSimilarity("alpha")); got != 0.0 {
		t.Errorf("jaccardSimilarity(empty, non-empty) = %v, want 0.0", got)
	}
}

// TestMMRRelevanceProxy_TiedAtZeroIsStillSignal pins the presence-based
// "relevance available" detection: a batch whose supplied relevance values
// all tie at exactly 0 is REAL signal (normalized to all-equal 1.0 by
// MinMaxNormalizeRelevance's tie handling), not a cue to invent the
// artificial rank-position gradient reserved for a nil/absent relevance map.
func TestMMRRelevanceProxy_TiedAtZeroIsStillSignal(t *testing.T) {
	rest := []store.SearchResult{
		tbSR(1, "alpha content", 1),
		tbSR(2, "beta content", 2),
		tbSR(3, "gamma content", 3),
	}
	tied := map[int64]float64{1: 0, 2: 0, 3: 0}

	got := mmrRelevanceOrRankProxy(rest, tied)
	for _, r := range rest {
		if got[r.ID] != 1.0 {
			t.Errorf("tied-at-zero relevance: row %d = %v, want 1.0 (all-equal tie), not a rank gradient", r.ID, got[r.ID])
		}
	}

	// Absent map: the documented fallback gradient still applies.
	proxy := mmrRelevanceOrRankProxy(rest, nil)
	if proxy[1] != 1.0 || proxy[3] != 0.0 {
		t.Errorf("nil relevance: want rank gradient 1.0..0.0, got first=%v last=%v", proxy[1], proxy[3])
	}
}
