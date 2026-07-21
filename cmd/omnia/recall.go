package main

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/embed"
	"github.com/velion/omnia/internal/mcp"
	"github.com/velion/omnia/internal/recall"
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

// loadAppConfigWithRecallAutodetect loads config.yaml (config.DefaultPath())
// and runs the Ollama auto-detect (issue #83, maybeAutoDetectRecall) so the
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
var loadAppConfigWithRecallAutodetect = func() (*config.Config, error) {
	appCfg, err := config.Load(config.DefaultPath())
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
	return buildRecallService(s, appCfg.Recall, appCfg.Embeddings, dataDir)
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
	if recallSvc == nil {
		return storeSearch(s, query, opts)
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
		return storeSearch(s, query, opts)
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}
	return mcp.HydrateFusedResults(s, fused, limit, mcp.RecallScopeFilter{
		Type:    opts.Type,
		Project: opts.Project,
		Scope:   opts.Scope,
	}), nil
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
func buildRecallService(s *store.Store, recallCfg config.RecallConfig, embCfg config.EmbeddingsConfig, dataDir string) *recall.Service {
	if !recallCfg.Enabled {
		return nil
	}

	dbPath := config.ResolveEmbeddingsDBPath(embCfg.DBPath, dataDir)
	embStore, err := embed.OpenStore(dbPath)
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
	lexical := mcp.NewStoreLexicalSearcher(s)

	return recall.NewService(lexical, searcher, recall.FuseParams{
		RRFK:        recallCfg.RRFK,
		DenseK:      recallCfg.DenseK,
		StrongFloor: recallCfg.StrongFloor,
		BaseFloor:   recallCfg.BaseFloor,
		MaxResults:  recallCfg.MaxResults,
	})
}
