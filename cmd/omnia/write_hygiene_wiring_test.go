package main

import (
	"errors"
	"testing"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/store"
)

// write_hygiene_wiring_test.go — RED→GREEN tests for Omnia v0.3.1 "Write
// Hygiene" PR4 (design obs #1668 D2/D5, spec obs #1666 write-gate domain):
// confirm cmdServe/cmdMCP/cmdContext/cmdSave each thread write_hygiene.*
// from config.yaml into store.Config BEFORE storeNew constructs the
// *store.Store (s.cfg is immutable after New, so this MUST happen
// pre-construction — same requirement v0.3 PR3 established for
// ContextTokenBudget, see context_budget_wiring_test.go, whose
// withCapturedStoreConfig helper this file reuses). cmdSave is included
// because it is a named production save entry point (design's "CLI save",
// D2: "EVERY save entry point... gets identical dedup — zero drift (#140
// lesson)") even though it never had ContextTokenBudget wiring (that
// feature only matters for FormatContext, which cmdSave never calls).
// cmdImport/cmdSync are intentionally NOT wired here: cmdImport routes
// through Store.Import (a raw restore path that bypasses
// AddObservation/SaveObservation entirely, by existing v0.3 design) and
// cmdSync never calls AddObservation either — wiring store.Config fields
// there would be dead code with no behavioral effect.

func withWriteHygieneAppConfig(t *testing.T, wh config.WriteHygieneConfig) {
	t.Helper()
	old := loadAppConfigWithRecallAutodetect
	loadAppConfigWithRecallAutodetect = func() (*config.Config, error) {
		return &config.Config{WriteHygiene: wh}, nil
	}
	t.Cleanup(func() { loadAppConfigWithRecallAutodetect = old })
}

func assertWriteHygieneWired(t *testing.T, captured *store.Config, wh config.WriteHygieneConfig) {
	t.Helper()
	if captured.WriteHygieneEnabled != wh.Enabled {
		t.Errorf("WriteHygieneEnabled = %v, want %v", captured.WriteHygieneEnabled, wh.Enabled)
	}
	if captured.NoopThreshold != wh.NoopThreshold {
		t.Errorf("NoopThreshold = %v, want %v", captured.NoopThreshold, wh.NoopThreshold)
	}
	if captured.UpdateThreshold != wh.UpdateThreshold {
		t.Errorf("UpdateThreshold = %v, want %v", captured.UpdateThreshold, wh.UpdateThreshold)
	}
	if captured.ShrinkGuard != wh.ShrinkGuard {
		t.Errorf("ShrinkGuard = %v, want %v", captured.ShrinkGuard, wh.ShrinkGuard)
	}
	if captured.CandidateLimit != wh.CandidateLimit {
		t.Errorf("CandidateLimit = %v, want %v", captured.CandidateLimit, wh.CandidateLimit)
	}
}

var writeHygieneEnabledFixture = config.WriteHygieneConfig{
	Enabled:         true,
	NoopThreshold:   0.98,
	UpdateThreshold: 0.9,
	ShrinkGuard:     0.9,
	CandidateLimit:  10,
}

var writeHygieneKillSwitchFixture = config.WriteHygieneConfig{
	Enabled:         false,
	NoopThreshold:   0.98,
	UpdateThreshold: 0.9,
	ShrinkGuard:     0.9,
	CandidateLimit:  10,
}

func TestCmdContext_ThreadsWriteHygieneFromConfig(t *testing.T) {
	t.Run("enabled propagates all thresholds", func(t *testing.T) {
		stubRuntimeHooks(t)
		cfg := testConfig(t)
		captured := withCapturedStoreConfig(t)
		withWriteHygieneAppConfig(t, writeHygieneEnabledFixture)
		withArgs(t, "omnia", "context")

		cmdContext(cfg)

		assertWriteHygieneWired(t, captured, writeHygieneEnabledFixture)
	})

	t.Run("kill-switch enabled:false flows through to gate off", func(t *testing.T) {
		stubRuntimeHooks(t)
		cfg := testConfig(t)
		captured := withCapturedStoreConfig(t)
		withWriteHygieneAppConfig(t, writeHygieneKillSwitchFixture)
		withArgs(t, "omnia", "context")

		cmdContext(cfg)

		if captured.WriteHygieneEnabled {
			t.Fatalf("expected write_hygiene.enabled=false to keep store.Config.WriteHygieneEnabled false, got true")
		}
	})

	t.Run("missing config.yaml degrades to gate off", func(t *testing.T) {
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

		if captured.WriteHygieneEnabled {
			t.Fatalf("expected missing config.yaml to degrade WriteHygieneEnabled to false, got true")
		}
	})
}

func TestCmdMCP_ThreadsWriteHygieneFromConfig(t *testing.T) {
	t.Run("enabled propagates all thresholds", func(t *testing.T) {
		stubRuntimeHooks(t)
		cfg := testConfig(t)
		captured := withCapturedStoreConfig(t)
		withWriteHygieneAppConfig(t, writeHygieneEnabledFixture)
		withArgs(t, "omnia", "mcp")

		cmdMCP(cfg)

		assertWriteHygieneWired(t, captured, writeHygieneEnabledFixture)
	})

	t.Run("kill-switch enabled:false flows through to gate off", func(t *testing.T) {
		stubRuntimeHooks(t)
		cfg := testConfig(t)
		captured := withCapturedStoreConfig(t)
		withWriteHygieneAppConfig(t, writeHygieneKillSwitchFixture)
		withArgs(t, "omnia", "mcp")

		cmdMCP(cfg)

		if captured.WriteHygieneEnabled {
			t.Fatalf("expected write_hygiene.enabled=false to keep store.Config.WriteHygieneEnabled false, got true")
		}
	})
}

