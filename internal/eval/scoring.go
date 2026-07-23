package eval

import (
	"context"
	"fmt"
	"strings"

	"github.com/velion/omnia/internal/llm"
)

// judgeFreeCapabilities is spec EVAL-5's set that MUST NOT require an LLM
// judge: Recall and State-Update/Staleness cases are scored via
// substring/exact-match (and, when an embedder is configured, an
// embedding-similarity threshold as a second-chance check).
var judgeFreeCapabilities = map[Capability]bool{
	CapabilityRecall:      true,
	CapabilityStateUpdate: true,
}

// judgeRequiredCapabilities is spec EVAL-5's set that MAY use an LLM judge:
// Causal and State-Abstraction cases need semantic judgment a substring
// match cannot capture.
var judgeRequiredCapabilities = map[Capability]bool{
	CapabilityCausal:           true,
	CapabilityStateAbstraction: true,
}

// hitRelations is the subset of the locked internal/llm relation vocabulary
// (see llm.Verdict.Relation) that counts as a scoring hit when the judge
// compares a case's ExpectedFact against retrieved text: "compatible"
// (consistent, confirms the fact) and "related" (same topic, no
// contradiction). "conflicts_with", "not_conflict", "supersedes", and
// "scoped" all count as a miss — none of them means "the retrieved text
// correctly states the expected fact".
var hitRelations = map[string]bool{
	"compatible": true,
	"related":    true,
}

// DefaultEmbeddingThreshold is the minimum cosine similarity counted as an
// embedding-threshold hit when JudgeFreeScorer.Threshold is left at zero.
const DefaultEmbeddingThreshold = 0.75

// Scorer decides whether retrieved text is a hit for one EvalCase, and
// reports the judge-token cost incurred while deciding (spec EVAL-5).
// Judge-free scorers always report 0 judgeTokens.
type Scorer interface {
	Score(ctx context.Context, c EvalCase, retrieved string) (hit bool, judgeTokens int, err error)
}

// EmbeddingSimilarity is the minimal embedding capability JudgeFreeScorer
// needs: turn text into a comparable vector. internal/embed.Client (and any
// test fake) satisfies this without eval importing embed's HTTP transport
// concerns beyond the interface.
type EmbeddingSimilarity interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// JudgeFreeScorer scores Recall and State-Update/Staleness cases without any
// LLM (spec EVAL-5): exact/substring match on ExpectedFact first, then —
// only when Embedder is set and the substring match misses — an embedding
// cosine-similarity threshold check. It rejects any capability outside that
// pair rather than silently guessing a verdict.
type JudgeFreeScorer struct {
	// Embedder is optional. When nil, only substring/exact match runs.
	Embedder EmbeddingSimilarity
	// Threshold is the minimum cosine similarity counted as a hit. Zero uses
	// DefaultEmbeddingThreshold.
	Threshold float64
}

var _ Scorer = JudgeFreeScorer{}

// Score implements Scorer for JudgeFreeScorer.
func (s JudgeFreeScorer) Score(ctx context.Context, c EvalCase, retrieved string) (bool, int, error) {
	if !judgeFreeCapabilities[c.Capability] {
		return false, 0, fmt.Errorf("eval: JudgeFreeScorer: case %q has capability %q, want recall or state_update", c.ID, c.Capability)
	}
	if factMatches(c.ExpectedFact, retrieved) {
		return true, 0, nil
	}
	if s.Embedder == nil {
		return false, 0, nil
	}
	hit, err := s.embeddingThresholdHit(ctx, c.ExpectedFact, retrieved)
	if err != nil {
		return false, 0, fmt.Errorf("eval: JudgeFreeScorer: case %q: %w", c.ID, err)
	}
	return hit, 0, nil
}

// factMatches covers both spec EVAL-5's "exact-match" and "substring" modes:
// case-insensitive containment already subsumes exact equality (a fully
// equal, trimmed pair is trivially a substring of itself), so one check
// serves both without duplicated logic.
func factMatches(expected, retrieved string) bool {
	if expected == "" {
		return false
	}
	return strings.Contains(strings.ToLower(retrieved), strings.ToLower(strings.TrimSpace(expected)))
}

