package main

import (
	"fmt"
	"os"

	"github.com/velion/omnia/internal/codegraph"
	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/store"
)

var loadCodeGraphConfig = func() (bool, error) {
	cfg, err := config.Load(config.DefaultPath())
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return cfg.CodeGraph.Enabled, nil
}

func cmdBlame(cfg store.Config) {
	enabled, err := loadCodeGraphConfig()
	if err != nil {
		fmt.Printf("code graph configuration error: %v\n", err)
		return
	}
	if !enabled {
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
