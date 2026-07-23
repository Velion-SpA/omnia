package mcp

import (
	"math"
	"testing"
	"time"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/store"
)

func floatsClose(a, b, epsilon float64) bool {
	return math.Abs(a-b) < epsilon
}

// ─── Phase 2: Ranking Primitives ────────────────────────────────────────────

// TestRankScore_WeightedSum pins the Generative Agents weighted-sum shape:
// final = w.Relevance*relevance + w.Recency*recency + w.Importance*importance.
// This resolves the spec's open weighted-sum-vs-multiplicative note in favor
// of a weighted sum.
func TestRankScore_WeightedSum(t *testing.T) {
	w := config.RankingWeights{Recency: 2, Importance: 3, Relevance: 5}
	got := RankScore(0.4, 0.6, 0.8, w)
	want := 5*0.4 + 2*0.6 + 3*0.8
	if !floatsClose(got, want, 1e-9) {
		t.Errorf("RankScore = %v, want %v", got, want)
	}
}

// TestComputeRecency_HalfLifeDecay locks the decay shape (Requirement:
// Recency Decay Never Hard-Filters): 1.0 at t=0, 0.5 at t=halfLife,
// monotonically decreasing, and never reaching exactly 0 no matter how old.
func TestComputeRecency_HalfLifeDecay(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	halfLife := 14.0

	t0 := now.Format("2006-01-02 15:04:05")
	got, ok := ComputeRecency(t0, now, halfLife)
	if !ok {
		t.Fatalf("ComputeRecency(t=0): ok=false, want true")
	}
	if !floatsClose(got, 1.0, 1e-6) {
		t.Errorf("ComputeRecency(t=0) = %v, want 1.0", got)
	}

	tHalf := now.Add(-14 * 24 * time.Hour).Format("2006-01-02 15:04:05")
	got, ok = ComputeRecency(tHalf, now, halfLife)
	if !ok {
		t.Fatalf("ComputeRecency(t=halfLife): ok=false, want true")
	}
	if !floatsClose(got, 0.5, 1e-6) {
		t.Errorf("ComputeRecency(t=halfLife) = %v, want 0.5", got)
	}

	// Monotonic: further elapsed time must score lower.
	tFar := now.Add(-60 * 24 * time.Hour).Format("2006-01-02 15:04:05")
	gotFar, ok := ComputeRecency(tFar, now, halfLife)
	if !ok {
		t.Fatalf("ComputeRecency(t=60d): ok=false, want true")
	}
	if gotFar >= got {
		t.Errorf("ComputeRecency(t=60d) = %v, want < ComputeRecency(t=14d) = %v (monotonic decay)", gotFar, got)
	}

	// Never exactly 0, even very old.
	tAncient := now.Add(-3650 * 24 * time.Hour).Format("2006-01-02 15:04:05")
	gotAncient, ok := ComputeRecency(tAncient, now, halfLife)
	if !ok {
		t.Fatalf("ComputeRecency(t=10y): ok=false, want true")
	}
	if gotAncient <= 0 {
		t.Errorf("ComputeRecency(t=10y) = %v, want > 0 (recency must never hard-filter)", gotAncient)
	}

	// Unparseable UpdatedAt -> ok=false.
	if _, ok := ComputeRecency("not-a-timestamp", now, halfLife); ok {
		t.Error("ComputeRecency(garbage): ok=true, want false")
	}
}

// TestImportanceScore_NormalizedToUnitRange locks the normalization contract:
// per-type weight divided by the max configured weight, landing
// decision/architecture at 1.0 by default.
func TestImportanceScore_NormalizedToUnitRange(t *testing.T) {
	cfg := config.RankingConfig{}

	got := ImportanceScore("decision", cfg)
	if !floatsClose(got, 1.0, 1e-9) {
		t.Errorf("ImportanceScore(decision) = %v, want 1.0 (top default tier)", got)
	}

	got = ImportanceScore("bugfix", cfg)
	want := 2.0 / 3.0
	if !floatsClose(got, want, 1e-9) {
		t.Errorf("ImportanceScore(bugfix) = %v, want %v", got, want)
	}

	got = ImportanceScore("tool_use", cfg)
	want = 1.0 / 3.0
	if !floatsClose(got, want, 1e-9) {
		t.Errorf("ImportanceScore(tool_use) = %v, want %v", got, want)
	}

	// An override raises the ceiling every score normalizes against.
	cfgWithOverride := config.RankingConfig{ImportanceOverrides: map[string]float32{"decision": 6}}
	got = ImportanceScore("decision", cfgWithOverride)
	if !floatsClose(got, 1.0, 1e-9) {
		t.Errorf("ImportanceScore(decision, override=6) = %v, want 1.0 (still the max)", got)
	}
	got = ImportanceScore("tool_use", cfgWithOverride)
	want = 1.0 / 6.0
	if !floatsClose(got, want, 1e-9) {
		t.Errorf("ImportanceScore(tool_use) with a raised ceiling = %v, want %v", got, want)
	}
}

