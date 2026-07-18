package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/core"
	engramsink "github.com/velion/omnia/internal/sink/engram"
	"github.com/velion/omnia/internal/source/atlassian"
	discord "github.com/velion/omnia/internal/source/discord"
	github "github.com/velion/omnia/internal/source/github"
	jira "github.com/velion/omnia/internal/source/jira"
	"github.com/velion/omnia/internal/state"
)

// cmdCollect runs the external-source collectors (GitHub, Discord, ...) that pull
// activity into the local Omnia daemon. It is the unified-binary home of what used
// to be the standalone `omnia sync` / `omnia status` collector commands.
//
// Usage:
//
//	omnia collect [--config PATH] [--dry-run] [--source github|discord|jira] [--since RFC3339]
//	omnia collect status [--config PATH]
func cmdCollect(args []string) {
	// A bare "status" sub-argument switches to the status report.
	if len(args) > 0 && args[0] == "status" {
		if err := runCollectStatus(args[1:]); err != nil {
			fatal(err)
		}
		return
	}

	fs := flag.NewFlagSet("collect", flag.ExitOnError)
	configPath := fs.String("config", config.DefaultPath(), "path to collectors config file")
	dryRun := fs.Bool("dry-run", false, "print what would be saved without writing")
	sourceFlag := fs.String("source", "", "run only this source (github|discord|jira)")
	sinceFlag := fs.String("since", "", "override since time (RFC3339)")
	if err := fs.Parse(args); err != nil {
		fatal(err)
	}

	if err := runCollect(*configPath, *dryRun, *sourceFlag, *sinceFlag); err != nil {
		fatal(err)
	}
}

// collectSessionDir returns the directory of the omnia binary, used as the session
// directory so collector sessions are scoped to this install rather than an
// arbitrary path.
func collectSessionDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "/"
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return filepath.Dir(exe)
	}
	return filepath.Dir(resolved)
}

func runCollect(configPath string, dryRun bool, sourceFilter, sinceStr string) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	st, err := state.New(state.DefaultPath())
	if err != nil {
		return fmt.Errorf("init state: %w", err)
	}

	sink := engramsink.New(cfg.Engram.BaseURL)
	ctx := context.Background()

	sessionDir := collectSessionDir()

	router := config.NewRouter(cfg.Routes, cfg.Engram.DefaultProject)

	var sources []core.Source

	if (sourceFilter == "" || sourceFilter == "github") && cfg.Sources.GitHub.Enabled {
		src := github.New(cfg.Sources.GitHub.Repos, router, cfg.Sources.GitHub.Token, st)
		src.SetBackfillDays(cfg.BackfillDays)
		src.SetIncludeCommits(cfg.Sources.GitHub.IncludeCommits)
		src.SetMaxCommitsPerRepo(cfg.Sources.GitHub.MaxCommitsPerRepo)

		if os.Getenv("GITHUB_TOKEN") == "" && cfg.Sources.GitHub.Token == "" {
			logger.Warn("no GitHub token configured; set GITHUB_TOKEN or github.token in config")
		}

		sources = append(sources, src)

		if !dryRun {
			for _, repo := range cfg.Sources.GitHub.Repos {
				project := router.ResolveGitHub(repo)
				if err := sink.EnsureSession(ctx, project, sessionDir); err != nil {
					logger.Warn("could not ensure omnia session", "project", project, "error", err)
				}
			}
		}
	}

	if (sourceFilter == "" || sourceFilter == "discord") && cfg.Sources.Discord.Enabled {
		if cfg.Sources.Discord.Token == "" && os.Getenv("DISCORD_BOT_TOKEN") == "" {
			return fmt.Errorf("discord source is enabled but no token found; set DISCORD_BOT_TOKEN or discord.token in config")
		}
		src := discord.New(cfg.Sources.Discord.Channels, router, cfg.Sources.Discord.Token, st)
		sources = append(sources, src)

		if !dryRun {
			for _, ch := range cfg.Sources.Discord.Channels {
				project := router.ResolveDiscord(ch.ID, ch.Guild)
				if err := sink.EnsureSession(ctx, project, sessionDir); err != nil {
					logger.Warn("could not ensure omnia session", "project", project, "error", err)
				}
			}
		}
	}

	if (sourceFilter == "" || sourceFilter == "jira") && cfg.Sources.Atlassian.Jira.Enabled {
		if cfg.Sources.Atlassian.Email == "" || cfg.Sources.Atlassian.Token == "" {
			logger.Warn("jira source enabled but no Atlassian credentials configured; set sources.atlassian.email/token in config")
		}
		client := atlassian.New(cfg.Sources.Atlassian.SiteURL, cfg.Sources.Atlassian.Email, cfg.Sources.Atlassian.Token)
		src := jira.New(client, cfg.Sources.Atlassian.Jira.ProjectKeys, router, st)
		sources = append(sources, src)

		if !dryRun {
			for _, key := range cfg.Sources.Atlassian.Jira.ProjectKeys {
				project := router.ResolveJira(key)
				if err := sink.EnsureSession(ctx, project, sessionDir); err != nil {
					logger.Warn("could not ensure omnia session", "project", project, "error", err)
				}
			}
		}
	}

	if len(sources) == 0 {
		logger.Info("no sources enabled; check your config")
		return nil
	}

	var opts core.RunOptions
	if sinceStr != "" {
		t, err := time.Parse(time.RFC3339, sinceStr)
		if err != nil {
			return fmt.Errorf("parse --since: %w", err)
		}
		opts.Since = &t
	}

	pipeline := core.NewPipeline(sources, sink, st, dryRun, cfg.BackfillDays, logger)
	return pipeline.Run(ctx, opts)
}

