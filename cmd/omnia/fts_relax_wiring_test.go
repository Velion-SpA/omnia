package main

import (
	"errors"
	"testing"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/store"
)

// fts_relax_wiring_test.go — RED→GREEN tests for the omnia-0.3.1-write-hygiene
// PR7 follow-up fix (design obs #1668 D7, spec fts-recall domain): confirm
// cmdServe/cmdMCP/cmdContext/cmdSave each thread `recall.fts_relax_on_zero`
// from config.yaml into store.Config.DisableFTSRelax BEFORE storeNew
// constructs the *store.Store (s.cfg is immutable after New — same
// requirement write_hygiene_wiring_test.go/context_budget_wiring_test.go
// established), reusing those files' withCapturedStoreConfig/testConfig/
// withArgs/stubRuntimeHooks helpers exactly.
//
// UNLIKE WriteHygieneEnabled/ContextTokenBudget (both positive-named,
// off-by-default fields whose "missing config.yaml" case degrades to
// disabled), store.Config.DisableFTSRelax is deliberately INVERTED: the
// zero-hit relaxation ladder is a strictly-additive fix (it only ever fires
// when the strict pass found literally zero rows, and never reorders or
// removes an existing hit) that should protect every install out of the
// box, including one with no config.yaml at all — so the Go zero value
// (false) must mean "ladder ACTIVE", and only an explicit
// `recall.fts_relax_on_zero: false` in a successfully-loaded config.yaml
// should ever set DisableFTSRelax=true. This is why "missing config.yaml"
// below asserts the ladder STAYS ON (DisableFTSRelax=false) — the opposite
// polarity from every other config-driven gate's own "missing config
// degrades to off" convention, and exactly the point of choosing an
// inverted field name here.

func withFTSRelaxAppConfig(t *testing.T, ftsRelaxOnZero bool) {
	t.Helper()
	old := loadAppConfigWithRecallAutodetect
	loadAppConfigWithRecallAutodetect = func() (*config.Config, error) {
		return &config.Config{Recall: config.RecallConfig{FTSRelaxOnZero: ftsRelaxOnZero}}, nil
	}
	t.Cleanup(func() { loadAppConfigWithRecallAutodetect = old })
}

func TestCmdContext_ThreadsFTSRelaxFromConfig(t *testing.T) {
	t.Run("recall.fts_relax_on_zero:true (default) keeps DisableFTSRelax false", func(t *testing.T) {
		stubRuntimeHooks(t)
		cfg := testConfig(t)
		captured := withCapturedStoreConfig(t)
		withFTSRelaxAppConfig(t, true)
		withArgs(t, "omnia", "context")

		cmdContext(cfg)

		if captured.DisableFTSRelax {
			t.Fatalf("expected recall.fts_relax_on_zero:true to keep store.Config.DisableFTSRelax false, got true")
		}
	})

	t.Run("recall.fts_relax_on_zero:false flows through to DisableFTSRelax true", func(t *testing.T) {
		stubRuntimeHooks(t)
		cfg := testConfig(t)
		captured := withCapturedStoreConfig(t)
		withFTSRelaxAppConfig(t, false)
		withArgs(t, "omnia", "context")

		cmdContext(cfg)

		if !captured.DisableFTSRelax {
			t.Fatalf("expected recall.fts_relax_on_zero:false to set store.Config.DisableFTSRelax true, got false")
		}
	})

	t.Run("missing config.yaml keeps the ladder ON (DisableFTSRelax stays false)", func(t *testing.T) {
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

		if captured.DisableFTSRelax {
			t.Fatalf("expected missing config.yaml to leave the strictly-additive ladder ON (DisableFTSRelax=false), got true")
		}
	})
}

func TestCmdMCP_ThreadsFTSRelaxFromConfig(t *testing.T) {
	t.Run("recall.fts_relax_on_zero:true (default) keeps DisableFTSRelax false", func(t *testing.T) {
		stubRuntimeHooks(t)
		cfg := testConfig(t)
		captured := withCapturedStoreConfig(t)
		withFTSRelaxAppConfig(t, true)
		withArgs(t, "omnia", "mcp")

		cmdMCP(cfg)

		if captured.DisableFTSRelax {
			t.Fatalf("expected recall.fts_relax_on_zero:true to keep store.Config.DisableFTSRelax false, got true")
		}
	})

	t.Run("recall.fts_relax_on_zero:false flows through to DisableFTSRelax true", func(t *testing.T) {
		stubRuntimeHooks(t)
		cfg := testConfig(t)
		captured := withCapturedStoreConfig(t)
		withFTSRelaxAppConfig(t, false)
		withArgs(t, "omnia", "mcp")

		cmdMCP(cfg)

		if !captured.DisableFTSRelax {
			t.Fatalf("expected recall.fts_relax_on_zero:false to set store.Config.DisableFTSRelax true, got false")
		}
	})
}

