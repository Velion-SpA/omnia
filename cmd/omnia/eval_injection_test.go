package main

// ─── omnia eval --injection (issue #143) ─────────────────────────────────
//
// RED: an MCP-pipeline-backed fetcher so `omnia eval` can measure the v0.3
// "Context Economy" injection flags (type-lens/MMR/token-budget) instead of
// always scoring against the raw top-1 FTS5 hit. Mirrors
// eval_test.go/main_test.go's fixture/helper conventions.

import (
	"context"
	"strings"
	"testing"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/embed"
	"github.com/velion/omnia/internal/eval"
	"github.com/velion/omnia/internal/mcp"
	"github.com/velion/omnia/internal/recall"
	"github.com/velion/omnia/internal/store"
	"github.com/velion/omnia/internal/token"
)

// relevanceFromRank rebuilds the pre-review-fix relevance map
// (relevance[r.ID] = -r.Rank) that applyInjectionPipeline used to compute
// internally before it took relevance as an explicit parameter (review fix,
// #143 HIGH — see pipelineBackedFetcher's doc comment). The pure-fixture
// tests below are exercising ApplyTypeLens/ApplyMMR/ApplyTokenBudget
// composition, not the retrieval branch, so they keep deriving relevance
// from each fixture row's own Rank field, unchanged from before.
func relevanceFromRank(results []store.SearchResult) map[int64]float64 {
	relevance := make(map[int64]float64, len(results))
	for _, r := range results {
		relevance[r.ID] = -r.Rank
	}
	return relevance
}

// ── applyInjectionPipeline: pure-function tests over hand-built fixtures ──

// TestApplyInjectionPipeline_AllDisabledIsNoOp is the parity floor the three
// composed passes (ApplyTypeLens/ApplyMMR/ApplyTokenBudget) each already
// individually guarantee ("byte-for-byte no-op when off"): this test proves
// the COMPOSITION preserves that contract too, with every sub-gate at its
// zero-value (disabled) default.
func TestApplyInjectionPipeline_AllDisabledIsNoOp(t *testing.T) {
	in := []store.SearchResult{
		{Observation: store.Observation{ID: 1, Content: "alpha bravo charlie"}, Rank: -3},
		{Observation: store.Observation{ID: 2, Content: "delta echo foxtrot"}, Rank: -2},
		{Observation: store.Observation{ID: 3, Content: "golf hotel india"}, Rank: -1},
	}

	out := applyInjectionPipeline("some query", in, relevanceFromRank(in), config.InjectionConfig{})

	if len(out) != len(in) {
		t.Fatalf("len(out) = %d, want %d (untouched)", len(out), len(in))
	}
	for i := range in {
		if out[i].ID != in[i].ID || out[i].Content != in[i].Content {
			t.Errorf("out[%d] = (id=%d content=%q), want (id=%d content=%q)",
				i, out[i].ID, out[i].Content, in[i].ID, in[i].Content)
		}
	}
}

// TestApplyInjectionPipeline_BudgetTrimsToFewerResultsAndLowerTokens is the
// "with budget enabled small -> fewer results + lower token count" scenario.
// MaxTokens is hand-computed (via the SAME token.EstimateTokens primitive
// ApplyTokenBudget itself uses) to exactly fit the first two items and
// exclude the third — TrimToBudget's own documented "top-N-complete,
// boundary inclusive" semantics.
func TestApplyInjectionPipeline_BudgetTrimsToFewerResultsAndLowerTokens(t *testing.T) {
	in := []store.SearchResult{
		{Observation: store.Observation{ID: 1, Content: "the quick brown fox jumps over"}, Rank: -3},
		{Observation: store.Observation{ID: 2, Content: "a lazy dog sleeps in the sun"}, Rank: -2},
		{Observation: store.Observation{ID: 3, Content: "meanwhile a cat watches quietly"}, Rank: -1},
	}

	tok1 := token.EstimateTokens(in[0].Content)
	tok2 := token.EstimateTokens(in[1].Content)
	tokAll := tok1 + tok2 + token.EstimateTokens(in[2].Content)

	cfg := config.InjectionConfig{Budget: config.TokenBudgetConfig{Enabled: true, MaxTokens: tok1 + tok2}}
	out := applyInjectionPipeline("some query", in, relevanceFromRank(in), cfg)

	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2 (third item must be dropped, not partially included)", len(out))
	}
	if out[0].ID != 1 || out[1].ID != 2 {
		t.Fatalf("out IDs = [%d %d], want [1 2] (top-N-complete, in order)", out[0].ID, out[1].ID)
	}

	gotTokens := 0
	for _, r := range out {
		gotTokens += token.EstimateTokens(r.Content)
	}
	if gotTokens != tok1+tok2 {
		t.Errorf("gotTokens = %d, want %d", gotTokens, tok1+tok2)
	}
	if gotTokens >= tokAll {
		t.Errorf("gotTokens = %d, want < tokAll (%d) — budget must lower total token count", gotTokens, tokAll)
	}
}

