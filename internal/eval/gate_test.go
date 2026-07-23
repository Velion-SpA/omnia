package eval

import (
	"context"
	"testing"
)

// fixedSummary builds a RunSummary whose overall accuracy is exactly
// accuracy, backed by MinRuns identical passes so EvaluateGate's own
// "insufficient runs" floor never trips these tests.
func fixedSummary(t *testing.T, accuracy float64) RunSummary {
	t.Helper()
	cfg := Config{Run: func(ctx context.Context) (Report, error) { return fixedReport(accuracy >= 1.0, 100), nil }}
	summary, err := RunHarness(context.Background(), cfg, MinRuns)
	if err != nil {
		t.Fatalf("RunHarness: %v", err)
	}
	return summary
}

// TestGate_AdvisoryNeverBlocks is spec EVAL-8's "Advisory mode never blocks
// release" scenario: a detected regression still leaves Blocked false.
func TestGate_AdvisoryNeverBlocks(t *testing.T) {
	summary := fixedSummary(t, 0.0) // overall accuracy 0.0
	result, err := EvaluateGate(summary, GateModeAdvisory, 0.9, 0.05)
	if err != nil {
		t.Fatalf("EvaluateGate: %v", err)
	}
	if !result.Regressed {
		t.Fatal("expected a regression to be detected (0.0 vs baseline 0.9)")
	}
	if result.Blocked {
		t.Error("advisory mode must never block (spec EVAL-8)")
	}
}

// TestGate_BlockingFailsPastThreshold is spec EVAL-8's "Blocking mode fails
// the release step" scenario.
func TestGate_BlockingFailsPastThreshold(t *testing.T) {
	summary := fixedSummary(t, 0.0)
	result, err := EvaluateGate(summary, GateModeBlocking, 0.9, 0.05)
	if err != nil {
		t.Fatalf("EvaluateGate: %v", err)
	}
	if !result.Blocked {
		t.Error("blocking mode past threshold must block (spec EVAL-8)")
	}
}

// TestGate_BlockingWithinThresholdDoesNotBlock ensures blocking mode only
// fails PAST the configured threshold, not on any accuracy drop at all.
func TestGate_BlockingWithinThresholdDoesNotBlock(t *testing.T) {
	summary := fixedSummary(t, 1.0) // no regression at all
	result, err := EvaluateGate(summary, GateModeBlocking, 1.0, 0.05)
	if err != nil {
		t.Fatalf("EvaluateGate: %v", err)
	}
	if result.Regressed || result.Blocked {
		t.Errorf("expected no regression/block, got %+v", result)
	}
}

// TestGate_InsufficientRunsRefusesDecision guards against a hand-built
// RunSummary bypassing spec EVAL-6's reproducibility floor.
func TestGate_InsufficientRunsRefusesDecision(t *testing.T) {
	summary := RunSummary{Runs: 1}
	if _, err := EvaluateGate(summary, GateModeAdvisory, 0.9, 0.05); err == nil {
		t.Error("expected EvaluateGate to refuse a summary with fewer than MinRuns runs (spec EVAL-6)")
	}
}

// TestGate_InvalidModeErrors guards the GateMode contract.
func TestGate_InvalidModeErrors(t *testing.T) {
	summary := fixedSummary(t, 1.0)
	if _, err := EvaluateGate(summary, GateMode("yolo"), 0.9, 0.05); err == nil {
		t.Error("expected EvaluateGate to reject an unknown GateMode")
	}
}
