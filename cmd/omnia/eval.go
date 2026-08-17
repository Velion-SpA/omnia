package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/embed"
	"github.com/velion/omnia/internal/envx"
	"github.com/velion/omnia/internal/eval"
	"github.com/velion/omnia/internal/llm"
	"github.com/velion/omnia/internal/mcp"
	"github.com/velion/omnia/internal/recall"
	"github.com/velion/omnia/internal/store"
	"github.com/velion/omnia/internal/token"
)

// defaultEvalABPairsPath resolves the eval harness's bilingual AB-pairs
// retrieval-only data file relative to a repo checkout — matching how the
// harness's own package (internal/embed) already references its testdata.
// Unlike the eval corpus below, this is best-effort (see
// defaultRunEvalHarness's retrieval-only section): a load failure just
// skips that section rather than failing the whole run, so a cwd-relative
// default here does not carry finding #1's production-breaking risk.
const defaultEvalABPairsPath = "internal/embed/testdata/ab_pairs.json"

// evalCorpusPathFlagDefault is the --corpus flag's default value: an EMPTY
// string, which loadEvalCorpus (below) treats as "use the corpus embedded
// in the binary" (finding #1). The pre-fix default was a cwd-relative path
// ("internal/eval/testdata/cases.json"), which only resolved when the
// process's cwd happened to be a repo checkout root — any real installed
// binary run from a normal location (a user's home directory, /tmp,
// anywhere) failed to find it, silently making `omnia rank-train`'s
// promotion gate permanently non-functional in production. An explicit
// --corpus PATH still loads from that file exactly as before, preserving
// today's ability to point at a custom/larger corpus for a real eval run.
const evalCorpusPathFlagDefault = ""

// loadEvalCorpus resolves the eval corpus for a harness run: an explicit
// non-empty path always loads from that file via eval.LoadCorpus
// (preserving the existing ability to point at a custom/larger corpus);
// an empty path (no --corpus override — the default, see
// evalCorpusPathFlagDefault) loads the corpus embedded in the binary via
// eval.EmbeddedCorpus, which works identically regardless of cwd.
func loadEvalCorpus(path string) ([]eval.EvalCase, error) {
	if strings.TrimSpace(path) == "" {
		return eval.EmbeddedCorpus()
	}
	return eval.LoadCorpus(path)
}

