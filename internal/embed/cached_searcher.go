package embed

import "context"

// CachedSearcher decorates a Searcher, memoizing EmbedQuery in an in-process
// QueryCache (P6, docs/conversational-retrieval-plan.md). Search and
// SearchScoped pass straight through to next, unmodified — caching only
// ever applies to the query-embedding call, never to the vector search
// itself, matching the plan's scope of "an LRU keyed on the normalized
// query string," not a general recall cache.
//
// CachedSearcher must only be constructed when an operator has explicitly
// opted in via config — this package has no opinion on that config (it
// depends only on engramdb, meta, and the standard library, per this
// package's own doc comment), so the zero-value-is-default gate and its
// wiring live at the composition root, not here.
type CachedSearcher struct {
	next  Searcher
	cache *QueryCache
}

// NewCachedSearcher wraps next, caching EmbedQuery results in a QueryCache
// scoped to modelID with the given capacity (<=0 uses
// defaultQueryCacheCapacity — see docs/conversational-retrieval-plan.md P6's
// "~200 entries"). modelID should be the exact embedding model identifier
// the caller configured next with (e.g. Config.Embeddings.Model), so a
// model switch can never serve a stale vector — see QueryCache.key.
func NewCachedSearcher(next Searcher, modelID string, capacity int) *CachedSearcher {
	return &CachedSearcher{next: next, cache: NewQueryCache(modelID, capacity)}
}

// EmbedQuery serves from the cache on a normalized-query hit; on a miss it
// delegates to next and stores the result before returning. An error from
// next is never cached — only successful embeddings are, so a transient
// Ollama failure doesn't get "stuck" as a cached error.
func (c *CachedSearcher) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	if vec, ok := c.cache.Get(text); ok {
		return vec, nil
	}
	vec, err := c.next.EmbedQuery(ctx, text)
	if err != nil {
		return nil, err
	}
	c.cache.Put(text, vec)
	return vec, nil
}

// Search passes through to next unchanged — only EmbedQuery is cached.
func (c *CachedSearcher) Search(ctx context.Context, vec []float32, k int) ([]Hit, error) {
	return c.next.Search(ctx, vec, k)
}

// SearchScoped satisfies ScopedSearcher, delegating to next's own
// SearchScoped when next implements it, and degrading to unscoped Search
// otherwise. This mirrors recall.Service.semanticHits' own fallback (see
// internal/recall/service.go) so wrapping a Searcher in a CachedSearcher
// never silently drops project scoping for a next that supports it — a
// caller type-asserting a CachedSearcher against ScopedSearcher always sees
// the same capability next itself has.
func (c *CachedSearcher) SearchScoped(ctx context.Context, vec []float32, k int, project string) ([]Hit, error) {
	if scoped, ok := c.next.(ScopedSearcher); ok {
		return scoped.SearchScoped(ctx, vec, k, project)
	}
	return c.next.Search(ctx, vec, k)
}

// Stats returns the underlying QueryCache's hit/miss counters and size —
// P6's ship/delete-gate instrumentation. See QueryCache's doc comment.
func (c *CachedSearcher) Stats() QueryCacheStats {
	return c.cache.Stats()
}

// SetModelID re-points the underlying QueryCache at a new model identity —
// see QueryCache.SetModelID's doc for when a composition root needs this
// (config reload without a process restart).
func (c *CachedSearcher) SetModelID(modelID string) {
	c.cache.SetModelID(modelID)
}
