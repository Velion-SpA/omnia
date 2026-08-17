package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/eval"
	"github.com/velion/omnia/internal/store"
)

// fakeRunSummary builds a minimal eval.RunSummary whose overall accuracy is
// exactly accuracy, backed by eval.MinRuns runs — enough to satisfy
// eval.EvaluateGate's own reproducibility floor without touching a real
// store, corpus file, or LLM CLI.
func fakeRunSummary(t *testing.T, accuracy float64) eval.RunSummary {
	t.Helper()
	hit := accuracy >= 1.0
	cfg := eval.Config{Run: func(ctx context.Context) (eval.Report, error) {
		results := []eval.CaseResult{
			{Case: eval.EvalCase{Capability: eval.CapabilityRecall, Language: eval.LanguageEN}, Hit: hit, TotalTokens: 100},
		}
		return eval.BuildReport(results), nil
	}}
	summary, err := eval.RunHarness(context.Background(), cfg, eval.MinRuns)
	if err != nil {
		t.Fatalf("eval.RunHarness: %v", err)
	}
	return summary
}

// TestCmdEval_AdvisoryNeverBlocks is spec EVAL-8's "Advisory mode never
// blocks release" scenario, exercised through the CLI entry point: even a
// large regression must never call exitFunc(1) in advisory mode (the
// default).
func TestCmdEval_AdvisoryNeverBlocks(t *testing.T) {
	oldRun, oldGate, oldExit := runEvalHarness, evaluateGate, exitFunc
	t.Cleanup(func() { runEvalHarness, evaluateGate, exitFunc = oldRun, oldGate, oldExit })

	runEvalHarness = func(ctx context.Context, opts evalRunOptions) (eval.RunSummary, error) {
		return fakeRunSummary(t, 0.0), nil // regressed overall accuracy
	}
	evaluateGate = eval.EvaluateGate

	var exitCode int
	var exited bool
	exitFunc = func(code int) { exitCode = code; exited = true }

	cmdEval([]string{"--mode", "advisory", "--baseline", "0.9", "--threshold", "0.05"})

	if exited {
		t.Errorf("advisory mode must never call exitFunc, got exitFunc(%d)", exitCode)
	}
}

// TestCmdEval_BlockingExitsNonZeroPastThreshold is spec EVAL-8's "Blocking
// mode fails the release step" scenario, exercised through the CLI: a
// regression past --threshold in --mode blocking must exitFunc(1).
func TestCmdEval_BlockingExitsNonZeroPastThreshold(t *testing.T) {
	oldRun, oldGate, oldExit := runEvalHarness, evaluateGate, exitFunc
	t.Cleanup(func() { runEvalHarness, evaluateGate, exitFunc = oldRun, oldGate, oldExit })

	runEvalHarness = func(ctx context.Context, opts evalRunOptions) (eval.RunSummary, error) {
		return fakeRunSummary(t, 0.0), nil // regressed overall accuracy
	}
	evaluateGate = eval.EvaluateGate

	var exitCode int
	var exited bool
	exitFunc = func(code int) { exitCode = code; exited = true }

	cmdEval([]string{"--mode", "blocking", "--baseline", "0.9", "--threshold", "0.05"})

	if !exited || exitCode != 1 {
		t.Errorf("blocking mode past threshold must exitFunc(1), got exited=%v code=%d", exited, exitCode)
	}
}

// TestCmdEval_BlockingWithinThresholdDoesNotExit ensures blocking mode only
// exits non-zero PAST the threshold, not on any harness run at all.
func TestCmdEval_BlockingWithinThresholdDoesNotExit(t *testing.T) {
	oldRun, oldGate, oldExit := runEvalHarness, evaluateGate, exitFunc
	t.Cleanup(func() { runEvalHarness, evaluateGate, exitFunc = oldRun, oldGate, oldExit })

	runEvalHarness = func(ctx context.Context, opts evalRunOptions) (eval.RunSummary, error) {
		return fakeRunSummary(t, 1.0), nil // no regression
	}
	evaluateGate = eval.EvaluateGate

	var exited bool
	exitFunc = func(code int) { exited = true }

	cmdEval([]string{"--mode", "blocking", "--baseline", "0.9", "--threshold", "0.05"})

	if exited {
		t.Error("blocking mode with no regression must not exit non-zero")
	}
}