func TestCmdServe_ThreadsFTSRelaxFromConfig(t *testing.T) {
	t.Run("recall.fts_relax_on_zero:true (default) keeps DisableFTSRelax false", func(t *testing.T) {
		stubRuntimeHooks(t)
		cfg := testConfig(t)
		captured := withCapturedStoreConfig(t)
		withFTSRelaxAppConfig(t, true)
		withArgs(t, "omnia", "serve")

		cmdServe(cfg)

		if captured.DisableFTSRelax {
			t.Fatalf("expected recall.fts_relax_on_zero:true to keep store.Config.DisableFTSRelax false, got true")
		}
	})

	t.Run("recall.fts_relax_on_zero:false flows through to DisableFTSRelax true", func(t *testing.T) {
		stubRuntimeHooks(t)
		cfg := testConfig(t)
		captured := withCapturedStoreConfig(t)
		withFTSRelaxAppConfig(t, false)
		withArgs(t, "omnia", "serve")

		cmdServe(cfg)

		if !captured.DisableFTSRelax {
			t.Fatalf("expected recall.fts_relax_on_zero:false to set store.Config.DisableFTSRelax true, got false")
		}
	})
}

func TestCmdSave_ThreadsFTSRelaxFromConfig(t *testing.T) {
	t.Run("recall.fts_relax_on_zero:true (default) keeps DisableFTSRelax false", func(t *testing.T) {
		stubRuntimeHooks(t)
		cfg := testConfig(t)
		captured := withCapturedStoreConfig(t)
		withFTSRelaxAppConfig(t, true)
		withArgs(t, "omnia", "save", "title", "content")

		cmdSave(cfg)

		if captured.DisableFTSRelax {
			t.Fatalf("expected recall.fts_relax_on_zero:true to keep store.Config.DisableFTSRelax false, got true")
		}
	})

	t.Run("recall.fts_relax_on_zero:false flows through to DisableFTSRelax true", func(t *testing.T) {
		stubRuntimeHooks(t)
		cfg := testConfig(t)
		captured := withCapturedStoreConfig(t)
		withFTSRelaxAppConfig(t, false)
		withArgs(t, "omnia", "save", "title", "content")

		cmdSave(cfg)

		if !captured.DisableFTSRelax {
			t.Fatalf("expected recall.fts_relax_on_zero:false to set store.Config.DisableFTSRelax true, got false")
		}
	})
}

// ─── End-to-end: production search path honors the kill-switch ─────────────

// TestFTSRelaxWiring_EndToEnd_ProductionSearchPath proves the wiring
// end-to-end through the REAL production code path
// (loadAppConfigWithRecallAutodetect → cmdContext → storeNew), then reopens
// the store with the exact store.Config cmdContext built and issues a real
// Store.Search call against it — `omnia search`'s own cmdSearch dispatch is
// NOT one of the 4 wired sites (write-hygiene's own precedent didn't wire it
// either), so this uses cmdContext (one of the 4) purely as the vehicle to
// construct a production-shaped store.Config, then verifies the search
// BEHAVIOR that config produces.
func TestFTSRelaxWiring_EndToEnd_ProductionSearchPath(t *testing.T) {
	const title = "Limpieza automática de memorias duplicadas"
	const content = "El sistema detecta y limpia las memorias duplicadas automáticamente antes de guardarlas en el almacenamiento."
	// "cómo" never occurs verbatim in the content above, so the strict pass
	// returns 0 rows — only the relaxation ladder can find it.
	const query = "cómo limpia el sistema las memorias duplicadas"

	seed := func(t *testing.T, cfg store.Config) {
		t.Helper()
		s, err := store.New(cfg)
		if err != nil {
			t.Fatalf("seed store: %v", err)
		}
		defer s.Close()
		if err := s.CreateSession("seed", "engram", "/tmp"); err != nil {
			t.Fatalf("create session: %v", err)
		}
		if _, err := s.AddObservation(store.AddObservationParams{
			SessionID: "seed",
			Type:      "discovery",
			Title:     title,
			Content:   content,
			Project:   "engram",
			Scope:     "project",
		}); err != nil {
			t.Fatalf("seed observation: %v", err)
		}
	}

	t.Run("recall.fts_relax_on_zero:true (default): ladder finds the doc", func(t *testing.T) {
		stubRuntimeHooks(t)
		cfg := testConfig(t)
		seed(t, cfg)
		captured := withCapturedStoreConfig(t)
		withFTSRelaxAppConfig(t, true)
		withArgs(t, "omnia", "context")
		cmdContext(cfg)

		s, err := store.New(*captured)
		if err != nil {
			t.Fatalf("reopen store: %v", err)
		}
		defer s.Close()

		results, err := s.Search(query, store.SearchOptions{Project: "engram", Limit: 10})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("expected the relaxation ladder to find 1 result with the kill-switch off, got %d", len(results))
		}
	})

	t.Run("recall.fts_relax_on_zero:false: golden byte-for-byte pre-PR7 zero-hit behavior", func(t *testing.T) {
		stubRuntimeHooks(t)
		cfg := testConfig(t)
		seed(t, cfg)
		captured := withCapturedStoreConfig(t)
		withFTSRelaxAppConfig(t, false)
		withArgs(t, "omnia", "context")
		cmdContext(cfg)

		s, err := store.New(*captured)
		if err != nil {
			t.Fatalf("reopen store: %v", err)
		}
		defer s.Close()

		var diag store.SearchDiag
		results, err := s.Search(query, store.SearchOptions{Project: "engram", Limit: 10, Diag: &diag})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(results) != 0 {
			t.Fatalf("expected the kill-switch to restore pre-PR7 zero-hit behavior (0 results), got %d", len(results))
		}
		if diag != (store.SearchDiag{}) {
			t.Fatalf("expected zero-value Diag when the kill-switch prevents the ladder from ever firing, got %+v", diag)
		}
	})
}
