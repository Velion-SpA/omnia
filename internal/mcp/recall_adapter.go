package mcp

import (
	"context"
	"strings"

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

// RecallScopeFilter mirrors store.Search's project/scope/type WHERE-clause
// semantics (internal/store/store.go Search) so HydrateFusedResults can
// re-apply the exact same isolation guarantee to the semantic leg of hybrid
// recall.
//
// Bugfix context: the lexical leg is already scoped correctly, because
// StoreLexicalSearcher forwards these same fields straight into
// store.Search's SQL WHERE clause. The semantic leg has no such guarantee —
// embed.Store.Search has no project column in its WHERE clause at all, since
// the embeddings DB is a single shared, machine-wide store populated by
// embed.Reconcile from every project — so a fused Result coming from the
// semantic side may legitimately belong to a different project/scope/type
// than the caller asked for. RecallScopeFilter re-checks that at hydration
// time, once the full store.Observation (with its real Project/Scope/Type)
// is available.
//
// An empty field disables that constraint, exactly like store.SearchOptions:
// Project=="" means "any project", Scope=="" means "any scope", Type==""
// means "any type".
//
// Exported (issue #86) so cmd/omnia's CLI search and HTTP /search wiring can
// reuse the exact same hydration/scope-filter logic mem_search already uses,
// instead of re-implementing (and risking diverging from) it.
type RecallScopeFilter struct {
	Type    string
	Project string // expected already normalized (store.NormalizeProject), matching searchProject in handleSearch
	Scope   string // raw caller value; normalized here via store.NormalizeScope, mirroring store.Search
}

// matches reports whether obs satisfies f, using store.Search's own
// "empty field = no constraint" semantics.
func (f RecallScopeFilter) matches(obs *store.Observation) bool {
	if f.Type != "" && obs.Type != f.Type {
		return false
	}
	if f.Project != "" {
		if obs.Project == nil || !strings.EqualFold(*obs.Project, f.Project) {
			return false
		}
	}
	if f.Scope != "" && obs.Scope != store.NormalizeScope(f.Scope) {
		return false
	}
	return true
}

// semanticOverFetchFactor and minSemanticOverFetch control how many
// candidates RecallFetchLimit requests from recall.Service.Search compared
// to the caller's real mem_search limit.
//
// Bugfix context: because embed.Store.Search's k is drawn from the same
// shared, unscoped embeddings DB described above, the top-k nearest
// neighbors it returns may include candidates from other projects that
// RecallScopeFilter will then reject during hydration. If we only requested
// exactly `limit` candidates, a query where several of those top-k neighbors
// happen to belong to another project could under-fill the final response
// even though enough same-project matches exist further down the ranking.
// Over-fetching gives the filter enough raw candidates to still fill up to
// `limit` valid, correctly scoped results. This is effectively free: both
// embed.Store.Search and its fakes already rank the full stored set before
// slicing to k, so a bigger k changes only how much of that
// already-computed ranking is returned, not how much work is done.
const (
	semanticOverFetchFactor = 5
	minSemanticOverFetch    = 20
)

// RecallFetchLimit computes the candidate count to request from
// recall.Service.Search for a caller-facing limit, so the post-fusion
// project/scope/type filter in HydrateFusedResults has enough headroom to
// still return up to limit valid results (see semanticOverFetchFactor).
// HydrateFusedResults itself still caps the final, filtered output at the
// caller's real limit — this only widens the raw candidate pool fed into
// Fuse.
//
// Exported (issue #86) alongside HydrateFusedResults/RecallScopeFilter so
// cmd/omnia's CLI search and HTTP /search wiring request the same
// over-fetched candidate pool mem_search does, rather than under-filling on
// a smaller limit.
func RecallFetchLimit(limit int) int {
	fetch := limit * semanticOverFetchFactor
	if fetch < minSemanticOverFetch {
		fetch = minSemanticOverFetch
	}
	return fetch
}

// HydrateFusedResults resolves recall.Service's fused, ranked ID list back
// into full store.SearchResult records for the mem_search response — the
// "rank then hydrate" pattern: recall.Fuse never carries Project/Type/
// Content (design D6/PR2 boundary: recall must not import store), so the
// wiring layer re-fetches each result's full row by ID after fusion decides
// the order.
//
// filter re-applies the caller's project/scope/type constraints during
// hydration (bugfix: recall.Fuse's semantic-side input has no project
// awareness, so without this re-check a semantically similar memory from a
// different project could leak into the fused, hydrated response — see
// RecallScopeFilter's doc). A candidate that fails filter is skipped and
// does NOT count against limit, so filtering happens strictly BEFORE the cap
// below: a correctly scoped query still returns up to limit valid results
// instead of being under-filled by cross-project noise that happened to
// rank higher in the fused order.
//
// The result is capped at limit (mirroring store.Search's own limit
// behavior) because recall.FuseParams.MaxResults is a separate,
// independently configured ceiling (default 50) that may exceed the
// caller's requested mem_search limit (default 10). IDs that no longer
// resolve between fuse and hydrate (e.g. deleted concurrently) are skipped
// rather than failing the whole search.
//
// Exported (issue #86) so cmd/omnia's `omnia search` CLI and `omnia serve`'s
// HTTP GET /search can hydrate recall.Service's fused results identically to
// mem_search, instead of reimplementing this rank-then-hydrate glue and
// risking it drifting out of sync with the MCP path.
func HydrateFusedResults(s *store.Store, fused []recall.Result, limit int, filter RecallScopeFilter) []store.SearchResult {
	out := make([]store.SearchResult, 0, len(fused))
	for _, r := range fused {
		obs, err := s.GetObservation(r.ID)
		if err != nil {
			continue
		}
		if !filter.matches(obs) {
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
