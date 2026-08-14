package embed

import (
	"sync"
	"testing"
)

func TestNormalizeQuery(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{"already normalized", "already normal", "already normal"},
		{"trims and lowercases", "  Hello   World  ", "hello world"},
		{"collapses mixed whitespace", "MIXED Case\tTabs\nNewlines", "mixed case tabs newlines"},
		{"no accent folding", "café ÀÉ", "café àé"},
		{"empty string", "", ""},
		{"whitespace only", "   \t\n  ", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeQuery(tt.query); got != tt.want {
				t.Errorf("normalizeQuery(%q) = %q, want %q", tt.query, got, tt.want)
			}
		})
	}
}

func TestQueryCache_GetMissThenPutThenHit(t *testing.T) {
	c := NewQueryCache("jina", 10)

	if _, ok := c.Get("what is omnia"); ok {
		t.Fatal("Get on empty cache: got hit, want miss")
	}

	vec := []float32{1, 2, 3}
	c.Put("what is omnia", vec)

	got, ok := c.Get("what is omnia")
	if !ok {
		t.Fatal("Get after Put: got miss, want hit")
	}
	if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Errorf("Get vec: got %v, want %v", got, vec)
	}

	stats := c.Stats()
	if stats.Hits != 1 || stats.Misses != 1 {
		t.Errorf("Stats: got hits=%d misses=%d, want hits=1 misses=1", stats.Hits, stats.Misses)
	}
}

func TestQueryCache_NormalizesKeyOnLookup(t *testing.T) {
	c := NewQueryCache("jina", 10)
	c.Put("Hello   World", []float32{1})

	if _, ok := c.Get("  hello world "); !ok {
		t.Error("Get with different casing/whitespace: got miss, want hit (normalization should collapse both to the same key)")
	}
}

func TestQueryCache_ReturnedVectorIsNotAliased(t *testing.T) {
	c := NewQueryCache("jina", 10)
	original := []float32{1, 2, 3}
	c.Put("q", original)

	// Mutate the caller's own slice after Put — must not affect the cache.
	original[0] = 999

	got, ok := c.Get("q")
	if !ok {
		t.Fatal("Get: want hit")
	}
	if got[0] != 1 {
		t.Errorf("cache aliased caller's Put slice: got %v, want [1 2 3]", got)
	}

	// Mutate the slice returned by Get — must not affect a later Get.
	got[0] = 42
	got2, _ := c.Get("q")
	if got2[0] != 1 {
		t.Errorf("cache aliased caller's Get slice: got %v, want [1 2 3]", got2)
	}
}

func TestQueryCache_DifferentModelsDoNotCollide(t *testing.T) {
	jina := NewQueryCache("jina-embeddings-v2-base-es", 10)
	bge := NewQueryCache("bge-m3", 10)

	jina.Put("same query", []float32{1, 0, 0})
	bge.Put("same query", []float32{0, 1, 0})

	jinaVec, ok := jina.Get("same query")
	if !ok || jinaVec[0] != 1 {
		t.Errorf("jina cache: got %v, ok=%v, want [1 0 0] ok=true", jinaVec, ok)
	}
	bgeVec, ok := bge.Get("same query")
	if !ok || bgeVec[1] != 1 {
		t.Errorf("bge cache: got %v, ok=%v, want [0 1 0] ok=true", bgeVec, ok)
	}
}

func TestQueryCache_SetModelID_StaleEntriesBecomeUnreachable(t *testing.T) {
	c := NewQueryCache("model-a", 10)
	c.Put("query", []float32{1, 0})

	c.SetModelID("model-b")
	if _, ok := c.Get("query"); ok {
		t.Fatal("Get after SetModelID to a different model: got hit, want miss (must not serve model-a's vector under model-b)")
	}

	c.Put("query", []float32{0, 1})
	got, ok := c.Get("query")
	if !ok || got[1] != 1 {
		t.Errorf("Get under model-b after its own Put: got %v ok=%v, want [0 1] ok=true", got, ok)
	}

	// Switching back to a model that still has surviving entries resurfaces
	// genuinely correct vectors for that model — not staleness.
	c.SetModelID("model-a")
	got, ok = c.Get("query")
	if !ok || got[0] != 1 {
		t.Errorf("Get after switching back to model-a: got %v ok=%v, want [1 0] ok=true", got, ok)
	}
}