// TestCmdEval_NoBaselineSkipsGate ensures the gate decision is entirely
// skipped (and never blocks) when --baseline is left at its default (0).
func TestCmdEval_NoBaselineSkipsGate(t *testing.T) {
	oldRun, oldGate, oldExit := runEvalHarness, evaluateGate, exitFunc
	t.Cleanup(func() { runEvalHarness, evaluateGate, exitFunc = oldRun, oldGate, oldExit })

	gateCalled := false
	runEvalHarness = func(ctx context.Context, opts evalRunOptions) (eval.RunSummary, error) {
		return fakeRunSummary(t, 0.0), nil
	}
	evaluateGate = func(summary eval.RunSummary, mode eval.GateMode, baselineAccuracy, threshold float64) (eval.GateResult, error) {
		gateCalled = true
		return eval.GateResult{}, nil
	}

	var exited bool
	exitFunc = func(code int) { exited = true }

	cmdEval([]string{"--mode", "blocking"}) // no --baseline

	if gateCalled {
		t.Error("expected evaluateGate to be skipped when --baseline is not supplied")
	}
	if exited {
		t.Error("expected no exit when the gate decision is skipped")
	}
}

// TestCmdEval_InvalidModeExitsNonZero guards the --mode flag's contract.
func TestCmdEval_InvalidModeExitsNonZero(t *testing.T) {
	oldExit := exitFunc
	t.Cleanup(func() { exitFunc = oldExit })

	var exitCode int
	var exited bool
	exitFunc = func(code int) { exitCode = code; exited = true }

	cmdEval([]string{"--mode", "yolo"})

	if !exited || exitCode != 1 {
		t.Errorf("expected exitFunc(1) for an invalid --mode, got exited=%v code=%d", exited, exitCode)
	}
}

// TestCmdEval_HarnessErrorExitsNonZero ensures a harness-wiring failure
// (e.g. corpus below spec EVAL-2's floor) surfaces as a non-zero exit rather
// than silently printing an empty report.
func TestCmdEval_HarnessErrorExitsNonZero(t *testing.T) {
	oldRun, oldExit := runEvalHarness, exitFunc
	t.Cleanup(func() { runEvalHarness, exitFunc = oldRun, oldExit })

	runEvalHarness = func(ctx context.Context, opts evalRunOptions) (eval.RunSummary, error) {
		return eval.RunSummary{}, errBoomEval
	}

	var exited bool
	var exitCode int
	exitFunc = func(code int) { exited = true; exitCode = code }

	cmdEval(nil)

	if !exited || exitCode != 1 {
		t.Errorf("expected exitFunc(1) on a harness error, got exited=%v code=%d", exited, exitCode)
	}
}

var errBoomEval = &evalTestError{"boom"}

type evalTestError struct{ msg string }

func (e *evalTestError) Error() string { return e.msg }

// TestLoadEvalCorpus_EmptyPathUsesEmbedded is finding #1's core unit test:
// an empty --corpus path (the flag's default, evalCorpusPathFlagDefault)
// must resolve via the binary-embedded corpus, not a cwd-relative
// filesystem path. It must never fail with a "file not found"-style error
// — the ONLY acceptable failure is a *eval.CorpusSizeError, if the
// embedded starter corpus is still below spec EVAL-2's 50-case floor (a
// separate, already-known, out-of-scope gap; see internal/eval's own
// TestEmbeddedCorpus_WorksFromAnyCWD for the direct cwd-independence
// proof).
func TestLoadEvalCorpus_EmptyPathUsesEmbedded(t *testing.T) {
	cases, err := loadEvalCorpus("")
	if err != nil {
		var sizeErr *eval.CorpusSizeError
		if !errors.As(err, &sizeErr) {
			t.Fatalf("loadEvalCorpus(\"\") failed with a non-size error (embedded corpus not wired?): %v", err)
		}
		return
	}
	if len(cases) == 0 {
		t.Fatal("loadEvalCorpus(\"\") returned zero cases with no error")
	}
}

