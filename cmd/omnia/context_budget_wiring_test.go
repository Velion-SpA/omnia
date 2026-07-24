package main

import (
	"errors"
	"testing"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/store"
)

// context_budget_wiring_test.go — RED→GREEN tests for Omnia v0.3 "Context
// Economy" PR3 (design obs #1643/D8, spec obs #1642): confirm
// cmdContext/cmdMCP/cmdServe each set store.Config.ContextTokenBudget from
// injection.context_budget BEFORE storeNew constructs the *store.Store
// (s.cfg is immutable after New, so this MUST happen pre-construction).
// Mirrors PR2's mcpCfg.Injection wiring precedent (main.go's cmdMCP), but
// captures the store.Config actually passed to storeNew instead of a
// MCPConfig field.

func withCapturedStoreConfig(t *testing.T) *store.Config {
	t.Helper()
	var captured store.Config
	oldStoreNew := storeNew
	storeNew = func(cfg store.Config) (*store.Store, error) {
		captured = cfg
		return oldStoreNew(cfg)
	}
	t.Cleanup(func() { storeNew = oldStoreNew })
	return &captured
}

func withContextBudgetAppConfig(t *testing.T, enabled bool, maxTokens int) {
	t.Helper()
	old := loadAppConfigWithRecallAutodetect
	loadAppConfigWithRecallAutodetect = func() (*config.Config, error) {
		return &config.Config{
			Injection: config.InjectionConfig{
				ContextBudget: config.TokenBudgetConfig{Enabled: enabled, MaxTokens: maxTokens},
			},
		}, nil
	}
	t.Cleanup(func() { loadAppConfigWithRecallAutodetect = old })
}

func TestCmdContext_ThreadsContextTokenBudgetFromConfig(t *testing.T) {
	t.Run("enabled propagates MaxTokens", func(t *testing.T) {
		stubRuntimeHooks(t)
		cfg := testConfig(t)
		captured := withCapturedStoreConfig(t)
		withContextBudgetAppConfig(t, true, 777)
		withArgs(t, "omnia", "context")

		cmdContext(cfg)

		if captured.ContextTokenBudget != 777 {
			t.Fatalf("expected cmdContext to thread injection.context_budget.max_tokens=777 into store.Config.ContextTokenBudget, got %d", captured.ContextTokenBudget)
		}
	})

	t.Run("disabled stays zero (default no-op)", func(t *testing.T) {
		stubRuntimeHooks(t)
		cfg := testConfig(t)
		captured := withCapturedStoreConfig(t)
		withContextBudgetAppConfig(t, false, 1500)
		withArgs(t, "omnia", "context")

		cmdContext(cfg)

		if captured.ContextTokenBudget != 0 {
			t.Fatalf("expected disabled injection.context_budget to leave store.Config.ContextTokenBudget at 0, got %d", captured.ContextTokenBudget)
		}
	})

	t.Run("missing config.yaml degrades to zero", func(t *testing.T) {
		stubRuntimeHooks(t)
		cfg := testConfig(t)
		captured := withCapturedStoreConfig(t)
		old := loadAppConfigWithRecallAutodetect
		loadAppConfigWithRecallAutodetect = func() (*config.Config, error) {
			return nil, errors.New("no config.yaml")
		}
		t.Cleanup(func() { loadAppConfigWithRecallAutodetect = old })
		withArgs(t, "omnia", "context")

		cmdContext(cfg)

		if captured.ContextTokenBudget != 0 {
			t.Fatalf("expected missing config.yaml to degrade to ContextTokenBudget=0, got %d", captured.ContextTokenBudget)
		}
	})
}

func TestCmdMCP_ThreadsContextTokenBudgetFromConfig(t *testing.T) {
	t.Run("enabled propagates MaxTokens", func(t *testing.T) {
		stubRuntimeHooks(t)
		cfg := testConfig(t)
		captured := withCapturedStoreConfig(t)
		withContextBudgetAppConfig(t, true, 321)
		withArgs(t, "omnia", "mcp")

		cmdMCP(cfg)

		if captured.ContextTokenBudget != 321 {
			t.Fatalf("expected cmdMCP to thread injection.context_budget.max_tokens=321 into store.Config.ContextTokenBudget, got %d", captured.ContextTokenBudget)
		}
	})

	t.Run("disabled stays zero (default no-op)", func(t *testing.T) {
		stubRuntimeHooks(t)
		cfg := testConfig(t)
		captured := withCapturedStoreConfig(t)
		withContextBudgetAppConfig(t, false, 1500)
		withArgs(t, "omnia", "mcp")

		cmdMCP(cfg)

		if captured.ContextTokenBudget != 0 {
			t.Fatalf("expected disabled injection.context_budget to leave store.Config.ContextTokenBudget at 0, got %d", captured.ContextTokenBudget)
		}
	})
}

func TestCmdServe_ThreadsContextTokenBudgetFromConfig(t *testing.T) {
	t.Run("enabled propagates MaxTokens", func(t *testing.T) {
		stubRuntimeHooks(t)
		cfg := testConfig(t)
		captured := withCapturedStoreConfig(t)
		withContextBudgetAppConfig(t, true, 999)
		withArgs(t, "omnia", "serve")

		cmdServe(cfg)

		if captured.ContextTokenBudget != 999 {
			t.Fatalf("expected cmdServe to thread injection.context_budget.max_tokens=999 into store.Config.ContextTokenBudget, got %d", captured.ContextTokenBudget)
		}
	})

	t.Run("disabled stays zero (default no-op)", func(t *testing.T) {
		stubRuntimeHooks(t)
		cfg := testConfig(t)
		captured := withCapturedStoreConfig(t)
		withContextBudgetAppConfig(t, false, 1500)
		withArgs(t, "omnia", "serve")

		cmdServe(cfg)

		if captured.ContextTokenBudget != 0 {
			t.Fatalf("expected disabled injection.context_budget to leave store.Config.ContextTokenBudget at 0, got %d", captured.ContextTokenBudget)
		}
	})
}
