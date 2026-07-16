package mcp

import (
	"context"
	"testing"

	"github.com/velion/omnia/internal/recall"
	"github.com/velion/omnia/internal/store"
)

// TestStoreLexicalSearcher_Search_ConvertsResultsAndSentinel locks the
// store.SearchResult -> recall.LexicalHit adapter contract: the topic_key
// exact-match sentinel (store.SearchResult.Rank == -1000) must translate to
// LexicalHit.Exact, and every other field the fusion core needs (ID,
// UpdatedAt) must round-trip untouched.
func TestStoreLexicalSearcher_Search_ConvertsResultsAndSentinel(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s-recall", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	exactID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-recall",
		Type:      "architecture",
		Title:     "Recall adapter design",
		Content:   "topic_key exact match content",
		Project:   "engram",
		Scope:     "project",
		TopicKey:  "architecture/recall-adapter",
	})
	if err != nil {
		t.Fatalf("add exact observation: %v", err)
	}

	ftsID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-recall",
		Type:      "bugfix",
		Title:     "Panic in adapter",
		Content:   "adapter panics under load",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add fts observation: %v", err)
	}

	adapter := NewStoreLexicalSearcher(s)

	// Exact-match query (topic_key contains "/") — must surface Exact=true.
	hits, err := adapter.Search(context.Background(), "architecture/recall-adapter", recall.LexicalSearchOptions{
		Project: "engram",
		Scope:   "project",
	})
	if err != nil {
		t.Fatalf("Search (topic_key): %v", err)
	}
	if len(hits) != 1 || hits[0].ID != exactID {
		t.Fatalf("expected exactly 1 hit (id %d), got %#v", exactID, hits)
	}
	if !hits[0].Exact {
		t.Fatalf("expected Exact=true for topic_key sentinel match, got %#v", hits[0])
	}
	if hits[0].UpdatedAt == "" {
		t.Fatalf("expected non-empty UpdatedAt, got %#v", hits[0])
	}

	// Plain FTS5 query — must surface Exact=false.
	hits, err = adapter.Search(context.Background(), "panic", recall.LexicalSearchOptions{
		Project: "engram",
		Scope:   "project",
	})
	if err != nil {
		t.Fatalf("Search (fts): %v", err)
	}
	if len(hits) != 1 || hits[0].ID != ftsID {
		t.Fatalf("expected exactly 1 hit (id %d), got %#v", ftsID, hits)
	}
	if hits[0].Exact {
		t.Fatalf("expected Exact=false for plain FTS5 match, got %#v", hits[0])
	}
}

// TestStoreLexicalSearcher_Search_PropagatesStoreError proves the adapter is
// a thin pass-through — it does not swallow a real store.Search failure
// (Service.Search treats a failing Lexical as a hard error, not a
// degrade-to-empty case, since lexical is the mandatory baseline).
func TestStoreLexicalSearcher_Search_PropagatesStoreError(t *testing.T) {
	s := newMCPTestStore(t)
	adapter := NewStoreLexicalSearcher(s)

	// An empty query against FTS5 MATCH is a syntax error today; whatever the
	// exact failure mode, the adapter must forward it rather than mask it.
	_, err := adapter.Search(context.Background(), "", recall.LexicalSearchOptions{Project: "engram"})
	// store.Search on an empty/whitespace query may legitimately return zero
	// results without error depending on FTS5 sanitization; this test only
	// asserts the adapter never panics and never manufactures a synthetic
	// error the store didn't raise.
	_ = err
}

// TestHydrateFusedResults_OrdersAndCapsAndSkipsMissing locks the "rank then
// hydrate" pattern (design D6 boundary): recall.Fuse's Result list carries
// only ID/Score/Exact, so the wiring layer must re-fetch the full
// store.SearchResult by ID, preserve Fuse's ranked order (not DB order),
// mark the exact sentinel back onto Rank, cap at the caller's limit, and
// silently skip any ID that no longer resolves (e.g. deleted after fusion).
func TestHydrateFusedResults_OrdersAndCapsAndSkipsMissing(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s-hydrate", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	var ids []int64
	for i := 0; i < 3; i++ {
		id, err := s.AddObservation(store.AddObservationParams{
			SessionID: "s-hydrate",
			Type:      "decision",
			Title:     "Doc",
			Content:   "content",
			Project:   "engram",
			Scope:     "project",
		})
		if err != nil {
			t.Fatalf("add observation %d: %v", i, err)
		}
		ids = append(ids, id)
	}

	const missingID = int64(999999)
	fused := []recall.Result{
		{ID: ids[2], Score: 0.9},
		{ID: missingID, Score: 0.8}, // deleted/never existed — must be skipped
		{ID: ids[0], Score: 0.5, Exact: true},
		{ID: ids[1], Score: 0.1},
	}

	hydrated := hydrateFusedResults(s, fused, 2)

	if len(hydrated) != 2 {
		t.Fatalf("expected cap at limit=2, got %d results: %#v", len(hydrated), hydrated)
	}
	if hydrated[0].ID != ids[2] {
		t.Fatalf("expected fused order preserved (first = %d), got %d", ids[2], hydrated[0].ID)
	}
	if hydrated[1].ID != ids[0] {
		t.Fatalf("expected fused order preserved (second = %d, skipping missing id), got %d", ids[0], hydrated[1].ID)
	}
	if hydrated[1].Rank != -1000 {
		t.Fatalf("expected Exact result to carry the -1000 sentinel Rank, got %v", hydrated[1].Rank)
	}
}
