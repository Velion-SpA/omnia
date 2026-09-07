package embed

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// cachedSearcherFake is a minimal Searcher fake that counts EmbedQuery calls,
// used to prove CachedSearcher actually avoids calling the wrapped Searcher
// on a cache hit — no live Ollama needed.
type cachedSearcherFake struct {
	mu    sync.Mutex
	calls int
	vec   []float32
	err   error
}

func (f *cachedSearcherFake) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return f.vec, nil
}

func (f *cachedSearcherFake) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *cachedSearcherFake) Search(ctx context.Context, vec []float32, k int) ([]Hit, error) {
	return []Hit{{SyncID: "unscoped"}}, nil
}

// scopedFake additionally implements ScopedSearcher, so CachedSearcher.
// SearchScoped can be tested against a next that supports project scoping.
type scopedFake struct {
	*cachedSearcherFake
	scopedCalls int
	lastProject string
}

func (f *scopedFake) SearchScoped(ctx context.Context, vec []float32, k int, project string) ([]Hit, error) {
	f.scopedCalls++
	f.lastProject = project
	return []Hit{{SyncID: "scoped"}}, nil
}

func TestCachedSearcher_EmbedQuery_CachesOnNormalizedQuery(t *testing.T) {
	fake := &cachedSearcherFake{vec: []float32{1, 2, 3}}
	cs := NewCachedSearcher(fake, "jina", 10)
	ctx := context.Background()

	vec, err := cs.EmbedQuery(ctx, "What is Omnia")
	if err != nil {
		t.Fatalf("EmbedQuery: %v", err)
	}
	if len(vec) != 3 || vec[0] != 1 {
		t.Errorf("EmbedQuery vec: got %v, want [1 2 3]", vec)
	}
	if got := fake.callCount(); got != 1 {
		t.Fatalf("underlying calls after first EmbedQuery: got %d, want 1", got)
	}

	// Same query, different case/whitespace — must hit the cache, not call next.
	if _, err := cs.EmbedQuery(ctx, "  what is omnia  "); err != nil {
		t.Fatalf("EmbedQuery (cached): %v", err)
	}
	if got := fake.callCount(); got != 1 {
		t.Errorf("underlying calls after cached EmbedQuery: got %d, want 1 (should have been served from cache)", got)
	}

	// A genuinely different query must still reach next.
	if _, err := cs.EmbedQuery(ctx, "something else entirely"); err != nil {
		t.Fatalf("EmbedQuery (different query): %v", err)
	}
	if got := fake.callCount(); got != 2 {
		t.Errorf("underlying calls after a different query: got %d, want 2", got)
	}

	stats := cs.Stats()
	if stats.Hits != 1 || stats.Misses != 2 {
		t.Errorf("Stats: got hits=%d misses=%d, want hits=1 misses=2", stats.Hits, stats.Misses)
	}
}

func TestCachedSearcher_EmbedQuery_ErrorsAreNeverCached(t *testing.T) {
	fake := &cachedSearcherFake{err: errors.New("ollama unreachable")}
	cs := NewCachedSearcher(fake, "jina", 10)
	ctx := context.Background()

	if _, err := cs.EmbedQuery(ctx, "q"); err == nil {
		t.Fatal("EmbedQuery: want error, got nil")
	}
	if _, err := cs.EmbedQuery(ctx, "q"); err == nil {
		t.Fatal("EmbedQuery (second attempt): want error, got nil")
	}
	if got := fake.callCount(); got != 2 {
		t.Errorf("underlying calls: got %d, want 2 (a failed embed must never be cached)", got)
	}
}

func TestCachedSearcher_Search_PassesThroughUnchanged(t *testing.T) {
	fake := &cachedSearcherFake{}
	cs := NewCachedSearcher(fake, "jina", 10)

	hits, err := cs.Search(context.Background(), []float32{1, 0, 0}, 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].SyncID != "unscoped" {
		t.Errorf("Search: got %+v, want a single unscoped hit", hits)
	}
}