func runCollectStatus(args []string) error {
	fs := flag.NewFlagSet("collect status", flag.ExitOnError)
	configPath := fs.String("config", config.DefaultPath(), "path to collectors config file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	sink := engramsink.New(cfg.Engram.BaseURL)
	ctx := context.Background()

	if err := sink.Health(ctx); err != nil {
		fmt.Printf("omnia daemon: UNREACHABLE (%v)\n", err)
	} else {
		fmt.Printf("omnia daemon: OK (%s)\n", cfg.Engram.BaseURL)
	}

	st, err := state.New(state.DefaultPath())
	if err != nil {
		fmt.Printf("state: error (%v)\n", err)
		return nil
	}

	fmt.Printf("state file: %s\n", state.DefaultPath())

	if cfg.Sources.GitHub.Enabled {
		for _, repo := range cfg.Sources.GitHub.Repos {
			if v, ok := st.GetCursor("github", repo); ok {
				fmt.Printf("  github cursor [%s]: %s\n", repo, v)
			} else {
				fmt.Printf("  github cursor [%s]: (none — will use backfill of %d days)\n", repo, cfg.BackfillDays)
			}
		}
	}
	if cfg.Sources.Discord.Enabled {
		for _, ch := range cfg.Sources.Discord.Channels {
			if v, ok := st.GetCursor("discord", ch.ID); ok {
				fmt.Printf("  discord cursor [#%s / %s]: %s\n", ch.Name, ch.ID, v)
			} else {
				fmt.Printf("  discord cursor [#%s / %s]: (none — will backfill)\n", ch.Name, ch.ID)
			}
		}
	}
	if cfg.Sources.Atlassian.Jira.Enabled {
		for _, key := range cfg.Sources.Atlassian.Jira.ProjectKeys {
			if v, ok := st.GetCursor("jira", key); ok {
				fmt.Printf("  jira cursor [%s]: %s\n", key, v)
			} else {
				fmt.Printf("  jira cursor [%s]: (none — will fetch all issues)\n", key)
			}
		}
	}

	fmt.Printf("sources: github=%v discord=%v jira=%v\n",
		cfg.Sources.GitHub.Enabled, cfg.Sources.Discord.Enabled, cfg.Sources.Atlassian.Jira.Enabled)
	return nil
}
