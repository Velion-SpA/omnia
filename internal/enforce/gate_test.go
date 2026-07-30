package enforce

import (
	"context"
	"testing"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/store"
)

func testProcedure(kind, expr string) store.Procedure {
	return store.Procedure{
		SyncID:            "proc-fixture",
		Polarity:          store.ProcedurePolarityAntiPlaybook,
		Trigger:           "fixture trigger",
		PostconditionKind: kind,
		PostconditionExpr: expr,
		State:             store.ProcedureStateTrusted,
	}
}

// TestDecide_AllPostconditionsPassYieldsPass (task 7.5, REQ-413) verifies
// that when every matched trusted procedure's postcondition is satisfied,
// the verdict is exactly `pass`.
func TestDecide_AllPostconditionsPassYieldsPass(t *testing.T) {
	procs := []store.Procedure{testProcedure(store.PostconditionTestsPass, "")}
	opts := DecideOptions{
		Config: config.EnforcementConfig{
			Mode:     "flag",
			Commands: config.EnforcementCommandsConfig{Tests: "exit 0"},
		},
	}
	result := Decide(context.Background(), procs, opts)
	if result.Verdict != VerdictPass {
		t.Fatalf("Verdict = %q, want %q; result=%+v", result.Verdict, VerdictPass, result)
	}
	if len(result.Violations) != 0 {
		t.Fatalf("expected no violations on an all-pass decision, got %+v", result.Violations)
	}
}

// TestDecide_FailureUnderDefaultModeFlagsNotBlocks (task 7.5, REQ-413/414,
// edge case scenario from spec.md) verifies a failing postcondition with
// Mode left at its default ("flag") yields `flag`, never `block` — the
// caller's workflow is never halted by a first-slice install.
func TestDecide_FailureUnderDefaultModeFlagsNotBlocks(t *testing.T) {
	procs := []store.Procedure{testProcedure(store.PostconditionTestsPass, "")}
	opts := DecideOptions{
		Config: config.EnforcementConfig{
			Mode:     "flag",
			Commands: config.EnforcementCommandsConfig{Tests: "exit 1"},
		},
	}
	result := Decide(context.Background(), procs, opts)
	if result.Verdict != VerdictFlag {
		t.Fatalf("Verdict = %q, want %q; result=%+v", result.Verdict, VerdictFlag, result)
	}
	if len(result.Violations) != 1 || result.Violations[0].ExitCode != 1 {
		t.Fatalf("expected exactly 1 violation with exit code 1, got %+v", result.Violations)
	}
	if !result.Overridable {
		t.Fatalf("a flagged violation must be reported as overridable")
	}
}

// TestDecide_BlockModeHaltsOnViolation (spec.md scenario: "Block mode halts
// on violation") verifies that with enforcement.mode explicitly set to
// "block", a failing postcondition yields `block`.
func TestDecide_BlockModeHaltsOnViolation(t *testing.T) {
	procs := []store.Procedure{testProcedure(store.PostconditionTestsPass, "")}
	opts := DecideOptions{
		Config: config.EnforcementConfig{
			Mode:     "block",
			Commands: config.EnforcementCommandsConfig{Tests: "exit 1"},
		},
	}
	result := Decide(context.Background(), procs, opts)
	if result.Verdict != VerdictBlock {
		t.Fatalf("Verdict = %q, want %q; result=%+v", result.Verdict, VerdictBlock, result)
	}
}

// TestDecide_UnconfiguredCommandSkipsNeverBlocks (fail-safe by design,
// design.md "Failure/degradation": "Command for a needed kind not
// configured → SKIP that procedure with a note, never block") verifies a
// trusted procedure whose postcondition kind has no configured command is
// skipped, and the overall verdict still passes even under block mode.
func TestDecide_UnconfiguredCommandSkipsNeverBlocks(t *testing.T) {
	procs := []store.Procedure{testProcedure(store.PostconditionLintClean, "")}
	opts := DecideOptions{
		Config: config.EnforcementConfig{
			Mode: "block", // even under block mode, a missing command config never blocks
		},
	}
	result := Decide(context.Background(), procs, opts)
	if result.Verdict != VerdictPass {
		t.Fatalf("Verdict = %q, want %q (unconfigured command must never block); result=%+v", result.Verdict, VerdictPass, result)
	}
	if len(result.Violations) != 1 || result.Violations[0].Outcome != outcomeSkipped {
		t.Fatalf("expected 1 skipped-with-note violation entry, got %+v", result.Violations)
	}
}

// TestDecide_CustomPostconditionGatedBehindAllowCustomCommands (ADR-6:
// "custom is gated OFF behind enforcement.allow_custom_commands — procedure-
// supplied strings are less trusted") verifies a custom postcondition never
// runs unless the operator explicitly opts in.
func TestDecide_CustomPostconditionGatedBehindAllowCustomCommands(t *testing.T) {
	procs := []store.Procedure{testProcedure(store.PostconditionCustom, "exit 1")}

	disabled := Decide(context.Background(), procs, DecideOptions{
		Config: config.EnforcementConfig{Mode: "flag", AllowCustomCommands: false},
	})
	if disabled.Verdict != VerdictPass {
		t.Fatalf("custom postcondition must be skipped (never run) when AllowCustomCommands=false; got %+v", disabled)
	}

	enabled := Decide(context.Background(), procs, DecideOptions{
		Config: config.EnforcementConfig{Mode: "flag", AllowCustomCommands: true},
	})
	if enabled.Verdict != VerdictFlag {
		t.Fatalf("custom postcondition must actually run once AllowCustomCommands=true; got %+v", enabled)
	}
}

// TestDecide_AllFourPostconditionKindsRunConfiguredCommands (task 7.8
// coverage: all four postcondition_kind values) verifies tests_pass,
// lint_clean, build_green, and custom each resolve and run their own
// configured command independently.
func TestDecide_AllFourPostconditionKindsRunConfiguredCommands(t *testing.T) {
	cfg := config.EnforcementConfig{
		Mode: "flag",
		Commands: config.EnforcementCommandsConfig{
			Tests: "exit 0",
			Lint:  "exit 1",
			Build: "exit 0",
		},
		AllowCustomCommands: true,
	}
	procs := []store.Procedure{
		{SyncID: "p-tests", PostconditionKind: store.PostconditionTestsPass, State: store.ProcedureStateTrusted},
		{SyncID: "p-lint", PostconditionKind: store.PostconditionLintClean, State: store.ProcedureStateTrusted},
		{SyncID: "p-build", PostconditionKind: store.PostconditionBuildGreen, State: store.ProcedureStateTrusted},
		{SyncID: "p-custom", PostconditionKind: store.PostconditionCustom, PostconditionExpr: "exit 1", State: store.ProcedureStateTrusted},
	}
	result := Decide(context.Background(), procs, DecideOptions{Config: cfg})
	if result.Verdict != VerdictFlag {
		t.Fatalf("expected flag (lint+custom fail), got %+v", result)
	}
	failed := map[string]bool{}
	for _, v := range result.Violations {
		failed[v.ProcedureSyncID] = true
	}
	if !failed["p-lint"] || !failed["p-custom"] {
		t.Fatalf("expected p-lint and p-custom to be reported as violations, got %+v", result.Violations)
	}
	if failed["p-tests"] || failed["p-build"] {
		t.Fatalf("did not expect p-tests/p-build to be reported as violations, got %+v", result.Violations)
	}
}
