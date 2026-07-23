package eval

import (
	"context"
	"fmt"
	"math"
)

// MinRuns and MaxRuns are spec EVAL-6's reproducibility bounds: a
// configuration MUST be executed 3-5 times, never fewer (a single run's
// number is not reproducible) and never treated as unbounded.
const (
	MinRuns = 3
	MaxRuns = 5
)

// RunFunc executes ONE full harness pass and returns its Report. Wrapping the
// pass as an injectable function — rather than RunHarness owning corpus
// load, retrieval, and scoring directly — is what keeps RunHarness itself
// pure and unit-testable: production callers (cmd/omnia/eval.go) wire RunFunc
// to RunOnce against the real store; tests wire it to a deterministic fake.
type RunFunc func(ctx context.Context) (Report, error)

// Config wraps the single pass a harness run repeats (spec EVAL-6).
type Config struct {
	Run RunFunc
}

// MetricStats is the mean and standard deviation of one metric across N runs
// (spec EVAL-6: "report mean and standard deviation of accuracy and
// quality-per-1k-tokens"). StdDev uses the sample (N-1) formula and is 0 for
// N<2, since a single value has no defined spread.
type MetricStats struct {
	Mean   float64
	StdDev float64
	N      int
}

// SegmentStats is one report segment's (capability, language, or overall)
// MetricStats for both accuracy and quality-per-1k-tokens, aggregated across
// every run in a RunSummary.
type SegmentStats struct {
	Label        string
	Accuracy     MetricStats
	QualityPer1k MetricStats
}

// RunSummary is spec EVAL-6's reproducibility report: MetricStats for every
// capability, every language, and the overall (all-capabilities) figure,
// computed across Runs repeated passes, plus the raw per-run Reports for
// callers that need retrieval sections or other per-run detail (e.g. a CLI
// that prints the last run's Report.Retrieval alongside the aggregated
// stats).
type RunSummary struct {
	Runs         int
	ByCapability map[Capability]SegmentStats
	ByLanguage   map[Language]SegmentStats
	// Overall folds every capability segment into one all-cases figure. Every
	// EvalCase carries exactly one Capability (spec EVAL-2), so this counts
	// each case exactly once — unlike ByLanguage, which is an equally valid
	// but redundant full partition of the same cases.
	Overall SegmentStats
	Reports []Report
}

// RunHarness executes cfg.Run exactly runs times and aggregates the results
// into a RunSummary (spec EVAL-6). runs MUST be in [MinRuns, MaxRuns]; a
// single run's number is not reproducible, so RunHarness refuses to produce
// a summary for runs outside that range rather than silently reporting on
// one pass (spec EVAL-6 "Insufficient runs blocks gating" scenario) or an
// unbounded number of passes.
func RunHarness(ctx context.Context, cfg Config, runs int) (RunSummary, error) {
	if runs < MinRuns || runs > MaxRuns {
		return RunSummary{}, fmt.Errorf("eval: RunHarness: runs must be in [%d,%d] for a reproducible result (spec EVAL-6), got %d", MinRuns, MaxRuns, runs)
	}
	if cfg.Run == nil {
		return RunSummary{}, fmt.Errorf("eval: RunHarness: cfg.Run is nil")
	}

	reports := make([]Report, 0, runs)
	for i := 0; i < runs; i++ {
		r, err := cfg.Run(ctx)
		if err != nil {
			return RunSummary{}, fmt.Errorf("eval: RunHarness: run %d/%d: %w", i+1, runs, err)
		}
		reports = append(reports, r)
	}

	return RunSummary{
		Runs:         runs,
		Reports:      reports,
		ByCapability: statsByCapability(reports),
		ByLanguage:   statsByLanguage(reports),
		Overall:      segmentStatsFor("overall", reports, overallSegment),
	}, nil
}

func statsByCapability(reports []Report) map[Capability]SegmentStats {
	out := make(map[Capability]SegmentStats, len(allCapabilities))
	for _, cap := range allCapabilities {
		cap := cap
		out[cap] = segmentStatsFor(string(cap), reports, func(r Report) Segment { return r.ByCapability[cap] })
	}
	return out
}

func statsByLanguage(reports []Report) map[Language]SegmentStats {
	out := make(map[Language]SegmentStats, len(allLanguages))
	for _, lang := range allLanguages {
		lang := lang
		out[lang] = segmentStatsFor(string(lang), reports, func(r Report) Segment { return r.ByLanguage[lang] })
	}
	return out
}

// overallSegment folds one run's ByCapability segments into a single Segment
// spanning the whole corpus.
func overallSegment(r Report) Segment {
	seg := Segment{Label: "overall"}
	for _, cap := range allCapabilities {
		s := r.ByCapability[cap]
		seg.Total += s.Total
		seg.Hits += s.Hits
		seg.TotalTokens += s.TotalTokens
	}
	return seg
}

// segmentStatsFor computes SegmentStats for one label across reports, pulling
// each run's Segment via get and folding its Accuracy()/QualityPer1kTokens()
// into MetricStats.
func segmentStatsFor(label string, reports []Report, get func(Report) Segment) SegmentStats {
	acc := make([]float64, 0, len(reports))
	qual := make([]float64, 0, len(reports))
	for _, r := range reports {
		seg := get(r)
		acc = append(acc, seg.Accuracy())
		qual = append(qual, seg.QualityPer1kTokens())
	}
	return SegmentStats{
		Label:        label,
		Accuracy:     computeMetricStats(acc),
		QualityPer1k: computeMetricStats(qual),
	}
}

// computeMetricStats returns the mean and sample standard deviation of
// values. N<2 reports StdDev 0 — a single value has no defined spread.
func computeMetricStats(values []float64) MetricStats {
	n := len(values)
	if n == 0 {
		return MetricStats{}
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	mean := sum / float64(n)
	if n < 2 {
		return MetricStats{Mean: mean, N: n}
	}
	var sqDiff float64
	for _, v := range values {
		d := v - mean
		sqDiff += d * d
	}
	return MetricStats{Mean: mean, StdDev: math.Sqrt(sqDiff / float64(n-1)), N: n}
}
