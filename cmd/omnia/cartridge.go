package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/velion/omnia/internal/cartridge"
	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/ranker"
	"github.com/velion/omnia/internal/store"
)

// cmdCartridge dispatches `omnia cartridge <build|load>`, mirroring the
// existing nested-subcommand-namespace convention already used by
// cloud/embed/migrate/eval (main.go's own os.Args[2:]-parsing subcommands;
// spec.md's "Naming" section verifies no rename is needed).
func cmdCartridge(cfg store.Config) {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: omnia cartridge <build|load> [--repo PATH] [--project NAME] [--config PATH]")
		exitFunc(1)
		return
	}
	switch os.Args[2] {
	case "build":
		cmdCartridgeBuild(cfg, os.Args[3:])
	case "load":
		cmdCartridgeLoad(cfg, os.Args[3:])
	case "--help", "-h", "help":
		fmt.Println("usage: omnia cartridge <build|load> [--repo PATH] [--project NAME] [--config PATH]")
	default:
		fmt.Fprintf(os.Stderr, "unknown cartridge subcommand: %s\n", os.Args[2])
		exitFunc(1)
	}
}

// cartridgeFlags parses the shared build/load flag surface.
func cartridgeFlags(name string, args []string) (repo, project, configPath string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	repoFlag := fs.String("repo", "", "repository root (default: current directory)")
	projectFlag := fs.String("project", "", "project name to scope the cartridge to")
	configFlag := fs.String("config", config.DefaultPath(), "path to config file")
	_ = fs.Parse(args)
	return *repoFlag, *projectFlag, *configFlag
}

// loadCartridgeConfig mirrors this codebase's established config-loading
// convention (loadCodeGraphConfig/cmdConsolidate/cmdRankTrain): ANY
// config.Load failure — including the very common "no config.yaml yet"
// fresh-install case — degrades to disabled rather than a fatal exit; this
// is an optional, default-OFF capability, not a required setting (REQ-450).
func loadCartridgeConfig(path string) (config.Config, bool) {
	appCfg, err := config.Load(path)
	if err != nil || !appCfg.Cartridge.Enabled {
		return config.Config{}, false
	}
	return *appCfg, true
}

// cartridgeDataDir resolves where cartridge artifacts live: the operator's
// explicit CartridgeConfig.Dir override, or <dataDir>/cartridges by default
// — the same "override-or-derive-from-DataDir" idiom RankerConfig.ModelDir
// already uses (cmd/omnia/rank_train.go).
func cartridgeDataDir(appCfg config.Config, dataDir string) string {
	if appCfg.Cartridge.Dir != "" {
		return appCfg.Cartridge.Dir
	}
	return filepath.Join(dataDir, "cartridges")
}

// loadCartridgeRankerModel mirrors cmdMCP's exact "load if enabled, nil
// otherwise" convention (main.go's mcpCfg.LearnedRankerModel wiring): any
// load failure (disabled, no model trained yet, corrupt/mismatched model)
// degrades to nil — Build then falls back to RankResults' plain
// recency/importance order, never an error.
func loadCartridgeRankerModel(appCfg config.Config, dataDir string) *ranker.Model {
	if !appCfg.Ranker.Enabled {
		return nil
	}
	dir := appCfg.Ranker.ModelDir
	if dir == "" {
		dir = filepath.Join(dataDir, "ranker")
	}
	if model, err := ranker.LoadCurrent(dir); err == nil {
		return &model
	}
	return nil
}

func cmdCartridgeBuild(cfg store.Config, args []string) {
	repoArg, project, configPath := cartridgeFlags("cartridge build", args)
	appCfg, enabled := loadCartridgeConfig(configPath)
	if !enabled {
		fmt.Println("capability disabled")
		return
	}
	if repoArg == "" {
		repoArg, _ = os.Getwd()
	}
	repoRoot, headSHA, err := cartridge.ResolveRepo(context.Background(), repoArg)
	if err != nil {
		fmt.Printf("cartridge: cold start (no git repo context): %v\n", err)
		return
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	c, err := cartridge.Build(cartridge.BuildParams{
		Store:       s,
		Project:     project,
		RepoRoot:    repoRoot,
		HeadSHA:     headSHA,
		TopN:        appCfg.Cartridge.TopMemories,
		Ranking:     appCfg.Recall.Ranking,
		RankerCfg:   appCfg.Ranker,
		RankerModel: loadCartridgeRankerModel(appCfg, cfg.DataDir),
	})
	if err != nil {
		fatal(err)
		return
	}
	path, err := cartridge.Save(cartridgeDataDir(appCfg, cfg.DataDir), c)
	if err != nil {
		fatal(err)
		return
	}
	fmt.Printf("cartridge: built %s (head %s, %d memories, %d anchors, %d procedures)\n",
		path, headSHA, len(c.TopMemories), len(c.Anchors), len(c.TrustedProcedures))
}

func cmdCartridgeLoad(cfg store.Config, args []string) {
	repoArg, _, configPath := cartridgeFlags("cartridge load", args)
	appCfg, enabled := loadCartridgeConfig(configPath)
	if !enabled {
		fmt.Println("capability disabled")
		return
	}
	if repoArg == "" {
		repoArg, _ = os.Getwd()
	}
	repoRoot, headSHA, err := cartridge.ResolveRepo(context.Background(), repoArg)
	if err != nil {
		fmt.Printf("cartridge: cold start (no git repo context): %v\n", err)
		return
	}

	result := cartridge.Load(cartridgeDataDir(appCfg, cfg.DataDir), repoRoot, headSHA)
	switch {
	case result.Fresh:
		fmt.Printf("cartridge: warm start — %d memories, %d anchors, %d procedures (head %s)\n",
			len(result.Cartridge.TopMemories), len(result.Cartridge.Anchors), len(result.Cartridge.TrustedProcedures), headSHA)
	case result.Reason == cartridge.ReasonStaleCommit:
		fmt.Printf("cartridge: stale (built at %s, HEAD is now %s) — cold start\n", result.Cartridge.HeadSHA, headSHA)
	case result.Reason == cartridge.ReasonOldSchemaVersion:
		fmt.Println("cartridge: format version mismatch — cold start")
	default:
		fmt.Println("cartridge: no cartridge found — cold start")
	}
}
