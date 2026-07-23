package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/embed"
	"github.com/velion/omnia/internal/envx"
	"github.com/velion/omnia/internal/eval"
	"github.com/velion/omnia/internal/llm"
	"github.com/velion/omnia/internal/store"
)

// defaultEvalCorpusPath and defaultEvalABPairsPath resolve the eval
// harness's data files relative to a repo checkout — matching how the
// harness's own package (internal/eval) already references its testdata.
const (
	defaultEvalCorpusPath  = "internal/eval/testdata/cases.json"
	defaultEvalABPairsPath = "internal/embed/testdata/ab_pairs.json"
)

// evalRunOptions bundles cmdEval's resolved flags for the (injectable)
// harness-execution seam (runEvalHarness).
type evalRunOptions struct {
	CorpusPath  string
	ABPairsPath string
	ConfigPath  string
	Runs        int
}

var (
	// runEvalHarness is injectable for testing — the production
	// implementation (defaultRunEvalHarness) wires the real corpus, store,
	// embedder, and (optionally) LLM judge into an eval.Config and calls
	// eval.RunHarness (spec EVAL-6). Tests substitute a fake that returns a
	// canned eval.RunSummary without touching the real store, corpus file,
	// or any LLM CLI.
	runEvalHarness = defaultRunEvalHarness

	// evaluateGate is injectable for the same reason — defaults to the real
	// eval.EvaluateGate (spec EVAL-8).
	evaluateGate = eval.EvaluateGate
)

// cmdEval is `omnia eval`: the CLI + release-gate entry point for the
// memory-quality eval harness (spec sdd/omnia-eval-harness). It loads the
// eval corpus, runs the harness eval.MinRuns-eval.MaxRuns times for
// reproducibility (spec EVAL-6), prints the segmented report (by capability,
// by language, and overall) plus a best-effort retrieval-only recall@k
// section (spec EVAL-7), and — only when --baseline is supplied — evaluates
// a release gate (spec EVAL-8): advisory by default (logs a regression,
// never blocks); blocking only past --threshold and only in --mode blocking.
//
// Usage:
//
//	omnia eval [--mode advisory|blocking] [--runs N] [--threshold F]
//	           [--baseline F] [--corpus PATH] [--ab-pairs PATH] [--config PATH]
func cmdEval(args []string) {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	mode := fs.String("mode", string(eval.GateModeAdvisory), "release-gate mode: advisory|blocking (default advisory — spec EVAL-8)")
	runs := fs.Int("runs", eval.MinRuns, fmt.Sprintf("reproducibility runs, must be in [%d,%d] (spec EVAL-6)", eval.MinRuns, eval.MaxRuns))
	threshold := fs.Float64("threshold", 0.05, "max allowed accuracy regression vs --baseline before a blocking gate fails")
	baseline := fs.Float64("baseline", 0, "baseline overall accuracy to compare against; 0 (default) skips the gate decision and only prints the report")
	corpusPath := fs.String("corpus", defaultEvalCorpusPath, "path to the eval corpus JSON (spec EVAL-2)")
	abPairsPath := fs.String("ab-pairs", defaultEvalABPairsPath, "path to the bilingual AB pairs JSON for the retrieval-only section (spec EVAL-7)")
	configPath := fs.String("config", config.DefaultPath(), "path to config file (embeddings + recall settings)")
	if err := fs.Parse(args); err != nil {
		fatal(err)
		return
	}

	gateMode := eval.GateMode(strings.ToLower(strings.TrimSpace(*mode)))
	if gateMode != eval.GateModeAdvisory && gateMode != eval.GateModeBlocking {
		fmt.Fprintf(os.Stderr, "error: --mode must be %q or %q, got %q\n", eval.GateModeAdvisory, eval.GateModeBlocking, *mode)
		exitFunc(1)
		return
	}

	summary, err := runEvalHarness(context.Background(), evalRunOptions{
		CorpusPath:  *corpusPath,
		ABPairsPath: *abPairsPath,
		ConfigPath:  *configPath,
		Runs:        *runs,
	})
	if err != nil {
		fatal(fmt.Errorf("eval: %w", err))
		return
	}

	printEvalSummary(summary)

	if *baseline <= 0 {
		fmt.Println("\ngate: no --baseline supplied — skipping release-gate decision (report only)")
		return
	}

	result, err := evaluateGate(summary, gateMode, *baseline, *threshold)
	if err != nil {
		fatal(fmt.Errorf("eval gate: %w", err))
		return
	}
	printGateResult(result)
	if result.Blocked {
		exitFunc(1)
	}
}

