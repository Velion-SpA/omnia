package eval

import (
	"context"
	"errors"
	"testing"

	"github.com/velion/omnia/internal/llm"
)

// fakeJudge is a minimal llm.AgentRunner test double — the same pattern
// internal/llm/runner_test.go's fakeRunner uses — so scoring tests never
// shell out to a real CLI.
type fakeJudge struct {
	verdict    llm.Verdict
	err        error
	calls      int
	lastPrompt string
}

var _ llm.AgentRunner = (*fakeJudge)(nil)

func (f *fakeJudge) Compare(ctx context.Context, prompt string) (llm.Verdict, error) {
	f.calls++
	f.lastPrompt = prompt
	return f.verdict, f.err
}

// fakeEmbedder returns a fixed vector per exact input string so embedding-
// threshold tests can control cosine similarity deterministically without a
// live embedding model.
type fakeEmbedder struct {
	vectors map[string][]float32
}

func (f fakeEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if v, ok := f.vectors[text]; ok {
		return v, nil
	}
	return nil, errors.New("fakeEmbedder: no vector for text")
}

func recallCase() EvalCase {
	return EvalCase{ID: "c1", ObservationID: "obs-1", Capability: CapabilityRecall, Query: "q", Language: LanguageEN, ExpectedFact: "the sky is blue"}
}

func stateUpdateCase() EvalCase {
	return EvalCase{ID: "c2", ObservationID: "obs-2", Capability: CapabilityStateUpdate, Query: "q", Language: LanguageEN, ExpectedFact: "current status is active"}
}

func causalCase() EvalCase {
	return EvalCase{ID: "c3", ObservationID: "obs-3", Capability: CapabilityCausal, Query: "q", Language: LanguageEN, ExpectedFact: "chosen because it scales better"}
}

func stateAbstractionCase() EvalCase {
	return EvalCase{ID: "c4", ObservationID: "obs-4", Capability: CapabilityStateAbstraction, Query: "q", Language: LanguageES, ExpectedFact: "unified under one design system"}
}

// ─── Score() top-level dispatcher ──────────────────────────────────────────

func TestScoreRecall_SubstringMatch_NoJudgeTokens(t *testing.T) {
	c := recallCase()
	hit, tokens, err := Score(context.Background(), c, "Everyone knows the sky is blue today.", nil)
	if err != nil {
		t.Fatalf("Score: unexpected error: %v", err)
	}
	if !hit {
		t.Error("expected a hit on substring match, got miss")
	}
	if tokens != 0 {
		t.Errorf("judgeTokens = %d, want 0 (judge-free capability must never cost judge tokens)", tokens)
	}
}

func TestScoreRecall_SubstringMiss_NoEmbedder(t *testing.T) {
	c := recallCase()
	hit, tokens, err := Score(context.Background(), c, "completely unrelated text", nil)
	if err != nil {
		t.Fatalf("Score: unexpected error: %v", err)
	}
	if hit {
		t.Error("expected a miss when no substring match and no embedder configured")
	}
	if tokens != 0 {
		t.Errorf("judgeTokens = %d, want 0", tokens)
	}
}

func TestScoreStateUpdate_ExactOrEmbeddingThreshold(t *testing.T) {
	c := stateUpdateCase()

	t.Run("exact match hits without an embedder", func(t *testing.T) {
		hit, tokens, err := Score(context.Background(), c, "current status is active", nil)
		if err != nil {
			t.Fatalf("Score: unexpected error: %v", err)
		}
		if !hit {
			t.Error("expected exact-match hit")
		}
		if tokens != 0 {
			t.Errorf("judgeTokens = %d, want 0", tokens)
		}
	})

	t.Run("embedding threshold hits when substring misses but vectors are close", func(t *testing.T) {
		emb := fakeEmbedder{vectors: map[string][]float32{
			c.ExpectedFact:             {1, 0},
			"system is up and running": {0.99, 0.14107}, // dot product ~0.99, above default 0.75 threshold
		}}
		scorer := JudgeFreeScorer{Embedder: emb}
		hit, tokens, err := scorer.Score(context.Background(), c, "system is up and running")
		if err != nil {
			t.Fatalf("Score: unexpected error: %v", err)
		}
		if !hit {
			t.Error("expected embedding-threshold hit")
		}
		if tokens != 0 {
			t.Errorf("judgeTokens = %d, want 0 (embedding threshold is judge-free)", tokens)
		}
	})

	t.Run("embedding threshold misses when vectors are far apart", func(t *testing.T) {
		emb := fakeEmbedder{vectors: map[string][]float32{
			c.ExpectedFact:            {1, 0},
			"totally different topic": {0, 1}, // cosine 0.0
		}}
		scorer := JudgeFreeScorer{Embedder: emb}
		hit, _, err := scorer.Score(context.Background(), c, "totally different topic")
		if err != nil {
			t.Fatalf("Score: unexpected error: %v", err)
		}
		if hit {
			t.Error("expected a miss when embedding similarity is far below threshold")
		}
	})
}

