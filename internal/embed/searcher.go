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

// ScopedSearcher is an OPTIONAL capability a Searcher implementation may
// additionally satisfy: search restricted to a single project, with the
// top-k computed WITHIN that project instead of globally-then-filtered.
//
// Bugfix context (engram obs #1436): Store.Search brute-forces cosine over
// every project's vectors. For a project that is a small fraction of the
// store, a query where other projects score higher crowds the target
// project's valid matches out of the global top-k before any caller-side
// project filter runs. recall.Service.semanticHits type-asserts its
// Semantic field against this interface and prefers SearchScoped whenever a
// project is known, falling back to plain Search when the concrete Searcher
// doesn't implement it (e.g. test fakes) — so this is purely additive: no
// existing Searcher implementation or caller is required to change.
type ScopedSearcher interface {
	// SearchScoped returns the top-k stored rows, restricted to project, by
	// cosine similarity to vec. project == "" behaves like Search (no
	// restriction).
	SearchScoped(ctx context.Context, vec []float32, k int, project string) ([]Hit, error)
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

// SearchScoped delegates to the configured Store's project-scoped brute-force
// cosine search, satisfying ScopedSearcher. project == "" searches the whole
// store, identical to Search.
func (s *LocalSearcher) SearchScoped(ctx context.Context, vec []float32, k int, project string) ([]Hit, error) {
	return s.store.SearchScoped(ctx, vec, k, project)
}

// Graph delegates to the configured Store's k-NN similarity graph builder,
// scoped to projects (nil/empty = whole store, see Store.GraphScoped — H3
// perf fix). Not part of the Searcher interface, but kept so LocalSearcher
// continues to satisfy dashboard.SemanticIndex (which also needs Graph)
// without a second adapter type.
func (s *LocalSearcher) Graph(projects []string, k int, minScore float32) ([]GraphNode, []GraphEdge, error) {
	return s.store.GraphScoped(projects, k, minScore)
}
