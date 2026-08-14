package embed

import (
	"container/list"
	"strings"
	"sync"
)

// defaultQueryCacheCapacity is the entry-count LRU eviction bound
// (docs/conversational-retrieval-plan.md P6: "an LRU keyed on the
// normalized query string... ~200 entries"). A caller may override it via
// NewQueryCache/NewCachedSearcher's capacity argument; this constant is
// only the fallback when capacity <= 0.
const defaultQueryCacheCapacity = 200

// QueryCache is an in-process, concurrency-safe LRU cache from a normalized
// query string to its embedding vector, scoped to one embedding model.
//
// P6 (docs/conversational-retrieval-plan.md): GET /search's 82-104ms is
// dominated by the network hop to the Ollama embedding server, but that is
// NOT where a voice turn's perceived latency lives (~2.5s end-to-end
// target) — this cache is included only because it is nearly free, not
// because it is a priority, and the plan is explicitly sceptical of it: a
// voice assistant may simply never repeat a query verbatim, in which case
// this is dead code that looks like an optimization. The SHIP GATE is
// real-traffic hit rate measured over about a week — under ~20%, delete
// this cache. Stats/HitRate below exist so that decision can be made from
// data, not vibes; they are the point of this file, not the speedup.
type QueryCache struct {
	mu       sync.Mutex
	capacity int
	modelID  string

	ll      *list.List               // front = most recently used, back = eviction candidate
	entries map[string]*list.Element // key -> element wrapping *queryCacheEntry

	hits   uint64
	misses uint64
}

type queryCacheEntry struct {
	key string
	vec []float32
}

// NewQueryCache builds a QueryCache scoped to modelID (see key's doc for why
// the model identity is part of every cache key). capacity <= 0 uses
// defaultQueryCacheCapacity.
func NewQueryCache(modelID string, capacity int) *QueryCache {
	if capacity <= 0 {
		capacity = defaultQueryCacheCapacity
	}
	return &QueryCache{
		capacity: capacity,
		modelID:  modelID,
		ll:       list.New(),
		entries:  make(map[string]*list.Element, capacity),
	}
}

// SetModelID updates the model identity every subsequent key() uses. Most
// callers never need this — a config-driven model choice is normally fixed
// for the process lifetime, so one QueryCache per (composition-root-built)
// CachedSearcher is enough. It exists for a composition root that reloads
// config without a process restart: calling SetModelID after an
// `embeddings.model` change re-points the same cache instance at the new
// model's key prefix WITHOUT a separate flush step — entries recorded under
// the previous modelID simply become unreachable under the new prefix (see
// key's doc), so a reload can never mix vectors from two models under one
// key. Switching back to a previously-used modelID is also safe: any
// surviving entries under that prefix were genuinely produced by that model,
// so re-surfacing them is correct, not stale.
func (c *QueryCache) SetModelID(modelID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.modelID = modelID
}

// key combines c.modelID with the normalized query so that switching
// embedding models (an `embeddings.model` config edit, or an operator
// pointing at a different Ollama model) can never serve a stale vector from
// a different model's embedding space — P6's explicit requirement. This is
// a keying strategy, not active invalidation: entries recorded under a
// previous model's prefix simply become permanently unreachable once
// modelID changes, and age out under normal LRU pressure like any other
// cold entry — no separate flush/reset path is needed. Callers must hold
// c.mu (Get/Put already do).
func (c *QueryCache) key(query string) string {
	return c.modelID + "\x00" + normalizeQuery(query)
}

// normalizeQuery is DELIBERATELY conservative: trim + lowercase + collapse
// internal whitespace, nothing more. P6 explicitly rules out stemming,
// accent-folding, or any other semantic normalization — a cache that
// returns the embedding of a DIFFERENT query than the one asked is a
// correctness bug, and one that would be nearly invisible in production (a
// wrong-but-plausible vector doesn't error, it just silently narrows recall
// for whoever typed something slightly different). strings.Fields already
// trims leading/trailing whitespace and splits on any run of whitespace, so
// rejoining with single spaces gives trim + collapse in one pass;
// strings.ToLower is plain Unicode case folding, not accent-folding — "café"
// stays "café", it does not become "cafe".
func normalizeQuery(query string) string {
	return strings.Join(strings.Fields(strings.ToLower(query)), " ")
}

// Get returns a defensive copy of the cached vector for query under c's
// model. The returned slice is never an alias into the cache's stored
// vector, so the caller mutating it cannot corrupt the cache. Every call
// counts as either a hit or a miss for Stats/HitRate.
func (c *QueryCache) Get(query string) ([]float32, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	k := c.key(query)
	el, ok := c.entries[k]
	if !ok {
		c.misses++
		return nil, false
	}
	c.ll.MoveToFront(el)
	c.hits++
	return cloneVec(el.Value.(*queryCacheEntry).vec), true
}

// Put stores a defensive copy of vec for query under c's model, evicting the
// least-recently-used entry if c is now over capacity. Put never touches the
// hit/miss counters — those are Get's job, and on CachedSearcher's
// EmbedQuery path a Put always follows a Get miss, so counting there too
// would double-count every fill as its own miss.
func (c *QueryCache) Put(query string, vec []float32) {
	stored := cloneVec(vec)

	c.mu.Lock()
	defer c.mu.Unlock()

	k := c.key(query)
	if el, ok := c.entries[k]; ok {
		el.Value.(*queryCacheEntry).vec = stored
		c.ll.MoveToFront(el)
		return
	}
	el := c.ll.PushFront(&queryCacheEntry{key: k, vec: stored})
	c.entries[k] = el
	if c.ll.Len() > c.capacity {
		c.evictOldestLocked()
	}
}

// evictOldestLocked removes the least-recently-used entry. Callers must hold
// c.mu.
func (c *QueryCache) evictOldestLocked() {
	oldest := c.ll.Back()
	if oldest == nil {
		return
	}
	c.ll.Remove(oldest)
	delete(c.entries, oldest.Value.(*queryCacheEntry).key)
}

func cloneVec(v []float32) []float32 {
	out := make([]float32, len(v))
	copy(out, v)
	return out
}

// QueryCacheStats is a point-in-time snapshot of a QueryCache's hit/miss
// counters and size — the instrumentation P6 exists to produce (see
// QueryCache's doc comment for the ship/delete gate this feeds).
type QueryCacheStats struct {
	Hits     uint64
	Misses   uint64
	Entries  int
	Capacity int
}

// HitRate returns Hits / (Hits + Misses), or 0 when there have been no
// lookups yet (avoids a 0/0 NaN that would otherwise poison a dashboard
// average).
func (s QueryCacheStats) HitRate() float64 {
	total := s.Hits + s.Misses
	if total == 0 {
		return 0
	}
	return float64(s.Hits) / float64(total)
}

// Stats returns a snapshot of c's current hit/miss counters and size.
func (c *QueryCache) Stats() QueryCacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return QueryCacheStats{
		Hits:     c.hits,
		Misses:   c.misses,
		Entries:  c.ll.Len(),
		Capacity: c.capacity,
	}
}
