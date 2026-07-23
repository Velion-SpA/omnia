package eval

import (
	"context"
	"hash/fnv"
	"math"
	"os"
	"testing"

	"github.com/velion/omnia/internal/embed"
	"github.com/velion/omnia/internal/llm"
	"github.com/velion/omnia/internal/store"
)

// TestHarnessIntegration_EndToEndAcrossAllSections is Phase 9's integration
// test: it runs the REAL starter corpus (testdata/cases.json, spec EVAL-2's
// dogfooded material) and the REAL bilingual AB-pairs set
// (internal/embed/testdata/ab_pairs.json, spec EVAL-7) through the full
// harness wiring — RunOnce -> RunHarness (spec EVAL-6, 3 reproducibility
// runs) — and asserts every report section specs EVAL-1..EVAL-7 require is
// present: per-configuration token-breakdown detail (spec EVAL-1's
// aggregate-only floor), per-capability and per-language segments (spec
// EVAL-3), a distinct retrieval-only section (spec EVAL-7), end-task
// accuracy, and mean/stddev across runs (spec EVAL-6).
//
// testdata/cases.json currently has 11 cases (PR1 apply-progress: full
// 50-150 authoring per spec EVAL-2 is a deferred follow-up data-authoring
// task, not in this PR's scope). This test therefore loads it via the
// package-internal parseCorpus — the same schema/traceability validation
// LoadCorpus uses, WITHOUT LoadCorpus's production [50,150] count floor — so
// the wiring itself is exercised end-to-end against real, dogfooded data
// today rather than waiting on that follow-up. Production `omnia eval`
// (cmd/omnia/eval.go) still calls the real LoadCorpus and will correctly
// refuse to run until the corpus reaches 50 cases.
func TestHarnessIntegration_EndToEndAcrossAllSections(t *testing.T) {
	data, err := os.ReadFile("testdata/cases.json")
	if err != nil {
		t.Fatalf("read testdata/cases.json: %v", err)
	}
	cases, err := parseCorpus(data, "testdata/cases.json")
	if err != nil {
		t.Fatalf("parseCorpus: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("expected at least one case in testdata/cases.json")
	}

	// Self-contained temp store (same bootstrap as contradiction_test.go) —
	// seed one observation per case whose title mirrors the case's query and
	// whose content mirrors the expected fact, so a real FTS Search finds a
	// deterministic top hit for judge-free substring scoring.
	s := newContradictionTestStore(t)
	if err := s.CreateSession("eval-integration-test", "eval-harness", "/tmp/eval-harness"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	for _, c := range cases {
		if _, err := s.AddObservation(store.AddObservationParams{
			SessionID: "eval-integration-test",
			Type:      "architecture",
			Title:     c.Query,
			Content:   c.ExpectedFact,
			Project:   "eval-harness",
			Scope:     "project",
		}); err != nil {
			t.Fatalf("seed observation for case %q: %v", c.ID, err)
		}
	}

	fetch := func(ctx context.Context, c EvalCase) (RetrievedCase, error) {
		results, err := s.Search(c.Query, store.SearchOptions{Limit: 1})
		if err != nil {
			return RetrievedCase{}, err
		}
		if len(results) == 0 {
			return RetrievedCase{}, nil
		}
		return RetrievedCase{
			Retrieved:             results[0].Content,
			SurfacedObservationID: results[0].SyncID,
			Tokens:                TokenBreakdown{Retrieval: 10},
		}, nil
	}

	// Causal/State-Abstraction cases need a judge (spec EVAL-5); a fake
	// AgentRunner keeps this test hermetic (no real LLM CLI call).
	judge := &fakeJudge{verdict: llm.Verdict{Relation: "compatible"}}

	pairs, err := embed.LoadABPairs("../embed/testdata/ab_pairs.json")
	if err != nil {
		t.Fatalf("embed.LoadABPairs: %v", err)
	}

	runFunc := func(ctx context.Context) (Report, error) {
		report, err := RunOnce(ctx, cases, fetch, judge, s)
		if err != nil {
			return Report{}, err
		}
		section, err := RunRetrievalSection(ctx, "integration-fake-model", integrationFakeEmbedder{}, pairs, 5)
		if err != nil {
			return Report{}, err
		}
		report.Retrieval = &section
		return report, nil
	}

	summary, err := RunHarness(context.Background(), Config{Run: runFunc}, MinRuns)
	if err != nil {
		t.Fatalf("RunHarness: %v", err)
	}

	if summary.Runs != MinRuns {
		t.Errorf("summary.Runs = %d, want %d", summary.Runs, MinRuns)
	}
	if len(summary.Reports) != MinRuns {
		t.Fatalf("len(summary.Reports) = %d, want %d", len(summary.Reports), MinRuns)
	}

	// Spec EVAL-3: every capability and language bucket present.
	for _, cap := range allCapabilities {
		if _, ok := summary.ByCapability[cap]; !ok {
			t.Errorf("missing ByCapability[%s]", cap)
		}
	}
	for _, lang := range allLanguages {
		if _, ok := summary.ByLanguage[lang]; !ok {
			t.Errorf("missing ByLanguage[%s]", lang)
		}
	}

	// Spec EVAL-6: mean AND stddev computed across MinRuns runs for every
	// segment (Overall proves the aggregation ran; N confirms it's not a
	// single-run number).
	if summary.Overall.Accuracy.N != MinRuns {
		t.Errorf("Overall.Accuracy.N = %d, want %d", summary.Overall.Accuracy.N, MinRuns)
	}
	if summary.Overall.QualityPer1k.N != MinRuns {
		t.Errorf("Overall.QualityPer1k.N = %d, want %d", summary.Overall.QualityPer1k.N, MinRuns)
	}

	// Spec EVAL-7: a distinct retrieval-only section on every run, never
	// merged into ByCapability/ByLanguage.
	for i, r := range summary.Reports {
		if r.Retrieval == nil {
			t.Errorf("run %d: expected a retrieval-only section (spec EVAL-7), got nil", i)
			continue
		}
		if r.Retrieval.Result.Total == 0 {
			t.Errorf("run %d: retrieval section ran over zero pairs", i)
		}
	}

	// Spec EVAL-1: every case's token cost is accounted for, so
	// quality-per-1k-tokens is well-defined (not the un-computable
	// zero-token floor) whenever the segment has cases.
	for cap, seg := range summary.ByCapability {
		if seg.Accuracy.N != MinRuns {
			t.Errorf("ByCapability[%s].Accuracy.N = %d, want %d", cap, seg.Accuracy.N, MinRuns)
		}
	}
}

// integrationFakeEmbedder is a deterministic, dependency-free embed.Embedder
// test double (fnv hash -> fixed-dim unit vector) so RunRetrievalSection's
// recall@k computation runs without a live Ollama instance. It satisfies
// embed.Embedder structurally (Embed(ctx, text) ([]float32, error)).
type integrationFakeEmbedder struct{}

func (integrationFakeEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	h := fnv.New32a()
	_, _ = h.Write([]byte(text))
	seed := h.Sum32()
	vec := make([]float32, 8)
	for i := range vec {
		if (seed>>uint(i))&1 == 1 {
			vec[i] = 1
		} else {
			vec[i] = -1
		}
	}
	return normalizeVector(vec), nil
}

func normalizeVector(v []float32) []float32 {
	var sumSq float64
	for _, x := range v {
		sumSq += float64(x) * float64(x)
	}
	if sumSq == 0 {
		return v
	}
	norm := math.Sqrt(sumSq)
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(float64(x) / norm)
	}
	return out
}