func (s JudgeFreeScorer) embeddingThresholdHit(ctx context.Context, expected, retrieved string) (bool, error) {
	threshold := s.Threshold
	if threshold == 0 {
		threshold = DefaultEmbeddingThreshold
	}
	expVec, err := s.Embedder.Embed(ctx, expected)
	if err != nil {
		return false, fmt.Errorf("embed expected fact: %w", err)
	}
	retVec, err := s.Embedder.Embed(ctx, retrieved)
	if err != nil {
		return false, fmt.Errorf("embed retrieved text: %w", err)
	}
	return cosineSimilarity(expVec, retVec) >= threshold, nil
}

// cosineSimilarity assumes unit-normalized vectors — every Embedder in this
// codebase normalizes its output (internal/embed.Client.Embed) — so the dot
// product IS the cosine similarity, the same primitive
// internal/embed.RunModelAB and Store.Search already use.
func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var sum float64
	for i := range a {
		sum += float64(a[i]) * float64(b[i])
	}
	return sum
}

// LLMJudgeScorer scores Causal and State-Abstraction cases via the existing
// internal/llm.AgentRunner semantic-comparison abstraction (spec EVAL-5): it
// asks the judge to compare the case's ExpectedFact against the retrieved
// text using llm.BuildPrompt's locked relation vocabulary — the same JSON
// contract every AgentRunner implementation already parses — and counts a
// hit when the judge classifies the pair "compatible" or "related". Every
// completed call's estimated cost (via the existing llm.EstimateScanCost) is
// reported as judgeTokens so it can be summed into the case's total_tokens
// (spec EVAL-1, EVAL-5).
type LLMJudgeScorer struct {
	Judge llm.AgentRunner
}

var _ Scorer = LLMJudgeScorer{}

// Score implements Scorer for LLMJudgeScorer.
func (s LLMJudgeScorer) Score(ctx context.Context, c EvalCase, retrieved string) (bool, int, error) {
	if !judgeRequiredCapabilities[c.Capability] {
		return false, 0, fmt.Errorf("eval: LLMJudgeScorer: case %q has capability %q, want causal or state_abstraction", c.ID, c.Capability)
	}
	if s.Judge == nil {
		return false, 0, fmt.Errorf("eval: LLMJudgeScorer: case %q: no judge configured (spec EVAL-5 causal/state_abstraction cases require one)", c.ID)
	}
	prompt := llm.BuildPrompt(
		llm.ObservationSnippet{ID: c.ID + "-expected", Title: "Expected fact", Content: c.ExpectedFact, Type: "eval-expected"},
		llm.ObservationSnippet{ID: c.ID + "-retrieved", Title: "Retrieved answer", Content: retrieved, Type: "eval-retrieved"},
	)
	verdict, err := s.Judge.Compare(ctx, prompt)
	if err != nil {
		return false, 0, fmt.Errorf("eval: LLMJudgeScorer: case %q: judge compare: %w", c.ID, err)
	}
	inputTokens, outputTokens := llm.EstimateScanCost(1)
	return hitRelations[verdict.Relation], inputTokens + outputTokens, nil
}

// Score routes an EvalCase to the correct judge-free or judge-required
// scorer per spec EVAL-5's capability split. judge may be nil when scoring
// only Recall/State-Update cases; passing nil for a Causal/State-Abstraction
// case returns an error rather than silently scoring a miss.
//
// WARNING: Score constructs its JudgeFreeScorer with a nil Embedder, so the
// embedding-similarity-threshold fallback is never exercised through this
// entry point — only exact/substring match runs. Callers that need
// State-Update's embedding fallback (spec EVAL-5) must construct
// JudgeFreeScorer{Embedder: ...} directly instead of calling Score.
func Score(ctx context.Context, c EvalCase, retrieved string, judge llm.AgentRunner) (hit bool, judgeTokens int, err error) {
	switch {
	case judgeFreeCapabilities[c.Capability]:
		return JudgeFreeScorer{}.Score(ctx, c, retrieved)
	case judgeRequiredCapabilities[c.Capability]:
		return LLMJudgeScorer{Judge: judge}.Score(ctx, c, retrieved)
	default:
		return false, 0, fmt.Errorf("eval: Score: case %q has unscoreable capability %q", c.ID, c.Capability)
	}
}
