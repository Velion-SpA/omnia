package eval

import (
	"context"
	"fmt"
	"testing"
)

// fixedReport builds a minimal one-case Report for CapabilityRecall/LanguageEN
// whose accuracy is 1.0 (hit) or 0.0 (miss) depending on hit.
func fixedReport(hit bool, tokens int) Report {
	results := []CaseResult{
		{Case: EvalCase{Capability: CapabilityRecall, Language: LanguageEN}, Hit: hit, TotalTokens: tokens},
	}
	return BuildReport(results)
}

// TestRunHarness_ExecutesThreeToFiveRuns is spec EVAL-6's core requirement:
// every configuration MUST be executed 3-5 times, never a single pass.
func TestRunHarness_ExecutesThreeToFiveRuns(t *testing.T) {
	for _, runs := range []int{MinRuns, 4, MaxRuns} {
		runs := runs
		t.Run(fmt.Sprintf("runs=%d", runs), func(t *testing.T) {
			calls := 0
			cfg := Config{Run: func(ctx context.Context) (Report, error) {
				calls++
				return fixedReport(true, 100), nil
			}}
			summary, err := RunHarness(context.Background(), cfg, runs)
			if err != nil {
				t.Fatalf("RunHarness: %v", err)
			}
			if calls != runs {
				t.Errorf("cfg.Run called %d times, want %d", calls, runs)
			}
			if summary.Runs != runs {
				t.Errorf("summary.Runs = %d, want %d", summary.Runs, runs)
			}
			if len(summary.Reports) != runs {
				t.Errorf("len(summary.Reports) = %d, want %d", len(summary.Reports), runs)
			}
		})
	}
}

// TestRunHarness_RejectsOutOfBoundsRuns covers both sides of spec EVAL-6's
// [3,5] bound: too few runs is not reproducible, too many is unbounded.
func TestRunHarness_RejectsOutOfBoundsRuns(t *testing.T) {
	for _, runs := range []int{0, 1, 2, 6, 100} {
		runs := runs
		t.Run(fmt.Sprintf("runs=%d", runs), func(t *testing.T) {
			cfg := Config{Run: func(ctx context.Context) (Report, error) { return fixedReport(true, 100), nil }}
			if _, err := RunHarness(context.Background(), cfg, runs); err == nil {
				t.Errorf("RunHarness(runs=%d): expected error, got nil", runs)
			}
		})
	}
}

// TestGate_SingleRunRefusesDecision is spec EVAL-6's own scenario name:
// "GIVEN only 1 run exists for a configuration WHEN the release gate
// evaluates it THEN the gate refuses to decide and reports insufficient
// runs". RunHarness itself is the first enforcement point.
func TestGate_SingleRunRefusesDecision(t *testing.T) {
	cfg := Config{Run: func(ctx context.Context) (Report, error) { return fixedReport(true, 100), nil }}
	if _, err := RunHarness(context.Background(), cfg, 1); err == nil {
		t.Fatal("expected RunHarness(runs=1) to refuse — spec EVAL-6 'insufficient runs blocks gating'")
	}
}

// TestRunHarness_ReportsMeanAndStddev is spec EVAL-6's other core scenario:
// "GIVEN 5 completed runs of one configuration WHEN the report is generated
// THEN both mean and stddev appear for accuracy and quality-per-1k-tokens".
func TestRunHarness_ReportsMeanAndStddev(t *testing.T) {
	// Three runs with different Recall accuracy: 1.0 (hit), 0.0, 0.0 -> mean 1/3.
	hits := []bool{true, false, false}
	call := 0
	cfg := Config{Run: func(ctx context.Context) (Report, error) {
		r := fixedReport(hits[call], 100)
		call++
		return r, nil
	}}

	summary, err := RunHarness(context.Background(), cfg, MinRuns)
	if err != nil {
		t.Fatalf("RunHarness: %v", err)
	}

	stats := summary.ByCapability[CapabilityRecall]
	wantMean := 1.0 / 3.0
	if diff := stats.Accuracy.Mean - wantMean; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("mean accuracy = %v, want %v", stats.Accuracy.Mean, wantMean)
	}
	if stats.Accuracy.StdDev <= 0 {
		t.Errorf("stddev accuracy = %v, want > 0 for varying runs", stats.Accuracy.StdDev)
	}
	if stats.Accuracy.N != MinRuns {
		t.Errorf("N = %d, want %d", stats.Accuracy.N, MinRuns)
	}

	overall := summary.Overall
	if overall.Accuracy.N != MinRuns {
		t.Errorf("Overall N = %d, want %d", overall.Accuracy.N, MinRuns)
	}
	if overall.Accuracy.Mean != stats.Accuracy.Mean {
		t.Errorf("Overall mean = %v, want %v (only Recall cases exist in this fixture)", overall.Accuracy.Mean, stats.Accuracy.Mean)
	}

	// QualityPer1k must also be populated with mean/stddev, per spec EVAL-6.
	if summary.ByCapability[CapabilityRecall].QualityPer1k.N != MinRuns {
		t.Errorf("QualityPer1k.N = %d, want %d", summary.ByCapability[CapabilityRecall].QualityPer1k.N, MinRuns)
	}
}

// TestRunHarness_PropagatesRunError ensures a failing pass aborts the whole
// summary rather than silently averaging over fewer runs than requested.
func TestRunHarness_PropagatesRunError(t *testing.T) {
	cfg := Config{Run: func(ctx context.Context) (Report, error) {
		return Report{}, fmt.Errorf("boom")
	}}
	if _, err := RunHarness(context.Background(), cfg, MinRuns); err == nil {
		t.Fatal("expected RunHarness to propagate a run error")
	}
}

// TestRunHarness_NilRunFuncErrors guards the Config contract itself.
func TestRunHarness_NilRunFuncErrors(t *testing.T) {
	if _, err := RunHarness(context.Background(), Config{}, MinRuns); err == nil {
		t.Fatal("expected RunHarness to error on a nil Config.Run")
	}
}
