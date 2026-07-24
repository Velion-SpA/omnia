package config_test

import (
	"testing"

	"github.com/velion/omnia/internal/config"
)

// diversity_config_test.go — RED→GREEN tests for Omnia v0.3 "Context
// Economy" PR4 (design obs #1643 section 3.3/4, spec obs #1642
// injection-diversity domain). Unlike injection_config_test.go/
// context_budget_config_test.go's TokenBudgetConfig (which needs the
// explicit-vs-absent MaxTokens probe because 0 legitimately means
// "disabled"), Lambda/SimilarityThreshold follow the SIMPLER
// Recall.Ranking.Weights idiom (recall_ranking_config_test.go): a plain
// zero-check default, no probe — design.md section 4 says so explicitly
// ("mirroring the Ranking-weights default idiom").

// TestDiversity_DefaultsDisabled locks the same backward-compatible rollback
// guarantee every other Context Economy gate shares: a config with no
// `injection` section at all must default to Diversity.Enabled=false, with
// Lambda/SimilarityThreshold still filled to their documented defaults
// (0.7/0.9) so an operator who opts in with only
// `injection: { diversity: { enabled: true } }` gets sane MMR params instead
// of zero-valued ones that would silently disable relevance weighting
// (Lambda=0) or hard-drop nothing (SimilarityThreshold=0 would hard-drop
// everything, not nothing — either zero value is wrong by accident).
func TestDiversity_DefaultsDisabled(t *testing.T) {
	path := writeTempConfig(t, "engram:\n  base_url: http://127.0.0.1:7437\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Injection.Diversity.Enabled {
		t.Error("Injection.Diversity.Enabled: got true, want false by default")
	}
	if cfg.Injection.Diversity.Lambda != 0.7 {
		t.Errorf("Injection.Diversity.Lambda default: got %v, want 0.7", cfg.Injection.Diversity.Lambda)
	}
	if cfg.Injection.Diversity.SimilarityThreshold != 0.9 {
		t.Errorf("Injection.Diversity.SimilarityThreshold default: got %v, want 0.9", cfg.Injection.Diversity.SimilarityThreshold)
	}
}

// TestDiversity_ParsesOverrides is the full injection.diversity.* yaml
// roundtrip: every field an operator can set must load back exactly.
func TestDiversity_ParsesOverrides(t *testing.T) {
	path := writeTempConfig(t, ""+
		"injection:\n"+
		"  diversity:\n"+
		"    enabled: true\n"+
		"    lambda: 0.5\n"+
		"    similarity_threshold: 0.8\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Injection.Diversity.Enabled {
		t.Error("Injection.Diversity.Enabled: got false, want true")
	}
	if cfg.Injection.Diversity.Lambda != 0.5 {
		t.Errorf("Injection.Diversity.Lambda: got %v, want 0.5", cfg.Injection.Diversity.Lambda)
	}
	if cfg.Injection.Diversity.SimilarityThreshold != 0.8 {
		t.Errorf("Injection.Diversity.SimilarityThreshold: got %v, want 0.8", cfg.Injection.Diversity.SimilarityThreshold)
	}
}