// TestApplyInjectionPipeline_DiversityDropsNearDuplicate is the "with
// diversity enabled -> near-dup dropped" scenario: item[1] near-duplicates
// item[0] (differs by one word out of eight => Jaccard 0.75), well past a
// 0.5 SimilarityThreshold, so ApplyMMR hard-drops it; the unrelated item[2]
// survives.
func TestApplyInjectionPipeline_DiversityDropsNearDuplicate(t *testing.T) {
	in := []store.SearchResult{
		{Observation: store.Observation{ID: 1, Content: "database connection pool exhausted after timeout error"}, Rank: -3},
		{Observation: store.Observation{ID: 2, Content: "database connection pool exhausted after timeout failure"}, Rank: -2},
		{Observation: store.Observation{ID: 3, Content: "frontend button click animation lag"}, Rank: -1},
	}

	cfg := config.InjectionConfig{Diversity: config.DiversityConfig{Enabled: true, Lambda: 0.5, SimilarityThreshold: 0.5}}
	out := applyInjectionPipeline("some query", in, relevanceFromRank(in), cfg)

	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2 (near-duplicate must be hard-dropped)", len(out))
	}
	for _, r := range out {
		if r.ID == 2 {
			t.Fatalf("out still contains near-duplicate id=2: %+v", out)
		}
	}
	if out[0].ID != 1 {
		t.Errorf("out[0].ID = %d, want 1 (top-ranked row always starts selected)", out[0].ID)
	}
}