func TestCachedSearcher_SearchScoped_DelegatesWhenNextSupportsIt(t *testing.T) {
	fake := &scopedFake{cachedSearcherFake: &cachedSearcherFake{}}
	cs := NewCachedSearcher(fake, "jina", 10)

	hits, err := cs.SearchScoped(context.Background(), []float32{1, 0, 0}, 5, "project-x")
	if err != nil {
		t.Fatalf("SearchScoped: %v", err)
	}
	if fake.scopedCalls != 1 || fake.lastProject != "project-x" {
		t.Errorf("SearchScoped delegation: got scopedCalls=%d lastProject=%q, want 1 project-x", fake.scopedCalls, fake.lastProject)
	}
	if len(hits) != 1 || hits[0].SyncID != "scoped" {
		t.Errorf("SearchScoped: got %+v, want a single scoped hit", hits)
	}
}

func TestCachedSearcher_SearchScoped_DegradesWhenNextDoesNotSupportIt(t *testing.T) {
	fake := &cachedSearcherFake{} // does NOT implement ScopedSearcher
	cs := NewCachedSearcher(fake, "jina", 10)

	hits, err := cs.SearchScoped(context.Background(), []float32{1, 0, 0}, 5, "project-x")
	if err != nil {
		t.Fatalf("SearchScoped: %v", err)
	}
	if len(hits) != 1 || hits[0].SyncID != "unscoped" {
		t.Errorf("SearchScoped degrade: got %+v, want the unscoped Search fallback result", hits)
	}
}

func TestCachedSearcher_SetModelID_InvalidatesAcrossModelSwitch(t *testing.T) {
	fake := &cachedSearcherFake{vec: []float32{9, 9, 9}}
	cs := NewCachedSearcher(fake, "model-a", 10)
	ctx := context.Background()

	if _, err := cs.EmbedQuery(ctx, "q"); err != nil {
		t.Fatalf("EmbedQuery: %v", err)
	}
	if _, err := cs.EmbedQuery(ctx, "q"); err != nil {
		t.Fatalf("EmbedQuery (cached): %v", err)
	}
	if got := fake.callCount(); got != 1 {
		t.Fatalf("calls before model switch: got %d, want 1", got)
	}

	cs.SetModelID("model-b")
	if _, err := cs.EmbedQuery(ctx, "q"); err != nil {
		t.Fatalf("EmbedQuery after model switch: %v", err)
	}
	if got := fake.callCount(); got != 2 {
		t.Errorf("calls after model switch: got %d, want 2 (must not serve model-a's cached vector under model-b)", got)
	}
}

// TestCachedSearcher_ConcurrentEmbedQuery exercises the exact load shape
// GET /search produces: many concurrent HTTP handlers calling EmbedQuery on
// one shared CachedSearcher. Run with -race.
func TestCachedSearcher_ConcurrentEmbedQuery(t *testing.T) {
	fake := &cachedSearcherFake{vec: []float32{1, 2, 3}}
	cs := NewCachedSearcher(fake, "jina", 20)
	ctx := context.Background()

	queries := []string{"one", "two", "three", "four", "five"}
	const goroutines = 40

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(seed int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				q := queries[(seed+i)%len(queries)]
				if _, err := cs.EmbedQuery(ctx, q); err != nil {
					t.Errorf("EmbedQuery(%q): %v", q, err)
				}
				_ = cs.Stats()
			}
		}(g)
	}
	wg.Wait()

	stats := cs.Stats()
	if stats.Hits+stats.Misses == 0 {
		t.Fatal("no EmbedQuery calls were recorded")
	}
	// Only 5 distinct normalized queries exist, well under capacity, so the
	// underlying embedder should be called at most once per distinct query
	// (plus a small allowance for the initial concurrent miss stampede,
	// where multiple goroutines can race past an empty cache for the same
	// key before the first Put lands).
	if got := fake.callCount(); got > len(queries)*goroutines {
		t.Errorf("underlying calls: got %d, suspiciously high for %d distinct queries", got, len(queries))
	}
}
