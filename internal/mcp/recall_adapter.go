package mcp

import (
	"context"

	"github.com/velion/omnia/internal/recall"
	"github.com/velion/omnia/internal/store"
)

// StoreLexicalSearcher adapts *store.Store's FTS5 Search (the store's one
// true lexical authority) into the recall.LexicalSearcher port
// recall.Service composes against. It lives in this wiring layer — never
// inside internal/recall — so recall stays a dependency-free leaf that never
// imports internal/store (design D6, PR2 boundary).
type StoreLexicalSearcher struct {
	Store *store.Store
}

// NewStoreLexicalSearcher builds the store-backed recall.LexicalSearcher.
func NewStoreLexicalSearcher(s *store.Store) *StoreLexicalSearcher {
	return &StoreLexicalSearcher{Store: s}
}

// Search runs store.Store.Search (today's exact FTS5 query, unchanged) and
// converts its []store.SearchResult into []recall.LexicalHit, translating
// the topic_key exact-match sentinel (store.SearchResult.Rank == -1000) into
// LexicalHit.Exact so Fuse can pre-empt and dedup it correctly.
func (a *StoreLexicalSearcher) Search(ctx context.Context, query string, opts recall.LexicalSearchOptions) ([]recall.LexicalHit, error) {
	results, err := a.Store.Search(query, store.SearchOptions{
		Type:    opts.Type,
		Project: opts.Project,
		Scope:   opts.Scope,
		Limit:   opts.Limit,
	})
	if err != nil {
		return nil, err
	}
	hits := make([]recall.LexicalHit, 0, len(results))
	for _, r := range results {
		hits = append(hits, recall.LexicalHit{
			ID:        r.ID,
			UpdatedAt: r.UpdatedAt,
			Exact:     r.Rank == -1000,
		})
	}
	return hits, nil
}

// hydrateFusedResults resolves recall.Service's fused, ranked ID list back
// into full store.SearchResult records for the mem_search response — the
// "rank then hydrate" pattern: recall.Fuse never carries Project/Type/
// Content (design D6/PR2 boundary: recall must not import store), so the
// wiring layer re-fetches each result's full row by ID after fusion decides
// the order.
//
// The result is capped at limit (mirroring store.Search's own limit
// behavior) because recall.FuseParams.MaxResults is a separate,
// independently configured ceiling (default 50) that may exceed the
// caller's requested mem_search limit (default 10). IDs that no longer
// resolve between fuse and hydrate (e.g. deleted concurrently) are skipped
// rather than failing the whole search.
func hydrateFusedResults(s *store.Store, fused []recall.Result, limit int) []store.SearchResult {
	out := make([]store.SearchResult, 0, len(fused))
	for _, r := range fused {
		obs, err := s.GetObservation(r.ID)
		if err != nil {
			continue
		}
		sr := store.SearchResult{Observation: *obs}
		if r.Exact {
			sr.Rank = -1000
		}
		out = append(out, sr)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}
