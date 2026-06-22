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
	configPath := fs.String("config", config.DefaultPath(), "path to config file")
	if err := fs.Parse(args); err != nil {
		fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal(fmt.Errorf("load config: %w", err))
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

	store, err := embed.OpenStore(cfg.Embeddings.DBPath)
	if err != nil {
		fatal(fmt.Errorf("open embeddings store: %w", err))
	}
	defer store.Close()

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
}
