package mcp

// token_budget_test.go — RED→GREEN tests for Omnia v0.3 "Context Economy"
// PR2 (design obs #1643 decision 2, spec obs #1642 injection-budget
// REQ1/REQ2/REQ4/REQ5/REQ6). See preemption_invariant_test.go for the shared
// adversarial sentinel/signature-row invariant this pass must also satisfy —
// this file covers ApplyTokenBudget's own trim/no-op/exclusion behavior.

import (
	"strings"
	"testing"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/store"
	"github.com/velion/omnia/internal/token"
)

// tbSR builds a store.SearchResult with a given ID/Content/Rank, reusing
// recall_ranking_test.go's sr helper for the embedded Observation shape.
func tbSR(id int64, content string, rank float64) store.SearchResult {
	r := sr(id, "manual", "2024-01-01", rank)
	r.Content = content
	return r
}

// TestApplyTokenBudget_DisabledIsNoop is the backward-compat gate (spec
// REQ6): cfg.Enabled=false must return results completely untouched, same
// order, same values — byte-for-byte identical to pre-v0.3 output.
func TestApplyTokenBudget_DisabledIsNoop(t *testing.T) {
	results := []store.SearchResult{tbSR(1, "hello", 1), tbSR(2, "world", 2)}
	got := ApplyTokenBudget(results, config.TokenBudgetConfig{Enabled: false, MaxTokens: 1})
	if !equalInt64s(idsOfResults(got), idsOfResults(results)) {
		t.Fatalf("ApplyTokenBudget(disabled) reordered/dropped results: got %v, want %v", idsOfResults(got), idsOfResults(results))
	}
	for i := range got {
		if got[i].Content != results[i].Content {
			t.Errorf("ApplyTokenBudget(disabled) altered content at index %d: got %q, want %q", i, got[i].Content, results[i].Content)
		}
	}
}

// TestApplyTokenBudget_ZeroMaxTokensIsNoop: MaxTokens<=0 is treated the same
// as Enabled=false — "budget of zero" must never mean "keep nothing" by
// silent accident.
func TestApplyTokenBudget_ZeroMaxTokensIsNoop(t *testing.T) {
	results := []store.SearchResult{tbSR(1, "hello", 1)}
	got := ApplyTokenBudget(results, config.TokenBudgetConfig{Enabled: true, MaxTokens: 0})
	if !equalInt64s(idsOfResults(got), idsOfResults(results)) {
		t.Fatalf("ApplyTokenBudget(MaxTokens=0) = %v, want untouched %v", idsOfResults(got), idsOfResults(results))
	}
}

// TestApplyTokenBudget_TrimsTopRankedCompleteDropsRest (spec REQ1/REQ2): a
// 20-item result set is trimmed to fit MaxTokens, keeping top-ranked items
// COMPLETE (never partially truncated) and dropping the rest entirely.
func TestApplyTokenBudget_TrimsTopRankedCompleteDropsRest(t *testing.T) {
	// Each item's preview is exactly 40 chars -> EstimateTokens(40 runes) = (40+3)/4 = 10 tokens.
	item := strings.Repeat("a", 40)
	results := make([]store.SearchResult, 20)
	for i := range results {
		results[i] = tbSR(int64(i+1), item, float64(i+1))
	}
	// Budget for exactly 5 whole items (5*10 = 50 tokens); a 6th item would push to 60 > 50.
	got := ApplyTokenBudget(results, config.TokenBudgetConfig{Enabled: true, MaxTokens: 50})
	if len(got) != 5 {
		t.Fatalf("expected exactly 5 complete items kept, got %d: %v", len(got), idsOfResults(got))
	}
	wantIDs := []int64{1, 2, 3, 4, 5}
	if !equalInt64s(idsOfResults(got), wantIDs) {
		t.Fatalf("expected top-ranked items 1-5 kept in order, got %v", idsOfResults(got))
	}
	for i, r := range got {
		if r.Content != item {
			t.Errorf("item %d content was partially truncated: got %q, want full %q", i, r.Content, item)
		}
	}
}

// TestApplyTokenBudget_TinyBudgetExcludesPreemptedRowsFromAccounting (spec
// REQ5): sentinel/signature rows are excluded from budget accounting
// entirely. A budget smaller than the smallest eligible non-pre-empted item
// still returns pre-empted rows complete; non-pre-empted rows are dropped,
// with no error.
func TestApplyTokenBudget_TinyBudgetExcludesPreemptedRowsFromAccounting(t *testing.T) {
	sentinel := tbSR(99, "sentinel content that is fairly long on its own", 0)
	sentinel.Rank = exactSentinelRank
	sig := tbSR(50, "signature content that is also fairly long", 0)
	sig.SignatureMatch = true
	normal := tbSR(1, strings.Repeat("b", 4000), 1) // far larger than any tiny budget

	results := []store.SearchResult{sentinel, sig, normal}
	got := ApplyTokenBudget(results, config.TokenBudgetConfig{Enabled: true, MaxTokens: 1})

	if len(got) != 2 {
		t.Fatalf("expected only the 2 pre-empted rows to survive a tiny budget, got %d: %v", len(got), idsOfResults(got))
	}
	if got[0].ID != 99 || got[1].ID != 50 {
		t.Fatalf("expected pre-empted rows in original relative order [99 50], got %v", idsOfResults(got))
	}
	if got[0].Content != sentinel.Content || got[1].Content != sig.Content {
		t.Errorf("pre-empted row content must stay complete/unaltered even under a tiny budget")
	}
}

// TestPreviewTokens_MatchesEstimateOfRenderedPreview locks previewTokens'
// contract: it must estimate the SAME preview handleSearch actually renders
// (truncate(Content, 300)), not the full stored Content, so budget
// accounting matches real output size.
func TestPreviewTokens_MatchesEstimateOfRenderedPreview(t *testing.T) {
	short := tbSR(1, "short content", 1)
	if got, want := previewTokens(short), token.EstimateTokens("short content"); got != want {
		t.Errorf("previewTokens(short) = %d, want %d", got, want)
	}

	long := tbSR(2, strings.Repeat("x", 1000), 1)
	wantLong := token.EstimateTokens(truncate(long.Content, tokenBudgetPreviewChars))
	if got := previewTokens(long); got != wantLong {
		t.Errorf("previewTokens(long) = %d, want %d (estimate of the truncated 300-char preview, not the full content)", got, wantLong)
	}
}
