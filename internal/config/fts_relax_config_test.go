package config_test

import (
	"testing"

	"github.com/velion/omnia/internal/config"
)

// fts_relax_config_test.go — RED->GREEN tests for Omnia v0.3.1 PR7's
// `recall.fts_relax_on_zero` rollback flag (design obs #1668 D7, spec obs
// #1666 fts-recall domain, tasks.md PR7 7.2/7.3). Like write_hygiene.enabled
// (write_hygiene_config_test.go) — and UNLIKE recall.enabled itself, which
// defaults OFF (recall_config_test.go) — this flag defaults to TRUE: the
// zero-hit relaxation ladder (internal/store's Store.Search) is meant to be
// on for every install out of the box, with this key existing purely as an
// explicit rollback lever. It needs the same explicit-vs-absent probe shape
// as writeHygieneEnabledKeyPresent (config.go), inverted from
// recallEnabledKeyPresent's own: absent -> true (fill in the default),
// explicit `fts_relax_on_zero: false` -> sticks false.

// TestRecall_FTSRelaxOnZero_DefaultsTrueWhenAbsent locks the default-ON
// behavior: a config with no `recall.fts_relax_on_zero` key at all (not even
// a `recall:` section) must still resolve FTSRelaxOnZero=true.
func TestRecall_FTSRelaxOnZero_DefaultsTrueWhenAbsent(t *testing.T) {
	path := writeTempConfig(t, "engram:\n  base_url: http://127.0.0.1:7437\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Recall.FTSRelaxOnZero {
		t.Error("Recall.FTSRelaxOnZero: got false, want true by default (key entirely absent)")
	}
}

// TestRecall_FTSRelaxOnZero_ExplicitFalseSticks is the rollback case: an
// operator who explicitly writes `recall: { fts_relax_on_zero: false }` MUST
// see that stick — applyDefaults must never override an explicit false back
// to true.
func TestRecall_FTSRelaxOnZero_ExplicitFalseSticks(t *testing.T) {
	path := writeTempConfig(t, ""+
		"recall:\n"+
		"  fts_relax_on_zero: false\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Recall.FTSRelaxOnZero {
		t.Error("Recall.FTSRelaxOnZero with explicit fts_relax_on_zero: false: got true, want false (rollback flag must stick)")
	}
}

// TestRecall_FTSRelaxOnZero_ExplicitTrueStaysTrue is the redundant-but-
// explicit case: an operator who explicitly writes `fts_relax_on_zero: true`
// (same as the default) must still load true.
func TestRecall_FTSRelaxOnZero_ExplicitTrueStaysTrue(t *testing.T) {
	path := writeTempConfig(t, ""+
		"recall:\n"+
		"  fts_relax_on_zero: true\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Recall.FTSRelaxOnZero {
		t.Error("Recall.FTSRelaxOnZero with explicit fts_relax_on_zero: true: got false, want true")
	}
}

// TestRecall_FTSRelaxOnZero_IndependentOfRecallEnabled proves the two
// `recall.*` flags are independent: an install with `recall.enabled: false`
// (the hybrid-fusion gate, off by default and here left explicitly off)
// still gets FTSRelaxOnZero's own separate true default — the relaxation
// ladder lives in Store.Search itself (the FTS-only lexical path every
// caller already uses), not inside the optional hybrid recall.Service.
func TestRecall_FTSRelaxOnZero_IndependentOfRecallEnabled(t *testing.T) {
	path := writeTempConfig(t, ""+
		"recall:\n"+
		"  enabled: false\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Recall.Enabled {
		t.Error("Recall.Enabled: got true, want false (explicitly set)")
	}
	if !cfg.Recall.FTSRelaxOnZero {
		t.Error("Recall.FTSRelaxOnZero: got false, want true (independent default, unaffected by recall.enabled)")
	}
}
