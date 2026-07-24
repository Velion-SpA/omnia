package mcp

// token_budget.go — Omnia v0.3 "Context Economy" PR2 (design obs #1643
// decision 2, spec obs #1642 injection-budget domain): bounds handleSearch's
// total injected-content size by ESTIMATED TOKEN COUNT instead of the flat
// per-item char preview truncation that existed before this slice, using the
// shared internal/token primitive (PR1) so mem_search (this file),
// FormatContext (PR3), and cmd/omnia/recall_fix.go (this PR, migrated
// separately) can never drift out of sync (spec REQ4).
//
// ApplyTokenBudget is wired as the LAST pass in handleSearch's pipeline
// (after RankResults/ApplyStalenessDownrank; PR4/PR5 insert ApplyMMR/
// ApplyTypeLens before it), mirroring RankResults/ApplyStalenessDownrank's
// own gated-no-op, stable-partition shape (recall_ranking.go,
// anchor_adapter.go): the topic_key exact-match sentinel and any
// SignatureMatch row are partitioned OUT and ride OUTSIDE the budget —
// always emitted complete, never trimmed, never counted against MaxTokens
// (spec REQ5) — only the remaining rows are trimmed via
// internal/token.TrimToBudget's top-N-complete semantics (spec REQ2).

import (
	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/store"
	"github.com/velion/omnia/internal/token"
)

// tokenBudgetPreviewChars mirrors handleSearch's own preview truncation
// (truncate(r.Content, 300) in the display loop) so ApplyTokenBudget
// estimates the size of what will ACTUALLY be rendered, not the full
// (potentially much larger) stored Content.
const tokenBudgetPreviewChars = 300

// ApplyTokenBudget trims results to fit cfg.MaxTokens estimated tokens (spec
// REQ1: token-based, not char/count-based), keeping top-ranked rows complete
// in order and dropping the rest entirely — no partial/mid-content
// truncation (spec REQ2, internal/token.TrimToBudget's own contract).
//
// It is a gated no-op — results is returned completely untouched, same
// slice, same order — when cfg.Enabled is false or cfg.MaxTokens <= 0 (spec
// REQ6: disabled by default, byte-for-byte no-op when off). An empty
// results is also returned untouched.
//
// The topic_key exact-match sentinel (Rank == exactSentinelRank) and any
// SignatureMatch row are excluded from budget accounting entirely (spec
// REQ5): they are always emitted complete regardless of how small
// cfg.MaxTokens is, mirroring RankResults/ApplyStalenessDownrank's own
// pre-emption of both. Only the remaining ("rest") rows are trimmed via
// token.TrimToBudget. When cfg.MaxTokens is smaller than the smallest
// eligible "rest" item, TrimToBudget returns nothing for rest — the
// pre-empted rows still come back complete, which is correct (not an
// error): budget too small to fit ANY ordinary row must never force one in
// over budget, nor exclude the pre-empted rows that never competed for it.
func ApplyTokenBudget(results []store.SearchResult, cfg config.TokenBudgetConfig) []store.SearchResult {
	if !cfg.Enabled || cfg.MaxTokens <= 0 || len(results) == 0 {
		return results
	}

	var preempted, rest []store.SearchResult
	for _, r := range results {
		if r.Rank == exactSentinelRank || r.SignatureMatch {
			preempted = append(preempted, r)
		} else {
			rest = append(rest, r)
		}
	}
	if len(rest) == 0 {
		return results
	}

	kept, _ := token.TrimToBudget(rest, previewTokens, cfg.MaxTokens)

	out := make([]store.SearchResult, 0, len(preempted)+len(kept))
	out = append(out, preempted...)
	out = append(out, kept...)
	return out
}

// previewTokens estimates the token size of the SAME preview handleSearch
// actually renders for a result — truncate(r.Content, 300) — so budget
// accounting matches real output size instead of the full stored Content
// (which can be arbitrarily larger than what's ever displayed).
func previewTokens(r store.SearchResult) int {
	return token.EstimateTokens(truncate(r.Content, tokenBudgetPreviewChars))
}
