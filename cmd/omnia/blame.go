package main

import (
	"fmt"
	"os"

	"github.com/velion/omnia/internal/codegraph"
	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/store"
)

// loadCodeGraphConfig reports whether the code-decision-graph capability is
// enabled. Matching this codebase's established config-loading convention
// (main.go's appCfgErr == nil checks), ANY load failure — including the very
// common "no config.yaml yet" case on a fresh install — degrades to disabled
// rather than surfacing a configuration error: this is an optional,
// default-OFF gate, not a required setting.
var loadCodeGraphConfig = func() bool {
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		return false
	}
	return cfg.CodeGraph.Enabled
}

func cmdBlame(cfg store.Config) {
	if !loadCodeGraphConfig() {
		fmt.Println("capability disabled")
		return
	}
	if len(os.Args) < 3 {
		fmt.Println("usage: omnia blame <file>:<line> [--repo PATH]")
		return
	}
	file, line, err := codegraph.ParseFileLine(os.Args[2])
	if err != nil {
		fmt.Printf("invalid blame target: %v\n", err)
		return
	}
	repo := ""
	for i := 3; i+1 < len(os.Args); i++ {
		if os.Args[i] == "--repo" {
			repo = os.Args[i+1]
			break
		}
	}
	repo, file, err = codegraph.Normalize(repo, file)
	if err != nil {
		fmt.Printf("no repo context; hits: 0\n")
		return
	}
	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()
	hits, err := s.BlameLine(repo, file, line)
	if err != nil {
		fatal(err)
		return
	}
	fmt.Printf("line: %d\nhits: %d\n", line, len(hits))
	for _, hit := range hits {
		fmt.Printf("- %s %s:%d-%d %s %s\n", hit.AnchorStatus, hit.Anchor.FilePath, hit.Anchor.LineStart, hit.Anchor.LineEnd, hit.Memory.SyncID, hit.Memory.Title)
	}
}
