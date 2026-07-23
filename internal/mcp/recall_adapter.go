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

// ─── omnia-procedural-memory (design obs #1602 / spec obs #1606), PR2 Phase 7 ───
//
// Contrastive-pair retrieval: internal/recall stays pure (no pairing logic
// lives there, and recall.Fuse is untouched by this slice) — the PAIRING of
// the top-ranked TRUSTED playbook with the top-ranked TRUSTED anti_playbook
// happens here, at the same wiring-layer boundary HydrateFusedResults
// already occupies (design decision #5).

// ProcedureCard is the contrastive-pair envelope attached to mem_search/
// mem_context responses when procedural memory is enabled and at least one
// TRUSTED procedure matches. Only the polarity that actually has a trusted
// match is populated — the missing side is never fabricated (spec
// scenario: "only one polarity is trusted").
type ProcedureCard struct {
	Playbook     *ProcedureCardEntry `json:"playbook,omitempty"`
	AntiPlaybook *ProcedureCardEntry `json:"anti_playbook,omitempty"`
}

// ProcedureCardEntry is one polarity's half of a ProcedureCard.
type ProcedureCardEntry struct {
	SyncID            string   `json:"sync_id"`
	Trigger           string   `json:"trigger"`
	Steps             []string `json:"steps"`
	ExpectedOutcome   string   `json:"expected_outcome,omitempty"`
	PostconditionKind string   `json:"postcondition_kind"`
	Confidence        float64  `json:"confidence"`
}

// procedureCardEntryFrom converts a store.Procedure row into its
// ProcedureCardEntry projection (steps flattened to their template text —
// the card is a human/agent-facing summary, not the full stored program).
func procedureCardEntryFrom(p store.Procedure) *ProcedureCardEntry {
	steps := make([]string, 0, len(p.Steps))
	for _, st := range p.Steps {
		steps = append(steps, st.Template)
	}
	return &ProcedureCardEntry{
		SyncID:            p.SyncID,
		Trigger:           p.Trigger,
		Steps:             steps,
		ExpectedOutcome:   p.ExpectedOutcome,
		PostconditionKind: p.PostconditionKind,
		Confidence:        p.Confidence,
	}
}

// BuildProcedureCard picks the single top-ranked TRUSTED playbook and the
// single top-ranked TRUSTED anti_playbook matching query, and returns one
// combined card naming both — or just the one polarity that matched, never
// fabricating the other side. Returns nil when NEITHER polarity has a
// trusted match, so callers can skip attaching anything to the envelope
// (mirrors cfg.StructuralForgetting.Enabled's own "nothing to attach" no-op
// shape in handleSearch).
func BuildProcedureCard(s *store.Store, query string) *ProcedureCard {
	playbook := topTrustedProcedureByQuery(s, query, store.ProcedurePolarityPlaybook)
	antiPlaybook := topTrustedProcedureByQuery(s, query, store.ProcedurePolarityAntiPlaybook)
	if playbook == nil && antiPlaybook == nil {
		return nil
	}
	return &ProcedureCard{Playbook: playbook, AntiPlaybook: antiPlaybook}
}

func topTrustedProcedureByQuery(s *store.Store, query, polarity string) *ProcedureCardEntry {
	results, err := s.SearchProcedures(query, polarity, store.ProcedureStateTrusted, 1)
	if err != nil || len(results) == 0 {
		return nil
	}
	return procedureCardEntryFrom(results[0])
}

// BuildProcedureCardForProject is BuildProcedureCard's mem_context
// counterpart: mem_context has no free-text query to match procedures_fts
// against (it summarizes a whole project's context), so instead of a
// query-relevance match it surfaces the single most-recently-updated
// TRUSTED procedure of each polarity for the project — "what should I keep
// in mind for this project" rather than "what matches this search." Same
// "only attach what's actually trusted, never fabricate the missing
// polarity" contract as BuildProcedureCard.
func BuildProcedureCardForProject(s *store.Store, project, scope string) *ProcedureCard {
	playbook := topTrustedProcedureForProject(s, project, scope, store.ProcedurePolarityPlaybook)
	antiPlaybook := topTrustedProcedureForProject(s, project, scope, store.ProcedurePolarityAntiPlaybook)
	if playbook == nil && antiPlaybook == nil {
		return nil
	}
	return &ProcedureCard{Playbook: playbook, AntiPlaybook: antiPlaybook}
}

func topTrustedProcedureForProject(s *store.Store, project, scope, polarity string) *ProcedureCardEntry {
	results, err := s.ListProcedures(store.ListProceduresOptions{
		Project:  project,
		Scope:    scope,
		Polarity: polarity,
		State:    store.ProcedureStateTrusted,
		Limit:    1,
	})
	if err != nil || len(results) == 0 {
		return nil
	}
	return procedureCardEntryFrom(results[0])
}