func TestScoreCausal_UsesAgentRunner(t *testing.T) {
	judge := &fakeJudge{verdict: llm.Verdict{Relation: "compatible", Confidence: 0.9}}
	c := causalCase()

	hit, tokens, err := Score(context.Background(), c, "it scales better under load", judge)
	if err != nil {
		t.Fatalf("Score: unexpected error: %v", err)
	}
	if judge.calls != 1 {
		t.Errorf("judge.calls = %d, want 1 (Causal cases must invoke the AgentRunner)", judge.calls)
	}
	if judge.lastPrompt == "" {
		t.Error("expected a non-empty prompt sent to the judge")
	}
	if !hit {
		t.Error("expected a hit for Relation=compatible")
	}
	if tokens <= 0 {
		t.Errorf("judgeTokens = %d, want > 0 (a completed judge call must be accounted)", tokens)
	}
}

func TestScoreStateAbstraction_CountsJudgeTokensInTotal(t *testing.T) {
	judge := &fakeJudge{verdict: llm.Verdict{Relation: "related", Confidence: 0.7}}
	c := stateAbstractionCase()

	hit, tokens, err := Score(context.Background(), c, "one shared design system for both dashboards", judge)
	if err != nil {
		t.Fatalf("Score: unexpected error: %v", err)
	}
	if !hit {
		t.Error("expected a hit for Relation=related")
	}
	wantInput, wantOutput := llm.EstimateScanCost(1)
	if tokens != wantInput+wantOutput {
		t.Errorf("judgeTokens = %d, want %d (llm.EstimateScanCost(1) sum)", tokens, wantInput+wantOutput)
	}
}

func TestScoreCausal_MissOnConflict(t *testing.T) {
	judge := &fakeJudge{verdict: llm.Verdict{Relation: "conflicts_with", Confidence: 0.9}}
	hit, _, err := Score(context.Background(), causalCase(), "the opposite is true", judge)
	if err != nil {
		t.Fatalf("Score: unexpected error: %v", err)
	}
	if hit {
		t.Error("expected a miss for Relation=conflicts_with")
	}
}

func TestScoreCausal_JudgeError(t *testing.T) {
	sentinel := errors.New("cli not installed")
	judge := &fakeJudge{err: sentinel}
	hit, tokens, err := Score(context.Background(), causalCase(), "anything", judge)
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error propagated, got: %v", err)
	}
	if hit {
		t.Error("expected hit=false on judge error")
	}
	if tokens != 0 {
		t.Errorf("judgeTokens = %d, want 0 on judge error (no completed call to account)", tokens)
	}
}

func TestScoreCausal_NilJudgeErrors(t *testing.T) {
	_, _, err := Score(context.Background(), causalCase(), "anything", nil)
	if err == nil {
		t.Error("expected an error when a Causal case is scored with a nil judge")
	}
}

func TestJudgeFreeScorer_RejectsJudgeRequiredCapability(t *testing.T) {
	_, _, err := JudgeFreeScorer{}.Score(context.Background(), causalCase(), "anything")
	if err == nil {
		t.Error("expected JudgeFreeScorer to reject a Causal case")
	}
}

func TestLLMJudgeScorer_RejectsJudgeFreeCapability(t *testing.T) {
	judge := &fakeJudge{verdict: llm.Verdict{Relation: "compatible"}}
	_, _, err := LLMJudgeScorer{Judge: judge}.Score(context.Background(), recallCase(), "anything")
	if err == nil {
		t.Error("expected LLMJudgeScorer to reject a Recall case")
	}
	if judge.calls != 0 {
		t.Errorf("judge.calls = %d, want 0 (rejected before invoking the judge)", judge.calls)
	}
}
