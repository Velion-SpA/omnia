package eval

import "fmt"

// TokenBreakdown itemizes the token cost of one eval configuration per spec
// EVAL-1: total_tokens sums query-embedding, retrieval, injected-context, and
// judge tokens. Keeping the breakdown alongside the total lets a report
// explain WHERE cost went, not just how much — Judge is always 0 for
// Recall/State-Update cases (spec EVAL-5: no LLM judge, no judge cost).
type TokenBreakdown struct {
	QueryEmbed      int
	Retrieval       int
	InjectedContext int
	Judge           int
}

// Total returns the sum of every accounted token category — spec EVAL-1's
// total_tokens for this configuration.
func (b TokenBreakdown) Total() int {
	return b.QueryEmbed + b.Retrieval + b.InjectedContext + b.Judge
}

// QualityPer1kTokens computes spec EVAL-1's headline metric:
//
//	quality-per-1k-tokens = accuracy / (total_tokens / 1000)
//
// totalTokens <= 0 returns 0 rather than dividing by zero or producing +Inf —
// a configuration with no accounted token cost has no defined
// quality-per-1k-tokens figure, and an Inf would poison any sort/aggregate
// over the frontier.
func QualityPer1kTokens(accuracy float64, totalTokens int) float64 {
	if totalTokens <= 0 {
		return 0
	}
	return accuracy / (float64(totalTokens) / 1000)
}

// ParetoPoint is one (tokens, accuracy) point on the eval harness's Pareto
// frontier for a single configuration (spec EVAL-1) — a frontier of these
// points is reported instead of a single aggregate scalar.
type ParetoPoint struct {
	Model     string
	Tokens    int
	Accuracy  float64
	Breakdown TokenBreakdown
}

// NewParetoPoint builds a ParetoPoint whose Tokens is derived from
// breakdown.Total(), so a point's token count can never drift from its own
// itemized breakdown.
func NewParetoPoint(model string, accuracy float64, breakdown TokenBreakdown) ParetoPoint {
	return ParetoPoint{
		Model:     model,
		Tokens:    breakdown.Total(),
		Accuracy:  accuracy,
		Breakdown: breakdown,
	}
}

// QualityPer1kTokens returns this point's quality-per-1k-tokens score.
func (p ParetoPoint) QualityPer1kTokens() float64 {
	return QualityPer1kTokens(p.Accuracy, p.Tokens)
}

// Frontier is a Pareto frontier: the set of ParetoPoints produced by one
// harness run, one point per configuration (spec EVAL-1). Segmenting by
// capability/language (spec EVAL-3) and separating retrieval-only from
// end-task figures (spec EVAL-7) build on top of this in later work units —
// Frontier itself only guarantees the "not a single aggregate scalar" floor.
//
// CAVEAT: Frontier currently holds validated raw points as produced by one
// harness run — it does NOT yet prune Pareto-dominated points (a point
// strictly worse than another on both tokens and accuracy). Despite the
// name, callers should not assume every Point here is on the actual Pareto
// frontier until that pruning step is added (tracked as a follow-up, not yet
// scheduled in tasks.md).
type Frontier struct {
	Points []ParetoPoint
}

// Validate enforces spec EVAL-1's "aggregate-only report rejected" scenario:
// a frontier MUST carry at least one per-configuration point, and every
// point MUST expose a non-empty token breakdown so per-category detail
// survives — a bare (model, ratio) pair with no breakdown is exactly the
// single aggregate scalar the spec forbids.
func (f Frontier) Validate() error {
	if len(f.Points) == 0 {
		return fmt.Errorf("eval: frontier has no points — a single aggregate ratio alone is not a report (spec EVAL-1)")
	}
	for i, p := range f.Points {
		if p.Breakdown.Total() == 0 {
			return fmt.Errorf("eval: frontier point %d (%s) has no token breakdown — quality-per-1k-tokens alone is not a report (spec EVAL-1)", i, p.Model)
		}
	}
	return nil
}
