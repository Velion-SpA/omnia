package dashboard

import (
	"context"
	"log/slog"

	"github.com/Velion-SpA/omnia/internal/embed"
	"github.com/Velion-SpA/omnia/internal/engramdb"
)

// localDataSource is the production DataSource for the local dashboard (:7800).
// It wraps exactly the three backends the dashboard has always used: the Engram
// HTTP client (records + mutations), the engramdb SQLite reader (structural
// enumeration), and the embeddings store + client (semantic search + graph).
// db, emb and embClient are nil when their backend is unavailable, preserving the
// dashboard's original graceful-degradation behaviour.
type localDataSource struct {
	client    *engramClient
	db        *engramdb.DB   // nil → structural reads fall back to HTTP/FTS
	emb       *embed.Store   // nil → semantic search/graph unavailable
	embClient embed.Embedder // nil → semantic search unavailable
}

// newLocalDataSource opens the local backends, mirroring the original
// NewServer wiring. A failure to open engramdb or the embeddings store is logged
// and leaves that capability disabled rather than failing the whole dashboard.
func newLocalDataSource(cfg Config, logger *slog.Logger) *localDataSource {
	l := &localDataSource{client: newEngramClient(cfg.EngramURL)}

	db, err := engramdb.Open(cfg.EngramDataDir)
	if err != nil {
		logger.Warn("engramdb unavailable; structural queries fall back to HTTP/FTS", "err", err)
	} else {
		l.db = db
	}

	// Optional semantic-search layer. A failure to open the store leaves emb nil,
	// and browse transparently falls back to keyword (FTS) search.
	if cfg.EmbeddingsEnabled {
		store, err := embed.OpenStore(cfg.EmbeddingsDBPath)
		if err != nil {
			logger.Warn("embeddings store unavailable; semantic search disabled, using FTS", "err", err)
		} else {
			l.emb = store
			l.embClient = embed.New(cfg.EmbeddingsBaseURL, cfg.EmbeddingsModel, cfg.EmbeddingsDim)
			logger.Info("semantic search enabled", "model", cfg.EmbeddingsModel, "db", cfg.EmbeddingsDBPath)
		}
	}
	return l
}

func (l *localDataSource) Health(ctx context.Context) error { return l.client.Health(ctx) }

func (l *localDataSource) Records() RecordReader { return l.client }

func (l *localDataSource) Structural() (StructuralReader, bool) {
	if l.db == nil {
		return nil, false
	}
	return l.db, true
}

func (l *localDataSource) Semantic() (SemanticIndex, bool) {
	if l.emb == nil || l.embClient == nil {
		return nil, false
	}
	return localSemantic{store: l.emb, embClient: l.embClient}, true
}

func (l *localDataSource) Mutations() (MutationWriter, bool) { return l.client, true }

func (l *localDataSource) Close() error {
	// Close the SQLite handles so their WAL is checkpointed on clean shutdown.
	if l.emb != nil {
		_ = l.emb.Close()
	}
	if l.db != nil {
		return l.db.Close()
	}
	return nil
}

// localSemantic adapts the embeddings store + client to SemanticIndex. EmbedQuery
// pins the search_query task so interactive queries embed with the correct prefix.
type localSemantic struct {
	store     *embed.Store
	embClient embed.Embedder
}

func (s localSemantic) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	return s.embClient.Embed(ctx, text, embed.TaskQuery)
}

func (s localSemantic) Search(ctx context.Context, vec []float32, k int) ([]embed.Hit, error) {
	return s.store.Search(ctx, vec, k)
}

func (s localSemantic) Graph(k int, minScore float32) ([]embed.GraphNode, []embed.GraphEdge, error) {
	return s.store.Graph(k, minScore)
}