// ─── Phase 3: RankResults ────────────────────────────────────────────────────

func sr(id int64, typ, updatedAt string, rank float64) store.SearchResult {
	return store.SearchResult{
		Observation: store.Observation{ID: id, Type: typ, UpdatedAt: updatedAt},
		Rank:        rank,
	}
}

// TestRankResults_DisabledIsNoop is the backward-compat gate (Requirement:
// Backward-Compatible Default Behavior): Enabled=false must return results
// completely untouched, same order, same values.
func TestRankResults_DisabledIsNoop(t *testing.T) {
	now := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	results := []store.SearchResult{
		sr(1, "tool_use", "2026-07-01 00:00:00", 0),
		sr(2, "decision", "2026-01-01 00:00:00", 0),
	}
	relevance := map[int64]float64{1: 0.9, 2: 0.1}
	cfg := config.RankingConfig{Enabled: false, Weights: config.RankingWeights{Recency: 1, Importance: 1, Relevance: 1}, RecencyHalfLifeDays: 14}

	got := RankResults(results, relevance, cfg, now)
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 2 {
		t.Fatalf("RankResults(disabled) reordered results: got %v, want [1 2] untouched", idsOfResults(got))
	}
}

// TestRankResults_RelevanceGapWins: a large relevance gap survives ranking
// even with default weights.
func TestRankResults_RelevanceGapWins(t *testing.T) {
	now := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	// Same type/recency so only relevance differs.
	results := []store.SearchResult{
		sr(1, "tool_use", "2026-07-15 00:00:00", 0), // low relevance
		sr(2, "tool_use", "2026-07-15 00:00:00", 0), // high relevance
	}
	relevance := map[int64]float64{1: 0.05, 2: 0.95}
	cfg := config.RankingConfig{Enabled: true, Weights: config.RankingWeights{Recency: 1, Importance: 1, Relevance: 1}, RecencyHalfLifeDays: 14}

	got := RankResults(results, relevance, cfg, now)
	if got[0].ID != 2 {
		t.Fatalf("RankResults: expected the notably more relevant result (id 2) first, got order %v", idsOfResults(got))
	}
}

// TestRankResults_RecencyBreaksNearTie: near-identical relevance, one
// UpdatedAt far more recent — the fresher one must rank higher.
func TestRankResults_RecencyBreaksNearTie(t *testing.T) {
	now := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	results := []store.SearchResult{
		sr(1, "tool_use", "2025-01-01 00:00:00", 0), // stale
		sr(2, "tool_use", "2026-07-21 00:00:00", 0), // fresh
	}
	relevance := map[int64]float64{1: 0.50, 2: 0.51}
	cfg := config.RankingConfig{Enabled: true, Weights: config.RankingWeights{Recency: 1, Importance: 1, Relevance: 1}, RecencyHalfLifeDays: 14}

	got := RankResults(results, relevance, cfg, now)
	if got[0].ID != 2 {
		t.Fatalf("RankResults: expected the fresher near-tied result (id 2) first, got order %v", idsOfResults(got))
	}
}

// TestRankResults_DecisionOutranksToolUseAtParity: equal relevance and
// UpdatedAt — the decision observation must rank first.
func TestRankResults_DecisionOutranksToolUseAtParity(t *testing.T) {
	now := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	results := []store.SearchResult{
		sr(1, "tool_use", "2026-07-15 00:00:00", 0),
		sr(2, "decision", "2026-07-15 00:00:00", 0),
	}
	relevance := map[int64]float64{1: 0.5, 2: 0.5}
	cfg := config.RankingConfig{Enabled: true, Weights: config.RankingWeights{Recency: 1, Importance: 1, Relevance: 1}, RecencyHalfLifeDays: 14}

	got := RankResults(results, relevance, cfg, now)
	if got[0].ID != 2 {
		t.Fatalf("RankResults: expected decision (id 2) to outrank tool_use at parity, got order %v", idsOfResults(got))
	}
}

