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
