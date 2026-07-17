package clouddash

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"time"

	"github.com/velion/omnia/internal/cloud/cloudstore"
	"github.com/velion/omnia/internal/dashboard"
	"github.com/velion/omnia/internal/embed"
	"github.com/velion/omnia/internal/engramdb"
	"github.com/velion/omnia/internal/recall"
)

// CloudStore is the slice of *cloudstore.CloudStore the dashboard adapter needs.
// Both methods return the FULL replicated set (no per-account filtering); the
// adapter applies the request scope on top, exactly as the previous cloud
// dashboard did via filterRowsByScope.
type CloudStore interface {
	ListProjects(query string) ([]cloudstore.DashboardProjectRow, error)
	ListRecentObservations(project, query string, limit int) ([]cloudstore.DashboardObservationRow, error)
}

// Source adapts the cloud replicated store to dashboard.DataSource. It is
// read-only (the cloud serves replicated chunk data; edits happen on the origin
// engram). Semantic search (design D5, PR5 slice 3) is available once
// cloud_semantic.enabled is on AND the backing store supports embeddings
// search (CloudEmbeddingsSearcher); otherwise it degrades to substring search
// and the graph page renders its unavailable state, exactly as before.
type Source struct {
	store CloudStore

	// semanticEnabled gates cloud semantic parity behind config
	// (cloud_semantic.enabled, default false). Disabled reproduces the
	// pre-PR5-slice-3 behavior byte-for-byte (D7 rollback guarantee).
	semanticEnabled bool
	// embedder embeds an interactive query into a vector. May be nil when no
	// Ollama endpoint is reachable from the cloud host: EmbedQuery then
	// degrades cleanly (see cloudSemanticIndex.EmbedQuery) while corpus-side
	// Search over vectors already synced in from devices that DO run Ollama
	// (design D5's compute-locally-and-sync flow) still works for any caller
	// that supplies its own query vector.
	embedder embed.Embedder
}

// Option configures optional Source capabilities. Functional options keep
// every existing zero-option caller (the cloud server's default wiring, and
// every pre-PR5-slice-3 test) unaffected.
type Option func(*Source)

// WithCloudSemantic enables cloud semantic parity (design D5, PR5 slice 3).
// See Source.semanticEnabled/embedder for the degrade contract.
func WithCloudSemantic(enabled bool, embedder embed.Embedder) Option {
	return func(s *Source) {
		s.semanticEnabled = enabled
		s.embedder = embedder
	}
}

// New builds a cloud DataSource over the given store.
func New(store CloudStore, opts ...Option) *Source {
	s := &Source{store: store}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

var _ dashboard.DataSource = (*Source)(nil)

// CloudEmbeddingsSearcher is the optional embeddings-search capability a
// CloudStore backing may provide (satisfied by *cloudstore.CloudStore's
// SearchEmbeddings, PR5 slice 1). Detected via type assertion, mirroring the
// optional-capability pattern the rest of the cloud stack already uses (e.g.
// cloudserver.EmbeddingMutationStore) — a store that doesn't implement it
// simply means cloud semantic search is unavailable, and Semantic() falls
// back to (nil, false) regardless of cloud_semantic.enabled.
type CloudEmbeddingsSearcher interface {
	SearchEmbeddings(ctx context.Context, accountID, project string, vec []float32, k int) ([]cloudstore.EmbeddingHit, error)
}

// Health always reports healthy: the store is in-process, so there is no remote
// engine to probe. This drives the overview "engine up" pill.
func (s *Source) Health(context.Context) error { return nil }

func (s *Source) Records() dashboard.RecordReader { return cloudRecords{s} }

func (s *Source) Structural() (dashboard.StructuralReader, bool) { return cloudStructural{s}, true }

// Semantic returns a real cloudstore-backed semantic index once cloud
// semantic parity is enabled AND the backing store supports embeddings
// search (design D5, PR5 slice 3) — replacing the pre-slice-3 permanent
// (nil, false). Disabled (the default) or an embeddings-incapable store both
// degrade to (nil, false): browse falls back to substring search and the
// graph page renders its unavailable state, unchanged.
func (s *Source) Semantic() (dashboard.SemanticIndex, bool) {
	if !s.semanticEnabled {
		return nil, false
	}
	es, ok := s.store.(CloudEmbeddingsSearcher)
	if !ok {
		return nil, false
	}
	return cloudSemanticIndex{s: s, embeddings: es}, true
}

// cloudSemanticIndex adapts cloudstore's account+project-scoped brute-force
// cosine search to dashboard.SemanticIndex (design D5).
type cloudSemanticIndex struct {
	s          *Source
	embeddings CloudEmbeddingsSearcher
}

// EmbedQuery embeds interactive text into a vector via the configured
// embedder. It errors cleanly (never panics) when no embedder is configured
// — callers (dashboard.semanticSearch, cloudRecords.semanticHits) already
// treat an EmbedQuery error as "degrade to lexical", exactly like a
// down/unreachable local Ollama does today.
func (c cloudSemanticIndex) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	if c.s.embedder == nil {
		return nil, fmt.Errorf("clouddash: cloud semantic query embedding is not configured (no reachable embedder)")
	}
	return c.s.embedder.Embed(ctx, text)
}