// TestRankResults_OldImportantNotExcluded: an old, high-importance,
// high-relevance decision must stay in the results (never hard-filtered by
// recency), even if it ranks below a fresher, lower-importance match.
func TestRankResults_OldImportantNotExcluded(t *testing.T) {
	now := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	results := []store.SearchResult{
		sr(1, "decision", "2020-01-01 00:00:00", 0), // old but important+relevant
		sr(2, "tool_use", "2026-07-21 00:00:00", 0), // fresh, low importance/relevance
	}
	relevance := map[int64]float64{1: 0.9, 2: 0.2}
	cfg := config.RankingConfig{Enabled: true, Weights: config.RankingWeights{Recency: 1, Importance: 1, Relevance: 1}, RecencyHalfLifeDays: 14}

	got := RankResults(results, relevance, cfg, now)
	if len(got) != 2 {
		t.Fatalf("RankResults: expected the old important result to stay in output, got %v", idsOfResults(got))
	}
	found := false
	for _, r := range got {
		if r.ID == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("RankResults: old high-importance decision (id 1) must not be excluded, got %v", idsOfResults(got))
	}
}

// TestRankResults_ExactSentinelStaysFirst: Rank==-1000 rows pre-empt ranking
// entirely, mirroring recall.Fuse's own exact-match sentinel contract.
func TestRankResults_ExactSentinelStaysFirst(t *testing.T) {
	now := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	results := []store.SearchResult{
		sr(1, "tool_use", "2026-07-20 00:00:00", -1000), // exact sentinel, low relevance/importance
		sr(2, "decision", "2026-07-21 00:00:00", 0),     // would otherwise win on every component
	}
	relevance := map[int64]float64{1: 0.01, 2: 0.99}
	cfg := config.RankingConfig{Enabled: true, Weights: config.RankingWeights{Recency: 1, Importance: 1, Relevance: 1}, RecencyHalfLifeDays: 14}

	got := RankResults(results, relevance, cfg, now)
	if got[0].ID != 1 {
		t.Fatalf("RankResults: expected exact sentinel (id 1) to stay first, got order %v", idsOfResults(got))
	}
}

// srSignature builds a signature-match SearchResult (Rank ==
// signatureMatchRank, SignatureMatch == true) — the shape internal/store's
// Search produces for its error-signature lane (design obs #1498, slice 1).
func srSignature(id int64, typ, updatedAt string) store.SearchResult {
	r := sr(id, typ, updatedAt, -500)
	r.SignatureMatch = true
	return r
}

// TestRankResults_SignatureMatchDoesNotCorruptNormalization is the RED test
// for the signature-match sentinel bug: on the FTS5-only relevance path,
// handleSearch computes relevance[id] = -rank for every non-topic_key-
// sentinel row, including signature-match rows — whose Rank is -500, turning
// into a relevance of ~500. Left in the min-max normalization batch
// alongside ordinary bm25-derived relevance values (small positives), this
// outlier pins itself to 1.0 and crushes every ordinary row's normalized
// relevance toward 0 — which doesn't reorder the ordinary rows AMONG
// THEMSELVES (min-max is affine), but DOES collapse relevance's contribution
// to RankScore's weighted sum, letting recency/importance silently override
// a real, large relevance gap between ordinary rows (violating Requirement:
// Ranking Combines Relevance, Recency, and Importance's "strong relevance
// gap still wins" scenario).
//
// id=1 is fresh but barely relevant; id=2 is stale but hugely more relevant.
// Weights zero out importance and weight relevance 5x recency, so a CORRECT
// normalization must let id=2's relevance gap dominate id=1's recency edge.
// A signature-match row (id=99) is mixed in with an outlier relevance of 500
// in the map, mirroring the real handleSearch/CLI relevance-building bug.
func TestRankResults_SignatureMatchDoesNotCorruptNormalization(t *testing.T) {
	now := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	results := []store.SearchResult{
		sr(1, "tool_use", now.Format("2006-01-02 15:04:05"), 0),                         // fresh, barely relevant
		sr(2, "tool_use", now.Add(-3650*24*time.Hour).Format("2006-01-02 15:04:05"), 0), // ancient, hugely relevant
		srSignature(99, "bugfix", now.Format("2006-01-02 15:04:05")),
	}
	relevance := map[int64]float64{1: 0.05, 2: 0.95, 99: 500}
	cfg := config.RankingConfig{
		Enabled:             true,
		Weights:             config.RankingWeights{Recency: 1, Importance: 0, Relevance: 5},
		RecencyHalfLifeDays: 14,
	}

	got := RankResults(results, relevance, cfg, now)
	if len(got) != 3 {
		t.Fatalf("RankResults: expected all 3 rows to survive, got %v", idsOfResults(got))
	}
	if got[0].ID != 99 {
		t.Fatalf("RankResults: expected signature-match row (id 99) to pre-empt ranking like the topic_key sentinel, got order %v", idsOfResults(got))
	}
	if got[1].ID != 2 {
		t.Fatalf("RankResults: expected the hugely more relevant ordinary row (id 2) to outrank the fresher-but-barely-relevant row (id 1) — got order %v. "+
			"This fails when the signature-match row's outlier relevance (500) leaks into min-max normalization and crushes both ordinary rows' relevance toward 0, letting recency silently override a real relevance gap.",
			idsOfResults(got))
	}
	if got[2].ID != 1 {
		t.Fatalf("RankResults: expected order [99 2 1], got %v", idsOfResults(got))
	}
}