func printEvalSummary(summary eval.RunSummary) {
	fmt.Printf("Eval Harness Summary (%d reproducibility runs — spec EVAL-6)\n", summary.Runs)
	fmt.Println()
	fmt.Println("By capability (spec EVAL-3):")
	for _, cap := range []eval.Capability{eval.CapabilityRecall, eval.CapabilityCausal, eval.CapabilityStateUpdate, eval.CapabilityStateAbstraction} {
		s := summary.ByCapability[cap]
		fmt.Printf("  %-20s accuracy=%.3f±%.3f  quality/1k=%.3f±%.3f  (n=%d)\n",
			cap, s.Accuracy.Mean, s.Accuracy.StdDev, s.QualityPer1k.Mean, s.QualityPer1k.StdDev, s.Accuracy.N)
	}
	fmt.Println()
	fmt.Println("By language (spec EVAL-3):")
	for _, lang := range []eval.Language{eval.LanguageEN, eval.LanguageES} {
		s := summary.ByLanguage[lang]
		fmt.Printf("  %-20s accuracy=%.3f±%.3f  quality/1k=%.3f±%.3f  (n=%d)\n",
			lang, s.Accuracy.Mean, s.Accuracy.StdDev, s.QualityPer1k.Mean, s.QualityPer1k.StdDev, s.Accuracy.N)
	}
	fmt.Println()
	fmt.Printf("Overall: accuracy=%.3f±%.3f  quality/1k=%.3f±%.3f\n",
		summary.Overall.Accuracy.Mean, summary.Overall.Accuracy.StdDev, summary.Overall.QualityPer1k.Mean, summary.Overall.QualityPer1k.StdDev)

	// Retrieval-only recall@k section (spec EVAL-7) — attached to the LAST
	// run's Report only when the harness wiring populated one; never merged
	// into the end-task figures above (EVAL-7's no-merge rule).
	if n := len(summary.Reports); n > 0 {
		if r := summary.Reports[n-1].Retrieval; r != nil {
			fmt.Println()
			fmt.Printf("Retrieval-only recall@%d (spec EVAL-7, last run, model=%s): %.3f (%d/%d)\n",
				r.Result.K, r.Result.Model, r.Result.RecallAtK, r.Result.Hits, r.Result.Total)
		}
	}
}

func printGateResult(result eval.GateResult) {
	fmt.Println()
	fmt.Printf("Release Gate (spec EVAL-8, mode=%s)\n", result.Mode)
	fmt.Printf("  baseline accuracy: %.3f\n", result.BaselineAccuracy)
	fmt.Printf("  current accuracy:  %.3f\n", result.CurrentAccuracy)
	fmt.Printf("  threshold:         %.3f\n", result.Threshold)
	if !result.Regressed {
		fmt.Println("  verdict: no regression detected")
		return
	}
	if result.Mode == eval.GateModeBlocking {
		fmt.Println("  verdict: REGRESSION past threshold — BLOCKING (exit 1)")
	} else {
		fmt.Println("  verdict: regression past threshold — advisory mode, NOT blocking (logged only)")
	}
}

