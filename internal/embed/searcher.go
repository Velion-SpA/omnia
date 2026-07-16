package embed

import "context"

// Searcher is the single shared semantic vector-search port (design D6). It is
// the ONE seam both mem_search recall (internal/recall.Service) and
// memory-conflict-semantic's deferred FindCandidates depend on, so there is
// never a second vector-search implementation to keep in sync.
type Searcher interface {
	// EmbedQuery embeds interactive text (a search query) into a vector.
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
	// Search returns the top-k stored rows by cosine similarity to vec.
	Search(ctx context.Context, vec []float32, k int) ([]Hit, error)
}

// LocalSearcher is the default Searcher, backed by this package's own brute-
// force cosine Store and an Embedder (the Ollama HTTP client in production,
// a fake in tests). It also exposes Graph so it can satisfy
// dashboard.SemanticIndex unchanged — this type promotes what used to be the
// dashboard-local `localSemantic` adapter into a reusable, shared type.
type LocalSearcher struct {
	store    *Store
	embedder Embedder
}

// NewSearcher builds the default Searcher implementation over store and
// embedder. Either may be nil in tests that only exercise the other half of
// the delegation.
func NewSearcher(store *Store, embedder Embedder) *LocalSearcher {
	return &LocalSearcher{store: store, embedder: embedder}
}

// EmbedQuery delegates to the configured Embedder.
func (s *LocalSearcher) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	return s.embedder.Embed(ctx, text)
}

// Search delegates to the configured Store's brute-force cosine search.
func (s *LocalSearcher) Search(ctx context.Context, vec []float32, k int) ([]Hit, error) {
	return s.store.Search(ctx, vec, k)
}

// Graph delegates to the configured Store's k-NN similarity graph builder.
// Not part of the Searcher interface, but kept so LocalSearcher continues to
// satisfy dashboard.SemanticIndex (which also needs Graph) without a second
// adapter type.
func (s *LocalSearcher) Graph(k int, minScore float32) ([]GraphNode, []GraphEdge, error) {
	return s.store.Graph(k, minScore)
}
