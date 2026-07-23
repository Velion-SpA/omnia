package main

import (
	"context"
	"testing"

	"github.com/velion/omnia/internal/eval"
)

// fakeRunSummary builds a minimal eval.RunSummary whose overall accuracy is
// exactly accuracy, backed by eval.MinRuns runs — enough to satisfy
// eval.EvaluateGate's own reproducibility floor without touching a real
// store, corpus file, or LLM CLI.
func fakeRunSummary(t *testing.T, accuracy float64) eval.RunSummary {
	t.Helper()
	hit := accuracy >= 1.0
	cfg := eval.Config{Run: func(ctx context.Context) (eval.Report, error) {
		results := []eval.CaseResult{
			{Case: eval.EvalCase{Capability: eval.CapabilityRecall, Language: eval.LanguageEN}, Hit: hit, TotalTokens: 100},
		}
		return eval.BuildReport(results), nil
	}}
	summary, err := eval.RunHarness(context.Background(), cfg, eval.MinRuns)
	if err != nil {
		t.Fatalf("eval.RunHarness: %v", err)
	}
	return summary
}

// TestCmdEval_AdvisoryNeverBlocks is spec EVAL-8's "Advisory mode never
// blocks release" scenario, exercised through the CLI entry point: even a
// large regression must never call exitFunc(1) in advisory mode (the
// default).
func TestCmdEval_AdvisoryNeverBlocks(t *testing.T) {
	oldRun, oldGate, oldExit := runEvalHarness, evaluateGate, exitFunc
	t.Cleanup(func() { runEvalHarness, evaluateGate, exitFunc = oldRun, oldGate, oldExit })

	runEvalHarness = func(ctx context.Context, opts evalRunOptions) (eval.RunSummary, error) {
		return fakeRunSummary(t, 0.0), nil // regressed overall accuracy
	}
	evaluateGate = eval.EvaluateGate

	var exitCode int
	var exited bool
	exitFunc = func(code int) { exitCode = code; exited = true }

	cmdEval([]string{"--mode", "advisory", "--baseline", "0.9", "--threshold", "0.05"})

	if exited {
		t.Errorf("advisory mode must never call exitFunc, got exitFunc(%d)", exitCode)
	}
}

// TestCmdEval_BlockingExitsNonZeroPastThreshold is spec EVAL-8's "Blocking
// mode fails the release step" scenario, exercised through the CLI: a
// regression past --threshold in --mode blocking must exitFunc(1).
func TestCmdEval_BlockingExitsNonZeroPastThreshold(t *testing.T) {
	oldRun, oldGate, oldExit := runEvalHarness, evaluateGate, exitFunc
	t.Cleanup(func() { runEvalHarness, evaluateGate, exitFunc = oldRun, oldGate, oldExit })

	runEvalHarness = func(ctx context.Context, opts evalRunOptions) (eval.RunSummary, error) {
		return fakeRunSummary(t, 0.0), nil // regressed overall accuracy
	}
	evaluateGate = eval.EvaluateGate

	var exitCode int
	var exited bool
	exitFunc = func(code int) { exitCode = code; exited = true }

	cmdEval([]string{"--mode", "blocking", "--baseline", "0.9", "--threshold", "0.05"})

	if !exited || exitCode != 1 {
		t.Errorf("blocking mode past threshold must exitFunc(1), got exited=%v code=%d", exited, exitCode)
	}
}

// TestCmdEval_BlockingWithinThresholdDoesNotExit ensures blocking mode only
// exits non-zero PAST the threshold, not on any harness run at all.
func TestCmdEval_BlockingWithinThresholdDoesNotExit(t *testing.T) {
	oldRun, oldGate, oldExit := runEvalHarness, evaluateGate, exitFunc
	t.Cleanup(func() { runEvalHarness, evaluateGate, exitFunc = oldRun, oldGate, oldExit })

	runEvalHarness = func(ctx context.Context, opts evalRunOptions) (eval.RunSummary, error) {
		return fakeRunSummary(t, 1.0), nil // no regression
	}
	evaluateGate = eval.EvaluateGate

	var exited bool
	exitFunc = func(code int) { exited = true }

	cmdEval([]string{"--mode", "blocking", "--baseline", "0.9", "--threshold", "0.05"})

	if exited {
		t.Error("blocking mode with no regression must not exit non-zero")
	}
}

// TestCmdEval_NoBaselineSkipsGate ensures the gate decision is entirely
// skipped (and never blocks) when --baseline is left at its default (0).
func TestCmdEval_NoBaselineSkipsGate(t *testing.T) {
	oldRun, oldGate, oldExit := runEvalHarness, evaluateGate, exitFunc
	t.Cleanup(func() { runEvalHarness, evaluateGate, exitFunc = oldRun, oldGate, oldExit })

	gateCalled := false
	runEvalHarness = func(ctx context.Context, opts evalRunOptions) (eval.RunSummary, error) {
		return fakeRunSummary(t, 0.0), nil
	}
	evaluateGate = func(summary eval.RunSummary, mode eval.GateMode, baselineAccuracy, threshold float64) (eval.GateResult, error) {
		gateCalled = true
		return eval.GateResult{}, nil
	}

	var exited bool
	exitFunc = func(code int) { exited = true }

	cmdEval([]string{"--mode", "blocking"}) // no --baseline

	if gateCalled {
		t.Error("expected evaluateGate to be skipped when --baseline is not supplied")
	}
	if exited {
		t.Error("expected no exit when the gate decision is skipped")
	}
}

// TestCmdEval_InvalidModeExitsNonZero guards the --mode flag's contract.
func TestCmdEval_InvalidModeExitsNonZero(t *testing.T) {
	oldExit := exitFunc
	t.Cleanup(func() { exitFunc = oldExit })

	var exitCode int
	var exited bool
	exitFunc = func(code int) { exitCode = code; exited = true }

	cmdEval([]string{"--mode", "yolo"})

	if !exited || exitCode != 1 {
		t.Errorf("expected exitFunc(1) for an invalid --mode, got exited=%v code=%d", exited, exitCode)
	}
}

// TestCmdEval_HarnessErrorExitsNonZero ensures a harness-wiring failure
// (e.g. corpus below spec EVAL-2's floor) surfaces as a non-zero exit rather
// than silently printing an empty report.
func TestCmdEval_HarnessErrorExitsNonZero(t *testing.T) {
	oldRun, oldExit := runEvalHarness, exitFunc
	t.Cleanup(func() { runEvalHarness, exitFunc = oldRun, oldExit })

	runEvalHarness = func(ctx context.Context, opts evalRunOptions) (eval.RunSummary, error) {
		return eval.RunSummary{}, errBoomEval
	}

	var exited bool
	var exitCode int
	exitFunc = func(code int) { exited = true; exitCode = code }

	cmdEval(nil)

	if !exited || exitCode != 1 {
		t.Errorf("expected exitFunc(1) on a harness error, got exited=%v code=%d", exited, exitCode)
	}
}

var errBoomEval = &evalTestError{"boom"}

type evalTestError struct{ msg string }

func (e *evalTestError) Error() string { return e.msg }