// TestLoadEvalCorpus_ExplicitPathStillLoadsFromDisk is the backward-
// compatibility half of finding #1's fix: an explicit --corpus PATH must
// still load from THAT file (preserving the existing ability to point at
// a custom/larger corpus), not silently fall back to the embedded one.
func TestLoadEvalCorpus_ExplicitPathStillLoadsFromDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom_cases.json")
	if err := os.WriteFile(path, []byte(`[]`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadEvalCorpus(path)
	if err == nil {
		t.Fatal("expected an error for an empty custom corpus file")
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("expected the error to reference the explicit custom path %q (proving it loaded from disk, not the embedded corpus), got: %v", path, err)
	}
}

// ─── --profile / --target / --http-base-url (docs/conversational-retrieval-plan.md
// "Baseline first") ───────────────────────────────────────────────────────
//
// cmd/omnia/eval.go gained these three flags plus three fetchers
// (conversationalStoreFetcher, conversationalPipelineFetcher,
// conversationalHTTPFetcher) verified only by build + manual smoke test
// until now. Everything below covers: (1) flag parsing reaches
// conversationalRunOptions unchanged, (2) --profile/--target validation
// exits/errors the way --mode's existing tests already prove for the coding
// profile, and (3) each fetcher's own request/response contract in
// isolation — mirroring eval_injection_test.go's "pure-function fixture,
// then real-store integration, then CLI plumbing" layering for the coding
// profile's --injection flag.

// TestCmdEval_InvalidProfileExitsNonZero guards the --profile flag's
// contract, mirroring TestCmdEval_InvalidModeExitsNonZero for --mode.
func TestCmdEval_InvalidProfileExitsNonZero(t *testing.T) {
	oldExit := exitFunc
	t.Cleanup(func() { exitFunc = oldExit })

	var exitCode int
	var exited bool
	exitFunc = func(code int) { exitCode = code; exited = true }

	cmdEval([]string{"--profile", "bogus"})

	if !exited || exitCode != 1 {
		t.Errorf("expected exitFunc(1) for an invalid --profile, got exited=%v code=%d", exited, exitCode)
	}
}

// TestCmdEval_ConversationalProfile_PassesFlagsToRunner is the "flag
// parsing reaches the runner unchanged" scenario, mirroring
// TestCmdEval_InjectionFlagPlumbing for the coding profile's --injection.
func TestCmdEval_ConversationalProfile_PassesFlagsToRunner(t *testing.T) {
	oldRun := runConversationalEval
	t.Cleanup(func() { runConversationalEval = oldRun })

	var called bool
	var gotOpts conversationalRunOptions
	runConversationalEval = func(ctx context.Context, opts conversationalRunOptions) (eval.ConversationalRunSummary, error) {
		called = true
		gotOpts = opts
		return eval.ConversationalRunSummary{}, nil
	}

	cmdEval([]string{
		"--profile", "conversational",
		"--target", "http",
		"--http-base-url", "http://localhost:7799",
		"--corpus", "/tmp/custom_conversational_cases.json",
		"--config", "/tmp/custom_config.yaml",
		"--runs", "3",
		"--injection",
	})

	if !called {
		t.Fatal("expected --profile conversational to call runConversationalEval")
	}
	want := conversationalRunOptions{
		CorpusPath:  "/tmp/custom_conversational_cases.json",
		ConfigPath:  "/tmp/custom_config.yaml",
		Runs:        3,
		Target:      "http",
		HTTPBaseURL: "http://localhost:7799",
		Injection:   true,
	}
	if gotOpts != want {
		t.Errorf("runConversationalEval opts = %+v, want %+v", gotOpts, want)
	}
}

// TestCmdEval_ConversationalProfile_DefaultsTargetInprocessNoInjection
// guards the flag defaults (no --target/--injection supplied): current
// documented defaults (target=inprocess, injection=false) must reach the
// runner, mirroring TestCmdEval_InjectionFlagDefaultsFalse.
func TestCmdEval_ConversationalProfile_DefaultsTargetInprocessNoInjection(t *testing.T) {
	oldRun := runConversationalEval
	t.Cleanup(func() { runConversationalEval = oldRun })

	gotOpts := conversationalRunOptions{Target: "seeded-nonempty", Injection: true} // seeded non-zero so a wiring bug that never overwrites still fails
	runConversationalEval = func(ctx context.Context, opts conversationalRunOptions) (eval.ConversationalRunSummary, error) {
		gotOpts = opts
		return eval.ConversationalRunSummary{}, nil
	}

	cmdEval([]string{"--profile", "conversational"})

	if gotOpts.Target != "inprocess" {
		t.Errorf("Target = %q, want the documented default %q", gotOpts.Target, "inprocess")
	}
	if gotOpts.Injection {
		t.Error("expected --injection to default to false")
	}
}

// TestCmdEval_ConversationalProfile_RunnerErrorExitsNonZero mirrors
// TestCmdEval_HarnessErrorExitsNonZero for the conversational profile: a
// runner-wiring failure (e.g. --target=http with no --http-base-url) must
// surface as a non-zero exit, not a silently empty report.
func TestCmdEval_ConversationalProfile_RunnerErrorExitsNonZero(t *testing.T) {
	oldRun, oldExit := runConversationalEval, exitFunc
	t.Cleanup(func() { runConversationalEval, exitFunc = oldRun, oldExit })

	runConversationalEval = func(ctx context.Context, opts conversationalRunOptions) (eval.ConversationalRunSummary, error) {
		return eval.ConversationalRunSummary{}, errBoomEval
	}
	var exited bool
	var exitCode int
	exitFunc = func(code int) { exited = true; exitCode = code }

	cmdEval([]string{"--profile", "conversational"})

	if !exited || exitCode != 1 {
		t.Errorf("expected exitFunc(1) on a conversational runner error, got exited=%v code=%d", exited, exitCode)
	}
}

// TestDefaultRunConversationalEval_TargetHTTPRequiresBaseURL proves
// requirement 5's http target refuses to run against an empty base URL
// instead of silently building a malformed request.
func TestDefaultRunConversationalEval_TargetHTTPRequiresBaseURL(t *testing.T) {
	_, err := defaultRunConversationalEval(context.Background(), conversationalRunOptions{
		Target:      "http",
		HTTPBaseURL: "",
	})
	if err == nil {
		t.Fatal("expected an error when --target=http is used without --http-base-url")
	}
	if !strings.Contains(err.Error(), "http-base-url") {
		t.Errorf("expected the error to mention --http-base-url, got: %v", err)
	}
}

// TestDefaultRunConversationalEval_InvalidTargetErrors guards --target's
// enum contract: anything other than inprocess/http/"" must error before
// touching a store or an HTTP client.
func TestDefaultRunConversationalEval_InvalidTargetErrors(t *testing.T) {
	_, err := defaultRunConversationalEval(context.Background(), conversationalRunOptions{
		Target: "bogus",
	})
	if err == nil {
		t.Fatal("expected an error for an invalid --target")
	}
	if !strings.Contains(err.Error(), "target") {
		t.Errorf("expected the error to mention --target, got: %v", err)
	}
}

// TestDefaultRunConversationalEval_AsOfIncompatibleWithInjection proves the
// engram eval/http-search-as-of-isolation guard: --as-of + --injection must
// fail loudly before anything is measured, because the injection pipeline's
// recall.Service leg has no historical embeddings index — silently ignoring
// --as-of there would let a caller believe isolation held when it didn't.
func TestDefaultRunConversationalEval_AsOfIncompatibleWithInjection(t *testing.T) {
	_, err := defaultRunConversationalEval(context.Background(), conversationalRunOptions{
		AsOf:      "2026-08-14T03:00:00Z",
		Injection: true,
	})
	if err == nil {
		t.Fatal("expected an error when --as-of is combined with --injection")
	}
	if !strings.Contains(err.Error(), "as-of") || !strings.Contains(err.Error(), "injection") {
		t.Errorf("expected the error to name both --as-of and --injection, got: %v", err)
	}
}

// TestDefaultRunConversationalEval_AsOfNotSupportedWithAnswerTarget proves
// the same guard for --target=answer: GET /answer has no as_of seam (only
// GET /search and --target inprocess do), so this must fail loudly instead
// of silently running --target=answer live.
func TestDefaultRunConversationalEval_AsOfNotSupportedWithAnswerTarget(t *testing.T) {
	_, err := defaultRunConversationalEval(context.Background(), conversationalRunOptions{
		AsOf:        "2026-08-14T03:00:00Z",
		Target:      "answer",
		HTTPBaseURL: "http://localhost:0",
	})
	if err == nil {
		t.Fatal("expected an error when --as-of is combined with --target=answer")
	}
	if !strings.Contains(err.Error(), "as-of") || !strings.Contains(err.Error(), "answer") {
		t.Errorf("expected the error to name both --as-of and --target=answer, got: %v", err)
	}
}

// TestDefaultRunConversationalEval_AsOfFailsLoudlyWhenTimeTravelDisabled
// proves the --target=inprocess isolation guard: a store opened with
// time_travel.enabled=false would make store.SearchAsOf silently degrade to
// a live search (its own documented behavior), so defaultRunConversationalEval
// must refuse before building the fetcher at all, rather than letting a
// contaminated live store masquerade as an isolated one.
func TestDefaultRunConversationalEval_AsOfFailsLoudlyWhenTimeTravelDisabled(t *testing.T) {
	dataDir := t.TempDir()
	origStoreNew := storeNew
	storeNew = func(cfg store.Config) (*store.Store, error) {
		cfg.DataDir = dataDir
		cfg.TimeTravelEnabled = false
		return store.New(cfg)
	}
	defer func() { storeNew = origStoreNew }()

	_, err := defaultRunConversationalEval(context.Background(), conversationalRunOptions{
		AsOf:       "2026-08-14T03:00:00Z",
		Target:     "inprocess",
		ConfigPath: "/tmp/eval-as-of-guard-nonexistent-config.yaml",
	})
	if err == nil {
		t.Fatal("expected an error when --as-of is requested against a store with time_travel disabled")
	}
	if !strings.Contains(err.Error(), "time_travel is not enabled") {
		t.Errorf("expected the error to name the disabled time_travel guard, got: %v", err)
	}
}

// ── conversationalHTTPFetcher: real HTTP round-trip against httptest ──────

// TestConversationalHTTPFetcher_DecodesResults proves the fetcher builds
// GET /search?q=<query>&limit=<rankCandidateDepth> and decodes the bare
// []store.SearchResult array (the exact shape GET /search returns when no
// ?envelope=1 is requested — see internal/server's handleSearch), taking
// the top hit's Content/SyncID and the full ranked SyncID order.
func TestConversationalHTTPFetcher_DecodesResults(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		results := []store.SearchResult{
			{Observation: store.Observation{ID: 1, SyncID: "obs-abc", Content: "Omnia is a memory system"}},
			{Observation: store.Observation{ID: 2, SyncID: "obs-def", Content: "second hit"}},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(results); err != nil {
			t.Fatalf("encode fake response: %v", err)
		}
	}))
	defer srv.Close()

	fetch := conversationalHTTPFetcher(srv.Client(), srv.URL, false, "")
	got, err := fetch(context.Background(), eval.ConversationalCase{Query: "what is omnia"})
	if err != nil {
		t.Fatalf("conversationalHTTPFetcher: %v", err)
	}

	if gotPath != "/search" {
		t.Errorf("request path = %q, want %q", gotPath, "/search")
	}
	if !strings.Contains(gotQuery, "q=what") {
		t.Errorf("expected the query string to carry the case's Query (q=...), got %q", gotQuery)
	}
	if !strings.Contains(gotQuery, "limit=") {
		t.Errorf("expected a limit= query param bounding the candidate pool, got %q", gotQuery)
	}

	// Retrieved must be the assembled top-N context (bugfix: grounding used
	// to be scored against only the top hit's content, under-reporting
	// whenever the fact-bearing chunk ranked 2nd-4th — see
	// conversationalGroundingContextSize's doc comment). Both fixture
	// results fit within conversationalGroundingContextSize (4), so both
	// are joined.
	wantRetrieved := "Omnia is a memory system" + conversationalGroundingContextSeparator + "second hit"
	if got.Retrieved != wantRetrieved {
		t.Errorf("Retrieved = %q, want %q (assembled top-N context, not just the top hit)", got.Retrieved, wantRetrieved)
	}
	if got.SurfacedObservationID != "obs-abc" {
		t.Errorf("SurfacedObservationID = %q, want the top hit's sync_id", got.SurfacedObservationID)
	}
	if want := []string{"obs-abc", "obs-def"}; len(got.RankedObservationIDs) != len(want) || got.RankedObservationIDs[0] != want[0] || got.RankedObservationIDs[1] != want[1] {
		t.Errorf("RankedObservationIDs = %v, want %v (order preserved)", got.RankedObservationIDs, want)
	}
	if got.Tokens.Total() == 0 {
		t.Error("expected non-zero token accounting for a non-empty top hit")
	}
}