func TestCmdServe_ThreadsWriteHygieneFromConfig(t *testing.T) {
	t.Run("enabled propagates all thresholds", func(t *testing.T) {
		stubRuntimeHooks(t)
		cfg := testConfig(t)
		captured := withCapturedStoreConfig(t)
		withWriteHygieneAppConfig(t, writeHygieneEnabledFixture)
		withArgs(t, "omnia", "serve")

		cmdServe(cfg)

		assertWriteHygieneWired(t, captured, writeHygieneEnabledFixture)
	})

	t.Run("kill-switch enabled:false flows through to gate off", func(t *testing.T) {
		stubRuntimeHooks(t)
		cfg := testConfig(t)
		captured := withCapturedStoreConfig(t)
		withWriteHygieneAppConfig(t, writeHygieneKillSwitchFixture)
		withArgs(t, "omnia", "serve")

		cmdServe(cfg)

		if captured.WriteHygieneEnabled {
			t.Fatalf("expected write_hygiene.enabled=false to keep store.Config.WriteHygieneEnabled false, got true")
		}
	})
}

func TestCmdSave_ThreadsWriteHygieneFromConfig(t *testing.T) {
	t.Run("enabled propagates all thresholds", func(t *testing.T) {
		stubRuntimeHooks(t)
		cfg := testConfig(t)
		captured := withCapturedStoreConfig(t)
		withWriteHygieneAppConfig(t, writeHygieneEnabledFixture)
		withArgs(t, "omnia", "save", "title", "content")

		cmdSave(cfg)

		assertWriteHygieneWired(t, captured, writeHygieneEnabledFixture)
	})

	t.Run("kill-switch enabled:false flows through to gate off", func(t *testing.T) {
		stubRuntimeHooks(t)
		cfg := testConfig(t)
		captured := withCapturedStoreConfig(t)
		withWriteHygieneAppConfig(t, writeHygieneKillSwitchFixture)
		withArgs(t, "omnia", "save", "title", "content")

		cmdSave(cfg)

		if captured.WriteHygieneEnabled {
			t.Fatalf("expected write_hygiene.enabled=false to keep store.Config.WriteHygieneEnabled false, got true")
		}
	})
}

// ─── Task 4.3 — end-to-end integration check ────────────────────────────────

// TestWriteHygieneWiring_EndToEnd_ProductionSavePath drives the REAL `omnia
// save` CLI dispatch (cmdSave) twice against a real on-disk store, through
// the exact same config-wiring code path production uses (no store.Config
// literal built by hand) — proving the wiring itself, not re-deriving the
// write-gate's classification logic (already exhaustively covered by
// internal/store/write_gate_test.go's NOOP/AUTO-UPDATE/RELATE/SAVE/
// boundary/shrink-guard/determinism cases).
//
// Fixture: reuses internal/store's TestSaveObservation_LadderNoop pair
// verbatim (case/punctuation-only content variant, Jaccard 1.0 — comfortably
// >= the 0.98 default noop_threshold) with two DIFFERENT titles, so the
// pre-existing exact-title dedupe-hash-window (step 2, unchanged by this
// PR) never fires either way — isolating the write-gate steps (3-6) that
// WriteHygieneEnabled actually gates.
func TestWriteHygieneWiring_EndToEnd_ProductionSavePath(t *testing.T) {
	const title1 = "Null pointer crash writeup"
	const title2 = "Null pointer crash notes"
	const content1 = "Fixed the null pointer crash in the login handler by adding a nil check before dereferencing the session token."
	const content2 = "fixed the null pointer crash in the login handler by adding a nil check before dereferencing the session token"

	countObservations := func(t *testing.T, cfg store.Config) int {
		t.Helper()
		verify, err := store.New(cfg)
		if err != nil {
			t.Fatalf("reopen store for verification: %v", err)
		}
		defer verify.Close()
		stats, err := verify.Stats()
		if err != nil {
			t.Fatalf("Stats: %v", err)
		}
		return stats.TotalObservations
	}

	t.Run("gate on (default config): second near-duplicate save NOOPs, 1 row total", func(t *testing.T) {
		stubRuntimeHooks(t)
		cfg := testConfig(t)
		withWriteHygieneAppConfig(t, writeHygieneEnabledFixture)

		withArgs(t, "omnia", "save", title1, content1)
		cmdSave(cfg)
		withArgs(t, "omnia", "save", title2, content2)
		cmdSave(cfg)

		if got := countObservations(t, cfg); got != 1 {
			t.Fatalf("expected write-gate NOOP to keep exactly 1 observation through the production `omnia save` path, got %d", got)
		}
	})

	t.Run("kill-switch off (write_hygiene.enabled:false): duplicates exactly like pre-v0.3.1, 2 rows total", func(t *testing.T) {
		stubRuntimeHooks(t)
		cfg := testConfig(t)
		withWriteHygieneAppConfig(t, writeHygieneKillSwitchFixture)

		withArgs(t, "omnia", "save", title1, content1)
		cmdSave(cfg)
		withArgs(t, "omnia", "save", title2, content2)
		cmdSave(cfg)

		if got := countObservations(t, cfg); got != 2 {
			t.Fatalf("expected kill-switch (enabled:false) to duplicate exactly like pre-v0.3.1 (2 rows) through the production `omnia save` path, got %d", got)
		}
	})
}
