package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/consolidate"
	"github.com/velion/omnia/internal/embed"
	"github.com/velion/omnia/internal/store"
	"log"
)

func cmdConsolidate(args []string) {
	fs := flag.NewFlagSet("consolidate", flag.ExitOnError)
	path := fs.String("config", config.DefaultPath(), "path to config file")
	project := fs.String("project", "", "project")
	_ = fs.Parse(args)
	// Matching this codebase's established config-loading convention: ANY
	// load failure — including the very common "no config.yaml yet" case on
	// a fresh install — degrades to disabled rather than a fatal exit; this
	// is an optional, default-OFF capability, not a required setting.
	app, err := config.Load(*path)
	if err != nil || !app.Consolidation.Enabled {
		log.Printf("consolidation disabled; nothing to do")
		return
	}
	cfg, err := store.DefaultConfig()
	if err != nil {
		fatal(err)
	}
	s, err := store.New(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()
	es, err := embed.OpenStore(config.ResolveEmbeddingsDBPath(app.Embeddings.DBPath, cfg.DataDir))
	if err != nil {
		fatal(err)
	}
	defer es.Close()
	n, err := consolidate.Run(context.Background(), s, es, embed.New(app.Embeddings.BaseURL, app.Consolidation.Model, 0), app.Consolidation, *project)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("consolidate: wrote %d digests\n", n)
}