// TestAssembleGroundingContext is the table-driven unit test for the
// grounding-fix primitive itself: it must join up to
// conversationalGroundingContextSize results' Content, in order, dropping
// anything past that cutoff, and degrade to "" for zero results (preserving
// eval.ScoreAbsence's "nothing above the floor" check).
func TestAssembleGroundingContext(t *testing.T) {
	sep := conversationalGroundingContextSeparator
	mk := func(contents ...string) []store.SearchResult {
		out := make([]store.SearchResult, len(contents))
		for i, c := range contents {
			out[i] = store.SearchResult{Observation: store.Observation{Content: c}}
		}
		return out
	}

	tests := map[string]struct {
		results []store.SearchResult
		want    string
	}{
		"no results": {
			results: nil,
			want:    "",
		},
		"fewer than N": {
			results: mk("a", "b"),
			want:    "a" + sep + "b",
		},
		"exactly N": {
			results: mk("a", "b", "c", "d"),
			want:    "a" + sep + "b" + sep + "c" + sep + "d",
		},
		"more than N, extras dropped": {
			results: mk("a", "b", "c", "d", "e (rank 5, must not appear)"),
			want:    "a" + sep + "b" + sep + "c" + sep + "d",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := assembleGroundingContext(tc.results)
			if got != tc.want {
				t.Errorf("assembleGroundingContext = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestConversationalGrounding_CatchesFactAtRank2Through4 is the regression
// test for the bug this change fixes (docs/conversational-retrieval-plan.md,
// bug 2): grounding used to be scored against ONLY results[0].Content, so an
// expected fact ranking 2nd-4th — a chunk a real consumer like Hermes would
// still receive, since it assembles the top 4 — was scored as "not
// grounded". This reproduces exactly that shape: the top hit does NOT
// contain the expected fact, a lower-ranked hit does, and grounding must
// still come back true once scored over the assembled top-N context.
func TestConversationalGrounding_CatchesFactAtRank2Through4(t *testing.T) {
	results := []store.SearchResult{
		{Observation: store.Observation{SyncID: "obs-1", Content: "top hit — installation instructions, no identity fact here"}},
		{Observation: store.Observation{SyncID: "obs-2", Content: "Persistent memory for AI coding agents — the actual identity fact"}},
		{Observation: store.Observation{SyncID: "obs-3", Content: "unrelated third hit"}},
	}
	c := eval.ConversationalCase{
		ID:           "identity-1",
		Kind:         eval.KindIdentity,
		ExpectedFact: "Persistent memory for AI coding agents",
	}

	// Sanity check: the fact must NOT be in the top-1 result, or this test
	// would not actually exercise the fix (the old top-1-only code would
	// have passed too).
	if strings.Contains(strings.ToLower(results[0].Content), strings.ToLower(c.ExpectedFact)) {
		t.Fatal("test setup invalid: expected fact must not be in the top-1 result")
	}

	rc := eval.RetrievedCase{
		Retrieved:            assembleGroundingContext(results),
		RankedObservationIDs: rankedSyncIDs(results),
	}
	result, err := eval.ScoreConversationalCase(c, rc)
	if err != nil {
		t.Fatalf("ScoreConversationalCase: %v", err)
	}
	if result.Grounded == nil || !*result.Grounded {
		t.Errorf("Grounded = %v, want true (fact present at rank 2 of the assembled top-%d context)", result.Grounded, conversationalGroundingContextSize)
	}
}

// TestConversationalHTTPFetcher_EmptyResultsNoError is the "server found
// nothing" scenario: an empty results array is a genuine outcome (a miss),
// never an error.
func TestConversationalHTTPFetcher_EmptyResultsNoError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	fetch := conversationalHTTPFetcher(srv.Client(), srv.URL, false, "")
	got, err := fetch(context.Background(), eval.ConversationalCase{Query: "nothing matches this"})
	if err != nil {
		t.Fatalf("conversationalHTTPFetcher: %v", err)
	}
	if got.Retrieved != "" || got.SurfacedObservationID != "" || len(got.RankedObservationIDs) != 0 {
		t.Errorf("expected a zero-value RetrievedCase for empty results, got %+v", got)
	}
}

// TestConversationalHTTPFetcher_NonOKStatusReturnsError proves a non-200
// response surfaces as a real per-case error instead of being silently
// treated as "no results".
func TestConversationalHTTPFetcher_NonOKStatusReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	fetch := conversationalHTTPFetcher(srv.Client(), srv.URL, false, "")
	_, err := fetch(context.Background(), eval.ConversationalCase{Query: "x"})
	if err == nil {
		t.Fatal("expected an error for a non-200 GET /search response")
	}
}

// ── engram #2623 regression: conversationalHTTPFetcher/conversationalAnswerFetcher
// must actually scope by project ──────────────────────────────────────────

// TestConversationalHTTPFetcher_ScopesToProject is the regression test for
// engram #2623: before this fix, neither conversationalHTTPFetcher nor
// conversationalAnswerFetcher ever sent a project param, so every
// conversational number was silently searching all ~49 projects at once. A
// scoped case (Unscoped=false) must send project=<c.Project> and must NOT
// send all_projects.
func TestConversationalHTTPFetcher_ScopesToProject(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	fetch := conversationalHTTPFetcher(srv.Client(), srv.URL, false, "")
	if _, err := fetch(context.Background(), eval.ConversationalCase{Query: "q", Project: "omnia"}); err != nil {
		t.Fatalf("conversationalHTTPFetcher: %v", err)
	}

	q, err := url.ParseQuery(gotQuery)
	if err != nil {
		t.Fatalf("parse request query %q: %v", gotQuery, err)
	}
	if q.Get("project") != "omnia" {
		t.Errorf("project = %q, want %q — a scoped case must send project=", q.Get("project"), "omnia")
	}
	if q.Has("all_projects") {
		t.Errorf("all_projects was set on a scoped case: %q", gotQuery)
	}
}

// TestConversationalHTTPFetcher_UnscopedSendsAllProjects is the flip side:
// a case that declares Unscoped=true (the corpus's cross_project kind) must
// send all_projects=1 and must NOT send project=, so it keeps searching
// every project — the entire point of that kind (see
// ConversationalCase.Unscoped's doc).
func TestConversationalHTTPFetcher_UnscopedSendsAllProjects(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	fetch := conversationalHTTPFetcher(srv.Client(), srv.URL, false, "")
	if _, err := fetch(context.Background(), eval.ConversationalCase{Query: "q", Unscoped: true}); err != nil {
		t.Fatalf("conversationalHTTPFetcher: %v", err)
	}

	q, err := url.ParseQuery(gotQuery)
	if err != nil {
		t.Fatalf("parse request query %q: %v", gotQuery, err)
	}
	if q.Get("all_projects") != "1" {
		t.Errorf("all_projects = %q, want %q — an unscoped case must fan out to every project", q.Get("all_projects"), "1")
	}
	if q.Has("project") {
		t.Errorf("project was set on an unscoped case: %q", gotQuery)
	}
}

// TestConversationalHTTPFetcher_ForceUnscopedOverridesProject proves
// --force-unscoped (kept reachable for explicit before/after comparison
// against the pre-fix behavior) ignores a scoped case's own Project and
// still fans out to every project.
func TestConversationalHTTPFetcher_ForceUnscopedOverridesProject(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	fetch := conversationalHTTPFetcher(srv.Client(), srv.URL, true /* forceUnscoped */, "")
	if _, err := fetch(context.Background(), eval.ConversationalCase{Query: "q", Project: "omnia"}); err != nil {
		t.Fatalf("conversationalHTTPFetcher: %v", err)
	}

	q, err := url.ParseQuery(gotQuery)
	if err != nil {
		t.Fatalf("parse request query %q: %v", gotQuery, err)
	}
	if q.Get("all_projects") != "1" {
		t.Errorf("all_projects = %q, want %q — --force-unscoped must override a scoped case's project", q.Get("all_projects"), "1")
	}
	if q.Has("project") {
		t.Errorf("project was set despite --force-unscoped: %q", gotQuery)
	}
}

// TestConversationalHTTPFetcher_AsOfSendsEnvelopeAndVerifiesEcho proves the
// isolation-verification contract (engram eval/http-search-as-of-isolation):
// a non-empty asOf sends BOTH as_of= and envelope=1 (the bare-array shape
// has no echo field to verify against), and a matching echo is required
// before the fetcher trusts the response.
func TestConversationalHTTPFetcher_AsOfSendsEnvelopeAndVerifiesEcho(t *testing.T) {
	const cutoff = "2026-08-14T03:00:00Z"
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"id":1,"sync_id":"obs-abc","content":"isolated hit"}],"recall_degraded":true,"as_of":"` + cutoff + `"}`))
	}))
	defer srv.Close()

	fetch := conversationalHTTPFetcher(srv.Client(), srv.URL, false, cutoff)
	got, err := fetch(context.Background(), eval.ConversationalCase{Query: "what is omnia", Project: "omnia"})
	if err != nil {
		t.Fatalf("conversationalHTTPFetcher: %v", err)
	}

	q, err := url.ParseQuery(gotQuery)
	if err != nil {
		t.Fatalf("parse request query %q: %v", gotQuery, err)
	}
	if q.Get("as_of") != cutoff {
		t.Errorf("as_of = %q, want %q", q.Get("as_of"), cutoff)
	}
	if q.Get("envelope") != "1" {
		t.Errorf("envelope = %q, want %q — the echo can only be read from the widened shape", q.Get("envelope"), "1")
	}
	if got.SurfacedObservationID != "obs-abc" {
		t.Errorf("SurfacedObservationID = %q, want the envelope's decoded result", got.SurfacedObservationID)
	}
}

// TestConversationalHTTPFetcher_AsOfFailsLoudlyOnEchoMismatch is the guard's
// core proof: a server that does NOT echo back the exact as_of it was sent
// (empty AsOf — an older server binary with no as_of support, silently
// running a live search — or a mismatched value) must fail the fetch with
// an error, never silently return whatever results it got. This is the
// mechanism the whole store-isolation guard depends on: without it, an
// eval run against a stale server binary would silently measure a
// contaminated live store while believing itself isolated.
func TestConversationalHTTPFetcher_AsOfFailsLoudlyOnEchoMismatch(t *testing.T) {
	tests := map[string]string{
		"empty echo (server ignored as_of entirely)": `{"results":[{"id":1,"content":"live leak"}],"recall_degraded":false}`,
		"mismatched echo":                             `{"results":[{"id":1,"content":"live leak"}],"as_of":"2020-01-01T00:00:00Z"}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()

			fetch := conversationalHTTPFetcher(srv.Client(), srv.URL, false, "2026-08-14T03:00:00Z")
			_, err := fetch(context.Background(), eval.ConversationalCase{Query: "q", Project: "omnia"})
			if err == nil {
				t.Fatal("expected an error when the server does not echo the requested as_of, got nil (this would silently trust an unisolated store)")
			}
			if !strings.Contains(err.Error(), "isolation NOT verified") {
				t.Errorf("error = %v, want it to name the isolation failure explicitly", err)
			}
		})
	}
}

