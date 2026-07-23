package eval

import "fmt"

// GateMode controls whether a spec EVAL-8 release-gate decision can block a
// release. Default MUST be GateModeAdvisory (spec EVAL-8: "Default MUST be
// advisory").
type GateMode string

const (
	GateModeAdvisory GateMode = "advisory"
	GateModeBlocking GateMode = "blocking"
)

// GateResult is one release-gate decision over a RunSummary (spec EVAL-8).
type GateResult struct {
	Mode             GateMode
	BaselineAccuracy float64
	CurrentAccuracy  float64
	Threshold        float64
	// Regressed is true whenever CurrentAccuracy dropped more than Threshold
	// below BaselineAccuracy, REGARDLESS of Mode — advisory mode still
	// detects and reports a regression, it just never blocks on it (spec
	// EVAL-8 "Advisory mode never blocks release": "a regression is detected
	// ... the pipeline continues and the regression is logged").
	Regressed bool
	// Blocked is true only when Mode is blocking AND Regressed — advisory
	// mode never sets this.
	Blocked bool
}

// EvaluateGate decides a spec EVAL-8 release-gate outcome for summary's
// overall accuracy (summary.Overall, spec EVAL-6's mean across 3-5 runs —
// never a single run's number) against baselineAccuracy. A regression is
// CurrentAccuracy dropping more than threshold below baselineAccuracy.
//
// Per spec EVAL-6's "Insufficient runs blocks gating" scenario, EvaluateGate
// refuses to decide (returns an error) when summary backs fewer than MinRuns
// runs. This is a defense-in-depth check: eval.RunHarness already enforces
// the [MinRuns,MaxRuns] bound before producing a RunSummary, but a caller
// that hand-builds one (e.g. a test, or a future alternate summary source)
// must not be able to bypass the rule.
func EvaluateGate(summary RunSummary, mode GateMode, baselineAccuracy, threshold float64) (GateResult, error) {
	if mode != GateModeAdvisory && mode != GateModeBlocking {
		return GateResult{}, fmt.Errorf("eval: EvaluateGate: mode must be %q or %q, got %q", GateModeAdvisory, GateModeBlocking, mode)
	}
	if summary.Runs < MinRuns {
		return GateResult{}, fmt.Errorf("eval: EvaluateGate: summary has %d run(s), want at least %d for a reproducible decision (spec EVAL-6)", summary.Runs, MinRuns)
	}

	current := summary.Overall.Accuracy.Mean
	regressed := (baselineAccuracy - current) > threshold

	return GateResult{
		Mode:             mode,
		BaselineAccuracy: baselineAccuracy,
		CurrentAccuracy:  current,
		Threshold:        threshold,
		Regressed:        regressed,
		Blocked:          regressed && mode == GateModeBlocking,
	}, nil
}
