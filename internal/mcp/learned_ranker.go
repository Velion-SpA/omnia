package mcp

import (
	"sort"
	"time"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/ranker"
	"github.com/velion/omnia/internal/store"
)

// ApplyLearnedRanker is the optional final ranking pass. Its disabled, cold-start,
// missing-model, and invalid-model callers pass nil and receive the original slice
// unchanged, preserving the previous pipeline byte-for-byte.
func ApplyLearnedRanker(results []store.SearchResult, relevance map[int64]float64, cfg config.RankerConfig, model *ranker.Model, ranking config.RankingConfig, now time.Time) []store.SearchResult {
	if !cfg.Enabled || model == nil || len(results) == 0 {
		return results
	}
	var preempted, rest []store.SearchResult
	for _, result := range results {
		if result.Rank == exactSentinelRank || result.SignatureMatch {
			preempted = append(preempted, result)
			continue
		}
		rest = append(rest, result)
	}
	normalized := MinMaxNormalizeRelevance(rest, relevance)
	type scored struct {
		result store.SearchResult
		score  float64
	}
	scoredResults := make([]scored, 0, len(rest))
	for _, result := range rest {
		var outcome string
		if result.Outcome != nil {
			outcome = *result.Outcome
		}
		features := ranker.BuildFeatures(ranker.Candidate{LexicalRRF: normalized[result.ID], UpdatedAt: parseResultTime(result.UpdatedAt), Type: result.Type, Outcome: outcome}, ranking, now)
		scoredResults = append(scoredResults, scored{result, model.Predict(features)})
	}
	sort.SliceStable(scoredResults, func(i, j int) bool { return scoredResults[i].score > scoredResults[j].score })
	out := make([]store.SearchResult, 0, len(results))
	out = append(out, preempted...)
	for _, item := range scoredResults {
		out = append(out, item.result)
	}
	return out
}
func parseResultTime(value string) time.Time {
	parsed, err := parseRecencyTime(value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}