// TestConversationalAnswerFetcher_ScopesToProject is
// TestConversationalHTTPFetcher_ScopesToProject's GET /answer sibling —
// engram #2623 named conversationalAnswerFetcher explicitly as the other
// fetcher that never scoped.
func TestConversationalAnswerFetcher_ScopesToProject(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"context":"","confidence":"none","sources":[]}`))
	}))
	defer srv.Close()

	fetch := conversationalAnswerFetcher(srv.Client(), srv.URL, false)
	if _, err := fetch(context.Background(), eval.ConversationalCase{Query: "q", Project: "workly"}); err != nil {
		t.Fatalf("conversationalAnswerFetcher: %v", err)
	}

	q, err := url.ParseQuery(gotQuery)
	if err != nil {
		t.Fatalf("parse request query %q: %v", gotQuery, err)
	}
	if q.Get("project") != "workly" {
		t.Errorf("project = %q, want %q — a scoped case must send project=", q.Get("project"), "workly")
	}
	if q.Has("all_projects") {
		t.Errorf("all_projects was set on a scoped case: %q", gotQuery)
	}
}

// ── conversationalStoreFetcher / conversationalPipelineFetcher: real-store
// fetcher-selection parity, mirroring eval_injection_test.go's
// TestPipelineBackedFetcher_ParityWhenFlagsOff for the coding profile ─────

// TestConversationalFetchers_ParityWhenInjectionOff proves requirement 5's
// two in-process fetchers agree on the scoring-relevant fields
// (Retrieved/SurfacedObservationID) when every injection sub-gate is off —
// the same parity floor pipelineBackedFetcher already guarantees against
// storeBackedFetcher for the coding profile, now for their conversational
// siblings (the fetcher --injection actually selects between, per
// defaultRunConversationalEval's opts.Injection branch).
func TestConversationalFetchers_ParityWhenInjectionOff(t *testing.T) {
	cfg := testConfig(t)
	mustSeedObservation(t, cfg, "s1", "eval-conversational-parity", "architecture",
		"Ollama embedding layer", "internal/embed: Ollama HTTP client with unit-normalized vectors", "project")

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	c := eval.ConversationalCase{ID: "case-1", Query: "Ollama embedding layer"}

	storeCase, err := conversationalStoreFetcher(s, false, "")(context.Background(), c)
	if err != nil {
		t.Fatalf("conversationalStoreFetcher: %v", err)
	}
	if storeCase.Retrieved == "" {
		t.Fatal("test setup invalid: conversationalStoreFetcher found nothing")
	}

	pipelineCase, err := conversationalPipelineFetcher(s, nil, config.InjectionConfig{}, config.RankingConfig{}, false)(context.Background(), c)
	if err != nil {
		t.Fatalf("conversationalPipelineFetcher: %v", err)
	}
	if pipelineCase.Retrieved != storeCase.Retrieved {
		t.Errorf("Retrieved = %q, want %q (parity when every injection flag is off)", pipelineCase.Retrieved, storeCase.Retrieved)
	}
	if pipelineCase.SurfacedObservationID != storeCase.SurfacedObservationID {
		t.Errorf("SurfacedObservationID = %q, want %q (parity when every injection flag is off)", pipelineCase.SurfacedObservationID, storeCase.SurfacedObservationID)
	}
}

// TestConversationalPipelineFetcher_BudgetActuallyTrims is the "fetcher
// selection matters" half of the parity test above: with the injection
// token budget enabled and set tight, conversationalPipelineFetcher must
// produce a SMALLER ranked candidate list than conversationalStoreFetcher
// would for the same seeded data — proving --injection routes to a fetcher
// that actually applies config.InjectionConfig, not a fetcher that ignores it.
func TestConversationalPipelineFetcher_BudgetActuallyTrims(t *testing.T) {
	cfg := testConfig(t)
	for i := 0; i < 5; i++ {
		mustSeedObservation(t, cfg, "s1", "eval-conversational-budget", "manual",
			"Budget fixture "+strconv.Itoa(i), "conversational budget wiring fixture content "+strconv.Itoa(i), "project")
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	c := eval.ConversationalCase{ID: "case-1", Query: "conversational budget wiring fixture"}

	storeCase, err := conversationalStoreFetcher(s, false, "")(context.Background(), c)
	if err != nil {
		t.Fatalf("conversationalStoreFetcher: %v", err)
	}
	if len(storeCase.RankedObservationIDs) < 2 {
		t.Fatalf("test setup invalid: expected multiple ranked hits from conversationalStoreFetcher, got %d", len(storeCase.RankedObservationIDs))
	}

	tightBudget := config.InjectionConfig{Budget: config.TokenBudgetConfig{Enabled: true, MaxTokens: 1}}
	pipelineCase, err := conversationalPipelineFetcher(s, nil, tightBudget, config.RankingConfig{}, false)(context.Background(), c)
	if err != nil {
		t.Fatalf("conversationalPipelineFetcher: %v", err)
	}
	if len(pipelineCase.RankedObservationIDs) >= len(storeCase.RankedObservationIDs) {
		t.Errorf("conversationalPipelineFetcher RankedObservationIDs len = %d, want fewer than conversationalStoreFetcher's %d (injection.budget.max_tokens=1 must trim)",
			len(pipelineCase.RankedObservationIDs), len(storeCase.RankedObservationIDs))
	}
}