// ─── Phase 4: BuildReceipt ───────────────────────────────────────────────────

func floatPtr(v float64) *float64 { return &v }

// TestBuildReceipt_FullBreakdown: when every component is available, the
// receipt must surface lexical, semantic, fusion, recency, importance,
// final, and staleness_penalty all at once.
func TestBuildReceipt_FullBreakdown(t *testing.T) {
	receipt := BuildReceipt(floatPtr(-2.5), false, floatPtr(0.87), floatPtr(0.031), floatPtr(0.6), floatPtr(1.0), floatPtr(0.74))

	lexical, ok := receipt["lexical"].(map[string]any)
	if !ok {
		t.Fatalf("receipt[lexical] missing or wrong type: %#v", receipt["lexical"])
	}
	if lexical["rank"] != -2.5 {
		t.Errorf("lexical.rank = %v, want -2.5", lexical["rank"])
	}
	if lexical["exact_match"] != false {
		t.Errorf("lexical.exact_match = %v, want false", lexical["exact_match"])
	}
	if receipt["semantic"] != 0.87 {
		t.Errorf("receipt[semantic] = %v, want 0.87", receipt["semantic"])
	}
	if receipt["fusion"] != 0.031 {
		t.Errorf("receipt[fusion] = %v, want 0.031", receipt["fusion"])
	}
	if receipt["recency"] != 0.6 {
		t.Errorf("receipt[recency] = %v, want 0.6", receipt["recency"])
	}
	if receipt["importance"] != 1.0 {
		t.Errorf("receipt[importance] = %v, want 1.0", receipt["importance"])
	}
	if receipt["final"] != 0.74 {
		t.Errorf("receipt[final] = %v, want 0.74", receipt["final"])
	}
	if _, ok := receipt["staleness_penalty"]; !ok {
		t.Error("receipt[staleness_penalty] missing, want present (reserved forward-compat slot)")
	}
}

// TestBuildReceipt_OmittedByDefault documents BuildReceipt's own contract
// only; the integration-level "no explain arg -> no score_breakdown key at
// all" behavior is proven by TestHandleSearch_ExplainOmitted_NoBreakdownField
// in mcp_test.go against the real handleSearch wiring.
func TestBuildReceipt_OmittedByDefault(t *testing.T) {
	// A receipt built with every component nil must still return a map (the
	// caller decides whether to attach it at all) with every nullable
	// component actually null.
	receipt := BuildReceipt(nil, false, nil, nil, nil, nil, nil)
	lexical, ok := receipt["lexical"].(map[string]any)
	if !ok {
		t.Fatalf("receipt[lexical] missing or wrong type: %#v", receipt["lexical"])
	}
	if lexical["rank"] != nil {
		t.Errorf("lexical.rank = %v, want nil", lexical["rank"])
	}
	for _, key := range []string{"semantic", "fusion", "recency", "importance", "final"} {
		if receipt[key] != nil {
			t.Errorf("receipt[%s] = %v, want nil", key, receipt[key])
		}
	}
}

// TestBuildReceipt_SemanticNullWhenDisabled: the FTS5-only path (semantic
// recall disabled entirely) must still populate lexical/recency/importance/
// final while semantic (and fusion, since no RRF fusion ran) stay null.
func TestBuildReceipt_SemanticNullWhenDisabled(t *testing.T) {
	receipt := BuildReceipt(floatPtr(-1.2), false, nil, nil, floatPtr(0.4), floatPtr(0.33), floatPtr(0.6))
	if receipt["semantic"] != nil {
		t.Errorf("receipt[semantic] = %v, want nil (semantic recall disabled)", receipt["semantic"])
	}
	if receipt["fusion"] != nil {
		t.Errorf("receipt[fusion] = %v, want nil (no RRF fusion ran)", receipt["fusion"])
	}
	lexical := receipt["lexical"].(map[string]any)
	if lexical["rank"] != -1.2 {
		t.Errorf("lexical.rank = %v, want -1.2 (still populated)", lexical["rank"])
	}
	if receipt["recency"] != 0.4 {
		t.Errorf("receipt[recency] = %v, want 0.4 (still populated)", receipt["recency"])
	}
	if receipt["importance"] != 0.33 {
		t.Errorf("receipt[importance] = %v, want 0.33 (still populated)", receipt["importance"])
	}
	if receipt["final"] != 0.6 {
		t.Errorf("receipt[final] = %v, want 0.6 (still populated)", receipt["final"])
	}
}