// Search returns the top-k cloud_embeddings hits by cosine similarity to
// vec, STRICTLY scoped to the request's account+visible-project boundary
// (scopeFrom(ctx)) — the multi-tenant equivalent of the PR3 local
// cross-project recall fix. The operator scope (All=true) and any request
// with no resolvable account both have no single account_id tenant boundary
// to search cloud_embeddings under (its primary key is account_id+project+
// sync_id) — Search degrades to no semantic hits for them rather than
// guessing a boundary; the substring/lexical leg still serves these
// requests unaffected.
func (c cloudSemanticIndex) Search(ctx context.Context, vec []float32, k int) ([]embed.Hit, error) {
	sc := scopeFrom(ctx)
	accountID := strings.TrimSpace(sc.AccountID)
	if accountID == "" {
		return nil, nil
	}
	projects := sc.Names()
	if len(projects) == 0 {
		return nil, nil
	}

	var hits []cloudstore.EmbeddingHit
	for _, project := range projects {
		ph, err := c.embeddings.SearchEmbeddings(ctx, accountID, project, vec, k)
		if err != nil {
			// Best-effort per project: one bad/unreachable project must not
			// fail semantic search for every other visible project.
			continue
		}
		hits = append(hits, ph...)
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if k > 0 && len(hits) > k {
		hits = hits[:k]
	}

	out := make([]embed.Hit, 0, len(hits))
	for _, h := range hits {
		out = append(out, embed.Hit{SyncID: h.SyncID, ObsID: syncIDToInt(h.SyncID), Score: h.Score})
	}
	return out, nil
}

// Graph is deliberately unsupported: design D5 covers SEARCH parity only, not
// the k-NN similarity graph. It MUST return a non-nil error (never a silent
// empty result): internal/dashboard's handleGraph takes its honest
// "Available: false" branch only when Semantic() is unavailable OR Graph()
// errors — buildGraphView unconditionally sets Available: true on any
// non-error return, even zero nodes. A silent (nil, nil, nil) would render
// the cloud graph page as a false "0 memories" state instead of the honest
// "graph unsupported over cloud semantic" banner.
func (c cloudSemanticIndex) Graph(k int, minScore float32) ([]embed.GraphNode, []embed.GraphEdge, error) {
	return nil, nil, errors.New("clouddash: knowledge graph is not supported over cloud semantic search (design D5 scopes cloud parity to search only)")
}

// Mutations is unavailable: the cloud dashboard is a read-only view over
// replicated data. Edit/delete surface a clear "read-only" message.
func (s *Source) Mutations() (dashboard.MutationWriter, bool) { return nil, false }

func (s *Source) Close() error { return nil }

// scopedObservations returns every replicated observation visible to the
// request's scope, most-recent first (the store already orders them). It is the
// single choke point that enforces per-account isolation for record reads.
func (s *Source) scopedObservations(ctx context.Context) ([]cloudstore.DashboardObservationRow, error) {
	// limit 0 ⇒ no truncation: return the full visible set so callers can filter
	// and cap themselves. The store memoises its read model, so repeated calls
	// across a single page render are cheap.
	rows, err := s.store.ListRecentObservations("", "", 0)
	if err != nil {
		return nil, err
	}
	sc := scopeFrom(ctx)
	if sc.All {
		return rows, nil
	}
	out := make([]cloudstore.DashboardObservationRow, 0, len(rows))
	for _, r := range rows {
		if sc.CanView(r.Project) {
			out = append(out, r)
		}
	}
	return out, nil
}

// projects returns visible per-project counts. When canon is non-nil the project
// names are canonicalised and counts merged, mirroring engramdb.ProjectsCanonical.
func (s *Source) projects(ctx context.Context, canon func(string) string) ([]engramdb.ProjectCount, error) {
	rows, err := s.store.ListProjects("")
	if err != nil {
		return nil, err
	}
	sc := scopeFrom(ctx)
	agg := make(map[string]int, len(rows))
	for _, r := range rows {
		if !sc.CanView(r.Project) {
			continue
		}
		name := r.Project
		if canon != nil {
			name = canon(r.Project)
		}
		agg[name] += r.Observations
	}
	out := make([]engramdb.ProjectCount, 0, len(agg))
	for name, count := range agg {
		out = append(out, engramdb.ProjectCount{Name: name, Count: count})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ─── RecordReader ────────────────────────────────────────────────────────────

type cloudRecords struct{ s *Source }

// GetObservation resolves the synthetic int id back to a replicated row. Because
// it only scans the scoped set, a cross-account id resolves to "not found" — the
// dashboard renders a 404, never another account's memory.
func (c cloudRecords) GetObservation(ctx context.Context, id int) (*dashboard.Observation, error) {
	rows, err := c.s.scopedObservations(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if syncIDToInt(r.SyncID) == id {
			obs := rowToDashboardObs(r)
			return &obs, nil
		}
	}
	return nil, fmt.Errorf("observation %d not found", id)
}

// Search is the cloud dashboard's RecordReader entry point. When cloud
// semantic parity is enabled (design D5/D6, PR5 slice 3) and the query is
// non-empty, it fuses the substring lexical leg with cloud semantic hits via
// recall.Fuse — the SAME shared ranking core mem_search's local hybrid
// recall uses (PR3) — so an unembedded-but-synced memory still surfaces
// through the lexical leg (never silently dropped, cloud REQ4) and a
// same-language-only substring miss can still be found via its semantic
// neighbor (cloud bilingual parity, cloud REQ2).
//
// Disabled (cloud_semantic.enabled=false, the default) or an empty query
// both take the substringSearch path UNCHANGED — byte-for-byte the cloud
// dashboard's original behavior, zero migration risk (D7 rollback
// guarantee, cloud side).
func (c cloudRecords) Search(ctx context.Context, query, project string, limit int) ([]dashboard.Observation, error) {
	if strings.TrimSpace(query) == "" {
		return c.substringSearch(ctx, query, project, limit)
	}
	sem, ok := c.s.Semantic()
	if !ok {
		return c.substringSearch(ctx, query, project, limit)
	}
	return c.hybridSearch(ctx, sem, query, project, limit)
}

// substringSearch is the cloud dashboard's ORIGINAL fallback search: a
// substring scan over the scoped, project-filtered rows (the cloud has no
// FTS index). It matches title, content, project, topic key and type.
// Preserved verbatim as its own function so the rollback guarantee ("flag
// off reproduces prior behavior exactly") is a literal code-identity claim,
// not just a behavioral one — this is also the branch Search falls back to
// whenever cloud semantic is unavailable or the query is empty.
func (c cloudRecords) substringSearch(ctx context.Context, query, project string, limit int) ([]dashboard.Observation, error) {
	rows, err := c.s.scopedObservations(ctx)
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	project = strings.TrimSpace(project)
	out := make([]dashboard.Observation, 0, len(rows))
	for _, r := range rows {
		if project != "" && !projectMatches(r.Project, project) {
			continue
		}
		if q != "" && !rowMatchesQuery(r, q) {
			continue
		}
		out = append(out, rowToDashboardObs(r))
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// hybridSearch fuses the substring lexical leg with cloud semantic hits via
// recall.Fuse, then hydrates the fused, ranked ID list back into full
// dashboard.Observation records ("rank then hydrate", mirroring
// internal/mcp's hydrateFusedResults from PR3).
func (c cloudRecords) hybridSearch(ctx context.Context, sem dashboard.SemanticIndex, query, project string, limit int) ([]dashboard.Observation, error) {
	rows, err := c.s.scopedObservations(ctx)
	if err != nil {
		return nil, err
	}
	project = strings.TrimSpace(project)
	q := strings.ToLower(strings.TrimSpace(query))

	// rowsByHashID doubles as the hydration re-check (mirrors internal/mcp's
	// recallScopeFilter/hydrateFusedResults bugfix, PR3): it is built ONLY
	// from rows that already passed BOTH the per-request account scope
	// (scopedObservations) and the caller's project filter, so a fused
	// semantic hit belonging to an out-of-scope project or account can never
	// resolve to a row here and is silently dropped, never leaked.
	rowsByHashID := make(map[int]cloudstore.DashboardObservationRow, len(rows))
	var lexical []recall.LexicalHit
	for _, r := range rows {
		if project != "" && !projectMatches(r.Project, project) {
			continue
		}
		id := syncIDToInt(r.SyncID)
		rowsByHashID[id] = r
		if rowMatchesQuery(r, q) {
			lexical = append(lexical, recall.LexicalHit{ID: int64(id), UpdatedAt: r.CreatedAt})
		}
	}

	semantic := c.semanticHits(ctx, sem, query, limit)
	fused := recall.Fuse(lexical, semantic, recall.DefaultFuseParams())

	out := make([]dashboard.Observation, 0, len(fused))
	for _, f := range fused {
		r, ok := rowsByHashID[int(f.ID)]
		if !ok {
			continue // out-of-scope/out-of-project semantic hit — dropped, never leaked
		}
		out = append(out, rowToDashboardObs(r))
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// cloudSemanticOverFetchFactor/cloudMinSemanticOverFetch mirror
// internal/mcp/recall_adapter.go's semanticOverFetchFactor/
// minSemanticOverFetch exactly (PR3 precedent): a scope spanning multiple
// visible projects means cloudSemanticIndex.Search's per-project top-k pool
// may include candidates hybridSearch's project/account re-check will
// later drop, so over-fetching gives that re-check enough headroom to still
// fill up to limit valid, correctly scoped results.
const (
	cloudSemanticOverFetchFactor = 5
	cloudMinSemanticOverFetch    = 20
)

func cloudRecallFetchLimit(limit int) int {
	fetch := limit * cloudSemanticOverFetchFactor
	if fetch < cloudMinSemanticOverFetch {
		fetch = cloudMinSemanticOverFetch
	}
	return fetch
}

// semanticHits best-effort embeds query and searches sem, degrading to an
// empty slice (never an error) on any failure — mirrors
// internal/recall.Service.semanticHits' degrade-safe contract exactly, so a
// down/unconfigured embedder never fails the whole search, only drops its
// semantic boost. The embed call is bounded so a slow/unreachable Ollama
// can't hang the request (mirrors internal/dashboard's semanticSearch).
func (c cloudRecords) semanticHits(ctx context.Context, sem dashboard.SemanticIndex, query string, limit int) []recall.SemanticHit {
	embCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	vec, err := sem.EmbedQuery(embCtx, query)
	if err != nil {
		return nil
	}
	hits, err := sem.Search(ctx, vec, cloudRecallFetchLimit(limit))
	if err != nil {
		return nil
	}
	out := make([]recall.SemanticHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, recall.SemanticHit{ID: int64(h.ObsID), Score: h.Score})
	}
	return out
}

// ─── StructuralReader ────────────────────────────────────────────────────────

type cloudStructural struct{ s *Source }

func (c cloudStructural) List(ctx context.Context, f engramdb.Filter) ([]engramdb.Observation, error) {
	rows, err := c.s.scopedObservations(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]engramdb.Observation, 0, len(rows))
	for _, r := range rows {
		if filterMatches(r, f) {
			out = append(out, rowToEngramdbObs(r))
		}
	}
	// Apply offset/limit, mirroring engramdb.List (Limit defaults to 200 when 0).
	off := f.Offset
	if off < 0 {
		off = 0
	}
	if off >= len(out) {
		return []engramdb.Observation{}, nil
	}
	out = out[off:]
	lim := f.Limit
	if lim <= 0 {
		lim = 200
	}
	if len(out) > lim {
		out = out[:lim]
	}
	return out, nil
}

func (c cloudStructural) ListByIDs(ctx context.Context, ids []int) ([]engramdb.Observation, error) {
	want := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	rows, err := c.s.scopedObservations(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]engramdb.Observation, 0, len(ids))
	for _, r := range rows {
		if _, ok := want[syncIDToInt(r.SyncID)]; ok {
			out = append(out, rowToEngramdbObs(r))
		}
	}
	return out, nil
}

func (c cloudStructural) Projects(ctx context.Context) ([]engramdb.ProjectCount, error) {
	return c.s.projects(ctx, nil)
}

func (c cloudStructural) ProjectsCanonical(ctx context.Context, canonicalize func(string) string) ([]engramdb.ProjectCount, error) {
	return c.s.projects(ctx, canonicalize)
}

func (c cloudStructural) Types(ctx context.Context) ([]engramdb.TypeCount, error) {
	rows, err := c.s.scopedObservations(ctx)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int)
	for _, r := range rows {
		t := strings.TrimSpace(r.Type)
		if t != "" {
			counts[t]++
		}
	}
	out := make([]engramdb.TypeCount, 0, len(counts))
	for name, count := range counts {
		out = append(out, engramdb.TypeCount{Name: name, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// ─── filtering + mapping helpers ─────────────────────────────────────────────

func filterMatches(r cloudstore.DashboardObservationRow, f engramdb.Filter) bool {
	if f.Type != "" && r.Type != f.Type {
		return false
	}
	switch {
	case len(f.RawProjects) > 0:
		matched := false
		for _, p := range f.RawProjects {
			if r.Project == p {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	case f.CanonicalProject != "":
		// engramdb matches LOWER(TRIM(project)) = LOWER(TRIM(?)).
		if !strings.EqualFold(strings.TrimSpace(r.Project), strings.TrimSpace(f.CanonicalProject)) {
			return false
		}
	case f.Project != "":
		if r.Project != f.Project {
			return false
		}
	}
	return true
}

func projectMatches(rowProject, want string) bool {
	return strings.EqualFold(strings.TrimSpace(rowProject), want)
}

func rowMatchesQuery(r cloudstore.DashboardObservationRow, q string) bool {
	return strings.Contains(strings.ToLower(r.Title), q) ||
		strings.Contains(strings.ToLower(r.Content), q) ||
		strings.Contains(strings.ToLower(r.Project), q) ||
		strings.Contains(strings.ToLower(r.TopicKey), q) ||
		strings.Contains(strings.ToLower(r.Type), q)
}

func rowToDashboardObs(r cloudstore.DashboardObservationRow) dashboard.Observation {
	return dashboard.Observation{
		ID:        syncIDToInt(r.SyncID),
		SyncID:    r.SyncID,
		SessionID: r.SessionID,
		Type:      r.Type,
		Title:     r.Title,
		Content:   r.Content,
		Project:   r.Project,
		TopicKey:  r.TopicKey,
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.CreatedAt, // cloud rows expose only a creation timestamp
	}
}

func rowToEngramdbObs(r cloudstore.DashboardObservationRow) engramdb.Observation {
	return engramdb.Observation{
		ID:        syncIDToInt(r.SyncID),
		SyncID:    r.SyncID,
		Type:      r.Type,
		Title:     r.Title,
		Content:   r.Content,
		Project:   r.Project,
		TopicKey:  r.TopicKey,
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.CreatedAt,
	}
}

// syncIDToInt derives a stable, positive 31-bit integer id from a row's sync id.
// The local dashboard keys /detail/{id} on engram's integer ids; cloud rows have
// only a composite (project/session/sync) identity. Hashing the sync id gives a
// deterministic id that round-trips through /detail/{id} and survives restarts
// without any persisted map. 31 bits keeps the value positive and arch-portable
// (fits int on 32-bit builds); collisions are astronomically unlikely at
// dashboard scale and at worst point two memories at the same detail link.
func syncIDToInt(syncID string) int {
	h := fnv.New64a()
	_, _ = h.Write([]byte(syncID))
	v := int(h.Sum64() & 0x7FFFFFFF)
	if v == 0 {
		v = 1
	}
	return v
}
