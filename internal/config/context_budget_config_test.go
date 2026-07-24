package config_test

import (
	"testing"

	"github.com/velion/omnia/internal/config"
)

// context_budget_config_test.go — RED→GREEN tests for Omnia v0.3 "Context
// Economy" PR3 (design obs #1643/D8, spec obs #1642, injection-budget domain
// REQ3). Mirrors injection_config_test.go's shape (PR2's Budget sub-gate)
// exactly, for the sibling ContextBudget sub-gate that fixes FormatContext's
// pre-existing unbounded-aggregate-bucket defect.

// TestContextBudget_DefaultsDisabled locks the same backward-compatible
// rollback guarantee every other Context Economy gate shares: a config with
// no `injection` section at all must default to ContextBudget.Enabled=false,
// with MaxTokens still filled to its documented default (1500) so an
// operator who opts in with only
// `injection: { context_budget: { enabled: true } }` gets a sane ceiling,
// not a zero-valued budget that would drop every FormatContext bucket.
func TestContextBudget_DefaultsDisabled(t *testing.T) {
	path := writeTempConfig(t, "engram:\n  base_url: http://127.0.0.1:7437\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Injection.ContextBudget.Enabled {
		t.Error("Injection.ContextBudget.Enabled: got true, want false by default")
	}
	if cfg.Injection.ContextBudget.MaxTokens != 1500 {
		t.Errorf("Injection.ContextBudget.MaxTokens default: got %v, want 1500", cfg.Injection.ContextBudget.MaxTokens)
	}
}

// TestContextBudget_ParsesOverrides is the full injection.context_budget.*
// yaml roundtrip: every field an operator can set must load back exactly.
func TestContextBudget_ParsesOverrides(t *testing.T) {
	path := writeTempConfig(t, ""+
		"injection:\n"+
		"  context_budget:\n"+
		"    enabled: true\n"+
		"    max_tokens: 600\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Injection.ContextBudget.Enabled {
		t.Error("Injection.ContextBudget.Enabled: got false, want true")
	}
	if cfg.Injection.ContextBudget.MaxTokens != 600 {
		t.Errorf("Injection.ContextBudget.MaxTokens: got %v, want 600", cfg.Injection.ContextBudget.MaxTokens)
	}
}

// TestContextBudget_ExplicitZeroMaxTokensStaysZero mirrors
// TestInjectionBudget_ExplicitZeroMaxTokensStaysZero (PR2 review fix)
// applied to the ContextBudget sub-gate: an operator who explicitly writes
// `max_tokens: 0` is deliberately reaching FormatContext's own
// ContextTokenBudget<=0 "disabled" branch, not accidentally leaving the key
// unset — applyDefaults must not silently override that back to 1500.
func TestContextBudget_ExplicitZeroMaxTokensStaysZero(t *testing.T) {
	path := writeTempConfig(t, ""+
		"injection:\n"+
		"  context_budget:\n"+
		"    enabled: true\n"+
		"    max_tokens: 0\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Injection.ContextBudget.MaxTokens != 0 {
		t.Errorf("Injection.ContextBudget.MaxTokens with explicit max_tokens: 0: got %v, want 0 (explicit zero must stick)", cfg.Injection.ContextBudget.MaxTokens)
	}
}