// TestBuildReceipt_StalenessPenaltyReservedZero: staleness_penalty is always
// 0 this slice — reserved forward-compat slot for
// memory-structural-forgetting (obs #1595 Requirement 6) to populate later
// without a schema change, regardless of what other components are set.
func TestBuildReceipt_StalenessPenaltyReservedZero(t *testing.T) {
	receipt := BuildReceipt(floatPtr(-1), true, floatPtr(0.5), floatPtr(0.02), floatPtr(0.9), floatPtr(1.0), floatPtr(0.95))
	if receipt["staleness_penalty"] != 0 {
		t.Errorf("receipt[staleness_penalty] = %v, want 0 (reserved this slice)", receipt["staleness_penalty"])
	}

	receiptEmpty := BuildReceipt(nil, false, nil, nil, nil, nil, nil)
	if receiptEmpty["staleness_penalty"] != 0 {
		t.Errorf("receipt[staleness_penalty] (empty inputs) = %v, want 0", receiptEmpty["staleness_penalty"])
	}
}

// ─── Phase 5: BuildResultReceipt / MinMaxNormalizeRelevance (exported) ──────

// TestBuildResultReceipt_SignatureMatchTreatedAsMaximallyRelevant is the
// per-hit-receipt half of the signature-match normalization fix: a caller
// that correctly excludes signature-match rows from the normalization batch
// (mirroring the topic_key sentinel exclusion) leaves no entry for that row
// in normalizedRelevance. BuildResultReceipt must treat that omission as
// "maximally relevant" (normRel=1.0), the SAME way it already treats an
// excluded topic_key exact-match row — not as a missing-map-key zero, which
// would silently tank the row's own "final" score.
func TestBuildResultReceipt_SignatureMatchTreatedAsMaximallyRelevant(t *testing.T) {
	now := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	r := srSignature(99, "bugfix", now.Format("2006-01-02 15:04:05"))
	relevance := map[int64]float64{99: 500}
	// Deliberately empty: a caller that correctly excludes signature-match
	// rows from the normalization batch never populates this row's entry.
	normalizedRelevance := map[int64]float64{}
	cfg := config.RankingConfig{Weights: config.RankingWeights{Recency: 1, Importance: 1, Relevance: 1}, RecencyHalfLifeDays: 14}

	receipt := BuildResultReceipt(r, false, cfg, relevance, normalizedRelevance, now)

	lexical, ok := receipt["lexical"].(map[string]any)
	if !ok {
		t.Fatalf("receipt[lexical] missing or wrong type: %#v", receipt["lexical"])
	}
	if lexical["exact_match"] != false {
		t.Errorf("lexical.exact_match = %v, want false (signature match is not a topic_key exact match)", lexical["exact_match"])
	}

	recency, _ := ComputeRecency(r.UpdatedAt, now, cfg.RecencyHalfLifeDays)
	importance := ImportanceScore(r.Type, cfg)
	wantFinal := RankScore(1.0, recency, importance, cfg.Weights)
	if got := receipt["final"]; got != wantFinal {
		t.Errorf("receipt[final] = %v, want %v (signature match must normalize relevance to 1.0, not the missing-key default 0)", got, wantFinal)
	}
}

// TestMinMaxNormalizeRelevance_Exported locks MinMaxNormalizeRelevance's
// exported name/signature (structural nit: internal/mcp exports this
// primitive so cmd/omnia can reuse it instead of hand-rolling its own copy).
func TestMinMaxNormalizeRelevance_Exported(t *testing.T) {
	results := []store.SearchResult{
		sr(1, "tool_use", "2026-07-15 00:00:00", 0),
		sr(2, "tool_use", "2026-07-15 00:00:00", 0),
	}
	relevance := map[int64]float64{1: 0.0, 2: 1.0}
	got := MinMaxNormalizeRelevance(results, relevance)
	if got[1] != 0 || got[2] != 1 {
		t.Errorf("MinMaxNormalizeRelevance = %v, want {1:0 2:1}", got)
	}
}
