package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/embed"
	"github.com/velion/omnia/internal/mcp"
	"github.com/velion/omnia/internal/ranker"
	"github.com/velion/omnia/internal/recall"
	"github.com/velion/omnia/internal/server"
	"github.com/velion/omnia/internal/store"
)

// ollamaProbeTimeout bounds the Ollama reachability probe
// maybeAutoDetectRecall runs at startup. Short and non-configurable on
// purpose: this only ever runs once per `omnia serve`/`omnia mcp` startup,
// never per-request, so a slow/absent Ollama should fail fast rather than
// delay startup noticeably.
const ollamaProbeTimeout = 300 * time.Millisecond

// maybeAutoDetectRecall implements issue #83's Ollama auto-detect: when the
// operator never expressed an opinion on `recall.enabled` at all (Config.
// RecallEnabledExplicit is false) AND embeddings are enabled, it probes
// Ollama once and decides the default — enabling recall if reachable,
// leaving it FTS-only (with one clear stderr note) otherwise.
//
// It deliberately does NOT hard-default recall.enabled to true globally:
// that would break/slow every install without Ollama running, including
// ones that never opted into embeddings at all. The two gates below keep
// this scoped to exactly the case issue #83 describes:
//
//   - cfg.RecallEnabledExplicit: an explicit `enabled: true` or `enabled:
//     false` in config.yaml is a deliberate operator choice and is NEVER
//     overridden — the probe only runs when the key was never mentioned.
//   - cfg.Embeddings.Enabled: embeddings are opt-in and disabled by
//     default (EmbeddingsConfig's doc); a fresh install that hasn't
//     touched embeddings.enabled at all gets zero network calls and zero
//     new stderr output from this function — byte-for-byte the pre-#83
//     silent default.
//
// The probe itself (ollamaReachable) is cheap (a single short-timeout GET)
// and never fails the caller: any error, timeout, or non-2xx response is
// treated as "not reachable," logged once, and recall stays off.
func maybeAutoDetectRecall(cfg *config.Config) {
	if cfg.RecallEnabledExplicit || !cfg.Embeddings.Enabled {
		return
	}
	if ollamaReachable(cfg.Embeddings.BaseURL, ollamaProbeTimeout) {
		cfg.Recall.Enabled = true
		log.Printf("[recall] semantic recall auto-enabled: Ollama reachable at %s (set recall.enabled: false in config.yaml to opt out)", cfg.Embeddings.BaseURL)
		return
	}
	log.Printf("[recall] semantic recall disabled: Ollama not reachable at %s (mem_search stays FTS5-only; set recall.enabled: true in config.yaml to force it on once Ollama is running)", cfg.Embeddings.BaseURL)
}

