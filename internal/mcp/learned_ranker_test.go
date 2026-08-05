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

// TestApplyLearnedRanker_IgnoresSalienceEvenWhenEnabled: the learned
// ranker's fixed 6-feature schema has no salience slot. id1/id2 tie on every
// feature except OutcomeHistory (id2) and Salience (id1, much higher). With
// weights.salience heavily weighted, RankResults ranks id1 first — but
// ApplyLearnedRanker on that same output must rank purely on OutcomeHistory
// and put id2 first, proving it silently discards the salience-driven order.
func TestApplyLearnedRanker_IgnoresSalienceEvenWhenEnabled(t *testing.T) {
	sameTime := "2026-07-30 00:00:00"
	hiSalience := 0.95
	worked := "worked"
	results := []store.SearchResult{
		{Observation: store.Observation{ID: 1, Type: "decision", UpdatedAt: sameTime, Salience: &hiSalience}},
		{Observation: store.Observation{ID: 2, Type: "decision", UpdatedAt: sameTime, Outcome: &worked}},
	}
	relevance := map[int64]float64{1: 0.5, 2: 0.5}
	rankingCfg := config.RankingConfig{Enabled: true, Weights: config.RankingWeights{Relevance: 1, Recency: 1, Importance: 1, Salience: 100}}
	now, err := time.Parse("2006-01-02 15:04:05", sameTime)
	if err != nil {
		t.Fatalf("time.Parse: %v", err)
	}

	ranked := RankResults(results, relevance, rankingCfg, now)
	if ranked[0].ID != 1 {
		t.Fatalf("precondition failed: RankResults did not rank higher-salience id1 first: %v", idsOfResults(ranked))
	}

	model := &ranker.Model{FeatureSchema: ranker.FeatureSchema(), Weights: []float64{0, 0, 0, 0, 10, 0}}
	got := ApplyLearnedRanker(ranked, relevance, config.RankerConfig{Enabled: true}, model, rankingCfg, now)
	if got[0].ID != 2 {
		t.Fatalf("learned ranker did not override the salience-driven order: %v (salience has no feature slot and must not leak through)", idsOfResults(got))
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
