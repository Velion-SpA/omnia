package main

import (
	"log"
	"log/slog"
	"os"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/embed"
	"github.com/velion/omnia/internal/store"
)

// buildAutoEmbedWorker constructs the async auto-embed worker wired into
// mcp.MCPConfig.AutoEmbed and the `omnia serve` HTTP save path
// (human-like-memory PR4). It embeds each just-saved memory out-of-band so it
// becomes semantically searchable within seconds, without a manual
// `omnia embed` run.
//
// embCfg.Enabled is the gate — NOT recall.enabled: auto-embed populates
// Omnia's own vector store, which the dashboard's semantic search and graph
// consume independently of whether mem_search recall fusion is on. When
// embeddings are disabled (the default), this returns nil: no worker runs,
// nothing is enqueued, and the save path stays byte-for-byte today's.
//
// The caller must Start the returned worker on a context cancelled at
// shutdown, then pass it into the save paths. A nil return is safe both to
// Start-guard on and to pass along.
//
// s (the engram Store) is used to wire the human-like-memory PR5 slice 2
// cloud-parity seam: after every successful local vector upsert, the worker
// records a SyncEntityEmbedding sync mutation via s.EnqueueEmbeddingMutation,
// exactly as JudgeRelation does for relations. internal/embed itself never
// imports internal/store — this composition root supplies the hook
// (architecture-guardrails: keep embed a leaf package). A nil s (not expected
// from real callers, but defensive) simply skips wiring the hook.
//
// dataDir is the active data directory the caller already resolved (e.g.
// store.Config.DataDir). It is only consulted when embCfg.DBPath is unset,
// to scope the embeddings store consistently with the #82 fix — an
// alternate OMNIA_DATA_DIR must never resolve to the same embeddings.db as
// the canonical instance. Pass "" when the caller has no data dir opinion
// (tests that always set an explicit DBPath are unaffected either way).
func buildAutoEmbedWorker(embCfg config.EmbeddingsConfig, s *store.Store, dataDir string) *embed.Worker {
	if !embCfg.Enabled {
		return nil
	}
	dbPath := config.ResolveEmbeddingsDBPath(embCfg.DBPath, dataDir)
	embStore, err := embed.OpenStore(dbPath)
	if err != nil {
		// Fail closed: the periodic `omnia embed`/Reconcile run still catches
		// anything saved meanwhile. A missing vector store must never make a
		// save fail or slow down.
		log.Printf("[auto-embed] embeddings store unavailable (%v); new memories will be embedded by the next reconcile run instead", err)
		return nil
	}
	client := embed.New(embCfg.BaseURL, embCfg.Model, embCfg.Dim)
	// A real logger (mirroring cmdEmbed's construction in cmd/omnia/embed.go)
	// so queue-full drops (Debug) and embed/upsert failures (Warn) actually
	// surface somewhere instead of vanishing silently — operators need that
	// signal to know when to run `omnia embed` themselves.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	worker := embed.NewWorker(embStore, client, embCfg.Model, embCfg.Dim, 0, logger)
	if s != nil {
		worker.SetUpsertHook(buildEmbeddingSyncHook(s, logger))
	}
	return worker
}

// buildEmbeddingSyncHook returns an embed.UpsertHook that records a
// SyncEntityEmbedding sync mutation for every successfully upserted row via
// s.EnqueueEmbeddingMutation, exactly as JudgeRelation does for relations
// (human-like-memory PR5 slice 2, cloud semantic parity). Extracted as its
// own function so it can be exercised directly in tests without depending on
// a real Ollama HTTP round trip.
func buildEmbeddingSyncHook(s *store.Store, logger *slog.Logger) embed.UpsertHook {
	return func(row embed.Row) {
		if err := s.EnqueueEmbeddingMutation(store.EmbeddingSyncInput{
			SyncID:      row.SyncID,
			Project:     row.Project,
			Type:        row.Type,
			Model:       row.Model,
			Dim:         row.Dim,
			Vector:      row.Vector,
			ContentHash: row.ContentHash,
			UpdatedAt:   row.UpdatedAt,
		}); err != nil {
			// Never fail the embed on a sync-queue problem — cloud parity is
			// a soft, additive concern (mirrors the fail-closed philosophy
			// above): the vector is already searchable locally either way,
			// and the next successful upsert for this sync_id (or
			// Reconcile's next pass) will retry.
			if logger != nil {
				logger.Warn("auto-embed: enqueue cloud sync mutation failed", "sync_id", row.SyncID, "err", err)
			}
		}
	}
}
