package main

import (
	"context"
	"os"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/diagnostic"
	"github.com/velion/omnia/internal/embed"
	"github.com/velion/omnia/internal/engramdb"
	"github.com/velion/omnia/internal/store"
)

// readEmbeddingSnapshot pairs the observation store's watermark with the
// embeddings store's, for diagnostic.EmbeddingLagCheck (#226).
//
// It resolves the embeddings database exactly the way `omnia embed` does —
// same data dir, same encryption config, same options helper — so the check
// reads the file the embedding job actually writes. Reading it any other way
// would risk reporting on a different store than the one that is behind.
func readEmbeddingSnapshot(ctx context.Context, s *store.Store) (diagnostic.EmbeddingSnapshot, error) {
	appCfg, err := config.Load(globalConfigPath(os.Args[1:]))
	if err != nil {
		// No readable config.yaml is the fresh-install case: embeddings are
		// off by default, which is a configuration, not a defect.
		return diagnostic.EmbeddingSnapshot{Enabled: false}, nil
	}
	if !appCfg.Embeddings.Enabled {
		return diagnostic.EmbeddingSnapshot{Enabled: false}, nil
	}

	maxID, count, err := s.ObservationWatermark()
	if err != nil {
		return diagnostic.EmbeddingSnapshot{}, err
	}

	dbPath := config.ResolveEmbeddingsDBPath(appCfg.Embeddings.DBPath, engramdb.ResolveDataDir(""))
	es, err := embed.OpenStore(dbPath, embedStoreOptions(appCfg.VecIndex.Enabled, appCfg.Encryption)...)
	if err != nil {
		return diagnostic.EmbeddingSnapshot{}, err
	}
	defer es.Close()

	lag, err := es.Lag(ctx)
	if err != nil {
		return diagnostic.EmbeddingSnapshot{}, err
	}

	return diagnostic.EmbeddingSnapshot{
		Enabled:           true,
		ObservationMaxID:  maxID,
		ObservationCount:  count,
		EmbeddingMaxObsID: lag.MaxObsID,
		EmbeddingCount:    lag.Count,
		NewestEmbeddedAt:  lag.NewestEmbeddedAt,
		EmbeddingsDBPath:  dbPath,
	}, nil
}
