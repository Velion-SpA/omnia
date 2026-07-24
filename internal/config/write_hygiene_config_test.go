package config_test

import (
	"testing"

	"github.com/velion/omnia/internal/config"
)

// write_hygiene_config_test.go — RED->GREEN tests for Omnia v0.3.1 "Write
// Hygiene" PR2 (design obs #1668 D5, spec obs #1666 write-gate domain REQ
// "Default-On With Kill-Switch"). Unlike every Context Economy gate in
// injection_config_test.go/diversity_config_test.go/recall_config_test.go
// (all default-OFF), write_hygiene.enabled defaults to TRUE — the gate
// itself lands in PR3; this PR is config-only, so these tests only pin the
// config surface, not gate behavior.
//
// The default-true convention needs the SAME explicit-vs-absent probe shape
// as recallEnabledKeyPresent/injectionBudgetMaxTokensKeyPresent (config.go),
// just INVERTED: absent -> true (fill in the default), explicit
// `enabled: false` -> sticks false (the kill-switch). A plain bool zero-check
// (`if !cfg.WriteHygiene.Enabled { cfg.WriteHygiene.Enabled = true }`) would
// make an explicit `enabled: false` indistinguishable from "never set" and
// silently force the gate back on — defeating the kill-switch entirely.

// TestWriteHygiene_DefaultsEnabledTrueWhenAbsent locks the default-ON
// behavior: a config with no `write_hygiene` section at all must still
// resolve Enabled=true, with every threshold/limit filled to its documented
// default.
func TestWriteHygiene_DefaultsEnabledTrueWhenAbsent(t *testing.T) {
	path := writeTempConfig(t, "engram:\n  base_url: http://127.0.0.1:7437\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.WriteHygiene.Enabled {
		t.Error("WriteHygiene.Enabled: got false, want true by default (no write_hygiene section at all)")
	}
	if cfg.WriteHygiene.NoopThreshold != 0.98 {
		t.Errorf("WriteHygiene.NoopThreshold default: got %v, want 0.98", cfg.WriteHygiene.NoopThreshold)
	}
	if cfg.WriteHygiene.UpdateThreshold != 0.9 {
		t.Errorf("WriteHygiene.UpdateThreshold default: got %v, want 0.9", cfg.WriteHygiene.UpdateThreshold)
	}
	if cfg.WriteHygiene.ShrinkGuard != 0.9 {
		t.Errorf("WriteHygiene.ShrinkGuard default: got %v, want 0.9", cfg.WriteHygiene.ShrinkGuard)
	}
	if cfg.WriteHygiene.CandidateLimit != 10 {
		t.Errorf("WriteHygiene.CandidateLimit default: got %v, want 10", cfg.WriteHygiene.CandidateLimit)
	}
	if cfg.WriteHygiene.MinContentLength != 10 {
		t.Errorf("WriteHygiene.MinContentLength default: got %v, want 10", cfg.WriteHygiene.MinContentLength)
	}
}

// TestWriteHygiene_ExplicitFalseSticks is the kill-switch case: an operator
// who explicitly writes `write_hygiene: { enabled: false }` MUST see that
// stick — applyDefaults must never override an explicit false back to true.
func TestWriteHygiene_ExplicitFalseSticks(t *testing.T) {
	path := writeTempConfig(t, ""+
		"write_hygiene:\n"+
		"  enabled: false\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.WriteHygiene.Enabled {
		t.Error("WriteHygiene.Enabled with explicit enabled: false: got true, want false (kill-switch must stick)")
	}
	// Thresholds still get their sane defaults even with the gate off, so a
	// later `enabled: true` flip (without restating thresholds) is safe.
	if cfg.WriteHygiene.NoopThreshold != 0.98 {
		t.Errorf("WriteHygiene.NoopThreshold with gate off: got %v, want 0.98 (defaults still filled)", cfg.WriteHygiene.NoopThreshold)
	}
}

// TestWriteHygiene_ExplicitTrueStaysTrue is the redundant-but-explicit case:
// an operator who explicitly writes `enabled: true` (same as the default)
// must still load true — this is mostly a documentation/roundtrip case since
// true is both the explicit and default value.
func TestWriteHygiene_ExplicitTrueStaysTrue(t *testing.T) {
	path := writeTempConfig(t, ""+
		"write_hygiene:\n"+
		"  enabled: true\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.WriteHygiene.Enabled {
		t.Error("WriteHygiene.Enabled with explicit enabled: true: got false, want true")
	}
}

// TestWriteHygiene_ParsesThresholdOverrides is the full write_hygiene.* yaml
// roundtrip: every threshold/limit field an operator can set must load back
// exactly, overriding its default.
func TestWriteHygiene_ParsesThresholdOverrides(t *testing.T) {
	path := writeTempConfig(t, ""+
		"write_hygiene:\n"+
		"  enabled: true\n"+
		"  noop_threshold: 0.95\n"+
		"  update_threshold: 0.85\n"+
		"  shrink_guard: 0.8\n"+
		"  candidate_limit: 5\n"+
		"  min_content_length: 25\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.WriteHygiene.NoopThreshold != 0.95 {
		t.Errorf("WriteHygiene.NoopThreshold: got %v, want 0.95", cfg.WriteHygiene.NoopThreshold)
	}
	if cfg.WriteHygiene.UpdateThreshold != 0.85 {
		t.Errorf("WriteHygiene.UpdateThreshold: got %v, want 0.85", cfg.WriteHygiene.UpdateThreshold)
	}
	if cfg.WriteHygiene.ShrinkGuard != 0.8 {
		t.Errorf("WriteHygiene.ShrinkGuard: got %v, want 0.8", cfg.WriteHygiene.ShrinkGuard)
	}
	if cfg.WriteHygiene.CandidateLimit != 5 {
		t.Errorf("WriteHygiene.CandidateLimit: got %v, want 5", cfg.WriteHygiene.CandidateLimit)
	}
	if cfg.WriteHygiene.MinContentLength != 25 {
		t.Errorf("WriteHygiene.MinContentLength: got %v, want 25", cfg.WriteHygiene.MinContentLength)
	}
}
