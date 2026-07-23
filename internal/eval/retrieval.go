package eval

import (
	"context"

	"github.com/velion/omnia/internal/embed"
)

// RetrievalSection is spec EVAL-7's intrinsic retrieval-only metric: recall@k
// over the existing bilingual ABPair set, reported as its own section rather
// than folded into the end-task Report/quality-per-1k-tokens figure (spec
// EVAL-7's no-merge rule — see Report.Retrieval).
type RetrievalSection struct {
	Result embed.ABResult
}

// RunRetrievalSection is a thin wrapper around the EXISTING
// internal/embed.RunModelAB recall@k mechanism (spec EVAL-7: "extending the
// existing RunModelAB/ABResult mechanism"). It adds no new retrieval logic —
// it exists only so a harness run has one entry point per report section,
// with this call site documenting which existing mechanism it reuses.
func RunRetrievalSection(ctx context.Context, model string, emb embed.Embedder, pairs []embed.ABPair, k int) (RetrievalSection, error) {
	result, err := embed.RunModelAB(ctx, model, emb, pairs, k)
	if err != nil {
		return RetrievalSection{}, err
	}
	return RetrievalSection{Result: result}, nil
}
