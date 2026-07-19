package clouddash

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/velion/omnia/internal/cloud/cloudstore"
	"github.com/velion/omnia/internal/engramdb"
)

// fakeCloudStore returns a fixed set of replicated rows across two projects so the
// adapter's per-account scoping can be exercised without a real Postgres store.
type fakeCloudStore struct {
	projects []cloudstore.DashboardProjectRow
	obs      []cloudstore.DashboardObservationRow
}

func (f fakeCloudStore) ListProjects(string) ([]cloudstore.DashboardProjectRow, error) {
	return f.projects, nil
}

func (f fakeCloudStore) ListRecentObservations(string, string, int) ([]cloudstore.DashboardObservationRow, error) {
	return f.obs, nil
}

func newTestSource() *Source {
	return New(fakeCloudStore{
		projects: []cloudstore.DashboardProjectRow{
			{Project: "alpha", Observations: 1},
			{Project: "beta", Observations: 1},
		},
		obs: []cloudstore.DashboardObservationRow{
			{Project: "alpha", SessionID: "s-a", SyncID: "a1", Type: "decision", Title: "Alpha memory", Content: "alpha body"},
			{Project: "beta", SessionID: "s-b", SyncID: "b1", Type: "decision", Title: "Beta memory", Content: "beta body"},
		},
	})
}

// TestAccountScopeSeesOnlyItsProjects proves the core RBAC guarantee: an account
// scoped to "alpha" never observes "beta" rows through any read path, and a
// cross-account detail lookup resolves to not-found (the dashboard renders a 404).
func TestAccountScopeSeesOnlyItsProjects(t *testing.T) {
	src := newTestSource()
	ctx := WithScope(context.Background(), NewScope(false, []string{"alpha"}, "acct-a"))

	structural, ok := src.Structural()
	if !ok {
		t.Fatal("cloud source must provide structural reads")
	}

	projects, err := structural.Projects(ctx)
	if err != nil {
		t.Fatalf("Projects: %v", err)
	}
	if len(projects) != 1 || projects[0].Name != "alpha" {
		t.Fatalf("account scoped to alpha must see only alpha, got %+v", projects)
	}

	rows, err := structural.List(ctx, engramdb.Filter{Limit: 100})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].Project != "alpha" {
		t.Fatalf("List must return only alpha rows, got %+v", rows)
	}

	records := src.Records()

	// The account's own memory resolves.
	if _, err := records.GetObservation(ctx, syncIDToInt("a1")); err != nil {
		t.Fatalf("account must see its own observation: %v", err)
	}
	// A cross-account memory is invisible — not found, never another account's data.
	if _, err := records.GetObservation(ctx, syncIDToInt("b1")); err == nil {
		t.Fatal("cross-account observation lookup must fail (404), not leak beta data")
	}

	found, err := records.Search(ctx, "memory", "", 100)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, o := range found {
		if o.Project != "alpha" {
			t.Fatalf("search must not leak %q rows to an alpha-scoped account", o.Project)
		}
	}
}

