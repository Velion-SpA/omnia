package eval

import (
	"context"
	"fmt"
)

// ConversationalRunFunc executes ONE full conversational-harness pass and
// returns its ConversationalReport — the conversational sibling of RunFunc
// (runner.go).
type ConversationalRunFunc func(ctx context.Context) (ConversationalReport, error)

// KindStats is one QuestionKind's mean/stddev across N reproducibility
// runs — the conversational sibling of SegmentStats (runner.go), carrying
// every per-kind rate KindSegment exposes so determinism can be verified on
// ALL of them, not just one headline number.
type KindStats struct {
	Kind              QuestionKind
	AccuracyAt1       MetricStats
	MRR               MetricStats
	GroundingRate     MetricStats
	HonestRefusalRate MetricStats
	// FalseRefusalRate is P3's other failure-mode rate (KindSegment.
	// FalseRefusalRate's own doc) — only meaningfully non-zero for
	// identity/status kinds scored against a fetcher that reports
	// RetrievedCase.Confidence (--target answer).
	FalseRefusalRate MetricStats
}

// ConversationalRunSummary is the reproducibility report for the
// conversational corpus, mirroring RunSummary (runner.go). Determinism is a
// hard requirement for this profile exactly like the coding-agent one ("the
// existing harness reports ±0.000 across repeated runs. Yours must too." —
// see TestRunConversationalHarness_Deterministic).
type ConversationalRunSummary struct {
	Runs   int
	ByKind map[QuestionKind]KindStats
}

// RunConversationalHarness executes run exactly runs times and aggregates
// the results into a ConversationalRunSummary, reusing runner.go's
// MinRuns/MaxRuns bound and computeMetricStats — the SAME reproducibility
// discipline RunHarness already enforces for the coding-agent corpus,
// applied to this corpus's own per-kind metrics instead of a single
// accuracy figure.
func RunConversationalHarness(ctx context.Context, run ConversationalRunFunc, runs int) (ConversationalRunSummary, error) {
	if runs < MinRuns || runs > MaxRuns {
		return ConversationalRunSummary{}, fmt.Errorf("eval: RunConversationalHarness: runs must be in [%d,%d] for a reproducible result, got %d", MinRuns, MaxRuns, runs)
	}
	if run == nil {
		return ConversationalRunSummary{}, fmt.Errorf("eval: RunConversationalHarness: run is nil")
	}

	reports := make([]ConversationalReport, 0, runs)
	for i := 0; i < runs; i++ {
		r, err := run(ctx)
		if err != nil {
			return ConversationalRunSummary{}, fmt.Errorf("eval: RunConversationalHarness: run %d/%d: %w", i+1, runs, err)
		}
		reports = append(reports, r)
	}

	byKind := make(map[QuestionKind]KindStats, len(AllQuestionKinds))
	for _, k := range AllQuestionKinds {
		acc := make([]float64, 0, runs)
		mrr := make([]float64, 0, runs)
		grounding := make([]float64, 0, runs)
		refusal := make([]float64, 0, runs)
		falseRefusal := make([]float64, 0, runs)
		for _, r := range reports {
			seg := r.ByKind[k]
			acc = append(acc, seg.AccuracyAt1())
			mrr = append(mrr, seg.MRR())
			grounding = append(grounding, seg.GroundingRate())
			refusal = append(refusal, seg.HonestRefusalRate())
			falseRefusal = append(falseRefusal, seg.FalseRefusalRate())
		}
		byKind[k] = KindStats{
			Kind:              k,
			AccuracyAt1:       computeMetricStats(acc),
			MRR:               computeMetricStats(mrr),
			GroundingRate:     computeMetricStats(grounding),
			HonestRefusalRate: computeMetricStats(refusal),
			FalseRefusalRate:  computeMetricStats(falseRefusal),
		}
	}

	return ConversationalRunSummary{Runs: runs, ByKind: byKind}, nil
}