// ollamaReachable reports whether the Ollama server at baseURL answers a
// GET to its /api/tags endpoint (the standard "list local models" route)
// within timeout. Any transport error, timeout, or non-2xx status is
// treated as unreachable — this is a best-effort liveness probe, not a
// model-availability check.
func ollamaReachable(baseURL string, timeout time.Duration) bool {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/api/tags", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// loadAppConfigWithRecallAutodetect loads config.yaml (the path named by a
// global --config/-config argument, else config.DefaultPath()) and runs the
// Ollama auto-detect (issue #83, maybeAutoDetectRecall) so the
// returned Config already reflects any auto-enabled recall.enabled. It is a
// var (not a plain func), matching this file's storeNew/newHTTPServer
// injection convention, so tests can stub it instead of touching the real
// config file on disk.
//
// This is the single shared "read config + auto-detect" seam issue #86 asks
// for: cmdMCP, cmdServe, and cmdSearch all call this exact function before
// building their recall.Service via buildRecallService, so all three of
// Omnia's search surfaces (MCP mem_search, HTTP GET /search, `omnia search`)
// stay wired identically and can never silently diverge on how recall gets
// enabled. Returns (nil, err) when the config file is missing/unparseable —
// callers degrade to FTS5-only search / no auto-embed, exactly like every
// other `omnia` subcommand's config.Load graceful-degradation convention.
// globalConfigPath(os.Args) rather than config.DefaultPath(): every surface
// that reads config through this seam (cmdMCP, cmdServe, cmdSearch) accepts a
// global `--config PATH` argument, and hardcoding the default path here made
// that argument silently do nothing — `omnia serve 7801 --config other.yaml`
// loaded $HOME/.config/omnia/config.yaml anyway, so an operator (or a
// measurement run) could not point a server at an alternate config at all.
// globalConfigPath already falls back to config.DefaultPath() when no such
// argument is present, so the no-argument behavior is unchanged.
var loadAppConfigWithRecallAutodetect = func() (*config.Config, error) {
	appCfg, err := config.Load(globalConfigPath(os.Args))
	if err != nil {
		return nil, err
	}
	maybeAutoDetectRecall(appCfg)
	return appCfg, nil
}

// buildRecallServiceForCLI is the cmdSearch/cmdServe counterpart of cmdMCP's
// inline recall wiring (main.go's cmdMCP, ~L1109-1131): it loads config via
// loadAppConfigWithRecallAutodetect and builds the recall.Service through the
// same buildRecallService used by MCP, so `omnia search` and HTTP GET
// /search see the exact same recall.enabled/embeddings gating mem_search
// does (issue #86). Returns nil on any graceful-degradation path — missing
// config, recall disabled, or an unavailable embeddings store — never an
// error, so callers can unconditionally fall back to the FTS5-only search
// path when this returns nil.
func buildRecallServiceForCLI(s *store.Store, dataDir string) *recall.Service {
	appCfg, err := loadAppConfigWithRecallAutodetect()
	if err != nil {
		return nil
	}
	return buildRecallService(s, appCfg.Recall, appCfg.Embeddings, dataDir, appCfg.VecIndex.Enabled, appCfg.Encryption)
}

// recallOrFTSSearch is the shared search-routing seam between `omnia search`
// (cmdSearch) and `omnia serve`'s HTTP GET /search (internal/server.Server,
// wired via SetSearch in cmdServe) — issue #86's "avoid divergence" ask.
//
// When recallSvc is non-nil, the query is routed through recall.Service.Search
// (the same hybrid lexical+semantic fusion mem_search uses) and the fused,
// ranked ID list is hydrated back into full store.SearchResult rows via
// mcp.HydrateFusedResults/mcp.RecallFetchLimit/mcp.RecallScopeFilter — the
// exact same "rank then hydrate" glue the MCP path uses, reused here instead
// of re-implemented, so the two paths can't drift apart.
//
// Graceful fallback (never crashes, never hangs): a nil recallSvc (recall
// disabled, config missing, Ollama unreachable at startup, or the
// embeddings store unavailable — all handled upstream by
// buildRecallServiceForCLI) falls back to storeSearch immediately. A
// recallSvc.Search error (the lexical leg failing, e.g. a malformed FTS5
// query) also falls back to storeSearch, which will surface the same
// underlying error — matching pre-existing FTS-only error behavior exactly.
// Ollama being unreachable AT QUERY TIME (as opposed to startup) is already
// handled inside recall.Service itself (semanticHits swallows the error and
// degrades to lexical-only) — no extra timeout/hang handling is needed here
// beyond what mem_search already relies on.
// recallQueryTimeout bounds a single recall query. A query-time embedding call
// goes to Ollama (60s client ceiling); without this bound a reachable-but-stalled
// Ollama could block a synchronous caller (e.g. GET /search) for up to a minute.
// A normal query embed is sub-second, so this only ever trips on a real stall,
// after which we fall back to FTS.
const recallQueryTimeout = 5 * time.Second

func recallOrFTSSearch(ctx context.Context, s *store.Store, recallSvc *recall.Service, query string, opts store.SearchOptions) ([]store.SearchResult, error) {
	results, _, _, err := recallOrFTSSearchWithRelevance(ctx, s, recallSvc, query, opts)
	return results, err
}

// cliExactSentinelRank mirrors internal/store's unexported
// topicKeySentinelRank (-1000), the same cross-package sentinel contract
// internal/mcp/recall_adapter.go and internal/mcp/recall_ranking.go already
// rely on under their own names.
const cliExactSentinelRank = -1000.0

// recallOrFTSSearchWithRelevance is recallOrFTSSearch's superset: alongside
// the same hydrated results, it also returns each result's un-normalized
// relevance signal keyed by Observation.ID — RRF fusion Score for the hybrid
// recall path, or negated FTS5 bm25 Rank for the FTS5-only path (mirroring
// internal/mcp's handleSearch wiring exactly) — and fusionRan, which reports
// whether RRF fusion ACTUALLY produced these results this call, as opposed to
// merely "a recall.Service was configured" — so `omnia search --explain`
// (task 5.3) can build a score breakdown consistent with mem_search's own
// explain surface without re-deriving or duplicating the fusion/hydration
// logic itself.
//
// fusionRan is deliberately a return value rather than something the caller
// derives from `recallSvc != nil`: recallSvc.Search can error mid-query (the
// lexical leg failing, e.g. a malformed FTS5 query) and this function
// gracefully falls back to storeSearch — a real recall.Service was
// configured, but fusion did NOT run for this particular query. A caller
// that used `recallSvc != nil` to decide fusion-vs-lexical labeling would
// mislabel that fallback's plain lexical relevance as a fusion score
// (blocking fix: --explain mislabels fusion vs lexical on mid-query FTS5
// fallback). recallOrFTSSearch delegates to this so existing callers keep
// their exact original two-value signature.
func recallOrFTSSearchWithRelevance(ctx context.Context, s *store.Store, recallSvc *recall.Service, query string, opts store.SearchOptions) ([]store.SearchResult, map[int64]float64, bool, error) {
	if recallSvc == nil {
		results, err := storeSearch(s, query, opts)
		return results, lexicalRelevance(results), false, err
	}

	// Normalize the project exactly like the store does (mcp.go does this too):
	// the semantic leg exact-matches on the normalized project name stored in the
	// embeddings table, so a differently-cased --project value would silently drop
	// semantic hits without this. Idempotent for the FTS fallback below.
	opts.Project, _ = store.NormalizeProject(opts.Project)

	sctx, cancel := context.WithTimeout(ctx, recallQueryTimeout)
	defer cancel()

	fused, err := recallSvc.Search(sctx, query, recall.LexicalSearchOptions{
		Type:    opts.Type,
		Project: opts.Project,
		Scope:   opts.Scope,
		Limit:   mcp.RecallFetchLimit(opts.Limit),
	})
	if err != nil {
		results, serr := storeSearch(s, query, opts)
		return results, lexicalRelevance(results), false, serr
	}

	relevance := make(map[int64]float64, len(fused))
	for _, fr := range fused {
		relevance[fr.ID] = fr.Score
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}
	results := mcp.HydrateFusedResults(s, fused, limit, mcp.RecallScopeFilter{
		Type:    opts.Type,
		Project: opts.Project,
		Scope:   opts.Scope,
	})
	return results, relevance, true, nil
}

// lexicalRelevance builds the FTS5-only-path relevance map: negated bm25
// Rank (a small negative "cost" — negating it makes a better match a larger
// positive relevance, matching the fused path's "higher is better"
// convention), skipping the topic_key exact-match sentinel entirely (it
// always pre-empts ranking, mirroring recall.Fuse/RankResults).
func lexicalRelevance(results []store.SearchResult) map[int64]float64 {
	relevance := make(map[int64]float64, len(results))
	for _, r := range results {
		if r.Rank == cliExactSentinelRank {
			continue
		}
		relevance[r.ID] = -r.Rank
	}
	return relevance
}

// loadRankingConfigForCLI loads just the recall.ranking.* config for `omnia
// search --explain` (task 5.4), independent of buildRecallServiceForCLI's
// own config load. Mirrors that function's graceful degradation: any load
// error yields the zero-value RankingConfig (ranking disabled, matching
// config.Load's own D7-style default), never a fatal error for a CLI search.
func loadRankingConfigForCLI() config.RankingConfig {
	appCfg, err := loadAppConfigWithRecallAutodetect()
	if err != nil {
		return config.RankingConfig{}
	}
	return appCfg.Recall.Ranking
}

// buildRecallService constructs the recall.Service wired into
// mcp.MCPConfig.Recall for `omnia mcp` (human-like-memory PR3, design
// D6/D7).
//
// recallCfg.Enabled is the SOLE gate (task 3.6, rollback safety): when
// false (the default), this returns nil WITHOUT opening the embeddings
// store or constructing an Ollama HTTP client. handleSearch's cfg.Recall ==
// nil branch then calls store.Search directly, exactly as it did before
// PR3, so the default path stays byte-for-byte today's FTS5-only behavior
// with zero new dependency touched (D7 rollback guarantee).
//
// embeddings.enabled is intentionally NOT consulted here: per
// config.go's RecallConfig doc, `recall: { enabled: true }` alone is meant
// to opt an operator in to the proven D1/D2 fusion defaults. If Ollama or
// the embeddings store are unreachable at query time (rather than at
// startup), recall.Service.Search already degrades to lexical-only
// automatically (internal/recall/service.go) — that degrade path is
// exercised by the PR3 wiring tests and needs no duplicate coverage here.
//
// dataDir is the active data directory the caller already resolved (e.g.
// store.Config.DataDir). It is only consulted when embCfg.DBPath is unset,
// to scope the embeddings store consistently with the #82 fix — an
// alternate OMNIA_DATA_DIR must never resolve to the same embeddings.db as
// the canonical instance. Pass "" when the caller has no data dir opinion
// (tests that always set an explicit DBPath are unaffected either way).
//
// vecIndexEnabled threads v0.4's sqlite-vec-index capability flag through to
// embed.OpenStore (design capability 7) — this is the SAME builder cmdMCP,
// cmdServe, `omnia search`, and `omnia eval --injection` all share, so
// enabling vector_index affects every one of those read surfaces uniformly.
//
// encCfg threads v0.4's memory-at-rest-security capability config through the
// same way (a zero-value/disabled EncryptionConfig reproduces pre-v0.4
// behavior exactly) — required once embeddings.db has been migrated to an
// encrypted file, otherwise this builder would try to reopen it via plain
// modernc and fail, silently degrading mem_search to FTS5-only.
func buildRecallService(s *store.Store, recallCfg config.RecallConfig, embCfg config.EmbeddingsConfig, dataDir string, vecIndexEnabled bool, encCfg config.EncryptionConfig) *recall.Service {
	if !recallCfg.Enabled {
		return nil
	}

	// EMBM-3/blocking-fix: reject an internally-inconsistent embeddings
	// config (a truncation/expansion Dim mismatched against the model's MRL
	// capability, see config.ValidateEmbeddings) before opening the
	// embeddings store or constructing an Ollama client — mirrors cmdEmbed's
	// and buildAutoEmbedWorker's validation. THIS is the mem_search product
	// path (cmdMCP/cmdServe via buildRecallService, cmdSearch/cmdServe's GET
	// /search via buildRecallServiceForCLI): without this check, a
	// misconfigured dim silently degraded every query to lexical-only (or
	// broken semantic hits) with no diagnostic pointing at config — fail
	// closed to nil (FTS5-only), matching the embeddings-store-unavailable
	// branch below, but LOUDLY naming the bad model/dim instead of silently
	// swallowing it.
	if err := config.ValidateEmbeddings(embCfg); err != nil {
		log.Printf("[recall] invalid embeddings config (%v); mem_search falls back to FTS5-only search", err)
		return nil
	}

	dbPath := config.ResolveEmbeddingsDBPath(embCfg.DBPath, dataDir)
	embStore, err := embed.OpenStore(dbPath, embedStoreOptions(vecIndexEnabled, encCfg)...)
	if err != nil {
		// The embeddings store is Omnia's own file, not the read-only
		// engram.db. If it can't even be opened, fail closed to the
		// well-tested cfg.Recall == nil (FTS5-only) path instead of starting
		// a half-wired recall.Service.
		log.Printf("[recall] embeddings store unavailable (%v); mem_search falls back to FTS5-only search", err)
		return nil
	}

	client := embed.New(embCfg.BaseURL, embCfg.Model, embCfg.Dim)
	searcher := embed.NewSearcher(embStore, client)

	// P6 (docs/conversational-retrieval-plan.md "Query embedding cache"):
	// wrap the searcher in embed.CachedSearcher when recall.query_cache.enabled
	// is true, memoizing EmbedQuery in a per-process LRU keyed on the
	// normalized query string. Both embed.LocalSearcher (searcher above) and
	// *embed.CachedSearcher satisfy embed.Searcher, so this is a drop-in
	// swap — recall.NewService below never needs to know which one it got.
	// Disabled by default (recallCfg.QueryCache.Enabled's own zero value),
	// matching this composition root's existing "flag off -> exact same
	// wiring as before the flag existed" convention for every other
	// RecallConfig/EmbeddingsConfig sub-gate.
	var semanticSearcher embed.Searcher = searcher
	if recallCfg.QueryCache.Enabled {
		semanticSearcher = embed.NewCachedSearcher(searcher, embCfg.Model, recallCfg.QueryCache.MaxEntries)
	}
	lexical := mcp.NewStoreLexicalSearcher(s)

	return recall.NewService(lexical, semanticSearcher, recall.FuseParams{
		RRFK:        recallCfg.RRFK,
		DenseK:      recallCfg.DenseK,
		StrongFloor: recallCfg.StrongFloor,
		BaseFloor:   recallCfg.BaseFloor,
		MaxResults:  recallCfg.MaxResults,
	})
}

// loadLearnedRankerForCLI mirrors cmdMCP's own learned_ranker.enabled wiring
// (main.go's MCPConfig construction) so GET /search's RankPipeline call (P0,
// docs/conversational-retrieval-plan.md) can score against the SAME trained
// local model mem_search does, instead of re-deriving or diverging from that
// load logic. A disabled ranker, or an enabled one with no loadable model on
// disk, both return a nil model — RankPipeline's ApplyLearnedRanker stage is
// then a pure no-op either way (its own cfg.Enabled/model==nil guard).
func loadLearnedRankerForCLI(appCfg *config.Config, dataDir string) (config.RankerConfig, *ranker.Model) {
	if !appCfg.Ranker.Enabled {
		return appCfg.Ranker, nil
	}
	dir := appCfg.Ranker.ModelDir
	if dir == "" {
		dir = filepath.Join(dataDir, "ranker")
	}
	model, err := ranker.LoadCurrent(dir)
	if err != nil {
		return appCfg.Ranker, nil
	}
	return appCfg.Ranker, &model
}

// buildHTTPSearchFunc wires GET /search onto the SAME post-fusion pipeline
// mem_search runs (P0, docs/conversational-retrieval-plan.md section
// 0.1/P0): recallOrFTSSearchWithRelevance for the fuse-then-hydrate leg
// (shared with `omnia search`, issue #86), then mcp.RankPipeline for
// RankResults -> ApplyLearnedRanker -> ApplyStalenessDownrank ->
// ApplyTypeLens -> ApplyMMR -> ApplyTokenBudget, then
// mcp.EvaluateRecallHealth for the #226 degradation envelope (P0 item 2).
// Before this existed, GET /search ran only the fuse-then-hydrate leg — a
// voice agent (Vel, via Hermes) on a strictly weaker retrieval path than a
// coding agent, over the exact same store (section 0.1's diagnosis).
//
// Every pipeline stage stays gated by the SAME appCfg.* keys mem_search
// itself reads (recall.ranking, injection.*, structural_forgetting,
// learned_ranker) — turning on the "voice profile" P0 item 5 asks for is a
// config.yaml edit (config.example.yaml documents a recommended profile),
// never a code change, matching this codebase's off-by-default/
// rollback-is-a-config-edit convention for every other Context Economy gate.
// This function is called UNCONDITIONALLY whenever appCfgErr == nil in
// cmdServe — behavior is controlled purely by appCfg's zero-value-is-off
// gates, mirroring RankPipeline's own "always call it, let config decide"
// convention.
//
// autoEmbed is the SAME *embed.Worker cmdServe already built for POST
// /observations' auto-embed (nil when embeddings are disabled) — reused
// here (via mcp.NewWatermarkReader) so the recall_degraded/embeddings_stale
// signal reads the exact file that actually backs semantic recall, not a
// separately resolved path that could drift from it.
func buildHTTPSearchFunc(s *store.Store, recallSvc *recall.Service, appCfg *config.Config, dataDir string, autoEmbed *embed.Worker) server.SearchFunc {
	learnedRankerCfg, learnedRankerModel := loadLearnedRankerForCLI(appCfg, dataDir)
	readWatermarks := mcp.NewWatermarkReader(s, autoEmbed)

	return func(ctx context.Context, query string, req server.SearchRequest) (server.SearchEnvelope, error) {
		// P5 (docs/conversational-retrieval-plan.md "P5 — Cross-project
		// retrieval", cmd/omnia/crossproject.go): a request that set
		// all_projects=1 or 2+ repeated project= params branches into the
		// fan-out-and-merge path entirely, BEFORE any of the single-project
		// code below runs. This ordering is what keeps the single-project
		// path (neither param present) byte-for-byte unchanged — the code
		// below is untouched from its pre-P5 form.
		if req.AllProjects || len(req.Projects) > 0 {
			return crossProjectSearchEnvelope(ctx, s, recallSvc, appCfg, readWatermarks, query, req)
		}

		results, relevance, fusionRan, err := recallOrFTSSearchWithRelevance(ctx, s, recallSvc, query, req.SearchOptions)
		if err != nil {
			return server.SearchEnvelope{}, err
		}
		now := time.Now()

		// memory-structural-forgetting: batch-load anchors for
		// ApplyStalenessDownrank, gated the SAME way handleSearch gates it
		// (structural_forgetting.enabled) — see mcp.go's own comment for
		// why a nil/empty map is a safe, pure no-op default when this is
		// off or there is nothing to look up.
		var anchorsByObs map[string][]store.MemoryAnchor
		if appCfg.StructuralForgetting.Enabled && len(results) > 0 {
			syncIDs := make([]string, 0, len(results))
			for _, r := range results {
				if r.SyncID != "" {
					syncIDs = append(syncIDs, r.SyncID)
				}
			}
			if len(syncIDs) > 0 {
				if am, aerr := s.GetAnchorsForObservations(syncIDs); aerr == nil {
					anchorsByObs = am
				}
				// Errors from anchor loading are swallowed — search must not fail.
			}
		}

		// P0 item 3: max_tokens is a per-request override for the injection
		// budget — applied regardless of injection.budget.enabled, so a
		// caller can always cap response size on demand (Hermes' own
		// ~1400-character budget) even when the operator left budgeting off
		// by default in config.yaml.
		budget := appCfg.Injection.Budget
		if req.MaxTokens > 0 {
			budget.Enabled = true
			budget.MaxTokens = req.MaxTokens
		}

		// explain=1 (P0 item 3) needs the SAME batch-normalized relevance
		// RankResults scores against, captured via RankPipelineOptions.
		// PreLensSnapshot at the same pipeline point mem_search's
		// handleSearch captures it (after RankResults/ApplyLearnedRanker/
		// ApplyStalenessDownrank, before ApplyTypeLens/ApplyMMR/
		// ApplyTokenBudget narrow the batch further) — see that field's own
		// doc in internal/mcp/rank_pipeline.go for why the ordering matters.
		var normalizedRelevance map[int64]float64
		var preLensSnapshot func([]store.SearchResult)
		if req.Explain {
			preLensSnapshot = func(snapshot []store.SearchResult) {
				nonSentinel := make([]store.SearchResult, 0, len(snapshot))
				for _, r := range snapshot {
					if r.Rank != cliExactSentinelRank && !r.SignatureMatch {
						nonSentinel = append(nonSentinel, r)
					}
				}
				normalizedRelevance = mcp.MinMaxNormalizeRelevance(nonSentinel, relevance)
			}
		}

		pipelineOut := mcp.RankPipeline(results, relevance, mcp.RankPipelineOptions{
			Ranking:            appCfg.Recall.Ranking,
			LearnedRanker:      learnedRankerCfg,
			LearnedRankerModel: learnedRankerModel,
			AnchorsByObs:       anchorsByObs,
			Query:              query,
			ExplicitType:       req.Type,
			TypeLens:           appCfg.Injection.TypeLens,
			Diversity:          appCfg.Injection.Diversity,
			Budget:             budget,
			PreLensSnapshot:    preLensSnapshot,
			IntentRouting:      appCfg.IntentRouting,
		}, now)

		envelope := server.SearchEnvelope{Results: pipelineOut.Results, Intent: pipelineOut.Intent}

		// #226 degradation envelope (P0 item 2): semanticActive mirrors
		// mem_search's own cfg.Recall != nil check — "was semantic recall
		// CONFIGURED at all", not "did fusion succeed for this one query"
		// (that distinction is fusionRan, used for BuildResultReceipt's
		// lexical-vs-fusion labeling below instead — see
		// recallOrFTSSearchWithRelevance's own doc for why the two must not
		// be conflated).
		health := mcp.EvaluateRecallHealth(ctx, recallSvc != nil, readWatermarks)
		if health.Degraded {
			envelope.RecallDegraded = true
			envelope.RecallDegradedReason = health.Reason
			if health.EmbeddingsStale {
				envelope.EmbeddingsStale = true
				envelope.EmbeddingsBehindBy = health.BehindBy
				envelope.NewestEmbeddedAt = health.NewestEmbeddedAt
			}
		}

		if req.Explain {
			envelope.ScoreBreakdown = make(map[string]map[string]any, len(pipelineOut.Results))
			for _, r := range pipelineOut.Results {
				stalenessPenalty := mcp.StalenessPenaltyFor(anchorsByObs[r.SyncID])
				envelope.ScoreBreakdown[strconv.FormatInt(r.ID, 10)] = mcp.BuildResultReceipt(
					r, fusionRan, appCfg.Recall.Ranking, relevance, normalizedRelevance, now, stalenessPenalty,
				)
			}
		}

		return envelope, nil
	}
}

// ─── P3: GET /answer (docs/conversational-retrieval-plan.md "Answer-shaped
// context endpoint") ───────────────────────────────────────────────────────

// answerFTSDiag runs a dedicated, Limit-1, lexical-only diagnostic query
// purely to read Store.Search's zero-hit relaxation-ladder outcome
// (store.SearchDiag) — the signal ClassifyAnswerConfidence's "the FTS
// relaxation ladder had to reach step 2 to return anything" `none` trigger
// needs.
//
// This is a SEPARATE query from the one that produced the real answer
// results, not a reuse of recallOrFTSSearchWithRelevance's own internal
// call, for one reason: when hybrid recall is active (recallSvc != nil),
// the real results come from recall.Service.Search, whose lexical leg
// (internal/mcp's StoreLexicalSearcher) does not forward a Diag pointer
// through recall.LexicalSearchOptions today (that struct lives in
// internal/recall, out of this change's file-ownership scope — see the
// plan's own note that SearchDiag "is currently thrown away by every
// caller," which this endpoint fixes by reading it independently rather
// than by widening recall's own interface). Store.Search's ladder only
// activates when the strict AND-of-terms pass returns zero rows (see
// SearchDiag's own doc), so this second query is cheap even on the common
// path where it never fires — Limit:1 keeps it to the smallest useful
// probe regardless.
func answerFTSDiag(s *store.Store, query string, opts store.SearchOptions) store.SearchDiag {
	var diag store.SearchDiag
	diagOpts := opts
	diagOpts.Limit = 1
	diagOpts.Diag = &diag
	_, _ = s.Search(query, diagOpts)
	return diag
}

// buildHTTPAnswerFunc wires GET /answer (P3) onto the SAME retrieval leg
// GET /search uses (recallOrFTSSearchWithRelevance, shared per issue #86)
// and the SAME mcp.RankPipeline post-fusion ranking (P0) — this endpoint is
// deliberately NOT a separate retrieval path, only a different SHAPING of
// the same ranked results: mcp.AssembleAnswerContext replaces
// ApplyTokenBudget's char-preview trim with structured-field extraction
// (mcp.ExtractAnswerText) plus a char (not token) budget, and
// mcp.ClassifyAnswerConfidence adds the P3 confidence signal GET /search
// has no equivalent of.
//
// No LLM call anywhere in this function (P3's hard constraint): every step
// is deterministic string/arithmetic work over what retrieval already
// computed.
func buildHTTPAnswerFunc(s *store.Store, recallSvc *recall.Service, appCfg *config.Config, dataDir string, autoEmbed *embed.Worker) server.AnswerFunc {
	learnedRankerCfg, learnedRankerModel := loadLearnedRankerForCLI(appCfg, dataDir)
	readWatermarks := mcp.NewWatermarkReader(s, autoEmbed)

	return func(ctx context.Context, query string, req server.AnswerRequest) (server.AnswerResponse, error) {
		// P5: same fan-out branch as buildHTTPSearchFunc above — see that
		// function's own comment for why this ordering keeps the
		// single-project path below byte-for-byte unchanged.
		if req.AllProjects || len(req.Projects) > 0 {
			return crossProjectAnswer(ctx, s, recallSvc, appCfg, readWatermarks, query, req)
		}

		results, relevance, fusionRan, err := recallOrFTSSearchWithRelevance(ctx, s, recallSvc, query, req.SearchOptions)
		if err != nil {
			return server.AnswerResponse{}, err
		}
		now := time.Now()

		var anchorsByObs map[string][]store.MemoryAnchor
		if appCfg.StructuralForgetting.Enabled && len(results) > 0 {
			syncIDs := make([]string, 0, len(results))
			for _, r := range results {
				if r.SyncID != "" {
					syncIDs = append(syncIDs, r.SyncID)
				}
			}
			if len(syncIDs) > 0 {
				if am, aerr := s.GetAnchorsForObservations(syncIDs); aerr == nil {
					anchorsByObs = am
				}
			}
		}

		pipelineOut := mcp.RankPipeline(results, relevance, mcp.RankPipelineOptions{
			Ranking:            appCfg.Recall.Ranking,
			LearnedRanker:      learnedRankerCfg,
			LearnedRankerModel: learnedRankerModel,
			AnchorsByObs:       anchorsByObs,
			Query:              query,
			TypeLens:           appCfg.Injection.TypeLens,
			Diversity:          appCfg.Injection.Diversity,
			// Budget is deliberately the zero value here, not
			// appCfg.Injection.Budget: P3's char-budget trim
			// (mcp.AssembleAnswerContext) REPLACES ApplyTokenBudget's
			// token-estimated preview trim for this endpoint rather than
			// stacking on top of it — running both would trim twice against
			// two different units (tokens vs. chars) for no benefit.
			IntentRouting: appCfg.IntentRouting,
		}, now)

		maxChars := req.MaxChars
		if maxChars <= 0 {
			maxChars = appCfg.Answer.MaxChars
		}
		assembled := mcp.AssembleAnswerContext(pipelineOut.Results, maxChars)

		diag := answerFTSDiag(s, query, req.SearchOptions)
		health := mcp.EvaluateRecallHealth(ctx, recallSvc != nil, readWatermarks)

		var topScore float64
		var hasTopScore bool
		for _, r := range pipelineOut.Results {
			if r.Rank == cliExactSentinelRank || r.SignatureMatch {
				continue // pre-empted rows carry no relevance score (see relevance map's own doc)
			}
			if sc, ok := relevance[r.ID]; ok {
				topScore, hasTopScore = sc, true
			}
			break
		}

		confidence := mcp.ClassifyAnswerConfidence(mcp.AnswerConfidenceSignals{
			HitCount:         len(results),
			TopScore:         topScore,
			HasTopScore:      hasTopScore,
			FTSRelaxed:       diag.Relaxed,
			FTSRelaxStep:     diag.Step,
			FusionRan:        fusionRan,
			RecallDegraded:   health.Degraded,
			SourcesAssembled: len(assembled.Sources),
		}, appCfg.Answer.NoneScoreFloor, appCfg.Answer.ConfidenceThreshold)

		sources := make([]server.AnswerSource, 0, len(assembled.Sources))
		for _, src := range assembled.Sources {
			sources = append(sources, server.AnswerSource{
				SyncID:    src.SyncID,
				Title:     src.Title,
				Type:      src.Type,
				UpdatedAt: src.UpdatedAt,
			})
		}

		return server.AnswerResponse{
			Context:    assembled.Context,
			Confidence: string(confidence),
			Intent:     pipelineOut.Intent,
			Sources:    sources,
			Degraded:   health.Degraded,
		}, nil
	}
}
