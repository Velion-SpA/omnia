package config_test

import (
	"testing"

	"github.com/velion/omnia/internal/config"
)

// injection_config_test.go — RED→GREEN tests for Omnia v0.3 "Context
// Economy" PR2 (design obs #1643, spec obs #1642, injection-budget domain
// REQ6). Mirrors recall_ranking_config_test.go's shape for its own
// backward-compatible-default + full-roundtrip pair.

// TestInjectionBudget_DefaultsDisabled locks the same backward-compatible
// rollback guarantee every other Context Economy gate shares: a config with
// no `injection` section at all must default to Budget.Enabled=false, with
// MaxTokens still filled to its documented default (1500) so an operator who
// opts in with only `injection: { budget: { enabled: true } }` gets a sane
// ceiling, not a zero-valued budget that would trim every result away.
func TestInjectionBudget_DefaultsDisabled(t *testing.T) {
	path := writeTempConfig(t, "engram:\n  base_url: http://127.0.0.1:7437\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Injection.Budget.Enabled {
		t.Error("Injection.Budget.Enabled: got true, want false by default")
	}
	if cfg.Injection.Budget.MaxTokens != 1500 {
		t.Errorf("Injection.Budget.MaxTokens default: got %v, want 1500", cfg.Injection.Budget.MaxTokens)
	}
}

// TestInjectionBudget_ParsesOverrides is the full injection.budget.* yaml
// roundtrip: every field an operator can set must load back exactly.
func TestInjectionBudget_ParsesOverrides(t *testing.T) {
	path := writeTempConfig(t, ""+
		"injection:\n"+
		"  budget:\n"+
		"    enabled: true\n"+
		"    max_tokens: 800\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Injection.Budget.Enabled {
		t.Error("Injection.Budget.Enabled: got false, want true")
	}
	if cfg.Injection.Budget.MaxTokens != 800 {
		t.Errorf("Injection.Budget.MaxTokens: got %v, want 800", cfg.Injection.Budget.MaxTokens)
	}
}

// TestInjectionBudget_ExplicitZeroMaxTokensStaysZero (PR2 review fix,
// WARNING): mirrors recallEnabledKeyPresent's explicit-vs-absent precedent
// (config.go:443-453) for injection.budget.max_tokens. applyDefaults must
// only fill MaxTokens->1500 when the key is entirely ABSENT from the YAML —
// an operator who explicitly writes `max_tokens: 0` is deliberately
// disabling ApplyTokenBudget's trim pass (its own MaxTokens<=0 "disabled"
// branch), and applyDefaults silently overriding that back to 1500 makes
// that branch unreachable from real config.
func TestInjectionBudget_ExplicitZeroMaxTokensStaysZero(t *testing.T) {
	path := writeTempConfig(t, ""+
		"injection:\n"+
		"  budget:\n"+
		"    enabled: true\n"+
		"    max_tokens: 0\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Injection.Budget.MaxTokens != 0 {
		t.Errorf("Injection.Budget.MaxTokens with explicit max_tokens: 0: got %v, want 0 (explicit zero must stick)", cfg.Injection.Budget.MaxTokens)
	}
}
