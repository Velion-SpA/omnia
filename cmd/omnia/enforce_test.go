package main

import (
	"strings"
	"testing"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/store"
)

func upsertCLIEnforceTestProcedure(t *testing.T, s *store.Store, project, trigger string) {
	t.Helper()
	if _, err := s.UpsertProcedure(store.Procedure{
		Project:  project,
		Polarity: store.ProcedurePolarityAntiPlaybook,
		Trigger:  trigger,
		Steps: []store.ProcedureStep{
			{Order: 1, Template: "run tests before declaring the change done"},
		},
		ExpectedOutcome:   "tests pass",
		PostconditionKind: store.PostconditionTestsPass,
		Confidence:        0.8,
		State:             store.ProcedureStateTrusted,
		SourceObsSyncIDs:  []string{"obs-fixture"},
	}); err != nil {
		t.Fatalf("UpsertProcedure: %v", err)
	}
}

// TestCmdEnforceDisabledDoesNotOpenStore (task 8.7/8.8, REQ-410) mirrors
// TestCmdBlameDisabledDoesNotOpenAnchorStore: omnia enforce must degrade to
// a structured "capability disabled" response and never open a store when
// enforcement.enabled is false.
func TestCmdEnforceDisabledDoesNotOpenStore(t *testing.T) {
	cfg := testConfig(t)
	old := loadEnforcementConfig
	loadEnforcementConfig = func() (config.EnforcementConfig, bool) { return config.EnforcementConfig{}, false }
	t.Cleanup(func() { loadEnforcementConfig = old })

	withArgs(t, "omnia", "enforce", "--files", "internal/enforce/matcher.go")
	stdout, stderr := captureOutput(t, func() { cmdEnforce(cfg) })
	if stderr != "" || !strings.Contains(stdout, "capability disabled") {
		t.Fatalf("expected structured disabled response, stdout=%q stderr=%q", stdout, stderr)
	}
}

// TestLoadEnforcementConfigDegradesToDisabledWhenConfigFileIsMissing mirrors
// TestLoadCodeGraphConfigDegradesToDisabledWhenConfigFileIsMissing: a fresh
// install with no config.yaml must degrade to disabled, never a fatal exit
// — the documented anti-pattern this codebase has already fixed 3 times
// elsewhere (blame, consolidate, rank-train).
func TestLoadEnforcementConfigDegradesToDisabledWhenConfigFileIsMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, enabled := loadEnforcementConfig(); enabled {
		t.Fatal("expected disabled when no config.yaml exists (fresh install), got enabled")
	}
}

// TestCmdEnforceEnabledFlagsFailingTrustedProcedure exercises the enabled
// end-to-end CLI path: a matching trusted procedure whose configured test
// command fails yields a `flag` verdict under the default mode.
func TestCmdEnforceEnabledFlagsFailingTrustedProcedure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	upsertCLIEnforceTestProcedure(t, s, "cli-enforce-test", "changes touching internal/enforce/matcher.go must run go test before completion")
	if err := s.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	old := loadEnforcementConfig
	loadEnforcementConfig = func() (config.EnforcementConfig, bool) {
		return config.EnforcementConfig{
			Enabled:  true,
			Mode:     "flag",
			Commands: config.EnforcementCommandsConfig{Tests: "exit 1"},
		}, true
	}
	t.Cleanup(func() { loadEnforcementConfig = old })

	withArgs(t, "omnia", "enforce", "--project", "cli-enforce-test", "--files", "internal/enforce/matcher.go")
	stdout, stderr := captureOutput(t, func() { cmdEnforce(cfg) })
	if stderr != "" || !strings.Contains(stdout, `"verdict": "flag"`) {
		t.Fatalf("expected a flag verdict in JSON output, stdout=%q stderr=%q", stdout, stderr)
	}
}

// TestCmdEnforceBlockModeExitsNonZero verifies `--block` forces block mode
// and the CLI exits non-zero, so a pre-commit hook/CI step can act on it
// (design.md: "CLI omnia enforce ... for hooks/CI use").
func TestCmdEnforceBlockModeExitsNonZero(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	upsertCLIEnforceTestProcedure(t, s, "cli-enforce-test", "changes touching internal/enforce/matcher.go must run go test before completion")
	if err := s.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	old := loadEnforcementConfig
	loadEnforcementConfig = func() (config.EnforcementConfig, bool) {
		return config.EnforcementConfig{
			Enabled:  true,
			Mode:     "flag",
			Commands: config.EnforcementCommandsConfig{Tests: "exit 1"},
		}, true
	}
	t.Cleanup(func() { loadEnforcementConfig = old })

	oldExit := exitFunc
	var exitCode int
	exited := false
	exitFunc = func(code int) { exitCode = code; exited = true }
	t.Cleanup(func() { exitFunc = oldExit })

	withArgs(t, "omnia", "enforce", "--project", "cli-enforce-test", "--files", "internal/enforce/matcher.go", "--block")
	stdout, _ := captureOutput(t, func() { cmdEnforce(cfg) })
	if !strings.Contains(stdout, `"verdict": "block"`) {
		t.Fatalf("expected a block verdict in JSON output, stdout=%q", stdout)
	}
	if !exited || exitCode != 1 {
		t.Fatalf("expected exitFunc(1) for a block verdict, exited=%v exitCode=%d", exited, exitCode)
	}
}
