package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/core"
	engram "github.com/velion/omnia/internal/sink/engram"
	discord "github.com/velion/omnia/internal/source/discord"
	github "github.com/velion/omnia/internal/source/github"
	"github.com/velion/omnia/internal/state"
)

const version = "0.1.0"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "omnia: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("omnia", flag.ContinueOnError)
	var (
		configPath = fs.String("config", config.DefaultPath(), "path to config file")
		dryRun     = fs.Bool("dry-run", false, "print what would be saved without writing")
		sourceFlag = fs.String("source", "", "run only this source (github|discord)")
		sinceFlag  = fs.String("since", "", "override since time (RFC3339)")
		showVer    = fs.Bool("version", false, "print version and exit")
	)

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *showVer {
		fmt.Printf("omnia %s\n", version)
		return nil
	}

	subcommand := fs.Arg(0)
	if subcommand == "" {
		subcommand = "sync"
	}

	switch subcommand {
	case "sync":
		return runSync(*configPath, *dryRun, *sourceFlag, *sinceFlag)
	case "status":
		return runStatus(*configPath)
	default:
		return fmt.Errorf("unknown subcommand %q (use: sync, status)", subcommand)
	}
}

func runSync(configPath string, dryRun bool, sourceFilter, sinceStr string) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	st, err := state.New(state.DefaultPath())
	if err != nil {
		return fmt.Errorf("init state: %w", err)
	}

	sink := engram.New(cfg.Engram.BaseURL)
	ctx := context.Background()

	// Build sources.
	var sources []core.Source

	if (sourceFilter == "" || sourceFilter == "github") && cfg.Sources.GitHub.Enabled {
		project := cfg.Sources.GitHub.Project
		if project == "" {
			project = cfg.Engram.DefaultProject
		}
		src := github.New(cfg.Sources.GitHub.Repos, project, "", st)
		sources = append(sources, src)

		// Ensure session exists.
		if !dryRun {
			if err := sink.EnsureSession(ctx, project, "/"); err != nil {
				logger.Warn("could not ensure engram session", "project", project, "error", err)
			}
		}
	}

	if (sourceFilter == "" || sourceFilter == "discord") && cfg.Sources.Discord.Enabled {
		project := cfg.Sources.Discord.Project
		if project == "" {
			project = cfg.Engram.DefaultProject
		}
		src := discord.New(cfg.Sources.Discord.Channels, project, "", st)
		sources = append(sources, src)

		if !dryRun {
			if err := sink.EnsureSession(ctx, project, "/"); err != nil {
				logger.Warn("could not ensure engram session", "project", project, "error", err)
			}
		}
	}

	if len(sources) == 0 {
		logger.Info("no sources enabled; check your config")
		return nil
	}

	// Resolve since override.
	var opts core.RunOptions
	if sinceStr != "" {
		t, err := time.Parse(time.RFC3339, sinceStr)
		if err != nil {
			return fmt.Errorf("parse --since: %w", err)
		}
		opts.Since = &t
	}

	pipeline := core.NewPipeline(sources, sink, st, dryRun, logger)
	return pipeline.Run(ctx, opts)
}

func runStatus(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	sink := engram.New(cfg.Engram.BaseURL)
	ctx := context.Background()

	if err := sink.Health(ctx); err != nil {
		fmt.Printf("engram: UNREACHABLE (%v)\n", err)
	} else {
		fmt.Printf("engram: OK (%s)\n", cfg.Engram.BaseURL)
	}

	st, err := state.New(state.DefaultPath())
	if err != nil {
		fmt.Printf("state: error (%v)\n", err)
		return nil
	}

	fmt.Printf("state file: %s\n", state.DefaultPath())
	if v, ok := st.GetCursor("github", ""); ok {
		fmt.Printf("  github cursor: %s\n", v)
	}

	githubEnabled := cfg.Sources.GitHub.Enabled
	discordEnabled := cfg.Sources.Discord.Enabled
	fmt.Printf("sources: github=%v discord=%v\n", githubEnabled, discordEnabled)
	return nil
}
