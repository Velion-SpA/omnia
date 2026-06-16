package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/core"
	"github.com/velion/omnia/internal/dashboard"
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
	case "dashboard":
		return runDashboard(args[1:], *configPath)
	default:
		return fmt.Errorf("unknown subcommand %q (use: sync, status, dashboard)", subcommand)
	}
}

// repoRoot returns the directory of the omnia binary, used as the session directory
// so Engram sessions are scoped to this repo rather than an arbitrary path (W6).
func repoRoot() string {
	exe, err := os.Executable()
	if err != nil {
		return "/"
	}
	// Follow symlinks (e.g. go install puts a wrapper in GOPATH/bin).
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return filepath.Dir(exe)
	}
	_ = runtime.GOOS // suppress unused import if needed later
	return filepath.Dir(resolved)
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

	sessionDir := repoRoot()

	// Build per-item project router from config.
	router := config.NewRouter(cfg.Routes, cfg.Engram.DefaultProject)

	// Build sources.
	var sources []core.Source

	if (sourceFilter == "" || sourceFilter == "github") && cfg.Sources.GitHub.Enabled {
		// S6: fail fast if token is missing for an enabled source.
		if cfg.Sources.GitHub.Token == "" && os.Getenv("GITHUB_TOKEN") == "" {
			// New() will try `gh auth token` as fallback; check after construction.
		}
		// W4: pass token from config; New() applies env → config → gh fallback precedence.
		src := github.New(cfg.Sources.GitHub.Repos, router, cfg.Sources.GitHub.Token, st)
		src.SetBackfillDays(cfg.BackfillDays)

		// S6: startup validation — ensure a token resolved.
		if os.Getenv("GITHUB_TOKEN") == "" && cfg.Sources.GitHub.Token == "" {
			// gh fallback was attempted inside New(); we can't easily inspect the result,
			// so only warn here. A missing token will produce 401/403 at fetch time.
			logger.Warn("no GitHub token configured; set GITHUB_TOKEN or github.token in config")
		}

		sources = append(sources, src)

		if !dryRun {
			// Ensure sessions for all configured repos.
			for _, repo := range cfg.Sources.GitHub.Repos {
				project := router.ResolveGitHub(repo)
				if err := sink.EnsureSession(ctx, project, sessionDir); err != nil {
					logger.Warn("could not ensure engram session", "project", project, "error", err)
				}
			}
		}
	}

	if (sourceFilter == "" || sourceFilter == "discord") && cfg.Sources.Discord.Enabled {
		// S6: fail fast if Discord token is completely absent.
		if cfg.Sources.Discord.Token == "" && os.Getenv("DISCORD_BOT_TOKEN") == "" {
			return fmt.Errorf("discord source is enabled but no token found; set DISCORD_BOT_TOKEN or discord.token in config")
		}
		// W4: pass token from config; New() applies env → config precedence.
		src := discord.New(cfg.Sources.Discord.Channels, router, cfg.Sources.Discord.Token, st)
		sources = append(sources, src)

		if !dryRun {
			// Ensure sessions for all configured channels.
			for _, ch := range cfg.Sources.Discord.Channels {
				project := router.ResolveDiscord(ch.ID, ch.Guild)
				if err := sink.EnsureSession(ctx, project, sessionDir); err != nil {
					logger.Warn("could not ensure engram session", "project", project, "error", err)
				}
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

	pipeline := core.NewPipeline(sources, sink, st, dryRun, cfg.BackfillDays, logger)
	return pipeline.Run(ctx, opts)
}

// runDashboard starts the Omnia web UI dashboard.
// Usage: omnia dashboard [--port 7799] [--engram http://127.0.0.1:7437]
func runDashboard(args []string, configPath string) error {
	fs := flag.NewFlagSet("dashboard", flag.ContinueOnError)
	port := fs.Int("port", 7799, "port to listen on (localhost only)")
	engramURL := fs.String("engram", "", "Engram daemon URL (defaults to config value)")
	engramDB := fs.String("engram-db", "", "path to directory containing engram.db (default: $ENGRAM_DATA_DIR or ~/.engram)")
	actor := fs.String("actor", "", "provisional actor identity for audit log (default: USER env var)")
	projectsFlag := fs.String("projects", "", "comma-separated extra project names to show in dashboard")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Load config to pull Engram URL, routes, and projects list.
	var (
		resolvedEngram = *engramURL
		configProjects []string
		configRoutes   map[string]string
		configHidden   []string
		configAliases  map[string]string
		configGroups   map[string][]string
	)
	if cfg, err := config.Load(configPath); err == nil {
		if resolvedEngram == "" {
			resolvedEngram = cfg.Engram.BaseURL
		}
		configProjects = cfg.Projects
		configRoutes = cfg.Routes
		configHidden = cfg.ProjectHidden
		configAliases = cfg.ProjectAliases
		configGroups = cfg.ProjectGroups
	}
	if resolvedEngram == "" {
		resolvedEngram = "http://127.0.0.1:7437"
	}

	// --projects flag augments the config list.
	if *projectsFlag != "" {
		for _, p := range strings.Split(*projectsFlag, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				configProjects = append(configProjects, p)
			}
		}
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	srv := dashboard.NewServer(dashboard.Config{
		Port:           *port,
		EngramURL:      resolvedEngram,
		EngramDataDir:  *engramDB,
		Actor:          *actor,
		Projects:       configProjects,
		Routes:         configRoutes,
		ProjectHidden:  configHidden,
		ProjectAliases: configAliases,
		ProjectGroups:  configGroups,
	}, logger)

	ctx := context.Background()
	return srv.Start(ctx)
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

	// W5: iterate all configured repos and channels and show each cursor.
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

	githubEnabled := cfg.Sources.GitHub.Enabled
	discordEnabled := cfg.Sources.Discord.Enabled
	fmt.Printf("sources: github=%v discord=%v\n", githubEnabled, discordEnabled)
	return nil
}