// defaultRunEvalHarness is the production eval.RunFunc wiring: it loads the
// corpus (spec EVAL-2's [50,150] floor enforced by eval.LoadCorpus), opens
// the real observation store, and scores every case via a store-search-backed
// RetrievedFetcher, repeating eval.MinRuns-eval.MaxRuns times via
// eval.RunHarness (spec EVAL-6).
//
// The bilingual AB-pairs retrieval-only section (spec EVAL-7) and the LLM
// judge (spec EVAL-5, causal/state_abstraction cases) are both best-effort:
// a disabled/unconfigured embedder or an unset OMNIA_AGENT_CLI degrades
// gracefully — matching cmdEmbed's and cmdConflicts --semantic's existing
// degrade conventions — EXCEPT that a corpus case which actually needs a
// judge and has none configured still surfaces as a real per-case scoring
// error (spec EVAL-5 requires judged causal/state_abstraction verdicts, not
// silent misses; see scoring.go's Score()).
func defaultRunEvalHarness(ctx context.Context, opts evalRunOptions) (eval.RunSummary, error) {
	cases, err := eval.LoadCorpus(opts.CorpusPath)
	if err != nil {
		return eval.RunSummary{}, fmt.Errorf("load corpus: %w", err)
	}

	cfg, err := store.DefaultConfig()
	if err != nil {
		return eval.RunSummary{}, fmt.Errorf("resolve store config: %w", err)
	}
	s, err := storeNew(cfg)
	if err != nil {
		return eval.RunSummary{}, fmt.Errorf("open store: %w", err)
	}
	defer s.Close()

	var judge llm.AgentRunner
	if name := strings.TrimSpace(envx.Get("OMNIA_AGENT_CLI")); name != "" {
		runner, err := llm.NewRunner(name)
		if err != nil {
			return eval.RunSummary{}, fmt.Errorf("build LLM judge (OMNIA_AGENT_CLI=%s): %w", name, err)
		}
		judge = runner
	}

	fetch := storeBackedFetcher(s)
	appCfg, appCfgErr := config.Load(opts.ConfigPath)

	runFunc := func(ctx context.Context) (eval.Report, error) {
		report, err := eval.RunOnce(ctx, cases, fetch, judge, s)
		if err != nil {
			return eval.Report{}, err
		}
		// Best-effort retrieval-only section (spec EVAL-7): only attempted
		// when embeddings are configured/enabled; a failure here (e.g.
		// Ollama unreachable) never fails the end-task run above.
		if appCfgErr == nil && appCfg.Embeddings.Enabled {
			if pairs, pairsErr := embed.LoadABPairs(opts.ABPairsPath); pairsErr == nil {
				client := embed.New(appCfg.Embeddings.BaseURL, appCfg.Embeddings.Model, appCfg.Embeddings.Dim)
				if section, secErr := eval.RunRetrievalSection(ctx, appCfg.Embeddings.Model, client, pairs, 5); secErr == nil {
					report.Retrieval = &section
				}
			}
		}
		return report, nil
	}

	return eval.RunHarness(ctx, eval.Config{Run: runFunc}, opts.Runs)
}

// storeBackedFetcher returns an eval.RetrievedFetcher that searches the real
// store for each case's Query and uses the top hit's content (or sync ID,
// for contradiction cases) as the retrieved result — the harness's single
// retrieval seam (see eval.RetrievedFetcher's doc comment).
func storeBackedFetcher(s *store.Store) eval.RetrievedFetcher {
	return func(ctx context.Context, c eval.EvalCase) (eval.RetrievedCase, error) {
		results, err := storeSearch(s, c.Query, store.SearchOptions{Limit: 1})
		if err != nil {
			return eval.RetrievedCase{}, fmt.Errorf("search: %w", err)
		}
		if len(results) == 0 {
			return eval.RetrievedCase{}, nil
		}
		top := results[0]
		return eval.RetrievedCase{
			Retrieved:             top.Content,
			SurfacedObservationID: top.SyncID,
			Tokens:                eval.TokenBreakdown{Retrieval: estimateTokenCount(top.Content)},
		}, nil
	}
}

// estimateTokenCount is a coarse, dependency-free token estimate (~4 chars
// per token) used only for the eval harness's non-judge token accounting
// (spec EVAL-1) — never for billing or LLM context-window math.
func estimateTokenCount(s string) int {
	n := len(s) / 4
	if n == 0 && s != "" {
		n = 1
	}
	return n
}
