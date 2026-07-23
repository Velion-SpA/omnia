package config_test

import (
	"testing"

	"github.com/velion/omnia/internal/config"
)

// TestProcedural_DefaultsDisabled locks the backward-compatibility guarantee
// (spec domain "backward-compatibility"): a config with no procedural
// section must default to enabled=false, while still filling in the
// governance-tuning params so config and code never drift once the flag IS
// enabled.
func TestProcedural_DefaultsDisabled(t *testing.T) {
	path := writeTempConfig(t, "engram:\n  base_url: http://127.0.0.1:7437\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Procedural.Enabled {
		t.Error("Procedural.Enabled: got true, want false by default")
	}
	if cfg.Procedural.TrustThreshold != 3 {
		t.Errorf("TrustThreshold default: got %d, want 3", cfg.Procedural.TrustThreshold)
	}
	if cfg.Procedural.ConfidenceFloor != 0.15 {
		t.Errorf("ConfidenceFloor default: got %v, want 0.15", cfg.Procedural.ConfidenceFloor)
	}
	if cfg.Procedural.ReviewAfterDays != 14 {
		t.Errorf("ReviewAfterDays default: got %d, want 14", cfg.Procedural.ReviewAfterDays)
	}
}

// TestProcedural_ParsesEnabledAndOverrides triangulates with a DIFFERENT
// setup: an operator who explicitly enables procedural memory and overrides
// every tuning param must see those exact values, not the defaults.
func TestProcedural_ParsesEnabledAndOverrides(t *testing.T) {
	path := writeTempConfig(t, ""+
		"procedural:\n"+
		"  enabled: true\n"+
		"  trust_threshold: 5\n"+
		"  confidence_floor: 0.3\n"+
		"  review_after_days: 30\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Procedural.Enabled {
		t.Error("Procedural.Enabled: got false, want true")
	}
	if cfg.Procedural.TrustThreshold != 5 {
		t.Errorf("TrustThreshold: got %d, want 5", cfg.Procedural.TrustThreshold)
	}
	if cfg.Procedural.ConfidenceFloor != 0.3 {
		t.Errorf("ConfidenceFloor: got %v, want 0.3", cfg.Procedural.ConfidenceFloor)
	}
	if cfg.Procedural.ReviewAfterDays != 30 {
		t.Errorf("ReviewAfterDays: got %d, want 30", cfg.Procedural.ReviewAfterDays)
	}
}
