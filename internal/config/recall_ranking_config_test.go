package config_test

import (
	"testing"

	"github.com/velion/omnia/internal/config"
)

// TestRankingConfig_DefaultsDisabled locks the same backward-compatible
// rollback guarantee TestRecall_DefaultsDisabled locks for recall.enabled
// (spec Requirement: Backward-Compatible Default Behavior): a config with no
// recall.ranking section at all must default to Enabled=false, with the
// three weights and the recency half-life still filled to their documented
// defaults (Requirement: Configurable Weights With Safe Defaults) so an
// operator who opts in with only `recall: { ranking: { enabled: true } }`
// gets the proven default weighting, not zero-valued weights that would
// silently zero out every RankScore.
func TestRankingConfig_DefaultsDisabled(t *testing.T) {
	path := writeTempConfig(t, "engram:\n  base_url: http://127.0.0.1:7437\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Recall.Ranking.Enabled {
		t.Error("Ranking.Enabled: got true, want false by default")
	}
	if cfg.Recall.Ranking.Weights.Recency != 1.0 {
		t.Errorf("Weights.Recency default: got %v, want 1.0", cfg.Recall.Ranking.Weights.Recency)
	}
	if cfg.Recall.Ranking.Weights.Importance != 1.0 {
		t.Errorf("Weights.Importance default: got %v, want 1.0", cfg.Recall.Ranking.Weights.Importance)
	}
	if cfg.Recall.Ranking.Weights.Relevance != 1.0 {
		t.Errorf("Weights.Relevance default: got %v, want 1.0", cfg.Recall.Ranking.Weights.Relevance)
	}
	if cfg.Recall.Ranking.RecencyHalfLifeDays != 14 {
		t.Errorf("RecencyHalfLifeDays default: got %v, want 14", cfg.Recall.Ranking.RecencyHalfLifeDays)
	}
}

// TestRankingConfig_ParsesOverrides is the full recall.ranking.* yaml
// roundtrip: every field an operator can set must load back exactly,
// including a per-type importance_overrides entry.
func TestRankingConfig_ParsesOverrides(t *testing.T) {
	path := writeTempConfig(t, ""+
		"recall:\n"+
		"  ranking:\n"+
		"    enabled: true\n"+
		"    recency_half_life_days: 7\n"+
		"    weights:\n"+
		"      recency: 0.5\n"+
		"      importance: 2\n"+
		"      relevance: 1.5\n"+
		"    importance_overrides:\n"+
		"      decision: 5\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Recall.Ranking.Enabled {
		t.Error("Ranking.Enabled: got false, want true")
	}
	if cfg.Recall.Ranking.RecencyHalfLifeDays != 7 {
		t.Errorf("RecencyHalfLifeDays: got %v, want 7", cfg.Recall.Ranking.RecencyHalfLifeDays)
	}
	if cfg.Recall.Ranking.Weights.Recency != 0.5 {
		t.Errorf("Weights.Recency: got %v, want 0.5", cfg.Recall.Ranking.Weights.Recency)
	}
	if cfg.Recall.Ranking.Weights.Importance != 2 {
		t.Errorf("Weights.Importance: got %v, want 2", cfg.Recall.Ranking.Weights.Importance)
	}
	if cfg.Recall.Ranking.Weights.Relevance != 1.5 {
		t.Errorf("Weights.Relevance: got %v, want 1.5", cfg.Recall.Ranking.Weights.Relevance)
	}
	if got := cfg.Recall.Ranking.ImportanceOverrides["decision"]; got != 5 {
		t.Errorf("ImportanceOverrides[decision]: got %v, want 5", got)
	}
}

// TestDefaultImportanceWeight_DecisionAboveChatter locks the importance
// heuristic-by-type ordering (Requirement: Importance Heuristic By
// Observation Type): decision/architecture must weigh strictly more than
// session-chatter types (tool_use/file_read/search).
func TestDefaultImportanceWeight_DecisionAboveChatter(t *testing.T) {
	chatterTypes := []string{"tool_use", "file_read", "search"}
	for _, chatter := range chatterTypes {
		chatterWeight := config.DefaultImportanceWeight(chatter)
		if w := config.DefaultImportanceWeight("decision"); w <= chatterWeight {
			t.Errorf("DefaultImportanceWeight(decision)=%v must outweigh DefaultImportanceWeight(%s)=%v", w, chatter, chatterWeight)
		}
		if w := config.DefaultImportanceWeight("architecture"); w <= chatterWeight {
			t.Errorf("DefaultImportanceWeight(architecture)=%v must outweigh DefaultImportanceWeight(%s)=%v", w, chatter, chatterWeight)
		}
	}
	// bugfix/pattern/manual are mid-tier: above chatter, at or below decision/architecture.
	for _, mid := range []string{"bugfix", "pattern", "manual"} {
		midWeight := config.DefaultImportanceWeight(mid)
		if midWeight <= config.DefaultImportanceWeight("tool_use") {
			t.Errorf("DefaultImportanceWeight(%s)=%v must outweigh chatter", mid, midWeight)
		}
		if midWeight > config.DefaultImportanceWeight("decision") {
			t.Errorf("DefaultImportanceWeight(%s)=%v must not exceed decision", mid, midWeight)
		}
	}
}