// TestApplyInjectionPipeline_ComposedOrderLensThenMMRThenBudget is the
// composed-order regression test (review fix, #143 LOW): no other test in
// this file enables 2+ gates together, so a reordering of
// applyInjectionPipeline's three composed calls (ApplyTypeLens -> ApplyMMR
// -> ApplyTokenBudget) would pass every other test silently. This fixture is
// hand-built so its expected output is correct ONLY under the exact
// documented order:
//
//   - Y (id=1, type=feature) and X (id=2, type=architecture) are mutual
//     near-duplicates (Jaccard ~0.78, past the 0.5 SimilarityThreshold). Z
//     (id=3) and W (id=4) are both unrelated to Y, X, and each other.
//   - The query triggers the "architecture" lens, so ApplyTypeLens lifts X
//     to the front IF it runs first.
//   - ApplyMMR always anchors on rest[0] (whichever row is first when MMR
//     runs) and hard-drops any remaining candidate too similar to an
//     already-selected row. Anchoring on X (lens ran first) permanently
//     drops Y (X's near-dup); anchoring on Y (MMR ran first, lens never got
//     a chance to promote X before Y's mutual-duplicate radius eliminated
//     it) permanently drops X instead — a DIFFERENT surviving set, not just
//     a different order, so a later lens pass over the survivors can never
//     recover the correct-order answer.
//   - ApplyTokenBudget then trims the diversified tail (W) — proving the
//     budget pass genuinely runs LAST, over the already-diversified list.
//
// Inversion evidence (documented here, verified live by temporarily
// swapping the ApplyTypeLens/ApplyMMR call order in applyInjectionPipeline
// and re-running this test — see the fix commit's PR notes / engram
// sdd/omnia-0.3-polish/eval-fetcher for the captured failure output):
// swapping lens and MMR changes the final IDs from [2 3] (X, Z) to [1 3]
// (Y, Z) — asserted explicitly below as the "wrong order would differ"
// evidence, not just implied by the correct-order assertion.
func TestApplyInjectionPipeline_ComposedOrderLensThenMMRThenBudget(t *testing.T) {
	in := []store.SearchResult{
		{Observation: store.Observation{ID: 1, Type: "feature", Content: "payment gateway retry backoff logic for failed transactions"}},
		{Observation: store.Observation{ID: 2, Type: "architecture", Content: "payment gateway retry backoff logic for failed charges"}},
		{Observation: store.Observation{ID: 3, Type: "feature", Content: "onboarding checklist for new engineering hires"}},
		{Observation: store.Observation{ID: 4, Type: "feature", Content: "database index rebuild schedule for nightly maintenance"}},
	}
	relevance := map[int64]float64{1: 0.9, 2: 0.85, 3: 0.7, 4: 0.5}
	cfg := config.InjectionConfig{
		TypeLens:  config.TypeLensConfig{Enabled: true},
		Diversity: config.DiversityConfig{Enabled: true, Lambda: 0.5, SimilarityThreshold: 0.5},
		Budget:    config.TokenBudgetConfig{Enabled: true, MaxTokens: 30},
	}

	out := applyInjectionPipeline("explain the design approach", in, relevance, cfg)

	wantIDs := []int64{2, 3}
	if len(out) != len(wantIDs) {
		t.Fatalf("len(out) = %d, want %d; out=%+v", len(out), len(wantIDs), out)
	}
	for i, want := range wantIDs {
		if out[i].ID != want {
			t.Fatalf("out IDs = %v, want %v (lens must run BEFORE MMR so MMR anchors on the lens-promoted architecture row, keeping id=2 and permanently dropping its near-duplicate id=1; budget then trims id=4 off the diversified tail — swapping the lens/MMR call order changes this to [1 3], see doc comment above)",
				idsOf(out), wantIDs)
		}
	}
}

func idsOf(results []store.SearchResult) []int64 {
	ids := make([]int64, 0, len(results))
	for _, r := range results {
		ids = append(ids, r.ID)
	}
	return ids
}

// ── pipelineBackedFetcher: real-store integration tests ──────────────────

// TestPipelineBackedFetcher_ParityWhenFlagsOff is the "fetcher with flags
// OFF == storeBackedFetcher results identical (parity)" scenario: the
// scoring-relevant fields (Retrieved, SurfacedObservationID — what actually
// feeds Score()/ScoreContradiction()) must match storeBackedFetcher exactly
// when every injection sub-gate is disabled. Tokens is NOT asserted equal
// here — pipelineBackedFetcher's Tokens.InjectedContext is a deliberately
// more accurate accounting than storeBackedFetcher's single-hit
// Tokens.Retrieval heuristic (see pipelineBackedFetcher's own doc comment),
// not a bug.
func TestPipelineBackedFetcher_ParityWhenFlagsOff(t *testing.T) {
	cfg := testConfig(t)
	mustSeedObservation(t, cfg, "s1", "eval-injection-parity", "architecture",
		"Ollama embedding layer", "internal/embed: Ollama HTTP client with unit-normalized vectors", "project")

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	c := eval.EvalCase{ID: "case-1", Query: "Ollama embedding layer", ExpectedFact: "unit-normalized vectors"}

	oldCase, err := storeBackedFetcher(s)(context.Background(), c)
	if err != nil {
		t.Fatalf("storeBackedFetcher: %v", err)
	}
	newCase, err := pipelineBackedFetcher(s, nil, config.InjectionConfig{})(context.Background(), c)
	if err != nil {
		t.Fatalf("pipelineBackedFetcher: %v", err)
	}

	if oldCase.Retrieved == "" {
		t.Fatal("test setup invalid: storeBackedFetcher found nothing")
	}
	if newCase.Retrieved != oldCase.Retrieved {
		t.Errorf("Retrieved = %q, want %q (parity when flags off)", newCase.Retrieved, oldCase.Retrieved)
	}
	if newCase.SurfacedObservationID != oldCase.SurfacedObservationID {
		t.Errorf("SurfacedObservationID = %q, want %q (parity when flags off)", newCase.SurfacedObservationID, oldCase.SurfacedObservationID)
	}

	// Sanity: both fetchers actually did SOME token accounting — just not
	// necessarily the same number (see doc comment above).
	if oldCase.Tokens.Total() == 0 {
		t.Error("storeBackedFetcher: expected non-zero Tokens")
	}
	if newCase.Tokens.Total() == 0 {
		t.Error("pipelineBackedFetcher: expected non-zero Tokens")
	}
}

