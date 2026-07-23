package eval

import (
	"context"
	"fmt"

	"github.com/velion/omnia/internal/llm"
)

// RetrievedCase is what a real search/recall path surfaces for one EvalCase's
// Query: the text to score against ExpectedFact, the sync ID of the
// observation that surfaced it (used only for contradiction cases, spec
// EVAL-4), and this case's non-judge token cost (query-embedding + retrieval
// + injected-context, spec EVAL-1). Judge tokens are added separately by
// RunOnce, since only Score()/ScoreContradiction() know whether a judge ran.
type RetrievedCase struct {
	Retrieved             string
	SurfacedObservationID string
	Tokens                TokenBreakdown
}

// RetrievedFetcher is the harness's single retrieval seam: specs EVAL-1
// through EVAL-5 deliberately leave HOW retrieval happens to the caller (see
// scoring.go's Score, which takes retrieved text as a plain parameter).
// Production callers (cmd/omnia/eval.go) wire this to the live store; tests
// wire it to a self-contained fixture store or a canned fake.
type RetrievedFetcher func(ctx context.Context, c EvalCase) (RetrievedCase, error)

// RunOnce executes ONE full end-to-end harness pass over cases: for each case
// it fetches retrieval via fetch, scores it via the spec EVAL-4 contradiction
// path (cases with SupersedesOf set) or the spec EVAL-5 judge-free/judge-
// required Score dispatcher (every other case), and folds every case into a
// spec EVAL-3 segmented Report. RunOnce is the single pass eval.RunHarness
// (spec EVAL-6) repeats 3-5 times for reproducibility — it is what a
// production Config.Run closure wraps, and what harness_integration_test.go
// exercises end-to-end against a self-contained store.
func RunOnce(ctx context.Context, cases []EvalCase, fetch RetrievedFetcher, judge llm.AgentRunner, relations RelationsGetter) (Report, error) {
	if fetch == nil {
		return Report{}, fmt.Errorf("eval: RunOnce: fetch is nil")
	}

	results := make([]CaseResult, 0, len(cases))
	for _, c := range cases {
		rc, err := fetch(ctx, c)
		if err != nil {
			return Report{}, fmt.Errorf("eval: RunOnce: case %q: fetch: %w", c.ID, err)
		}

		var hit bool
		judgeTokens := 0
		if c.SupersedesOf != nil {
			if relations == nil {
				return Report{}, fmt.Errorf("eval: RunOnce: case %q is a contradiction case (SupersedesOf set) but no RelationsGetter was supplied (spec EVAL-4)", c.ID)
			}
			hit, err = ScoreContradiction(relations, c, rc.SurfacedObservationID)
		} else {
			hit, judgeTokens, err = Score(ctx, c, rc.Retrieved, judge)
		}
		if err != nil {
			return Report{}, fmt.Errorf("eval: RunOnce: case %q: score: %w", c.ID, err)
		}

		tokens := rc.Tokens
		tokens.Judge += judgeTokens
		results = append(results, CaseResult{Case: c, Hit: hit, TotalTokens: tokens.Total()})
	}

	return BuildReport(results), nil
}
