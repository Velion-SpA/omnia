package config_test

import (
	"testing"

	"github.com/velion/omnia/internal/config"
)

// type_lens_config_test.go — RED→GREEN tests for Omnia v0.3 "Context
// Economy" PR5 (design obs #1643 section 3.5/4, spec obs #1642
// type-as-lens domain REQ6). TypeLensConfig carries only Enabled — no
// numeric field needs a default, so unlike DiversityConfig/TokenBudgetConfig
// there's nothing for applyDefaults to fill in beyond the zero-value-is-off
// convention every other Context Economy gate shares.

// TestTypeLens_DefaultsDisabled locks the same backward-compatible rollback
// guarantee every other Context Economy gate shares: a config with no
// `injection` section at all must default to TypeLens.Enabled=false.
func TestTypeLens_DefaultsDisabled(t *testing.T) {
	path := writeTempConfig(t, "engram:\n  base_url: http://127.0.0.1:7437\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Injection.TypeLens.Enabled {
		t.Error("Injection.TypeLens.Enabled: got true, want false by default")
	}
}

// TestTypeLens_ParsesOverrides is the full injection.type_lens.* yaml
// roundtrip.
func TestTypeLens_ParsesOverrides(t *testing.T) {
	path := writeTempConfig(t, ""+
		"injection:\n"+
		"  type_lens:\n"+
		"    enabled: true\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Injection.TypeLens.Enabled {
		t.Error("Injection.TypeLens.Enabled: got false, want true")
	}
}
