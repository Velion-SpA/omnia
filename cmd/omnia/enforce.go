package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/enforce"
	"github.com/velion/omnia/internal/store"
)

// loadEnforcementConfig reports the operator's EnforcementConfig, degrading
// to a disabled zero value on ANY config.Load failure — including the very
// common "no config.yaml yet" case on a fresh install. Matching this
// codebase's established config-loading convention (loadCodeGraphConfig,
// cmd/omnia/blame.go; the same fix already applied to consolidate.go and
// rank_train.go): a missing/unparseable config file is NOT a fatal error
// for an optional, default-OFF capability.
var loadEnforcementConfig = func() (config.EnforcementConfig, bool) {
	cfg, err := config.Load(config.DefaultPath())
	if err != nil || !cfg.Enforcement.Enabled {
		return config.EnforcementConfig{}, false
	}
	return cfg.Enforcement, true
}

// cmdEnforce is `omnia enforce [--files PATH]... [--repo PATH] [--project P]
// [--block] [--override --reason TEXT]` (design.md Capability 2 CLI,
// REQ-418): mirrors mem_enforce's own pass/flag/block/override contract for
// hook/CI use, sharing enforce.Evaluate as the single decision function
// both surfaces call.
func cmdEnforce(cfg store.Config) {
	enfCfg, enabled := loadEnforcementConfig()
	if !enabled {
		fmt.Println("capability disabled")
		return
	}

	args := os.Args[2:]
	var repoRoot, project, overrideReason string
	var files []string
	blockFlag := false
	override := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--files":
			if i+1 < len(args) {
				files = append(files, args[i+1])
				i++
			}
		case "--repo":
			if i+1 < len(args) {
				repoRoot = args[i+1]
				i++
			}
		case "--project":
			if i+1 < len(args) {
				project = args[i+1]
				i++
			}
		case "--block":
			blockFlag = true
		case "--override":
			override = true
		case "--reason":
			if i+1 < len(args) {
				overrideReason = args[i+1]
				i++
			}
		}
	}
	if blockFlag {
		enfCfg.Mode = "block"
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	result := enforce.Evaluate(context.Background(), s, enforce.EvalOptions{
		Config:         enfCfg,
		Project:        project,
		RepoRoot:       repoRoot,
		FilesTouched:   files,
		Override:       override,
		OverrideReason: overrideReason,
		Actor:          "cli",
	})

	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fatal(err)
		return
	}
	fmt.Println(string(out))

	if result.Verdict == enforce.VerdictBlock {
		exitFunc(1)
	}
}
