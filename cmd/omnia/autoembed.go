package main

import (
	"log"
	"log/slog"
	"os"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/embed"
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
func buildAutoEmbedWorker(embCfg config.EmbeddingsConfig) *embed.Worker {
	if !embCfg.Enabled {
		return nil
	}
	embStore, err := embed.OpenStore(embCfg.DBPath)
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
	return embed.NewWorker(embStore, client, embCfg.Model, embCfg.Dim, 0, logger)
}
