package intent

import (
	"strings"
	"testing"
	"time"
)

// benchQuery is a ~190-character bilingual-shaped query, close to the
// plan's stated ceiling ("a query of up to ~200 characters") — long enough
// that every regexp.MatchString call in signals has to scan a realistic
// worst case, not a three-word toy string, and it deliberately does NOT
// match any early-table rule (no rationale/delta phrase at the front), so
// ClassifyWithThreshold cannot skip most of the table via its
// confidence-already-beaten short-circuit.
const benchQuery = "che, quería preguntarte algo sobre cómo va el proyecto de retrieval conversacional este mes, en particular si ya terminamos de clasificar las siete intenciones y cuál es el estado del endpoint de answer que estábamos armando"

func TestClassify_LatencyBudgetUnderOneMillisecond(t *testing.T) {
	if len(benchQuery) > 250 {
		t.Fatalf("benchQuery is %d chars; keep it near the plan's ~200-char ceiling", len(benchQuery))
	}

	// Warm up (regexp internals cache some state on first use) before
	// timing, then take the average over many iterations rather than a
	// single wall-clock sample to avoid scheduler-noise flakiness.
	Classify(benchQuery)

	const iterations = 1000
	start := time.Now()
	for i := 0; i < iterations; i++ {
		Classify(benchQuery)
	}
	elapsed := time.Since(start)
	perCall := elapsed / iterations

	t.Logf("Classify: %d iterations in %v, %v/call", iterations, elapsed, perCall)
	const budget = 1 * time.Millisecond
	if perCall > budget {
		t.Errorf("Classify averaged %v/call over %d iterations; budget is < %v (plan P2: 'Budget < 1 ms, ~1%% of the 100 ms retrieval budget')", perCall, iterations, budget)
	}
}

// BenchmarkClassify is the `go test -bench` companion to the latency-budget
// test above — run with `go test ./internal/intent/... -bench=. -benchtime=1s`
// for ns/op and allocation counts.
func BenchmarkClassify(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		Classify(benchQuery)
	}
}

// BenchmarkClassify_ShortQuery benchmarks a realistic short voice-assistant
// query (the plan's motivating consumer speaks Spanish over voice, so
// queries skew short), as a lower bound alongside BenchmarkClassify's
// near-ceiling-length upper bound.
func BenchmarkClassify_ShortQuery(b *testing.B) {
	b.ReportAllocs()
	const q = "cómo va Workly"
	for i := 0; i < b.N; i++ {
		Classify(q)
	}
}

// BenchmarkClassify_NoMatch benchmarks the worst case for the
// confidence-already-beaten short-circuit in ClassifyWithThreshold: a
// query that matches nothing, so every single rule in signals actually
// runs its regexp.MatchString.
func BenchmarkClassify_NoMatch(b *testing.B) {
	b.ReportAllocs()
	q := strings.Repeat("lorem ipsum dolor sit amet consectetur adipiscing elit ", 3)
	for i := 0; i < b.N; i++ {
		Classify(q)
	}
}