// TestPipelineBackedFetcher_TokenAccountingMatchesPreviewBasis is the "token
// accounting matches the preview basis (hand-computed fixture)" scenario:
// with injection disabled (pipeline is a no-op, exactly the single seeded
// hit survives), Tokens.InjectedContext must equal
// token.EstimateTokens(truncate(content, 300)) computed independently here —
// the SAME basis handleSearch's own display loop and ApplyTokenBudget's own
// previewTokens use. Content is deliberately > 300 runes so truncate's own
// "..." suffix is exercised too, not just the identity branch.
func TestPipelineBackedFetcher_TokenAccountingMatchesPreviewBasis(t *testing.T) {
	cfg := testConfig(t)
	longContent := strings.Repeat("word ", 100) // 500 runes, well past the 300-rune preview cap
	mustSeedObservation(t, cfg, "s1", "eval-injection-basis", "architecture",
		"long content case", longContent, "project")

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	c := eval.EvalCase{ID: "case-1", Query: "long content case", ExpectedFact: "word"}

	got, err := pipelineBackedFetcher(s, nil, config.InjectionConfig{})(context.Background(), c)
	if err != nil {
		t.Fatalf("pipelineBackedFetcher: %v", err)
	}
	if got.Retrieved == "" {
		t.Fatal("test setup invalid: pipelineBackedFetcher found nothing")
	}

	want := token.EstimateTokens(truncate(got.Retrieved, injectionPreviewChars))
	if got.Tokens.InjectedContext != want {
		t.Errorf("Tokens.InjectedContext = %d, want %d (preview-truncated basis)", got.Tokens.InjectedContext, want)
	}
	// Also guard against silently counting the FULL (untruncated) content —
	// that would be a materially larger, wrong number for this fixture.
	if full := token.EstimateTokens(got.Retrieved); got.Tokens.InjectedContext >= full {
		t.Errorf("Tokens.InjectedContext = %d, must be < full-content estimate %d (must use the 300-char preview, not the full content)", got.Tokens.InjectedContext, full)
	}
}