func TestQueryCache_EvictsLeastRecentlyUsed(t *testing.T) {
	c := NewQueryCache("jina", 2)
	c.Put("a", []float32{1})
	c.Put("b", []float32{2})

	// Touch "a" so "b" becomes the least-recently-used entry.
	if _, ok := c.Get("a"); !ok {
		t.Fatal("Get(a): want hit")
	}

	c.Put("c", []float32{3}) // capacity 2 — evicts "b", the LRU entry.

	if _, ok := c.Get("b"); ok {
		t.Error("Get(b) after eviction: got hit, want miss")
	}
	if _, ok := c.Get("a"); !ok {
		t.Error("Get(a): want hit (recently used, should survive eviction)")
	}
	if _, ok := c.Get("c"); !ok {
		t.Error("Get(c): want hit (just inserted)")
	}

	if got := c.Stats().Entries; got != 2 {
		t.Errorf("Stats().Entries = %d, want 2", got)
	}
}

func TestQueryCache_PutOverwritesExistingEntryWithoutGrowing(t *testing.T) {
	c := NewQueryCache("jina", 10)
	c.Put("q", []float32{1})
	c.Put("q", []float32{2})

	got, ok := c.Get("q")
	if !ok || got[0] != 2 {
		t.Errorf("Get after overwrite: got %v ok=%v, want [2] ok=true", got, ok)
	}
	if got := c.Stats().Entries; got != 1 {
		t.Errorf("Stats().Entries after overwrite = %d, want 1", got)
	}
}

func TestQueryCache_DefaultCapacity(t *testing.T) {
	c := NewQueryCache("jina", 0)
	if c.capacity != defaultQueryCacheCapacity {
		t.Errorf("capacity = %d, want default %d", c.capacity, defaultQueryCacheCapacity)
	}

	c = NewQueryCache("jina", -5)
	if c.capacity != defaultQueryCacheCapacity {
		t.Errorf("capacity with negative arg = %d, want default %d", c.capacity, defaultQueryCacheCapacity)
	}
}

func TestQueryCacheStats_HitRate(t *testing.T) {
	tests := []struct {
		name   string
		stats  QueryCacheStats
		wantHR float64
	}{
		{"no lookups yet", QueryCacheStats{}, 0},
		{"all hits", QueryCacheStats{Hits: 10}, 1},
		{"all misses", QueryCacheStats{Misses: 10}, 0},
		{"mixed", QueryCacheStats{Hits: 3, Misses: 7}, 0.3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.stats.HitRate(); got != tt.wantHR {
				t.Errorf("HitRate() = %v, want %v", got, tt.wantHR)
			}
		})
	}
}

// TestQueryCache_ConcurrentAccess exercises Get/Put/Stats/SetModelID from
// many goroutines at once. GET /search is served from an HTTP handler
// (multiple concurrent requests share one process-wide cache), so this is
// the load shape that matters — run with -race.
func TestQueryCache_ConcurrentAccess(t *testing.T) {
	c := NewQueryCache("jina", 50)
	const goroutines = 32
	const opsPerGoroutine = 200

	queries := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot"}

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(seed int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				q := queries[(seed+i)%len(queries)]
				if _, ok := c.Get(q); !ok {
					c.Put(q, []float32{float32(seed), float32(i)})
				}
				_ = c.Stats()
				if i%50 == 0 {
					c.SetModelID("jina") // no-op value churn, exercises the write path
				}
			}
		}(g)
	}
	wg.Wait()

	stats := c.Stats()
	if stats.Hits+stats.Misses == 0 {
		t.Fatal("no Get calls were recorded")
	}
	if stats.Entries > stats.Capacity {
		t.Errorf("Entries=%d exceeds Capacity=%d after concurrent access", stats.Entries, stats.Capacity)
	}
}