// TestOperatorScopeSeesEverything proves the server operator (admin token) path
// retains full visibility across every replicated project.
func TestOperatorScopeSeesEverything(t *testing.T) {
	src := newTestSource()
	ctx := WithScope(context.Background(), NewScope(true, nil, ""))

	structural, _ := src.Structural()
	projects, err := structural.Projects(ctx)
	if err != nil {
		t.Fatalf("Projects: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("operator must see all projects, got %+v", projects)
	}

	if _, err := src.Records().GetObservation(ctx, syncIDToInt("b1")); err != nil {
		t.Fatalf("operator must see every observation: %v", err)
	}
}

// TestMissingScopeFailsClosed proves a request with no injected scope sees nothing,
// so a wiring mistake can only ever under-expose, never leak across accounts.
func TestMissingScopeFailsClosed(t *testing.T) {
	src := newTestSource()
	ctx := context.Background() // no scope injected

	structural, _ := src.Structural()
	projects, err := structural.Projects(ctx)
	if err != nil {
		t.Fatalf("Projects: %v", err)
	}
	if len(projects) != 0 {
		t.Fatalf("missing scope must expose nothing, got %+v", projects)
	}
	if _, err := src.Records().GetObservation(ctx, syncIDToInt("a1")); err == nil {
		t.Fatal("missing scope must not resolve any observation")
	}
}

// TestReadOnlyAndNoSemantic documents the cloud's degraded capabilities when
// cloud_semantic.enabled is left at its default (false): no mutations
// (read-only replica) and no embeddings (graph/semantic search off) — the
// exact PR5-slice-3 rollback guarantee (D7, cloud side).
func TestReadOnlyAndNoSemantic(t *testing.T) {
	src := newTestSource()
	if _, ok := src.Mutations(); ok {
		t.Fatal("cloud dashboard must be read-only")
	}
	if _, ok := src.Semantic(); ok {
		t.Fatal("cloud dashboard must not advertise a semantic index when cloud_semantic.enabled=false")
	}
}

// ─── PR5 slice 3: cloud semantic parity ──────────────────────────────────────

// embeddingFixture is one fake cloud_embeddings row, scoped by (accountID,
// project) — mirrors cloud_embeddings' real (account_id, project, sync_id)
// primary key (PR5 slice 1).
type embeddingFixture struct {
	accountID string
	project   string
	hits      []cloudstore.EmbeddingHit
}

// fakeEmbeddingsCloudStore extends fakeCloudStore with a fake
// CloudEmbeddingsSearcher, so Source.Semantic() can be exercised without a
// real Postgres-backed cloudstore.CloudStore. A plain fakeCloudStore (above)
// deliberately does NOT implement SearchEmbeddings, so it keeps proving
// Semantic() returns (nil, false) when the backing store lacks the
// capability, regardless of cloud_semantic.enabled.
type fakeEmbeddingsCloudStore struct {
	fakeCloudStore
	fixtures []embeddingFixture
	// searchErr, when set, is returned unconditionally by SearchEmbeddings —
	// simulates the embeddings backend (e.g. Postgres) erroring mid-request,
	// distinct from the "no fixture for this account/project" empty case.
	searchErr error
}

func (f fakeEmbeddingsCloudStore) SearchEmbeddings(_ context.Context, accountID, project string, _ []float32, k int) ([]cloudstore.EmbeddingHit, error) {
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	for _, fx := range f.fixtures {
		if fx.accountID == accountID && fx.project == project {
			hits := fx.hits
			if k > 0 && len(hits) > k {
				hits = hits[:k]
			}
			return hits, nil
		}
	}
	return nil, nil
}

// fakeEmbedder is a deterministic embed.Embedder double: it maps fixed query
// strings to fixed vectors so these tests never touch a real Ollama
// instance. An unrecognised query text is a test-setup bug, not a runtime
// degrade condition, so it errors loudly rather than silently returning a
// zero vector.
type fakeEmbedder struct {
	vectors map[string][]float32
	// err, when set, is returned unconditionally by Embed — used to simulate a
	// down/failing embedder mid-request (e.g. Ollama unreachable), distinct
	// from the "no fixture vector" test-setup-bug case below.
	err error
}

func (f fakeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	if v, ok := f.vectors[text]; ok {
		return v, nil
	}
	return nil, fmt.Errorf("fakeEmbedder: no fixture vector for %q", text)
}

// TestSemanticAvailable proves Semantic() returns a real cloudstore-backed
// index — not the permanent (nil, false) of every pre-PR5-slice-3 build —
// once cloud_semantic.enabled is on and the backing store supports
// embeddings search. [cloud REQ1]
func TestSemanticAvailable(t *testing.T) {
	store := fakeEmbeddingsCloudStore{fakeCloudStore: fakeCloudStore{
		projects: []cloudstore.DashboardProjectRow{{Project: "alpha", Observations: 1}},
		obs: []cloudstore.DashboardObservationRow{
			{Project: "alpha", SessionID: "s-a", SyncID: "a1", Type: "decision", Title: "Alpha memory", Content: "alpha body", CreatedAt: "2026-01-01 00:00:00"},
		},
	}}
	src := New(store, WithCloudSemantic(true, fakeEmbedder{}))

	sem, ok := src.Semantic()
	if !ok || sem == nil {
		t.Fatal("Semantic() must return a real index when cloud_semantic.enabled and the store supports embeddings search")
	}
}

// TestSemanticScopeIsolation proves an out-of-scope project is excluded from
// cloud semantic search results despite scoring HIGHER than any in-scope hit
// — the multi-tenant equivalent of the PR3 local cross-project recall leak
// fix, enforced here at the query level (cloudstore.SearchEmbeddings is only
// ever asked about scope-visible projects, so an out-of-scope vector is
// never even fetched). [cloud REQ1, REQ3]
func TestSemanticScopeIsolation(t *testing.T) {
	store := fakeEmbeddingsCloudStore{
		fakeCloudStore: fakeCloudStore{
			projects: []cloudstore.DashboardProjectRow{
				{Project: "alpha", Observations: 1},
				{Project: "beta", Observations: 1},
			},
			obs: []cloudstore.DashboardObservationRow{
				{Project: "alpha", SessionID: "s-a", SyncID: "a1", Type: "decision", Title: "Alpha memory", Content: "alpha body", CreatedAt: "2026-01-01 00:00:00"},
				{Project: "beta", SessionID: "s-b", SyncID: "b1", Type: "decision", Title: "Beta memory", Content: "beta body", CreatedAt: "2026-01-01 00:00:00"},
			},
		},
		fixtures: []embeddingFixture{
			{accountID: "acct-a", project: "alpha", hits: []cloudstore.EmbeddingHit{{SyncID: "a1", Score: 0.5}}},
			// beta scores HIGHER than alpha, but "beta" is out of the
			// requesting account's scope — it must never surface.
			{accountID: "acct-a", project: "beta", hits: []cloudstore.EmbeddingHit{{SyncID: "b1", Score: 0.99}}},
		},
	}
	src := New(store, WithCloudSemantic(true, fakeEmbedder{}))
	sem, ok := src.Semantic()
	if !ok {
		t.Fatal("setup: Semantic() must be available")
	}

	ctx := WithScope(context.Background(), NewScope(false, []string{"alpha"}, "acct-a"))
	hits, err := sem.Search(ctx, []float32{0.1, 0.2}, 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, h := range hits {
		if h.ObsID == syncIDToInt("b1") {
			t.Fatalf("out-of-scope project beta must never surface despite a higher score, got %+v", hits)
		}
	}
	if len(hits) != 1 || hits[0].ObsID != syncIDToInt("a1") {
		t.Fatalf("expected only the in-scope alpha hit, got %+v", hits)
	}
}

// TestSearchDegradesWithoutEmbedding proves an unembedded-but-synced memory
// (no cloud_embeddings row at all) still surfaces through the lexical
// (substring) leg when fused with cloud semantic hits via recall.Fuse — a
// missing embedding never hides an otherwise-matching memory. [cloud REQ4]
func TestSearchDegradesWithoutEmbedding(t *testing.T) {
	store := fakeEmbeddingsCloudStore{
		fakeCloudStore: fakeCloudStore{
			projects: []cloudstore.DashboardProjectRow{{Project: "alpha", Observations: 2}},
			obs: []cloudstore.DashboardObservationRow{
				{Project: "alpha", SessionID: "s-a", SyncID: "a1", Type: "decision", Title: "Deploy runbook", Content: "steps to deploy the service", CreatedAt: "2026-01-01 00:00:00"},
				{Project: "alpha", SessionID: "s-a", SyncID: "a2", Type: "decision", Title: "Deploy checklist", Content: "checklist before a deploy", CreatedAt: "2026-01-02 00:00:00"},
			},
		},
		// a2 was synced but never embedded — no fixture entry references it at
		// all — while a1 IS embedded. Both must still surface via substring.
		fixtures: []embeddingFixture{
			{accountID: "acct-a", project: "alpha", hits: []cloudstore.EmbeddingHit{{SyncID: "a1", Score: 0.9}}},
		},
	}
	src := New(store, WithCloudSemantic(true, fakeEmbedder{vectors: map[string][]float32{"deploy": {0.1, 0.2}}}))
	ctx := WithScope(context.Background(), NewScope(false, []string{"alpha"}, "acct-a"))

	results, err := src.Records().Search(ctx, "deploy", "", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	found := map[string]bool{}
	for _, o := range results {
		found[o.SyncID] = true
	}
	if !found["a1"] || !found["a2"] {
		t.Fatalf("both the embedded and unembedded-but-synced memory must surface via the lexical leg, got %+v", results)
	}
}

// TestCloudSearch_EmbedQueryError_DegradesToLexicalOnly proves that when the
// configured embedder ERRORS mid-request (e.g. an unreachable Ollama on the
// cloud host), cloudRecords.Search still returns the lexical/substring
// results instead of propagating the error or returning empty — mirroring
// internal/recall.Service's TestService_Search_EmbedQueryError_DegradesToLexicalOnly
// degrade contract on the cloud side. Existing coverage only proved the "row
// never embedded" gap (TestSearchDegradesWithoutEmbedding); this proves the
// embedder-failure gap. [cloud REQ4]
func TestCloudSearch_EmbedQueryError_DegradesToLexicalOnly(t *testing.T) {
	store := fakeEmbeddingsCloudStore{
		fakeCloudStore: fakeCloudStore{
			projects: []cloudstore.DashboardProjectRow{{Project: "alpha", Observations: 1}},
			obs: []cloudstore.DashboardObservationRow{
				{Project: "alpha", SessionID: "s-a", SyncID: "a1", Type: "decision", Title: "Deploy runbook", Content: "steps to deploy the service", CreatedAt: "2026-01-01 00:00:00"},
			},
		},
		// A fixture exists, but the embedder failing must short-circuit before
		// SearchEmbeddings is ever consulted — the lexical leg must carry the
		// whole result on its own.
		fixtures: []embeddingFixture{
			{accountID: "acct-a", project: "alpha", hits: []cloudstore.EmbeddingHit{{SyncID: "a1", Score: 0.9}}},
		},
	}
	src := New(store, WithCloudSemantic(true, fakeEmbedder{err: errors.New("ollama unreachable")}))
	ctx := WithScope(context.Background(), NewScope(false, []string{"alpha"}, "acct-a"))

	results, err := src.Records().Search(ctx, "deploy", "", 10)
	if err != nil {
		t.Fatalf("Search: expected no error (degrade to lexical-only), got %v", err)
	}
	if len(results) != 1 || results[0].SyncID != "a1" {
		t.Fatalf("expected the lexical match to surface despite the embedder failing, got %+v", results)
	}
}

// TestCloudSearch_SearchEmbeddingsError_DegradesToLexicalOnly proves that when
// SearchEmbeddings itself ERRORS mid-request (e.g. the Postgres embeddings
// backend is down or the query times out), cloudRecords.Search still returns
// the lexical/substring results — cloudSemanticIndex.Search treats a
// per-project SearchEmbeddings error as "no hits for that project", never a
// hard failure (source.go's Search: "one bad/unreachable project must not
// fail semantic search for every other visible project"). [cloud REQ4]
func TestCloudSearch_SearchEmbeddingsError_DegradesToLexicalOnly(t *testing.T) {
	store := fakeEmbeddingsCloudStore{
		fakeCloudStore: fakeCloudStore{
			projects: []cloudstore.DashboardProjectRow{{Project: "alpha", Observations: 1}},
			obs: []cloudstore.DashboardObservationRow{
				{Project: "alpha", SessionID: "s-a", SyncID: "a1", Type: "decision", Title: "Deploy runbook", Content: "steps to deploy the service", CreatedAt: "2026-01-01 00:00:00"},
			},
		},
		searchErr: errors.New("cloud_embeddings query failed"),
	}
	src := New(store, WithCloudSemantic(true, fakeEmbedder{vectors: map[string][]float32{"deploy": {0.1, 0.2}}}))
	ctx := WithScope(context.Background(), NewScope(false, []string{"alpha"}, "acct-a"))

	results, err := src.Records().Search(ctx, "deploy", "", 10)
	if err != nil {
		t.Fatalf("Search: expected no error (degrade to lexical-only), got %v", err)
	}
	if len(results) != 1 || results[0].SyncID != "a1" {
		t.Fatalf("expected the lexical match to surface despite SearchEmbeddings failing, got %+v", results)
	}
}

// TestBilingualCloudSemanticParity proves an ES query with ZERO substring
// overlap surfaces an EN-authored, embedded memory purely through the fused
// semantic leg — cloud bilingual parity (design D5/D6). [cloud REQ2]
func TestBilingualCloudSemanticParity(t *testing.T) {
	store := fakeEmbeddingsCloudStore{
		fakeCloudStore: fakeCloudStore{
			projects: []cloudstore.DashboardProjectRow{{Project: "alpha", Observations: 1}},
			obs: []cloudstore.DashboardObservationRow{
				{Project: "alpha", SessionID: "s-a", SyncID: "en1", Type: "decision", Title: "Deploy checklist", Content: "steps to deploy the service safely", CreatedAt: "2026-01-01 00:00:00"},
			},
		},
		fixtures: []embeddingFixture{
			{accountID: "acct-a", project: "alpha", hits: []cloudstore.EmbeddingHit{{SyncID: "en1", Score: 0.93}}},
		},
	}
	src := New(store, WithCloudSemantic(true, fakeEmbedder{vectors: map[string][]float32{"despliegue": {0.4, 0.5}}}))
	ctx := WithScope(context.Background(), NewScope(false, []string{"alpha"}, "acct-a"))

	results, err := src.Records().Search(ctx, "despliegue", "", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].SyncID != "en1" {
		t.Fatalf("expected the ES query to surface the EN-authored embedded memory via semantic fusion, got %+v", results)
	}
}

// TestCloudGraphReportsUnavailableNotEmpty proves cloudSemanticIndex.Graph()
// returns a non-nil error rather than silently fabricating an empty result
// when cloud semantic parity is enabled — design D5 explicitly scopes cloud
// parity to SEARCH only, never the k-NN similarity graph. This matters
// because internal/dashboard's handleGraph only takes its honest
// "Available: false" branch when Semantic() is unavailable OR Graph()
// errors; buildGraphView unconditionally sets Available: true on ANY
// non-error return, including zero nodes. Before this fix Graph() returned
// (nil, nil, nil), so the cloud graph page rendered a false "0 memories"
// state instead of the honest "graph unsupported over cloud semantic" banner.
// [WARNING fix, PR5 slice 3]
func TestCloudGraphReportsUnavailableNotEmpty(t *testing.T) {
	store := fakeEmbeddingsCloudStore{fakeCloudStore: fakeCloudStore{
		projects: []cloudstore.DashboardProjectRow{{Project: "alpha", Observations: 1}},
	}}
	src := New(store, WithCloudSemantic(true, fakeEmbedder{}))
	sem, ok := src.Semantic()
	if !ok {
		t.Fatal("setup: Semantic() must be available when cloud_semantic.enabled")
	}

	nodes, edges, err := sem.Graph(nil, 10, 0.5)
	if err == nil {
		t.Fatal("Graph() must return a non-nil error over cloud semantic — a silent (nil, nil, nil) makes internal/dashboard's handleGraph render a false \"0 memories\" state instead of the honest unavailable banner")
	}
	if nodes != nil || edges != nil {
		t.Fatalf("Graph() must not fabricate nodes/edges alongside its error, got nodes=%v edges=%v", nodes, edges)
	}
}

// TestCloudSemanticDisabledReproducesSubstringSearch is the Phase 6.3
// rollback confirmation: cloud_semantic.enabled=false (the default, no
// WithCloudSemantic option) must reproduce the cloud dashboard's original
// substring-only Search() byte-for-byte, and Semantic() must still report
// unavailable — zero migration risk, exactly as before PR5 slice 3.
func TestCloudSemanticDisabledReproducesSubstringSearch(t *testing.T) {
	rows := []cloudstore.DashboardObservationRow{
		{Project: "alpha", SessionID: "s-a", SyncID: "a1", Type: "decision", Title: "Deploy runbook", Content: "steps to deploy the service", CreatedAt: "2026-01-01 00:00:00"},
		{Project: "alpha", SessionID: "s-a", SyncID: "a2", Type: "decision", Title: "Unrelated note", Content: "grocery list", CreatedAt: "2026-01-02 00:00:00"},
	}
	store := fakeCloudStore{
		projects: []cloudstore.DashboardProjectRow{{Project: "alpha", Observations: 2}},
		obs:      rows,
	}
	src := New(store) // no WithCloudSemantic option — cloud_semantic.enabled defaults to false
	ctx := WithScope(context.Background(), NewScope(false, []string{"alpha"}, "acct-a"))

	results, err := src.Records().Search(ctx, "deploy", "", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].SyncID != "a1" {
		t.Fatalf("flag-off cloud search must reproduce substring-only behavior byte-for-byte, got %+v", results)
	}
	if _, ok := src.Semantic(); ok {
		t.Fatal("flag-off Semantic() must still report unavailable")
	}
}