// TestPipelineBackedFetcher_UsesRecallServiceWhenConfigured is the
// RED->GREEN test for the review's HIGH finding: pipelineBackedFetcher must
// branch on hybrid recall exactly like handleSearch does
// (internal/mcp/mcp.go's handleSearch, cfg.Recall != nil branch) instead of
// always retrieving via plain store.Search (FTS5-only). The seeded
// observation has ZERO lexical (word) overlap with the query — including in
// its title, which the FTS5 index also covers — so storeSearch alone can
// NEVER find it, but it IS the target of a fake semantic hit wired into a
// real recall.Service (mirrors internal/mcp/recall_wiring_test.go's
// TestHandleSearch_RecallEnabled_SurfacesSemanticOnlyParaphrase and
// cmd/omnia/recall_test.go's fakeCLIEmbedSearcher). A pipelineBackedFetcher
// that still always calls storeSearch regardless of recallSvc would return
// an empty RetrievedCase in both sub-cases below; only a fetcher that
// actually routes through recallSvc.Search + HydrateFusedResults surfaces
// it when recallSvc is configured.
func TestPipelineBackedFetcher_UsesRecallServiceWhenConfigured(t *testing.T) {
	cfg := testConfig(t)
	const query = "database connection pool exhausted"
	paraphraseID := mustSeedObservation(t, cfg, "s1", "eval-injection-recall", "bugfix",
		"Session traffic spike", "Users get disconnected when the load balancer times out", "project")

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	c := eval.EvalCase{ID: "case-1", Query: query, ExpectedFact: "disconnected"}

	// Sanity baseline: plain FTS5 search (recallSvc == nil) must find
	// NOTHING for this query — proving the fixture has zero lexical overlap
	// and that any hit below can only come from the recall branch.
	noRecall, err := pipelineBackedFetcher(s, nil, config.InjectionConfig{})(context.Background(), c)
	if err != nil {
		t.Fatalf("pipelineBackedFetcher(nil recall): %v", err)
	}
	if noRecall.Retrieved != "" {
		t.Fatalf("test setup invalid: FTS5-only fetcher unexpectedly found %q — the fixture must have zero lexical overlap with the query", noRecall.Retrieved)
	}

	semantic := fakeCLIEmbedSearcher{hits: []embed.Hit{{ObsID: int(paraphraseID), Score: 0.9}}}
	recallSvc := recall.NewService(mcp.NewStoreLexicalSearcher(s), semantic, recall.DefaultFuseParams())

	got, err := pipelineBackedFetcher(s, recallSvc, config.InjectionConfig{})(context.Background(), c)
	if err != nil {
		t.Fatalf("pipelineBackedFetcher(recall configured): %v", err)
	}
	if got.Retrieved == "" {
		t.Fatal("pipelineBackedFetcher with a configured recall.Service found nothing — it must route through recallSvc.Search + HydrateFusedResults, not plain storeSearch")
	}
	if got.Retrieved != "Users get disconnected when the load balancer times out" {
		t.Errorf("Retrieved = %q, want the semantic-only paraphrase content", got.Retrieved)
	}
}

// ── CLI plumbing ──────────────────────────────────────────────────────────

// TestCmdEval_InjectionFlagPlumbing is the "--injection flag plumbing"
// scenario: passing --injection must set evalRunOptions.Injection = true on
// the call to runEvalHarness.
func TestCmdEval_InjectionFlagPlumbing(t *testing.T) {
	oldRun, oldExit := runEvalHarness, exitFunc
	t.Cleanup(func() { runEvalHarness, exitFunc = oldRun, oldExit })

	var capturedInjection bool
	var called bool
	runEvalHarness = func(ctx context.Context, opts evalRunOptions) (eval.RunSummary, error) {
		called = true
		capturedInjection = opts.Injection
		return fakeRunSummary(t, 1.0), nil
	}
	exitFunc = func(code int) {}

	cmdEval([]string{"--injection"})

	if !called {
		t.Fatal("expected runEvalHarness to be called")
	}
	if !capturedInjection {
		t.Error("expected --injection to set evalRunOptions.Injection = true")
	}
}

// TestCmdEval_InjectionFlagDefaultsFalse guards the default (no --injection)
// path: current behavior must stay byte-for-byte unchanged.
func TestCmdEval_InjectionFlagDefaultsFalse(t *testing.T) {
	oldRun, oldExit := runEvalHarness, exitFunc
	t.Cleanup(func() { runEvalHarness, exitFunc = oldRun, oldExit })

	capturedInjection := true // seeded true so a wiring bug that never sets it false still fails
	runEvalHarness = func(ctx context.Context, opts evalRunOptions) (eval.RunSummary, error) {
		capturedInjection = opts.Injection
		return fakeRunSummary(t, 1.0), nil
	}
	exitFunc = func(code int) {}

	cmdEval(nil)

	if capturedInjection {
		t.Error("expected --injection to default to false")
	}
}
