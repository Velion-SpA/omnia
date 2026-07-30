package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/embed"
	"github.com/velion/omnia/internal/engramdb"
)

// cmdEmbed reconciles Omnia's own embeddings store with the live observations in
// the memory database. It reads the database READ-ONLY and writes only Omnia's
// own embeddings store. It is a no-op unless embeddings.enabled is true in config.
//
// Usage: omnia embed [--force] [--config PATH]
func cmdEmbed(args []string) {
	fs := flag.NewFlagSet("embed", flag.ExitOnError)
	force := fs.Bool("force", false, "re-embed every row, ignoring content/model hashes")
	reindex := fs.Bool("reindex", false, "discard and rebuild the derived Vec1 index only (requires vector_index.enabled; never touches source rows)")
	configPath := fs.String("config", config.DefaultPath(), "path to config file")
	if err := fs.Parse(args); err != nil {
		fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// A missing/unreadable config.yaml (the common fresh-install case) must
	// degrade to the same "nothing to do" outcome as embeddings being
	// explicitly disabled below — never a hard process exit. Mirrors the
	// established convention already used by `omnia blame`/`omnia
	// consolidate`/`omnia rank-train` (loadCodeGraphConfig et al.): ANY
	// config.Load failure is treated as "capability disabled," not surfaced
	// as a fatal error.
	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Info("embeddings disabled; nothing to do (no readable config.yaml; set embeddings.enabled: true to opt in)")
		return
	}
	// EMBM-3: reject an internally-inconsistent embeddings config (a
	// truncation Dim for a non-MRL model) before any Ollama call is made —
	// right after config.Load, exactly like every other config validation
	// in this command.
	if err := config.ValidateEmbeddings(cfg.Embeddings); err != nil {
		fatal(err)
	}
	if !cfg.Embeddings.Enabled {
		logger.Info("embeddings disabled; nothing to do (set embeddings.enabled: true to opt in)")
		return
	}

	// Read the memory database read-only ($OMNIA_DATA_DIR or ~/.omnia, with
	// legacy ~/.engram / engram.db compatibility handled by the resolver).
	reader, err := engramdb.Open("")
	if err != nil {
		fatal(fmt.Errorf("open memory database (read-only): %w", err))
	}
	defer reader.Close()

	// Scope the embeddings vector store to the SAME active data directory the
	// reader above just resolved (fixes #82). Previously this always opened
	// cfg.Embeddings.DBPath's single global default
	// (~/.local/share/omnia/embeddings.db) regardless of OMNIA_DATA_DIR, so
	// running `embed` against an alternate data dir reconciled a tiny alt
	// corpus against the REAL global store and pruned every vector that
	// wasn't in that alt corpus — silently wiping the primary instance's
	// embeddings. Resolving per-data-dir means an alt run gets its own store
	// file and can never prune the primary instance's vectors.
	dataDir := engramdb.ResolveDataDir("")
	dbPath := config.ResolveEmbeddingsDBPath(cfg.Embeddings.DBPath, dataDir)
	store, err := embed.OpenStore(dbPath, embedStoreOptions(cfg.VecIndex.Enabled, cfg.Encryption)...)
	if err != nil {
		fatal(fmt.Errorf("open embeddings store: %w", err))
	}
	defer store.Close()

	if *reindex && !cfg.VecIndex.Enabled {
		fmt.Println("embed: --reindex requires vector_index.enabled: true; capability disabled, nothing to do")
		return
	}

	client := embed.New(cfg.Embeddings.BaseURL, cfg.Embeddings.Model, cfg.Embeddings.Dim)
	ctx := context.Background()

	start := time.Now()
	stats, err := embed.Reconcile(ctx, reader, store, client, cfg.Embeddings.Model, cfg.Embeddings.Dim, *force, logger)
	if err != nil {
		fatal(fmt.Errorf("reconcile embeddings: %w", err))
	}
	total, _ := store.Count(ctx)
	fmt.Printf("embed: embedded %d / reused %d / pruned %d / skipped %d / errors %d (store now %d rows) in %s\n",
		stats.Embedded, stats.Reused, stats.Pruned, stats.Skipped, stats.Errors, total,
		time.Since(start).Round(time.Millisecond))

	// v0.4 sqlite-vec-index (design capability 7): every normal `omnia embed`
	// run keeps the derived Vec1 index caught up (VecBackfill is cheap when
	// most rows were already dual-written during Reconcile above) and
	// certifies readiness. `--reindex` is the stronger, explicit recovery/
	// model-change operation: discard ALL derived state and rebuild from
	// scratch. Both are no-ops (report.Enabled=false, nil error) when
	// vector_index is off — never a hard failure.
	if cfg.VecIndex.Enabled {
		var (
			report embed.VecMaintenanceReport
			opName string
		)
		if *reindex {
			report, err = store.VecReindex(ctx)
			opName = "reindex"
		} else {
			report, err = store.VecBackfill(ctx)
			opName = "backfill"
		}
		if err != nil {
			fatal(fmt.Errorf("vector index %s: %w", opName, err))
		}
		if report.Enabled {
			fmt.Printf("embed: vector index %s — indexed %d / skipped %d %v (active_dim=%d, total_rows=%d)\n",
				opName, report.Indexed, report.Skipped, report.SkippedReasons, report.ActiveDim, report.TotalRows)
		}
	}
}
