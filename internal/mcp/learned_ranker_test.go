package mcp

import (
	"testing"
	"time"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/ranker"
	"github.com/velion/omnia/internal/store"
)

func TestApplyLearnedRankerDisabledAndColdStartAreNoops(t *testing.T) {
	results := []store.SearchResult{{Observation: store.Observation{ID: 1, Type: "decision"}}, {Observation: store.Observation{ID: 2, Type: "bugfix"}}}
	original := append([]store.SearchResult(nil), results...)
	now := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	if got := ApplyLearnedRanker(results, map[int64]float64{1: 1, 2: 0}, config.RankerConfig{}, nil, config.RankingConfig{}, now); !sameIDs(got, original) {
		t.Fatalf("disabled changed results: %v", idsOfResults(got))
	}
	if got := ApplyLearnedRanker(results, map[int64]float64{1: 1, 2: 0}, config.RankerConfig{Enabled: true}, nil, config.RankingConfig{}, now); !sameIDs(got, original) {
		t.Fatalf("cold start changed results: %v", idsOfResults(got))
	}
}

func TestApplyLearnedRankerUsesPromotedModel(t *testing.T) {
	results := []store.SearchResult{{Observation: store.Observation{ID: 1, Type: "bugfix"}}, {Observation: store.Observation{ID: 2, Type: "decision"}}}
	model := &ranker.Model{FeatureSchema: ranker.FeatureSchema(), Weights: []float64{0, 0, 0, 10, 0, 0}}
	got := ApplyLearnedRanker(results, map[int64]float64{1: .5, 2: .5}, config.RankerConfig{Enabled: true}, model, config.RankingConfig{}, time.Now())
	if got[0].ID != 2 {
		t.Fatalf("model did not rerank results: %v", idsOfResults(got))
	}
}

func sameIDs(a, b []store.SearchResult) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			return false
		}
	}
	return true
}
