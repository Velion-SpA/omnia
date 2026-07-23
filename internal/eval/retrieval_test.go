package eval

import (
	"context"
	"fmt"
	"testing"

	"github.com/velion/omnia/internal/embed"
)

// fakeRetrievalEmbedder mirrors internal/embed's own fakeABEmbedder test
// double (model_ab_unit_test.go): a fixed text -> vector map, no HTTP or
// live Ollama needed. Kept local to this package rather than exported from
// internal/embed, matching JudgeFreeScorer's own fakeEmbedder pattern in
// scoring_test.go.
type fakeRetrievalEmbedder struct {
	vectors map[string][]float32
}

func (f *fakeRetrievalEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	vec, ok := f.vectors[text]
	if !ok {
		return nil, fmt.Errorf("fakeRetrievalEmbedder: no vector configured for %q", text)
	}
	return vec, nil
}

// TestRetrievalSection_WrapsRunModelAB is spec EVAL-7's intrinsic half:
// RunRetrievalSection must be a thin, behavior-preserving wrapper around the
// EXISTING internal/embed.RunModelAB recall@k mechanism — same inputs,
// same ABResult, nothing re-implemented.
func TestRetrievalSection_WrapsRunModelAB(t *testing.T) {
	pairs := []embed.ABPair{
		{ID: "a", QueryES: "qa", MemoryEN: "ma"},
		{ID: "b", QueryES: "qb", MemoryEN: "mb"},
	}
	fake := &fakeRetrievalEmbedder{vectors: map[string][]float32{
		"qa": {1, 0}, "ma": {1, 0},
		"qb": {0, 1}, "mb": {0, 1},
	}}

	got, err := RunRetrievalSection(context.Background(), "fake-model", fake, pairs, 1)
	if err != nil {
		t.Fatalf("RunRetrievalSection: %v", err)
	}
	want, err := embed.RunModelAB(context.Background(), "fake-model", fake, pairs, 1)
	if err != nil {
		t.Fatalf("embed.RunModelAB (reference): %v", err)
	}
	if got.Result != want {
		t.Errorf("RunRetrievalSection wrapped result = %+v, want identical to embed.RunModelAB result %+v", got.Result, want)
	}

	// Propagates the underlying error unchanged (e.g. empty pairs).
	if _, err := RunRetrievalSection(context.Background(), "fake-model", fake, nil, 1); err == nil {
		t.Error("expected RunRetrievalSection to propagate embed.RunModelAB's empty-pairs error")
	}
}

// TestReport_RetrievalAndEndTaskAreSeparateSections is spec EVAL-7's
// no-merge rule: a report carries the retrieval-only recall@k section and
// the end-task (ByCapability/ByLanguage) accuracy section as independent
// fields — setting one never derives or overwrites the other, and neither
// figure is folded into a single combined number.
func TestReport_RetrievalAndEndTaskAreSeparateSections(t *testing.T) {
	endTaskResults := []CaseResult{
		{Case: EvalCase{Capability: CapabilityRecall, Language: LanguageEN}, Hit: true, TotalTokens: 50},
	}
	report := BuildReport(endTaskResults)

	if report.Retrieval != nil {
		t.Errorf("BuildReport must not populate Retrieval on its own — got %+v", report.Retrieval)
	}

	report.Retrieval = &RetrievalSection{Result: embed.ABResult{
		Model: "fake-model", K: 5, Total: 10, Hits: 8, RecallAtK: 0.8,
	}}

	// End-task segments are untouched by attaching a retrieval section.
	recall := report.ByCapability[CapabilityRecall]
	if recall.Total != 1 || recall.Hits != 1 {
		t.Errorf("ByCapability[recall] changed after attaching Retrieval: %+v", recall)
	}
	if report.Retrieval.Result.RecallAtK != 0.8 {
		t.Errorf("Retrieval section not preserved: %+v", report.Retrieval)
	}
	// The two figures live in distinct, independently-typed fields
	// (Report.Retrieval *RetrievalSection vs Report.ByCapability/ByLanguage
	// map[...]Segment) — no shared "combined" number exists for a caller to
	// accidentally read as spec EVAL-7 forbids.
}
