package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"strings"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/dashboard"
)

// cmdDashboard starts the unified Omnia web UI dashboard (localhost only).
//
// Usage: omnia dashboard [--port 7800] [--daemon-url URL] [--data-dir DIR]
func cmdDashboard(args []string) {
	fs := flag.NewFlagSet("dashboard", flag.ExitOnError)
	port := fs.Int("port", 7800, "port to listen on (localhost only)")
	daemonURL := fs.String("daemon-url", "", "local Omnia daemon URL (defaults to config value)")
	// --engram is a hidden back-compat alias for --daemon-url (OBL-11: the flag name
	// predated the omnia rebrand; kept working for existing scripts but undocumented).
	fs.StringVar(daemonURL, "engram", "", "deprecated alias for --daemon-url (hidden)")
	dataDir := fs.String("data-dir", "", "path to the Omnia data directory (default: $OMNIA_DATA_DIR or ~/.omnia)")
	actor := fs.String("actor", "", "provisional actor identity for audit log (default: USER env var)")
	configPath := fs.String("config", config.DefaultPath(), "path to config file")
	projectsFlag := fs.String("projects", "", "comma-separated extra project names to show in dashboard")
	if err := fs.Parse(args); err != nil {
		fatal(err)
	}

	var (
		resolvedDaemon = *daemonURL
		configProjects []string
		configRoutes   map[string]string
		configHidden   []string
		configAliases  map[string]string
		configGroups   map[string][]string
		embCfg         config.EmbeddingsConfig
	)
	if cfg, err := config.Load(*configPath); err == nil {
		if resolvedDaemon == "" {
			resolvedDaemon = cfg.Engram.BaseURL
		}
		configProjects = cfg.Projects
		configRoutes = cfg.Routes
		configHidden = cfg.ProjectHidden
		configAliases = cfg.ProjectAliases
		configGroups = cfg.ProjectGroups
		embCfg = cfg.Embeddings
	}
	if resolvedDaemon == "" {
		resolvedDaemon = "http://127.0.0.1:7437"
	}

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
		Port:              *port,
		EngramURL:         resolvedDaemon,
		EngramDataDir:     *dataDir,
		Actor:             *actor,
		Projects:          configProjects,
		Routes:            configRoutes,
		ProjectHidden:     configHidden,
		ProjectAliases:    configAliases,
		ProjectGroups:     configGroups,
		EmbeddingsEnabled: embCfg.Enabled,
		EmbeddingsBaseURL: embCfg.BaseURL,
		EmbeddingsModel:   embCfg.Model,
		EmbeddingsDim:     embCfg.Dim,
		EmbeddingsDBPath:  embCfg.DBPath,
	}, logger)

	if err := srv.Start(context.Background()); err != nil {
		fatal(err)
	}
}
