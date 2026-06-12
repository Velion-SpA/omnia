package core

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Pipeline orchestrates fetching from sources and writing to a sink.
type Pipeline struct {
	sources []Source
	sink    Sink
	state   StateStore
	dryRun  bool
	logger  *slog.Logger
}

// NewPipeline creates a Pipeline. If dryRun is true, items are logged but not written.
func NewPipeline(sources []Source, sink Sink, state StateStore, dryRun bool, logger *slog.Logger) *Pipeline {
	if logger == nil {
		logger = slog.Default()
	}
	return &Pipeline{
		sources: sources,
		sink:    sink,
		state:   state,
		dryRun:  dryRun,
		logger:  logger,
	}
}

// RunOptions configures a pipeline run.
type RunOptions struct {
	Since *time.Time // nil means use state cursor or default backfill
}

// Run executes all sources and writes to the sink.
func (p *Pipeline) Run(ctx context.Context, opts RunOptions) error {
	for _, src := range p.sources {
		if err := p.runSource(ctx, src, opts); err != nil {
			p.logger.Error("source failed", "source", src.Name(), "error", err)
			// continue with other sources
		}
	}
	return nil
}

func (p *Pipeline) runSource(ctx context.Context, src Source, opts RunOptions) error {
	since := time.Now().AddDate(0, 0, -30)
	if opts.Since != nil {
		since = *opts.Since
	}

	p.logger.Info("fetching", "source", src.Name(), "since", since.Format(time.RFC3339))
	items, err := src.Fetch(ctx, since)
	if err != nil {
		return fmt.Errorf("fetch from %s: %w", src.Name(), err)
	}

	p.logger.Info("fetched items", "source", src.Name(), "count", len(items))

	for _, item := range items {
		if p.dryRun {
			fmt.Printf("[dry-run] title=%q type=%s topic_key=%s project=%s\n  content preview: %.200s\n\n",
				item.Title, item.Type, item.TopicKey, item.Project, item.Content)
			continue
		}
		if err := p.sink.Write(ctx, item); err != nil {
			p.logger.Error("write failed", "title", item.Title, "error", err)
		}
	}

	if err := p.state.Flush(); err != nil {
		p.logger.Warn("state flush failed", "error", err)
	}
	return nil
}
