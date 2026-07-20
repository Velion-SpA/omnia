package main

import (
	"log"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/embed"
	"github.com/velion/omnia/internal/mcp"
	"github.com/velion/omnia/internal/recall"
	"github.com/velion/omnia/internal/store"
)

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
