package core

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Pipeline orchestrates fetching from sources and writing to a sink.
type Pipeline struct {
	sources      []Source
	sink         Sink
	state        StateStore
	dryRun       bool
	backfillDays int
	logger       *slog.Logger
}

// NewPipeline creates a Pipeline. If dryRun is true, items are logged but not written.
// backfillDays controls how far back to look on the first run when no cursor exists.
func NewPipeline(sources []Source, sink Sink, state StateStore, dryRun bool, backfillDays int, logger *slog.Logger) *Pipeline {
	if logger == nil {
		logger = slog.Default()
	}
	if backfillDays <= 0 {
		backfillDays = 30
	}
	return &Pipeline{
		sources:      sources,
		sink:         sink,
		state:        state,
		dryRun:       dryRun,
		backfillDays: backfillDays,
		logger:       logger,
	}
}

// RunOptions configures a pipeline run.
type RunOptions struct {
	// Since, when non-nil, overrides state cursors and backfill calculation for all sources.
	Since *time.Time
}

// Run executes all sources and writes to the sink.
// If a source fails its sink writes, that source's cursor is not flushed and an error is returned,
// but remaining sources still run.
func (p *Pipeline) Run(ctx context.Context, opts RunOptions) error {
	var firstErr error
	for _, src := range p.sources {
		if err := p.runSource(ctx, src, opts); err != nil {
			p.logger.Error("source failed", "source", src.Name(), "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// runSource fetches from a single source and writes to the sink.
//
// Cursor flush is skipped for this source if any sink write fails after a retry,
// ensuring the next run re-fetches the same window. Because Engram upserts on
// topic_key+project, re-ingesting an already-saved window is safe and idempotent.
func (p *Pipeline) runSource(ctx context.Context, src Source, opts RunOptions) error {
	// C1: resolve since — explicit override wins, then per-source cursor, then backfill.
	var since time.Time
	if opts.Since != nil {
		since = *opts.Since
	} else {
		since = p.resolveSince(src.Name())
	}

	p.logger.Info("fetching", "source", src.Name(), "since", since.Format(time.RFC3339))
	items, err := src.Fetch(ctx, since)
	if err != nil {
		return fmt.Errorf("fetch from %s: %w", src.Name(), err)
	}

	p.logger.Info("fetched items", "source", src.Name(), "count", len(items))

	// C2: track write failures; a single persistent failure blocks cursor flush.
	writeOK := true
	for _, item := range items {
		if p.dryRun {
			fmt.Printf("[dry-run] title=%q type=%s topic_key=%s project=%s\n  content preview: %.200s\n\n",
				item.Title, item.Type, item.TopicKey, item.Project, item.Content)
			continue
		}
		if err := p.sink.Write(ctx, item); err != nil {
			// Retry once before giving up.
			if retryErr := p.sink.Write(ctx, item); retryErr != nil {
				p.logger.Error("write failed after retry", "title", item.Title, "error", retryErr)
				writeOK = false
			}
		}
	}

	// C2: only flush the cursor if all writes succeeded. A failed flush is a warning,
	// not a hard error, because the next run will re-fetch and upsert idempotently.
	if !p.dryRun && writeOK {
		if err := p.state.Flush(); err != nil {
			p.logger.Warn("state flush failed", "error", err)
		}
	}

	if !writeOK {
		return fmt.Errorf("one or more sink writes failed for source %s; cursor not advanced", src.Name())
	}
	return nil
}

// resolveSince returns the appropriate since time for a source.
// For the GitHub source the cursor is per-repo and managed inside the source itself;
// for sources like Discord the state store holds per-channel cursors. This method
// provides the top-level fallback when no cursor exists at all.
func (p *Pipeline) resolveSince(sourceName string) time.Time {
	return time.Now().AddDate(0, 0, -p.backfillDays)
}