// evalRunOptions bundles cmdEval's resolved flags for the (injectable)
// harness-execution seam (runEvalHarness).
type evalRunOptions struct {
	CorpusPath  string
	ABPairsPath string
	ConfigPath  string
	Runs        int
	// Injection selects the retrieval seam (issue #143): false (default)
	// keeps storeBackedFetcher — the raw top-1 FTS5 hit, byte-for-byte
	// unchanged from pre-#143 behavior. true switches to
	// pipelineBackedFetcher, which additionally runs the v0.3 "Context
	// Economy" injection passes (type-lens/MMR/token-budget) driven by the
	// loaded config's `injection` block, so the eval numbers reflect what an
	// agent would actually receive with those flags on.
	Injection bool
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
//	           [--injection]
//	omnia eval --profile conversational [--target inprocess|http|answer]
//	           [--http-base-url URL] [--corpus PATH] [--config PATH]
//	           [--runs N] [--injection]
//	omnia eval --profile conversational --multi-project --http-base-url URL
//	           [--corpus PATH]
func cmdEval(args []string) {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	mode := fs.String("mode", string(eval.GateModeAdvisory), "release-gate mode: advisory|blocking (default advisory — spec EVAL-8)")
	runs := fs.Int("runs", eval.MinRuns, fmt.Sprintf("reproducibility runs, must be in [%d,%d] (spec EVAL-6)", eval.MinRuns, eval.MaxRuns))
	threshold := fs.Float64("threshold", 0.05, "max allowed accuracy regression vs --baseline before a blocking gate fails")
	baseline := fs.Float64("baseline", 0, "baseline overall accuracy to compare against; 0 (default) skips the gate decision and only prints the report")
	corpusPath := fs.String("corpus", evalCorpusPathFlagDefault, "path to the eval corpus JSON; defaults to the corpus embedded in the binary, which works from any cwd. Shape depends on --profile: coding (spec EVAL-2) or conversational ([]eval.ConversationalCase) — set this to point at a custom/larger corpus file instead")
	abPairsPath := fs.String("ab-pairs", defaultEvalABPairsPath, "path to the bilingual AB pairs JSON for the retrieval-only section (spec EVAL-7); --profile coding only")
	configPath := fs.String("config", config.DefaultPath(), "path to config file (embeddings + recall settings)")
	injection := fs.Bool("injection", false, "opt-in: score against the v0.3 Context Economy injection pipeline (type-lens + MMR + token budget, driven by --config's `injection` block) instead of the raw top-1 FTS5 hit; default false keeps current behavior byte-for-byte unchanged (issue #143). --profile conversational --target inprocess only — no effect with --target http")
	profile := fs.String("profile", "coding", "eval corpus profile: coding (default, spec sdd/omnia-eval-harness) or conversational (docs/conversational-retrieval-plan.md's \"Baseline first\" item — identity/status/delta/open_items/rationale/cross_project/absence question kinds)")
	target := fs.String("target", "inprocess", "--profile conversational only: inprocess (searches the local store directly, honoring --injection), http (calls GET /search on a running server via --http-base-url — quantifies the P0 gap between GET /search and mem_search's full pipeline), or answer (calls GET /answer on a running server via --http-base-url — scores P3's calibrated confidence: false_confidence on absence cases, false_refusal on identity/status cases)")
	httpBaseURL := fs.String("http-base-url", "", "--profile conversational --target http|answer only: base URL of a running omnia server, e.g. http://localhost:7799 (no trailing slash)")
	multiProject := fs.Bool("multi-project", false, "--profile conversational --target http only: run ONLY the corpus's cross_project cases through GET /search?all_projects=1&envelope=1 (P5, docs/conversational-retrieval-plan.md), reporting accuracy@1 AND project diversity in the top-4 PER CASE (not just an aggregate) — the P5 measurement gate. Requires --http-base-url")
	forceUnscoped := fs.Bool("force-unscoped", false, "--profile conversational only: ignore every case's project/unscoped scoping and search ALL projects for every case, reproducing the harness's pre-engram-#2623 behavior — kept reachable for before/after comparison, never the default (the real consumer, Hermes' detectProject, always scopes to one project)")
	allowLive := fs.Bool("allow-live", false, "--profile conversational only: deliberately measure the LIVE store instead of an isolated view, accepting that this session's own memories about the eval will be scored as retrieved evidence. Exists so the refusal in place of --as-of is a speed bump rather than a wall — there are legitimate uses (debugging the harness itself, measuring a store no session is writing to). It is never the right flag for producing a number you intend to report.")
	asOf := fs.String("as-of", "", "--profile conversational only: recorded-time isolation (engram eval/http-search-as-of-isolation) — read the store as it stood at this RFC3339 timestamp instead of live. THIS IS THE FIX for a measured contamination bug: the session running this eval writes memories (mem_save, following this project's own Keywords: convention) that quote the corpus's gold facts verbatim, and the NEXT eval run then scores those notes as if they were genuine retrieved evidence (engram #2633: identity grounding was reported 0.625, corrected to 0.250, actually 0.000). Setting --as-of makes BOTH --target inprocess (store.SearchAsOf) and --target http (GET /search?as_of=, this endpoint's own isolation branch) read a view of the store that cannot contain anything written after the timestamp — verified, not assumed: this flag makes the eval FAIL LOUDLY (fatal, non-zero exit) instead of silently measuring a contaminated store whenever isolation cannot be confirmed (time_travel.enabled is false, the timestamp resolves to live data, or a --target http server doesn't echo the applied as_of back). Empty (default) keeps today's live-read behavior unchanged for every other use of this tool.")
	if err := fs.Parse(args); err != nil {
		fatal(err)
		return
	}

	normalizedProfile := strings.ToLower(strings.TrimSpace(*profile))
	if normalizedProfile != "coding" && normalizedProfile != "conversational" {
		fmt.Fprintf(os.Stderr, "error: --profile must be %q or %q, got %q\n", "coding", "conversational", *profile)
		exitFunc(1)
		return
	}

	if normalizedProfile == "conversational" && *multiProject {
		if err := runCrossProjectMeasurement(context.Background(), conversationalRunOptions{
			CorpusPath:  *corpusPath,
			ConfigPath:  *configPath,
			Target:      *target,
			HTTPBaseURL: *httpBaseURL,
		}); err != nil {
			fatal(fmt.Errorf("cross-project eval: %w", err))
		}
		return
	}

	if normalizedProfile == "conversational" {
		// Isolation is REQUIRED, not optional. Measured three times over: a
		// session that runs this eval and then writes memories about it
		// poisons the next run, because documenting an evaluation faithfully
		// means naming what the evaluation looks for, and this project's
		// `Keywords:` save convention then makes those strings maximally
		// findable. Three separate memories — a bugfix note, the retraction
		// documenting that bugfix note, and a field-test note — each became a
		// false pass independently, hours apart (engram
		// eval/decontamination-of-poisoning-memories). Identity grounding read
		// 0.625, then 0.250, and is actually 0.000.
		//
		// So the default cannot be "live unless you remember the flag".
		// Forgetting produced a wrong number three times, and a wrong number
		// that looks plausible is worse than a refusal. Running against live
		// state is still possible — it is just no longer what happens when
		// nobody thinks about it.
		if strings.TrimSpace(*asOf) == "" && !*allowLive {
			fatal(fmt.Errorf(
				"eval: --profile conversational requires isolation: pass --as-of RFC3339 (a timestamp from BEFORE this session's first mem_save), or --allow-live to deliberately measure the live store.\n"+
					"       Reading live state means this session's own notes about the eval are scored as retrieved evidence; that is not hypothetical, it happened three times (engram eval/decontamination-of-poisoning-memories).\n"+
					"       Suggested: --as-of %s",
				sessionStartHint()))
			return
		}
		summary, err := runConversationalEval(context.Background(), conversationalRunOptions{
			CorpusPath:    *corpusPath,
			ConfigPath:    *configPath,
			Runs:          *runs,
			Target:        *target,
			HTTPBaseURL:   *httpBaseURL,
			Injection:     *injection,
			ForceUnscoped: *forceUnscoped,
			AsOf:          *asOf,
		})
		if err != nil {
			fatal(fmt.Errorf("eval: %w", err))
			return
		}
		printConversationalSummary(summary)
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
		Injection:   *injection,
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

	// Order-sensitive section (#236). Everything printed above is a PRESENCE
	// measure — it asks whether the retrieved text contains the expected fact,
	// so re-ranking the candidate pool cannot move it. These figures are the
	// ones a ranking change shows up in, which is what makes rank-train's
	// promotion gate able to say more than "did not regress".
	//
	// Printed from the last run's Report and never merged into the accuracy
	// figures above, following the same no-merge rule EVAL-7's retrieval
	// section already follows. Absent when the run's fetcher supplied no ranked
	// lists — an unmeasured section stays visibly unmeasured.
	if n := len(summary.Reports); n > 0 {
		if rk := summary.Reports[n-1].Ranking; rk != nil {
			fmt.Println()
			fmt.Printf("Ranking quality (order-sensitive, last run, %d case(s) scored):\n", rk.Cases)
			fmt.Printf("  MRR                  %.3f\n", rk.MRR)
			for _, k := range eval.RankingKs {
				fmt.Printf("  recall@%-14d %.3f\n", k, rk.RecallAtK[k])
			}
			if rk.Unscoreable > 0 {
				fmt.Printf("  (%d case(s) not scoreable: no ranked list or no observation_id)\n", rk.Unscoreable)
			}
		}
	}

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
// evalStoreConfig threads v0.4 memory-at-rest-security's config.yaml wiring
// into the store config the eval harness opens with (#234).
//
// A nil appCfg — no readable config.yaml, the fresh-install case — returns base
// byte-for-byte, so encryption is never turned on by accident and eval behaves
// exactly as it did before this existed. Mirrors applyEncryptionConfig
// (main.go), kept as a pure function so the wiring is testable without opening
// a real store or touching a keychain.
//
// TimeTravelEnabled/HistoryRevisionCap (engram eval/http-search-as-of-
// isolation) mirror main.go's own applyTimeTravelConfig — before this, eval
// opened its store with these left at store.DefaultConfig()'s zero value
// (time-travel OFF) regardless of what config.yaml said, so `--target
// inprocess --as-of` could never work: store.SearchAsOf silently degrades to
// a live search whenever cfg.TimeTravelEnabled is false (see that method's
// own doc), and defaultRunConversationalEval's own isolation guard
// (s.TimeTravelEnabled()) would ALWAYS fail closed. This has no effect on
// eval's default (no --as-of) behavior: eval never writes, so enabling
// recorded-time history capture on a read-only process changes nothing
// observable except making the as-of read paths actually work.
func evalStoreConfig(base store.Config, appCfg *config.Config) store.Config {
	if appCfg == nil {
		return base
	}
	base.EncryptionEnabled = appCfg.Encryption.Enabled
	base.EncryptionKeychainService = appCfg.Encryption.KeychainService
	base.EncryptionAllowPlaintextFallback = appCfg.Encryption.AllowPlaintextFallback
	base.TimeTravelEnabled = appCfg.TimeTravel.Enabled
	base.HistoryRevisionCap = appCfg.TimeTravel.MaxRevisionsPerMemory
	return base
}

func defaultRunEvalHarness(ctx context.Context, opts evalRunOptions) (eval.RunSummary, error) {
	cases, err := loadEvalCorpus(opts.CorpusPath)
	if err != nil {
		return eval.RunSummary{}, fmt.Errorf("load corpus: %w", err)
	}

	cfg, err := store.DefaultConfig()
	if err != nil {
		return eval.RunSummary{}, fmt.Errorf("resolve store config: %w", err)
	}

	// #234: config.yaml must be loaded BEFORE the store is opened, not after.
	// cmdEval is dispatched standalone (not through run()'s shared
	// store.Config composition root), so v0.4 memory-at-rest-security's
	// wiring has to be applied here directly — exactly as cmdConsolidate
	// documents for the same reason. Without it, eval opens an encrypted
	// omnia.db through the plain modernc path and dies on the first pragma,
	// which ALSO takes rank-train's promotion gate down: the learned ranker
	// trains a model and can never promote one, reporting "evaluation
	// regressed or failed" when the harness simply could not open the store.
	appCfg, appCfgErr := config.Load(opts.ConfigPath)
	if appCfgErr != nil {
		appCfg = nil
	}
	s, err := storeNew(evalStoreConfig(cfg, appCfg))
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

	// appCfg/appCfgErr are resolved above, before the store is opened (#234).
	// EMBM-3/blocking-fix: reject an internally-inconsistent embeddings
	// config (a truncation/expansion Dim mismatched against the model's MRL
	// capability, see config.ValidateEmbeddings) right after config.Load,
	// exactly like cmdEmbed (cmd/omnia/embed.go) — fatal, not a silent
	// degrade, since eval is a release-gate tool and a misconfigured
	// embeddings section must never let a run silently skip or corrupt the
	// retrieval-only section (spec EVAL-7) without surfacing why. A missing
	// config file (appCfgErr != nil) is left alone: that already degrades
	// gracefully below (best-effort retrieval section skipped), unchanged.
	if appCfgErr == nil {
		if err := config.ValidateEmbeddings(appCfg.Embeddings); err != nil {
			return eval.RunSummary{}, fmt.Errorf("eval: invalid embeddings config: %w", err)
		}
	}

	// Retrieval seam (issue #143): --injection swaps in pipelineBackedFetcher
	// so eval scores against the SAME v0.3 "Context Economy" injection
	// passes handleSearch applies, driven by the loaded config's Injection
	// block. A config load failure (appCfgErr != nil) degrades to a
	// zero-value config.InjectionConfig — every sub-gate's Enabled defaults
	// to false, so pipelineBackedFetcher's passes are all no-ops in that
	// case, matching the same "missing config degrades gracefully"
	// convention the retrieval-only section below already follows.
	//
	// Review fix (#143 adversarial review, HIGH): pipelineBackedFetcher must
	// branch on hybrid recall exactly like handleSearch does
	// (internal/mcp/mcp.go's handleSearch, cfg.Recall != nil branch,
	// ~L1183-1224) — the SAME config file that drives the Injection block
	// above can ALSO enable `recall.enabled: true` (cmd/omnia/main.go's
	// cmdMCP, ~L1168-1169: mcpCfg.Recall = buildRecallService(s,
	// appCfg.Recall, appCfg.Embeddings, cfg.DataDir); recall.enabled may even
	// get auto-flipped on by the Ollama auto-detect, ~L1163-1165). Without
	// this, --injection would silently keep measuring the FTS5-only
	// candidate pool even when recall is actually on in production,
	// understating what an agent would receive. buildRecallService is the
	// SAME package-level helper cmdMCP itself calls — reused here, not
	// reimplemented, so eval and mem_search can't drift apart on how recall
	// gets built. recallCfg/embCfg both degrade to their zero value (recall
	// disabled) on a config load failure, mirroring injectionCfg below.
	fetch := storeBackedFetcher(s)
	if opts.Injection {
		var injectionCfg config.InjectionConfig
		var recallCfg config.RecallConfig
		var embCfg config.EmbeddingsConfig
		var vecIndexEnabled bool
		var encCfg config.EncryptionConfig
		var rankingCfg config.RankingConfig
		if appCfgErr == nil {
			injectionCfg = appCfg.Injection
			recallCfg = appCfg.Recall
			rankingCfg = appCfg.Recall.Ranking
			embCfg = appCfg.Embeddings
			vecIndexEnabled = appCfg.VecIndex.Enabled
			encCfg = appCfg.Encryption
		}
		recallSvc := buildRecallService(s, recallCfg, embCfg, cfg.DataDir, vecIndexEnabled, encCfg)
		fetch = pipelineBackedFetcher(s, recallSvc, injectionCfg, rankingCfg)
	}

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

// rankCandidateDepth is how deep the eval fetchers look when building the
// ranked candidate list for the order-sensitive metrics (#236). It matches the
// largest cutoff in eval.RankingKs: fetching fewer would make recall@10
// unmeasurable, fetching more would cost retrieval work no metric reads.
const rankCandidateDepth = 10

// rankedSyncIDs projects a ranked result set down to the sync IDs the
// order-sensitive metrics compare against EvalCase.ObservationID, preserving
// retrieval order. Rows without a sync ID are skipped rather than emitted as
// empty strings, which would silently occupy a rank position nothing can match.
func rankedSyncIDs(results []store.SearchResult) []string {
	out := make([]string, 0, len(results))
	for _, r := range results {
		if r.SyncID != "" {
			out = append(out, r.SyncID)
		}
	}
	return out
}

// storeBackedFetcher returns an eval.RetrievedFetcher that searches the real
// store for each case's Query and uses the top hit's content (or sync ID,
// for contradiction cases) as the retrieved result — the harness's single
// retrieval seam (see eval.RetrievedFetcher's doc comment).
func storeBackedFetcher(s *store.Store) eval.RetrievedFetcher {
	return func(ctx context.Context, c eval.EvalCase) (eval.RetrievedCase, error) {
		// Limit is rankCandidateDepth, not 1 (#236): scoring stays on the top
		// hit exactly as before, but a single-result fetch cannot express an
		// ORDER, so the order-sensitive metrics had nothing to read. The extra
		// rows are used only to build RankedObservationIDs — Retrieved and the
		// token accounting still come from results[0], so accuracy and
		// quality-per-1k are byte-for-byte what they were.
		results, err := storeSearch(s, c.Query, store.SearchOptions{Limit: rankCandidateDepth})
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
			RankedObservationIDs:  rankedSyncIDs(results),
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

// pipelineFetchLimit mirrors handleSearch's own default candidate-pool size
// (internal/mcp/mcp.go: `limit := intArg(req, "limit", 10)`) so
// pipelineBackedFetcher exercises the SAME batch size a real mem_search call
// would hand to the injection passes below — ApplyTypeLens/ApplyMMR need at
// least 2 candidates to do anything, and ApplyTokenBudget needs a
// realistically-sized batch to demonstrate a trim. storeBackedFetcher's own
// Limit:1 is deliberately NOT reused here: with only 1 candidate, every pass
// below is a trivial no-op and --injection would measure nothing.
const pipelineFetchLimit = 10

// injectionPreviewChars duplicates internal/mcp's own unexported
// tokenBudgetPreviewChars (token_budget.go) — the same "duplicate the
// primitive, document why" convention internal/mcp/recall_ranking.go's
// exactSentinelRank and internal/config's recencyTimeLayouts already use for
// crossing an unexported-boundary. It MUST stay 300, matching handleSearch's
// own preview truncation (`truncate(r.Content, 300)` in mcp.go's display
// loop) so pipelineBackedFetcher's token accounting counts the SAME preview
// basis handleSearch actually renders, not the full (potentially much
// larger) stored Content.
//
// The `truncate` call below (this file, cmd/omnia) is NOT literally
// handleSearch's own truncate — it is cmd/omnia's own separately-maintained
// unexported copy (cmd/omnia/main.go:3269), distinct from
// internal/mcp/mcp.go:3591's unexported truncate that handleSearch itself
// calls. The two currently have byte-identical bodies, but they are two
// same-shaped, cross-package copies (like injectionPreviewChars above), not
// one shared helper — either could drift from the other in a future change
// without the compiler ever noticing.
const injectionPreviewChars = 300

// applyInjectionPipeline re-ranks/trims a raw candidate batch through the
// SAME v0.3 "Context Economy" passes handleSearch applies, in the SAME
// order (internal/mcp/mcp.go's handleSearch, design obs #1643 section 2):
// ApplyTypeLens -> ApplyMMR -> ApplyTokenBudget. query is the case's own
// search query, used only for InferLensType's situational classification;
// explicitType is always "" because eval.EvalCase carries no per-case type
// filter (mirrors handleSearch's typ == "" branch — the lens is free to
// fire). relevance is the caller's own per-ID relevance signal, computed by
// the caller's own retrieval branch (see pipelineBackedFetcher) exactly the
// way handleSearch computes it for each of ITS two branches — RRF fusion
// Score for the hybrid recall path, or negated FTS5 rank for the FTS5-only
// path. It is passed in rather than derived here from each result's own
// Rank field because mcp.HydrateFusedResults does NOT repopulate a
// meaningful Rank for fused rows (only the topic_key sentinel gets Rank set,
// to -1000); deriving relevance from -r.Rank on those rows would silently
// flatten every ordinary fused row's relevance to 0 and break ApplyMMR's
// ranking (review fix, #143 HIGH).
//
// Scope decision (issue #143): only cfg's own sub-gates (TypeLens,
// Diversity, Budget) are wired here. cfg.RecallRanking
// (memory-recall-ranking) and cfg.StructuralForgetting are SEPARATE,
// independently-gated config blocks outside config.InjectionConfig and are
// out of scope for this fetcher. Both default to disabled in production
// exactly like every Injection sub-gate, so this omission has zero effect
// on the shipped default (nothing enabled); it only means an operator who
// has ALSO turned on ranking/structural-forgetting won't see that reflected
// in `--injection` eval numbers. RankResults itself never runs here (out of
// scope), so results stay in the caller's own retrieval order until a pass
// below re-sorts them, same as handleSearch when RecallRanking is disabled
// (the default).
//
// The ApplyTypeLens call — including the InferLensType classifier call
// itself — is gated behind cfg.TypeLens.Enabled, mirroring handleSearch's
// own "the gate guards the CLASSIFIER call too, not just the re-rank" idiom
// (mcp.go comment on its own ApplyTypeLens call site): no regex scan of the
// query runs when type_lens is off. ApplyMMR/ApplyTokenBudget are called
// unconditionally because each is already a gated no-op internally when its
// own cfg.Enabled is false — the same pattern handleSearch itself uses.
//
// Pure and side-effect-free over results (same contract as the three passes
// it composes), so it is directly unit-testable with hand-built
// store.SearchResult fixtures, independent of a real store.
func applyInjectionPipeline(query string, results []store.SearchResult, relevance map[int64]float64, cfg config.InjectionConfig) []store.SearchResult {
	if cfg.TypeLens.Enabled {
		lensType := mcp.InferLensType(query, "")
		results = mcp.ApplyTypeLens(results, lensType, cfg.TypeLens)
	}

	results = mcp.ApplyMMR(results, relevance, cfg.Diversity)
	results = mcp.ApplyTokenBudget(results, cfg.Budget)
	return results
}

// pipelineBackedFetcher returns an eval.RetrievedFetcher (issue #143) that
// wraps the SAME retrieval branch handleSearch itself uses with the v0.3
// Context Economy injection pipeline (applyInjectionPipeline), so
// `omnia eval --injection` measures what an agent would ACTUALLY receive
// with these flags on — not the raw top-1 FTS5 hit storeBackedFetcher always
// scores against.
//
// Retrieval branch (review fix, #143 HIGH): recallSvc mirrors
// handleSearch's own cfg.Recall != nil branch EXACTLY
// (internal/mcp/mcp.go's handleSearch, ~L1183-1224). A non-nil recallSvc
// (recall.enabled=true in the loaded config — see defaultRunEvalHarness,
// which builds it via the SAME buildRecallService cmdMCP calls) routes
// through recall.Service.Search + mcp.HydrateFusedResults, with relevance
// taken from each fused result's own Score (RRF fusion), exactly like
// handleSearch's if-branch. A nil recallSvc (recall disabled or
// unconfigured — the default) falls back to storeSearch + negated FTS5
// Rank as relevance, exactly like handleSearch's else-branch. Without this
// branch, --injection would always measure the FTS5-only candidate pool
// even when hybrid recall is actually configured in production, silently
// scoring a retrieval path production never uses whenever recall is on.
//
// Pre-emption (topic_key sentinel / error-signature lane, spec: Sentinel
// and Signature Pre-Emption Invariant) needs NO special handling in either
// branch. On the FTS5-only side, store.Store.Search's sentinel/signature
// lanes (store.go's topic_key-sentinel block and its "Signature lane"
// block) run UNCONDITIONALLY inside s.Search itself, gated only by the
// query TEXT'S OWN shape (a literal "/" for the topic_key lane; a
// distinctive-enough error-shaped n-gram, >=12 chars/2 tokens, for the
// signature lane) — NOT by any special caller-side "lane" argument
// storeSearch would need to opt into. On the recall side, the topic_key
// sentinel is carried through Fuse/HydrateFusedResults (recall.Result.Exact
// -> store.SearchResult.Rank = -1000), the same sentinel value the FTS5
// branch's rows carry. Either way, a sentinel/signature row CAN reach this
// fetcher exactly as it can reach handleSearch (an eval case's query
// happening to contain "/" or read as an error signature is unlikely for
// the current corpus's natural-language queries, but not impossible for
// future bugfix-flavored cases). It needs no extra code here because
// ApplyTypeLens/ApplyMMR/ApplyTokenBudget each already partition
// Rank==exactSentinelRank/SignatureMatch rows out first and always re-emit
// them untouched (see each function's own doc comment and
// internal/mcp/preemption_invariant_test.go) — this fetcher inherits that
// guarantee for free by calling the same three functions. The FTS5-only
// branch's relevance map deliberately does NOT skip the sentinel row before
// populating relevance (unlike handleSearch's own else-branch, which does)
// — a pre-existing, reviewed-as-harmless divergence: ApplyMMR/ApplyTokenBudget
// both partition the sentinel out before relevance is ever consulted, so
// the extra map entry is inert. This fetcher does not change that.
//
// Token accounting: Tokens.InjectedContext sums token.EstimateTokens over
// EVERY post-pipeline result's preview (truncate(r.Content, 300), the SAME
// basis handleSearch's own display loop and ApplyTokenBudget's own
// previewTokens use) — the full set that would actually be injected, not
// just the top hit. This is intentionally a DIFFERENT quantity than
// storeBackedFetcher's Tokens.Retrieval (a single top-1 heuristic estimate
// via the local, cruder estimateTokenCount): pipelineBackedFetcher answers
// "what would injection actually cost," storeBackedFetcher answers "roughly
// how big was the one snippet scored." See TestPipelineBackedFetcher_Parity*
// for the exact boundary of what stays identical between the two fetchers
// with every injection flag off (Retrieved/SurfacedObservationID — the
// scoring-relevant fields) and what does NOT (Tokens — a deliberately more
// accurate accounting, not a bug).
func pipelineBackedFetcher(s *store.Store, recallSvc *recall.Service, cfg config.InjectionConfig, ranking config.RankingConfig) eval.RetrievedFetcher {
	return func(ctx context.Context, c eval.EvalCase) (eval.RetrievedCase, error) {
		var (
			results   []store.SearchResult
			relevance map[int64]float64
		)

		if recallSvc != nil {
			fused, ferr := recallSvc.Search(ctx, c.Query, recall.LexicalSearchOptions{
				Limit: mcp.RecallFetchLimit(pipelineFetchLimit),
			})
			if ferr != nil {
				return eval.RetrievedCase{}, fmt.Errorf("search: %w", ferr)
			}
			relevance = make(map[int64]float64, len(fused))
			for _, fr := range fused {
				relevance[fr.ID] = fr.Score
			}
			results = mcp.HydrateFusedResults(s, fused, pipelineFetchLimit, mcp.RecallScopeFilter{})
		} else {
			r, err := storeSearch(s, c.Query, store.SearchOptions{Limit: pipelineFetchLimit})
			if err != nil {
				return eval.RetrievedCase{}, fmt.Errorf("search: %w", err)
			}
			results = r
			relevance = make(map[int64]float64, len(r))
			for _, rr := range r {
				relevance[rr.ID] = -rr.Rank
			}
		}

		if len(results) == 0 {
			return eval.RetrievedCase{}, nil
		}

		// #236: RankResults runs BEFORE the injection passes, exactly where
		// handleSearch runs it (internal/mcp/mcp.go — RankResults at 1486,
		// then TypeLens/MMR/Budget). It used to be skipped here as "out of
		// scope", which meant --injection could not reflect a ranking change
		// at all: with the order-sensitive metric added, relevance weight 0
		// and recency at 1000x still produced identical MRR and recall@k,
		// because the pass never ran. A disabled RankingConfig makes
		// RankResults a documented no-op, so the default path is unchanged.
		results = mcp.RankResults(results, relevance, ranking, time.Now().UTC())

		results = applyInjectionPipeline(c.Query, results, relevance, cfg)
		if len(results) == 0 {
			// A genuine outcome, not an error: e.g. a budget too small to fit
			// any eligible row (ApplyTokenBudget's own documented behavior)
			// starves retrieval entirely — treated the same as "no results"
			// above, so scoring correctly registers a miss instead of a
			// panic on results[0] below.
			return eval.RetrievedCase{}, nil
		}

		injectedTokens := 0
		for _, r := range results {
			injectedTokens += token.EstimateTokens(truncate(r.Content, injectionPreviewChars))
		}

		top := results[0]
		return eval.RetrievedCase{
			Retrieved:             top.Content,
			SurfacedObservationID: top.SyncID,
			Tokens:                eval.TokenBreakdown{InjectedContext: injectedTokens},
			// This path already HAD the ranked list — it is the injected
			// context, in the order the agent would receive it — and was
			// discarding everything but results[0] (#236).
			RankedObservationIDs: rankedSyncIDs(results),
		}, nil
	}
}

// ─── Conversational profile (docs/conversational-retrieval-plan.md,
// "Baseline first: extend `omnia eval` with a conversational corpus") ───
//
// Everything below wires eval.ConversationalCase/eval.RunConversationalHarness
// (internal/eval) into the CLI. It is deliberately thin: all scoring logic
// lives in internal/eval, where it is unit-tested against fixtures — this
// file only builds fetchers (requirement 5's "point the harness at either
// GET /search over HTTP or the in-process search path") and formats output.

// conversationalGroundingContextSize bounds how many top-ranked results'
// content the conversational profile assembles into RetrievedCase.Retrieved
// for grounding/honest-refusal scoring — matching what a real consumer of
// GET /search actually assembles before answering, not just the single top
// hit. Grounding is defined, both in the plan
// (docs/conversational-retrieval-plan.md, "Baseline first" item) and in
// this package's own doc comments (internal/eval/conversational_scoring.go:
// "did the retrieved context contain the evidence needed to answer"), over
// the assembled CONTEXT a consumer sees — and the real consumer, Hermes,
// assembles the top 4 results, not just results[0]. 4 is that documented
// number, not an arbitrary choice.
//
// Scoring only results[0] under-reports grounding whenever the fact-bearing
// chunk ranks 2nd-4th instead of 1st. This was measured directly: after
// fixing the repodoc chunk-title bug (internal/source/repodoc/chunk.go's
// DocTitle), README chunks holding the plan's paired facts ranked at
// various depths within the top 4 for several conversational queries, never
// always at rank 1 — a top-1-only grounding check would still have scored
// several of those as "not grounded" despite the evidence being squarely in
// what a real consumer would have received.
//
// This constant affects ONLY grounding (identity kind) and honest refusal
// (absence kind) scoring. accuracy@1 and MRR stay position-based over
// RankedObservationIDs (eval.BuildConversationalReport), which is always
// populated from the FULL ranked candidate list — unaffected by this
// constant.
const conversationalGroundingContextSize = 4

// conversationalGroundingContextSeparator joins each assembled result's
// content in assembleGroundingContext. It is distinctive enough that it
// cannot plausibly appear inside a real observation's Content and blur two
// adjacent chunks together under a factMatches substring check.
const conversationalGroundingContextSeparator = "\n\n---\n\n"

// assembleGroundingContext joins the top conversationalGroundingContextSize
// results' Content into the single string the conversational profile scores
// grounding and honest refusal against (see conversationalGroundingContextSize's
// doc comment for the "why top-N, not top-1" rationale). An empty results
// slice yields "", preserving eval.ScoreAbsence's "the fetcher returned
// zero results" verdict — concatenating nothing is still nothing.
func assembleGroundingContext(results []store.SearchResult) string {
	n := conversationalGroundingContextSize
	if n > len(results) {
		n = len(results)
	}
	parts := make([]string, 0, n)
	for _, r := range results[:n] {
		parts = append(parts, r.Content)
	}
	return strings.Join(parts, conversationalGroundingContextSeparator)
}

// conversationalCorpusPathFlagDefault mirrors evalCorpusPathFlagDefault's
// convention (an empty string means "use the embedded corpus") for the
// conversational profile's own embedded corpus
// (eval.EmbeddedConversationalCorpus).
const conversationalCorpusPathFlagDefault = ""

// loadConversationalCorpus mirrors loadEvalCorpus: an explicit non-empty
// path loads a caller-supplied conversational corpus file; the empty
// default loads the hand-authored seed corpus embedded in the binary
// (testdata/conversational_cases.json — see the corpus's own hard
// requirement that it stay hand-labelled, never mechanically generated).
func loadConversationalCorpus(path string) ([]eval.ConversationalCase, error) {
	if strings.TrimSpace(path) == "" {
		return eval.EmbeddedConversationalCorpus()
	}
	return eval.LoadConversationalCorpus(path)
}

// conversationalRunOptions bundles cmdEval's resolved flags for the
// conversational profile's (injectable) harness-execution seam
// (runConversationalEval) — the conversational sibling of evalRunOptions.
type conversationalRunOptions struct {
	CorpusPath string
	ConfigPath string
	Runs       int
	// Target selects requirement 5's retrieval-path comparison:
	// "inprocess" (default) searches the local store directly (honoring
	// Injection below); "http" calls GET /search on a running server at
	// HTTPBaseURL — the SAME bare endpoint Vel/Hermes call today (plan
	// section 0.1), with no ranking pipeline, no MMR, no token budget.
	// Running both against the same corpus is what quantifies the P0 gap.
	Target      string
	HTTPBaseURL string
	// Injection mirrors evalRunOptions.Injection, but only applies when
	// Target is "inprocess": it swaps conversationalStoreFetcher (raw
	// top-1 FTS5/store search, mirroring GET /search's own default
	// behavior) for conversationalPipelineFetcher (the v0.3 Context
	// Economy injection pipeline, mirroring mem_search's fuller path).
	Injection bool
	// ForceUnscoped reproduces the harness's pre-engram-#2623 behavior:
	// every case's own Project/Unscoped is ignored and every fetcher
	// searches ALL projects regardless of kind. Kept reachable (--force-
	// unscoped) purely for explicit before/after comparison against the
	// scoped numbers, which are the ones that match the real consumer
	// (Hermes' detectProject always scopes to one project) — never the
	// default.
	ForceUnscoped bool
	// AsOf, when non-empty, requests store-isolated recorded-time reads
	// (engram eval/http-search-as-of-isolation — see the --as-of flag's own
	// help text in cmdEval for the full contamination story this fixes).
	// Threaded into BOTH conversationalStoreFetcher (--target inprocess,
	// via store.Store.SearchAsOf) and conversationalHTTPFetcher (--target
	// http, via GET /search?as_of=, internal/server's own isolation
	// branch). Setting this is an explicit request for VERIFIED isolation:
	// defaultRunConversationalEval fails loudly (a returned error, which
	// cmdEval turns into a fatal exit) rather than silently falling back to
	// a live read whenever that verification cannot succeed.
	AsOf string
}

// runConversationalEval is injectable for testing, mirroring runEvalHarness
// — production wiring is defaultRunConversationalEval.
var runConversationalEval = defaultRunConversationalEval

// defaultRunConversationalEval loads the conversational corpus and builds a
// ConversationalFetcher per opts.Target, then runs it through
// eval.RunConversationalHarness for the same [MinRuns,MaxRuns]
// reproducibility discipline the coding profile already enforces.
//
// opts.AsOf (engram eval/http-search-as-of-isolation) is an explicit request
// for VERIFIED store isolation — see the --as-of flag's own help text
// (cmdEval) for the contamination bug this fixes. It is validated up front,
// before any fetcher is built: --target=answer has no as-of support (GET
// /answer was not touched by this fix — out of scope, see its own doc), and
// --injection has no as-of support either (the injection pipeline's
// recall.Service leg has no historical embeddings index — recorded-time
// reads are FTS5-only everywhere in this codebase, matching mem_search's own
// as_of branch). Both combinations fail loudly here rather than silently
// ignoring --as-of and measuring a live (unisolated) store.
func defaultRunConversationalEval(ctx context.Context, opts conversationalRunOptions) (eval.ConversationalRunSummary, error) {
	cases, err := loadConversationalCorpus(opts.CorpusPath)
	if err != nil {
		return eval.ConversationalRunSummary{}, fmt.Errorf("load conversational corpus: %w", err)
	}

	asOf := strings.TrimSpace(opts.AsOf)
	target := strings.ToLower(strings.TrimSpace(opts.Target))

	if asOf != "" && opts.Injection {
		return eval.ConversationalRunSummary{}, fmt.Errorf("conversational eval: --as-of is incompatible with --injection: recorded-time reads bypass the ranking/recall pipeline (no historical embeddings index exists), so injection scoring would silently ignore --as-of")
	}
	if asOf != "" && target == "answer" {
		return eval.ConversationalRunSummary{}, fmt.Errorf("conversational eval: --as-of is not supported with --target=answer (GET /answer has no recorded-time isolation seam — only GET /search and --target inprocess do)")
	}

	switch target {
	case "http":
		if strings.TrimSpace(opts.HTTPBaseURL) == "" {
			return eval.ConversationalRunSummary{}, fmt.Errorf("conversational eval: --http-base-url is required when --target=http")
		}
		fetch := conversationalHTTPFetcher(&http.Client{Timeout: 30 * time.Second}, opts.HTTPBaseURL, opts.ForceUnscoped, asOf)
		return runConversationalCases(ctx, cases, fetch, opts.Runs)

	case "answer":
		// P3 (docs/conversational-retrieval-plan.md "Answer-shaped context
		// endpoint"): the ONLY target that can score false_confidence/
		// false_refusal against GET /answer's own calibrated confidence
		// signal — inprocess/http both predate P3 and have no such signal
		// to read (see RetrievedCase.Confidence's own doc).
		if strings.TrimSpace(opts.HTTPBaseURL) == "" {
			return eval.ConversationalRunSummary{}, fmt.Errorf("conversational eval: --http-base-url is required when --target=answer")
		}
		fetch := conversationalAnswerFetcher(&http.Client{Timeout: 30 * time.Second}, opts.HTTPBaseURL, opts.ForceUnscoped)
		return runConversationalCases(ctx, cases, fetch, opts.Runs)

	case "inprocess", "":
		cfg, err := store.DefaultConfig()
		if err != nil {
			return eval.ConversationalRunSummary{}, fmt.Errorf("resolve store config: %w", err)
		}
		appCfg, appCfgErr := config.Load(opts.ConfigPath)
		if appCfgErr != nil {
			appCfg = nil
		}
		s, err := storeNew(evalStoreConfig(cfg, appCfg))
		if err != nil {
			return eval.ConversationalRunSummary{}, fmt.Errorf("open store: %w", err)
		}
		defer s.Close()

		if asOf != "" && !s.TimeTravelEnabled() {
			return eval.ConversationalRunSummary{}, fmt.Errorf("conversational eval: --as-of requested but time_travel is not enabled on this store; recorded-time isolation cannot be guaranteed (see time_travel.enabled in config.yaml) — refusing to silently measure a live, unisolated store")
		}

		var fetch eval.ConversationalFetcher
		if opts.Injection && appCfgErr == nil {
			recallSvc := buildRecallService(s, appCfg.Recall, appCfg.Embeddings, cfg.DataDir, appCfg.VecIndex.Enabled, appCfg.Encryption)
			fetch = conversationalPipelineFetcher(s, recallSvc, appCfg.Injection, appCfg.Recall.Ranking, opts.ForceUnscoped)
		} else {
			fetch = conversationalStoreFetcher(s, opts.ForceUnscoped, asOf)
		}
		return runConversationalCases(ctx, cases, fetch, opts.Runs)

	default:
		return eval.ConversationalRunSummary{}, fmt.Errorf("conversational eval: --target must be %q, %q or %q, got %q", "inprocess", "http", "answer", opts.Target)
	}
}

// runConversationalCases wraps eval.RunOnceConversational as a
// eval.ConversationalRunFunc and hands it to eval.RunConversationalHarness —
// shared tail for both branches of defaultRunConversationalEval.
func runConversationalCases(ctx context.Context, cases []eval.ConversationalCase, fetch eval.ConversationalFetcher, runs int) (eval.ConversationalRunSummary, error) {
	runFunc := func(ctx context.Context) (eval.ConversationalReport, error) {
		return eval.RunOnceConversational(ctx, cases, fetch)
	}
	return eval.RunConversationalHarness(ctx, runFunc, runs)
}

// conversationalCaseUnscoped reports whether fetching c should search EVERY
// project rather than being scoped to c.Project (engram #2623) — true when
// the corpus case itself declares kind cross_project (Unscoped) or the
// caller passed --force-unscoped to reproduce the harness's pre-fix
// all-projects-always behavior for explicit before/after comparison.
func conversationalCaseUnscoped(c eval.ConversationalCase, forceUnscoped bool) bool {
	return forceUnscoped || c.Unscoped
}

// conversationalStoreFetcher returns an eval.ConversationalFetcher that
// searches the real store directly for each case's Query — the
// conversational sibling of storeBackedFetcher (raw top-1 FTS5/store
// search, same Limit and same rankedSyncIDs projection), byte-for-byte the
// same retrieval GET /search runs when no SearchFunc override is wired
// (internal/server/server.go's handleSearch: `search := s.store.Search`
// unless SetSearch was called) — i.e. this is the in-process equivalent of
// today's default HTTP behavior, useful as a baseline distinct from
// conversationalPipelineFetcher's fuller mem_search-equivalent path.
//
// forceUnscoped, when true, overrides every case's own Project/Unscoped and
// always searches all projects — see conversationalCaseUnscoped.
//
// asOf, when non-empty (engram eval/http-search-as-of-isolation), routes
// every case through s.SearchAsOf instead of storeSearch (s.Search) — the
// store-isolation seam: the caller (defaultRunConversationalEval) has
// already verified s.TimeTravelEnabled() before this fetcher is ever built,
// so a non-empty asOf here is guaranteed to actually apply rather than
// silently degrade to a live read (see store.SearchAsOf's own doc for that
// degrade behavior, which is why the caller checks first).
func conversationalStoreFetcher(s *store.Store, forceUnscoped bool, asOf string) eval.ConversationalFetcher {
	return func(ctx context.Context, c eval.ConversationalCase) (eval.RetrievedCase, error) {
		opts := store.SearchOptions{Limit: rankCandidateDepth}
		if !conversationalCaseUnscoped(c, forceUnscoped) {
			opts.Project = c.Project
		}
		var results []store.SearchResult
		var err error
		if asOf != "" {
			results, err = s.SearchAsOf(c.Query, opts, asOf)
		} else {
			results, err = storeSearch(s, c.Query, opts)
		}
		if err != nil {
			return eval.RetrievedCase{}, fmt.Errorf("search: %w", err)
		}
		if len(results) == 0 {
			return eval.RetrievedCase{}, nil
		}
		top := results[0]
		return eval.RetrievedCase{
			// #P1/#bug: grounding must be scored over the assembled top-N
			// context a real consumer sees, not just the top hit — see
			// conversationalGroundingContextSize's doc comment.
			Retrieved:             assembleGroundingContext(results),
			SurfacedObservationID: top.SyncID,
			Tokens:                eval.TokenBreakdown{Retrieval: estimateTokenCount(top.Content)},
			RankedObservationIDs:  rankedSyncIDs(results),
		}, nil
	}
}

// conversationalPipelineFetcher returns an eval.ConversationalFetcher that
// scores against the SAME v0.3 Context Economy injection pipeline
// pipelineBackedFetcher uses for the coding profile (RankResults ->
// ApplyTypeLens -> ApplyMMR -> ApplyTokenBudget, with the same
// recall-vs-FTS5 branch as handleSearch — see pipelineBackedFetcher's own
// doc comment for the full parity argument, which applies here unchanged).
// Kept as its own function rather than sharing pipelineBackedFetcher's
// closure because the two are keyed by different case types
// (eval.EvalCase vs eval.ConversationalCase); the body below intentionally
// mirrors pipelineBackedFetcher's structure line-for-line so the two stay
// easy to diff against each other if one changes.
// forceUnscoped mirrors conversationalStoreFetcher's flag of the same name
// — see conversationalCaseUnscoped.
func conversationalPipelineFetcher(s *store.Store, recallSvc *recall.Service, cfg config.InjectionConfig, ranking config.RankingConfig, forceUnscoped bool) eval.ConversationalFetcher {
	return func(ctx context.Context, c eval.ConversationalCase) (eval.RetrievedCase, error) {
		var (
			results   []store.SearchResult
			relevance map[int64]float64
		)

		scopeProject := ""
		if !conversationalCaseUnscoped(c, forceUnscoped) {
			scopeProject = c.Project
		}

		if recallSvc != nil {
			fused, ferr := recallSvc.Search(ctx, c.Query, recall.LexicalSearchOptions{
				Project: scopeProject,
				Limit:   mcp.RecallFetchLimit(pipelineFetchLimit),
			})
			if ferr != nil {
				return eval.RetrievedCase{}, fmt.Errorf("search: %w", ferr)
			}
			relevance = make(map[int64]float64, len(fused))
			for _, fr := range fused {
				relevance[fr.ID] = fr.Score
			}
			// RecallScopeFilter re-checks Project at hydration time (its own
			// doc: the semantic side has no project awareness pre-fusion), so
			// it must carry the same scope the lexical leg above was given —
			// otherwise a cross-project semantic neighbor could leak back in.
			results = mcp.HydrateFusedResults(s, fused, pipelineFetchLimit, mcp.RecallScopeFilter{Project: scopeProject})
		} else {
			r, err := storeSearch(s, c.Query, store.SearchOptions{Project: scopeProject, Limit: pipelineFetchLimit})
			if err != nil {
				return eval.RetrievedCase{}, fmt.Errorf("search: %w", err)
			}
			results = r
			relevance = make(map[int64]float64, len(r))
			for _, rr := range r {
				relevance[rr.ID] = -rr.Rank
			}
		}

		if len(results) == 0 {
			return eval.RetrievedCase{}, nil
		}

		results = mcp.RankResults(results, relevance, ranking, time.Now().UTC())
		results = applyInjectionPipeline(c.Query, results, relevance, cfg)
		if len(results) == 0 {
			return eval.RetrievedCase{}, nil
		}

		injectedTokens := 0
		for _, r := range results {
			injectedTokens += token.EstimateTokens(truncate(r.Content, injectionPreviewChars))
		}

		top := results[0]
		return eval.RetrievedCase{
			// See conversationalGroundingContextSize's doc comment: grounding
			// must be scored over the assembled top-N context, not top-1.
			Retrieved:             assembleGroundingContext(results),
			SurfacedObservationID: top.SyncID,
			Tokens:                eval.TokenBreakdown{InjectedContext: injectedTokens},
			RankedObservationIDs:  rankedSyncIDs(results),
		}, nil
	}
}

// conversationalHTTPFetcher returns an eval.ConversationalFetcher that
// queries a running Omnia server's GET /search endpoint (requirement 5):
// the OTHER half of the "GET /search over HTTP vs the in-process search
// path" comparison this baseline item exists to produce. It talks to the
// SAME bare endpoint Vel/Hermes call today (docs/
// conversational-retrieval-plan.md section 0.1: "GET /search bypasses
// Omnia's entire ranking pipeline") — no injection pipeline, no MMR, no
// token budget, exactly what a real HTTP client receives — so comparing its
// report against conversationalStoreFetcher's or
// conversationalPipelineFetcher's (both in-process) on the SAME corpus is
// what quantifies the P0 gap.
//
// baseURL is the server's origin, e.g. "http://localhost:7799" — a trailing
// slash is tolerated and stripped.
//
// Every request scopes to c.Project via the server's `project=` query param
// (internal/server/server.go's resolveProjectFanoutParams), matching the
// real consumer: Hermes' detectProject always latches onto exactly one
// project before searching. A case whose Kind is cross_project (or when
// forceUnscoped is true) instead sends `all_projects=1`, searching every
// project — see conversationalCaseUnscoped. Before this fix (engram #2623)
// neither param was ever sent, so every conversational number this fetcher
// produced was silently unscoped.
//
// asOf, when non-empty (engram eval/http-search-as-of-isolation), sends
// `as_of=` AND `envelope=1` instead of the plain bare-array request — the
// server's own as_of branch (internal/server's searchRecordedTime) can only
// be verified by reading its echoed SearchEnvelope.AsOf back, and the
// back-compat bare-array shape has no field to carry that echo. This
// fetcher then VERIFIES the echo matches exactly what was requested before
// trusting a single result: a mismatch (empty AsOf, wrong value, or an old
// server binary that doesn't understand as_of at all and silently ran a
// live search instead) is a hard error, not a degraded-but-tolerated
// result — the entire point of this parameter is that the caller must be
// able to trust isolation held, not hope it did.
func conversationalHTTPFetcher(client *http.Client, baseURL string, forceUnscoped bool, asOf string) eval.ConversationalFetcher {
	return func(ctx context.Context, c eval.ConversationalCase) (eval.RetrievedCase, error) {
		u, err := url.Parse(strings.TrimRight(baseURL, "/") + "/search")
		if err != nil {
			return eval.RetrievedCase{}, fmt.Errorf("conversationalHTTPFetcher: parse base url %q: %w", baseURL, err)
		}
		q := u.Query()
		q.Set("q", c.Query)
		q.Set("limit", strconv.Itoa(rankCandidateDepth))
		if conversationalCaseUnscoped(c, forceUnscoped) {
			q.Set("all_projects", "1")
		} else {
			q.Set("project", c.Project)
		}
		if asOf != "" {
			q.Set("as_of", asOf)
			q.Set("envelope", "1")
		}
		u.RawQuery = q.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return eval.RetrievedCase{}, fmt.Errorf("conversationalHTTPFetcher: build request: %w", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return eval.RetrievedCase{}, fmt.Errorf("conversationalHTTPFetcher: GET %s: %w", u.String(), err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return eval.RetrievedCase{}, fmt.Errorf("conversationalHTTPFetcher: GET %s: status %d: %s", u.String(), resp.StatusCode, strings.TrimSpace(string(body)))
		}

		var results []store.SearchResult
		if asOf != "" {
			// envelope=1 shape (searchEnvelopeHTTPResponse below) — decoded
			// separately from the plain-request branch because the bare-array
			// shape carries no as_of echo to verify against.
			var envelope searchEnvelopeHTTPResponse
			if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
				return eval.RetrievedCase{}, fmt.Errorf("conversationalHTTPFetcher: decode envelope response: %w", err)
			}
			if envelope.AsOf != asOf {
				return eval.RetrievedCase{}, fmt.Errorf("conversationalHTTPFetcher: isolation NOT verified — requested as_of=%q but the server echoed as_of=%q (empty means it silently ran a live search instead, e.g. an older server binary with no as_of support); refusing to trust this store as isolated", asOf, envelope.AsOf)
			}
			results = envelope.Results
		} else {
			// GET /search's default response body is ([]store.SearchResult)
			// whether or not the server has a SearchFunc override wired
			// (internal/server/server.go's handleSearch doc comment: "the JSON
			// response shape ([]store.SearchResult) is identical either way") —
			// decoding into the real store type here, not a hand-rolled shadow
			// struct, keeps this fetcher from silently drifting out of sync
			// with the server's actual response shape.
			if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
				return eval.RetrievedCase{}, fmt.Errorf("conversationalHTTPFetcher: decode response: %w", err)
			}
		}
		if len(results) == 0 {
			return eval.RetrievedCase{}, nil
		}
		return eval.RetrievedCase{
			// See conversationalGroundingContextSize's doc comment: grounding
			// must be scored over the assembled top-N context a real
			// consumer (Hermes) would receive, not just results[0] — this
			// was the bug: grounding under-reported because only the single
			// top hit was ever checked for the expected fact.
			Retrieved:             assembleGroundingContext(results),
			SurfacedObservationID: results[0].SyncID,
			Tokens:                eval.TokenBreakdown{Retrieval: estimateTokenCount(results[0].Content)},
			RankedObservationIDs:  rankedSyncIDs(results),
		}, nil
	}
}

// searchEnvelopeHTTPResponse is conversationalHTTPFetcher's own shadow of
// GET /search?envelope=1's wire shape (internal/server's searchEnvelopeJSON)
// — the SAME "decode over HTTP into a local shadow struct" convention
// answerHTTPResponse below already follows for GET /answer, not by
// importing internal/server's type directly. Only the two fields this
// fetcher actually needs are declared: the results and the as_of echo it
// verifies against.
type searchEnvelopeHTTPResponse struct {
	Results []store.SearchResult `json:"results"`
	AsOf    string               `json:"as_of"`
}

// answerHTTPResponse mirrors server.AnswerResponse's JSON shape (P3) — a
// local shadow struct, not the real type, because cmd/omnia's eval fetchers
// decode over HTTP the same way conversationalHTTPFetcher already does for
// GET /search's response, never by importing internal/server's Go type
// directly into a JSON-shaped local decode.
type answerHTTPResponse struct {
	Context    string `json:"context"`
	Confidence string `json:"confidence"`
	Intent     string `json:"intent"`
	Sources    []struct {
		SyncID    string `json:"sync_id"`
		Title     string `json:"title"`
		Type      string `json:"type"`
		UpdatedAt string `json:"updated_at"`
	} `json:"sources"`
	Degraded bool `json:"degraded"`
}

// conversationalAnswerFetcher returns an eval.ConversationalFetcher that
// queries a running Omnia server's GET /answer endpoint (P3) — the ONLY
// fetcher that can measure P3's own two target metrics: false_confidence on
// absence cases (a "none" confidence counts as an honest refusal) and
// false_refusal on identity/status cases (a "none" confidence on a
// question the corpus guarantees has evidence counts as a false refusal).
// See RetrievedCase.Confidence's own doc for why this is the one fetcher
// that populates that field.
//
// Unlike conversationalHTTPFetcher, RankedObservationIDs here comes from
// AnswerResponse.Sources — the citations GET /answer ACTUALLY assembled
// into Context, in the same order — not from a broader raw candidate list,
// because P3's own anti-goal is exactly a response that cites more than it
// assembled (or cites the wrong identifier — sync_id here, never an
// integer id, matching AnswerSource's own contract).
// forceUnscoped mirrors conversationalHTTPFetcher's flag of the same name —
// see conversationalCaseUnscoped and that function's doc comment for the
// project/all_projects scoping rule, which applies identically here.
func conversationalAnswerFetcher(client *http.Client, baseURL string, forceUnscoped bool) eval.ConversationalFetcher {
	return func(ctx context.Context, c eval.ConversationalCase) (eval.RetrievedCase, error) {
		u, err := url.Parse(strings.TrimRight(baseURL, "/") + "/answer")
		if err != nil {
			return eval.RetrievedCase{}, fmt.Errorf("conversationalAnswerFetcher: parse base url %q: %w", baseURL, err)
		}
		q := u.Query()
		q.Set("q", c.Query)
		q.Set("limit", strconv.Itoa(rankCandidateDepth))
		if conversationalCaseUnscoped(c, forceUnscoped) {
			q.Set("all_projects", "1")
		} else {
			q.Set("project", c.Project)
		}
		u.RawQuery = q.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return eval.RetrievedCase{}, fmt.Errorf("conversationalAnswerFetcher: build request: %w", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return eval.RetrievedCase{}, fmt.Errorf("conversationalAnswerFetcher: GET %s: %w", u.String(), err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return eval.RetrievedCase{}, fmt.Errorf("conversationalAnswerFetcher: GET %s: status %d", u.String(), resp.StatusCode)
		}

		var body answerHTTPResponse
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return eval.RetrievedCase{}, fmt.Errorf("conversationalAnswerFetcher: decode response: %w", err)
		}

		ranked := make([]string, 0, len(body.Sources))
		for _, src := range body.Sources {
			if src.SyncID != "" {
				ranked = append(ranked, src.SyncID)
			}
		}
		var surfaced string
		if len(ranked) > 0 {
			surfaced = ranked[0]
		}

		return eval.RetrievedCase{
			Retrieved:             body.Context,
			SurfacedObservationID: surfaced,
			Confidence:            body.Confidence,
			Tokens:                eval.TokenBreakdown{Retrieval: estimateTokenCount(body.Context)},
			RankedObservationIDs:  ranked,
		}, nil
	}
}

// printConversationalSummary is printEvalSummary's conversational sibling:
// requirement 2's "never just one aggregate" per-kind report, plus
// requirement 3's honest-refusal/false-confidence pair for the absence
// kind and requirement 2's grounding rate for identity.
func printConversationalSummary(summary eval.ConversationalRunSummary) {
	fmt.Printf("Conversational Eval Harness Summary (%d reproducibility runs)\n", summary.Runs)
	fmt.Println("docs/conversational-retrieval-plan.md — \"Baseline first\" item")
	fmt.Println()
	fmt.Println("By question kind:")
	for _, k := range eval.AllQuestionKinds {
		s := summary.ByKind[k]
		fmt.Printf("  %-14s accuracy@1=%.3f±%.3f  MRR=%.3f±%.3f\n",
			k, s.AccuracyAt1.Mean, s.AccuracyAt1.StdDev, s.MRR.Mean, s.MRR.StdDev)
		switch k {
		case eval.KindIdentity:
			fmt.Printf("  %-14s grounding=%.3f±%.3f (requirement 2: did retrieved context contain the evidence needed to answer)\n",
				"", s.GroundingRate.Mean, s.GroundingRate.StdDev)
			fmt.Printf("  %-14s false_refusal=%.3f±%.3f (plan P3 target: ≤0.05 — --target answer only, see RetrievedCase.Confidence)\n",
				"", s.FalseRefusalRate.Mean, s.FalseRefusalRate.StdDev)
		case eval.KindStatus:
			fmt.Printf("  %-14s false_refusal=%.3f±%.3f (plan P3 target: ≤0.05 — --target answer only, see RetrievedCase.Confidence)\n",
				"", s.FalseRefusalRate.Mean, s.FalseRefusalRate.StdDev)
		case eval.KindAbsence:
			fmt.Printf("  %-14s honest_refusal=%.3f±%.3f  false_confidence=%.3f (requirement 3 / plan P3 target: ≤0.1)\n",
				"", s.HonestRefusalRate.Mean, s.HonestRefusalRate.StdDev, 1-s.HonestRefusalRate.Mean)
		}
	}
}

// sessionStartHint returns an RFC3339 timestamp a few hours back, offered in
// the isolation-required error as a starting point rather than a default.
//
// It is deliberately NOT applied automatically. A cutoff is a real choice with
// a real cost in both directions: too recent and the session's own notes are
// still included; too early and legitimate evidence is excluded along with
// them, which was measured (delta fell from 0.700 to 0.300 at a cutoff chosen
// for safety margin). Only the operator knows when their session actually
// began writing, so the tool proposes and the operator decides.
func sessionStartHint() string {
	return time.Now().UTC().Add(-6 * time.Hour).Format(time.RFC3339)
}
