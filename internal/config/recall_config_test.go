package config_test

import (
	"testing"

	"github.com/velion/omnia/internal/config"
)

// TestRecall_DefaultsDisabled locks the D7 rollback guarantee: a config with
// no recall section must default to enabled=false (mem_search keeps calling
// store.Search directly, byte-for-byte today's FTS5-only path) while still
// filling in the fusion params from internal/recall.DefaultFuseParams()'s
// values, so config and code never drift once recall IS enabled.
func TestRecall_DefaultsDisabled(t *testing.T) {
	path := writeTempConfig(t, "engram:\n  base_url: http://127.0.0.1:7437\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Recall.Enabled {
		t.Error("Recall.Enabled: got true, want false by default (D7 rollback guarantee)")
	}
	if cfg.Recall.RRFK != 60 {
		t.Errorf("RRFK default: got %d, want 60", cfg.Recall.RRFK)
	}
	if cfg.Recall.DenseK != 5 {
		t.Errorf("DenseK default: got %d, want 5", cfg.Recall.DenseK)
	}
	// jina-calibrated floors (issue #83): 0.35/0.25, not the original
	// 0.65/0.55 D2 constants (those were tuned for a different model and
	// starved recall for the default jina/jina-embeddings-v2-base-es model).
	if cfg.Recall.StrongFloor != 0.35 {
		t.Errorf("StrongFloor default: got %v, want 0.35 (jina-calibrated)", cfg.Recall.StrongFloor)
	}
	if cfg.Recall.BaseFloor != 0.25 {
		t.Errorf("BaseFloor default: got %v, want 0.25 (jina-calibrated)", cfg.Recall.BaseFloor)
	}
	if cfg.Recall.MaxResults != 50 {
		t.Errorf("MaxResults default: got %d, want 50", cfg.Recall.MaxResults)
	}
	if cfg.RecallEnabledExplicit {
		t.Error("RecallEnabledExplicit: got true, want false — the config has no recall section at all")
	}
}

// TestRecall_EnabledExplicitTracksPresenceNotValue proves
// Config.RecallEnabledExplicit distinguishes "recall.enabled explicitly set"
// from "never mentioned," independently of which value was set — this is
// the signal issue #83's Ollama auto-detect needs to avoid ever overriding
// an operator's explicit choice (including an explicit `false`).
func TestRecall_EnabledExplicitTracksPresenceNotValue(t *testing.T) {
	t.Run("explicit true", func(t *testing.T) {
		path := writeTempConfig(t, "recall:\n  enabled: true\n")
		cfg, err := config.Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !cfg.RecallEnabledExplicit {
			t.Error("RecallEnabledExplicit: got false, want true (enabled: true was set)")
		}
	})

	t.Run("explicit false", func(t *testing.T) {
		path := writeTempConfig(t, "recall:\n  enabled: false\n")
		cfg, err := config.Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !cfg.RecallEnabledExplicit {
			t.Error("RecallEnabledExplicit: got false, want true (enabled: false was still explicitly set)")
		}
		if cfg.Recall.Enabled {
			t.Error("Recall.Enabled: got true, want false")
		}
	})

	t.Run("recall section present but enabled key absent", func(t *testing.T) {
		path := writeTempConfig(t, "recall:\n  rrf_k: 30\n")
		cfg, err := config.Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.RecallEnabledExplicit {
			t.Error("RecallEnabledExplicit: got true, want false — only rrf_k was set, not enabled")
		}
	})

	t.Run("no recall section at all", func(t *testing.T) {
		path := writeTempConfig(t, "engram:\n  base_url: http://127.0.0.1:7437\n")
		cfg, err := config.Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.RecallEnabledExplicit {
			t.Error("RecallEnabledExplicit: got true, want false — no recall section exists")
		}
	})
}

func TestRecall_ParsesEnabled(t *testing.T) {
	path := writeTempConfig(t, ""+
		"recall:\n"+
		"  enabled: true\n"+
		"  rrf_k: 30\n"+
		"  dense_k: 3\n"+
		"  strong_floor: 0.7\n"+
		"  base_floor: 0.5\n"+
		"  max_results: 20\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Recall.Enabled {
		t.Error("Recall.Enabled: got false, want true")
	}
	if cfg.Recall.RRFK != 30 {
		t.Errorf("RRFK: got %d, want 30", cfg.Recall.RRFK)
	}
	if cfg.Recall.DenseK != 3 {
		t.Errorf("DenseK: got %d, want 3", cfg.Recall.DenseK)
	}
	if cfg.Recall.StrongFloor != 0.7 {
		t.Errorf("StrongFloor: got %v, want 0.7", cfg.Recall.StrongFloor)
	}
	if cfg.Recall.BaseFloor != 0.5 {
		t.Errorf("BaseFloor: got %v, want 0.5", cfg.Recall.BaseFloor)
	}
	if cfg.Recall.MaxResults != 20 {
		t.Errorf("MaxResults: got %d, want 20", cfg.Recall.MaxResults)
	}
}
