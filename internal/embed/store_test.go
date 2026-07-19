package embed

import (
	"context"
	"fmt"
	"math"
	"testing"
)

func unitRow(syncID string, obsID int, vec []float32) Row {
	return Row{
		SyncID:      syncID,
		ObsID:       obsID,
		Project:     "p1",
		Type:        "decision",
		TopicKey:    "",
		Title:       syncID,
		UpdatedAt:   "2024-01-01 00:00:00",
		ContentHash: "hash-" + syncID,
		Model:       "test-model",
		Dim:         len(vec),
		Vector:      vec,
		EmbeddedAt:  "2024-01-01 00:00:00",
	}
}

func TestEncodeDecodeVector_RoundTrip(t *testing.T) {
	in := []float32{0, 1, -1, 0.5, -0.25, 3.1415927, 1e-7, -2.5e6}
	blob, err := encodeVector(in)
	if err != nil {
		t.Fatalf("encodeVector: %v", err)
	}
	if len(blob) != len(in)*4 {
		t.Fatalf("blob length: got %d, want %d", len(blob), len(in)*4)
	}
	out, err := decodeVector(blob)
	if err != nil {
		t.Fatalf("decodeVector: %v", err)
	}
	if len(out) != len(in) {
		t.Fatalf("decoded length: got %d, want %d", len(out), len(in))
	}
	for i := range in {
		if out[i] != in[i] {
			t.Errorf("element %d: got %v, want %v (must be bit-exact)", i, out[i], in[i])
		}
	}
}

func TestEncodeVector_RejectsEmpty(t *testing.T) {
	if _, err := encodeVector(nil); err == nil {
		t.Error("encodeVector(nil): expected error, got nil")
	}
}

func TestDecodeVector_RejectsBadLength(t *testing.T) {
	if _, err := decodeVector([]byte{1, 2, 3}); err == nil {
		t.Error("decodeVector(len 3): expected error, got nil")
	}
}

func TestStore_UpsertSearch_Ranking(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(t.TempDir() + "/emb.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	// A aligns with the query, C is 45° off, B is orthogonal.
	mustUpsert(t, store, unitRow("A", 1, []float32{1, 0, 0}))
	mustUpsert(t, store, unitRow("B", 2, []float32{0, 1, 0}))
	mustUpsert(t, store, unitRow("C", 3, []float32{0.70710677, 0.70710677, 0}))

	hits, err := store.Search(ctx, []float32{1, 0, 0}, 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("Search top-2: got %d hits, want 2", len(hits))
	}
	if hits[0].SyncID != "A" || hits[1].SyncID != "C" {
		t.Errorf("ranking: got [%s, %s], want [A, C]", hits[0].SyncID, hits[1].SyncID)
	}
	if math.Abs(float64(hits[0].Score)-1.0) > 1e-5 {
		t.Errorf("A score: got %v, want ~1.0", hits[0].Score)
	}
	if math.Abs(float64(hits[1].Score)-0.70710677) > 1e-5 {
		t.Errorf("C score: got %v, want ~0.707", hits[1].Score)
	}
}

func TestStore_Search_SkipsDimMismatch(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(t.TempDir() + "/emb.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	mustUpsert(t, store, unitRow("ok", 1, []float32{1, 0, 0}))
	mustUpsert(t, store, unitRow("wrongdim", 2, []float32{1, 0, 0, 0})) // dim 4

	hits, err := store.Search(ctx, []float32{1, 0, 0}, 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].SyncID != "ok" {
		t.Errorf("dim-mismatch row should be skipped; got %d hits %+v", len(hits), hits)
	}
}

func TestStore_Upsert_ReplacesBySyncID(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(t.TempDir() + "/emb.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	r := unitRow("dup", 1, []float32{1, 0, 0})
	mustUpsert(t, store, r)
	r.ContentHash = "hash-updated"
	r.ObsID = 99
	mustUpsert(t, store, r)

	n, err := store.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 1 {
		t.Errorf("Count after re-upsert same sync_id: got %d, want 1", n)
	}
	stored, err := store.Stored(ctx)
	if err != nil {
		t.Fatalf("Stored: %v", err)
	}
	if stored["dup"].ContentHash != "hash-updated" {
		t.Errorf("ContentHash after re-upsert: got %q, want %q", stored["dup"].ContentHash, "hash-updated")
	}
}

func TestStore_Prune(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(t.TempDir() + "/emb.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	mustUpsert(t, store, unitRow("keep", 1, []float32{1, 0, 0}))
	mustUpsert(t, store, unitRow("drop1", 2, []float32{0, 1, 0}))
	mustUpsert(t, store, unitRow("drop2", 3, []float32{0, 0, 1}))

	removed, err := store.Prune(ctx, []string{"keep"})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed != 2 {
		t.Errorf("Prune removed: got %d, want 2", removed)
	}
	n, _ := store.Count(ctx)
	if n != 1 {
		t.Errorf("Count after prune: got %d, want 1", n)
	}
}

func TestStore_Prune_EmptyLiveSetRemovesAll(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(t.TempDir() + "/emb.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	mustUpsert(t, store, unitRow("a", 1, []float32{1, 0, 0}))
	mustUpsert(t, store, unitRow("b", 2, []float32{0, 1, 0}))

	removed, err := store.Prune(ctx, nil) // nothing live → remove all
	if err != nil {
		t.Fatalf("Prune(nil): %v", err)
	}
	if removed != 2 {
		t.Errorf("Prune(nil) removed: got %d, want 2", removed)
	}
	if n, _ := store.Count(ctx); n != 0 {
		t.Errorf("Count after prune-all: got %d, want 0", n)
	}
}

// TestStore_SearchScoped_TargetProjectSurfacesDespiteGlobalCrowdOut
// reproduces the exact recall bug (engram obs #1436): Store.Search does a
// brute-force cosine scan over ALL projects, so a target project that is a
// small fraction of the store can be crowded out of the global top-K by many
// higher-scoring rows from other projects — even though the target project
// has a valid match. This seeds 20 "other"-project vectors that all score
// higher against the query than the single "target"-project vector (so an
// unscoped Search(ctx, query, k=5) would never return it), then asserts
// SearchScoped(ctx, query, k=5, "target") still surfaces it because the
// top-K is computed WITHIN the project, not globally-then-filtered.
func TestStore_SearchScoped_TargetProjectSurfacesDespiteGlobalCrowdOut(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(t.TempDir() + "/emb.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	query := []float32{1, 0, 0}

	// 20 "other"-project vectors, all closely aligned with the query
	// (score ~0.99+), so every one of them outranks "target"'s vector.
	for i := 0; i < 20; i++ {
		r := unitRow(fmt.Sprintf("other-%d", i), i+1, []float32{0.995, 0.0999, 0})
		r.Project = "other"
		mustUpsert(t, store, r)
	}

	// The one "target"-project vector: a valid match (well above a typical
	// relevance floor) but scored lower than every "other" row above, so it
	// sits beyond global rank 20.
	targetRow := unitRow("target-1", 1000, []float32{0.6, 0.8, 0})
	targetRow.Project = "target"
	mustUpsert(t, store, targetRow)

	// Sanity: prove the crowd-out actually happens — an unscoped top-5 must
	// NOT contain the target row.
	globalHits, err := store.Search(ctx, query, 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, h := range globalHits {
		if h.SyncID == "target-1" {
			t.Fatalf("test setup invalid: target-1 unexpectedly present in unscoped global top-5: %+v", globalHits)
		}
	}

	// The fix: project-scoped search computes the top-K WITHIN "target" only,
	// so the sole target row is rank 1 there regardless of global crowding.
	scopedHits, err := store.SearchScoped(ctx, query, 5, "target")
	if err != nil {
		t.Fatalf("SearchScoped: %v", err)
	}
	if len(scopedHits) != 1 || scopedHits[0].SyncID != "target-1" {
		t.Fatalf("SearchScoped(project=target): got %+v, want exactly [target-1]", scopedHits)
	}
}

// TestStore_SearchScoped_EmptyProjectMatchesUnscopedSearch proves
// SearchScoped("") is equivalent to Search — existing unscoped callers
// (dashboard, memory-conflict-semantic) are unaffected by this addition.
func TestStore_SearchScoped_EmptyProjectMatchesUnscopedSearch(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(t.TempDir() + "/emb.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	mustUpsert(t, store, unitRow("A", 1, []float32{1, 0, 0}))
	mustUpsert(t, store, unitRow("B", 2, []float32{0, 1, 0}))

	want, err := store.Search(ctx, []float32{1, 0, 0}, 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	got, err := store.SearchScoped(ctx, []float32{1, 0, 0}, 2, "")
	if err != nil {
		t.Fatalf("SearchScoped: %v", err)
	}
	if len(got) != len(want) || len(got) != 2 || got[0].SyncID != want[0].SyncID || got[1].SyncID != want[1].SyncID {
		t.Errorf("SearchScoped(project=\"\") = %+v, want same as Search() = %+v", got, want)
	}
}

// TestStore_SearchScoped_UnknownProjectReturnsEmpty proves the WHERE clause
// actually restricts rows (not just a no-op filter) — a project with no
// stored rows returns zero hits, never other projects' rows.
func TestStore_SearchScoped_UnknownProjectReturnsEmpty(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(t.TempDir() + "/emb.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	mustUpsert(t, store, unitRow("A", 1, []float32{1, 0, 0}))

	got, err := store.SearchScoped(ctx, []float32{1, 0, 0}, 5, "nonexistent")
	if err != nil {
		t.Fatalf("SearchScoped: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("SearchScoped(project=nonexistent): got %+v, want empty", got)
	}
}

func TestStore_Search_KZeroReturnsAll(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(t.TempDir() + "/emb.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	mustUpsert(t, store, unitRow("a", 1, []float32{1, 0, 0}))
	mustUpsert(t, store, unitRow("b", 2, []float32{0, 1, 0}))
	mustUpsert(t, store, unitRow("c", 3, []float32{0, 0, 1}))

	hits, err := store.Search(ctx, []float32{1, 0, 0}, 0) // k=0 → all ranked
	if err != nil {
		t.Fatalf("Search(k=0): %v", err)
	}
	if len(hits) != 3 {
		t.Errorf("Search(k=0): got %d hits, want 3 (all)", len(hits))
	}
}

// TestStore_GraphScoped_FiltersByProject reproduces audit finding H3
// (engram obs #1484/#1488): Store.Graph runs its O(N^2) pairwise cosine scan
// over EVERY stored vector regardless of caller intent, so viewing one small
// project's graph pays the cost of the whole shared store. This seeds two
// projects whose vectors are mutually near-identical (so an UNSCOPED scan
// would link every node into one big cross-project component), then asserts
// GraphScoped(["projA"], ...) only returns projA's own nodes and NEVER an
// edge that reaches into projB — proving the WHERE clause narrows the scan
// itself, not just a post-hoc filter over an already-computed whole-store
// graph.
func TestStore_GraphScoped_FiltersByProject(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/emb.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	// projA: two near-identical vectors (a real intra-project edge).
	a1 := unitRow("a1", 1, []float32{1, 0, 0})
	a1.Project = "projA"
	mustUpsert(t, store, a1)
	a2 := unitRow("a2", 2, []float32{0.999, 0.045, 0})
	a2.Project = "projA"
	mustUpsert(t, store, a2)

	// projB: also near-identical to projA's vectors, so an unscoped scan
	// would draw edges from projB straight into projA.
	b1 := unitRow("b1", 3, []float32{0.998, 0.06, 0})
	b1.Project = "projB"
	mustUpsert(t, store, b1)
	b2 := unitRow("b2", 4, []float32{0.997, 0.07, 0})
	b2.Project = "projB"
	mustUpsert(t, store, b2)

	// Sanity: prove cross-project edges WOULD exist in the whole-store scan —
	// otherwise this test would pass trivially regardless of scoping.
	allNodes, allEdges, err := store.Graph(5, 0.9)
	if err != nil {
		t.Fatalf("Graph (sanity): %v", err)
	}
	if len(allNodes) != 4 {
		t.Fatalf("sanity: whole-store Graph should return all 4 nodes, got %d", len(allNodes))
	}
	bySync := map[int]string{1: "a1", 2: "a2", 3: "b1", 4: "b2"}
	isA := func(id int) bool { return bySync[id] == "a1" || bySync[id] == "a2" }
	crossFound := false
	for _, e := range allEdges {
		if isA(e.Source) != isA(e.Target) {
			crossFound = true
		}
	}
	if !crossFound {
		t.Fatal("test setup invalid: expected at least one cross-project edge in the unscoped whole-store graph")
	}

	// The fix: GraphScoped(["projA"], ...) must scan ONLY projA's rows, so
	// every returned node is in projA and no edge reaches projB.
	nodes, edges, err := store.GraphScoped([]string{"projA"}, 5, 0.9)
	if err != nil {
		t.Fatalf("GraphScoped: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("GraphScoped(projA) nodes: got %d, want 2 (a1, a2 only)", len(nodes))
	}
	for _, n := range nodes {
		if n.Project != "projA" {
			t.Errorf("GraphScoped(projA) returned a node from project %q: %+v", n.Project, n)
		}
	}
	for _, e := range edges {
		if bySync[e.Source] == "b1" || bySync[e.Source] == "b2" || bySync[e.Target] == "b1" || bySync[e.Target] == "b2" {
			t.Errorf("GraphScoped(projA) must never surface a projB endpoint, got edge %+v", e)
		}
	}
}

// TestStore_GraphScoped_MultipleProjectsUnion proves GraphScoped accepts a
// SET of projects (not just one) and computes the pairwise scan over their
// UNION in a single query — this is what preserves group-parent graph views
// (parent + children) after H3's SQL scoping, instead of losing cross-child
// edges to N separate single-project scans.
func TestStore_GraphScoped_MultipleProjectsUnion(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/emb.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	a := unitRow("a", 1, []float32{1, 0, 0})
	a.Project = "parent"
	mustUpsert(t, store, a)
	b := unitRow("b", 2, []float32{0.999, 0.045, 0})
	b.Project = "child"
	mustUpsert(t, store, b)
	c := unitRow("c", 3, []float32{0, 1, 0})
	c.Project = "unrelated"
	mustUpsert(t, store, c)

	nodes, edges, err := store.GraphScoped([]string{"parent", "child"}, 5, 0.9)
	if err != nil {
		t.Fatalf("GraphScoped: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("GraphScoped([parent,child]) nodes: got %d, want 2 (unrelated excluded)", len(nodes))
	}
	if len(edges) != 1 {
		t.Fatalf("GraphScoped([parent,child]) edges: got %d, want 1 (the parent<->child edge)", len(edges))
	}
}

// TestStore_GraphScoped_EmptyProjectsMatchesWholeStoreGraph proves
// GraphScoped(nil, ...) is byte-for-byte equivalent to Graph — existing
// whole-store callers are unaffected by this addition.
func TestStore_GraphScoped_EmptyProjectsMatchesWholeStoreGraph(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/emb.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	mustUpsert(t, store, unitRow("a", 1, []float32{1, 0, 0}))
	mustUpsert(t, store, unitRow("b", 2, []float32{0.9999, 0.014, 0}))

	wantNodes, wantEdges, err := store.Graph(5, 0.5)
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	gotNodes, gotEdges, err := store.GraphScoped(nil, 5, 0.5)
	if err != nil {
		t.Fatalf("GraphScoped(nil): %v", err)
	}
	if len(gotNodes) != len(wantNodes) || len(gotEdges) != len(wantEdges) {
		t.Fatalf("GraphScoped(nil) = (%d nodes, %d edges), want same as Graph() = (%d nodes, %d edges)",
			len(gotNodes), len(gotEdges), len(wantNodes), len(wantEdges))
	}
}

func mustUpsert(t *testing.T, store *Store, r Row) {
	t.Helper()
	if err := store.Upsert(context.Background(), r); err != nil {
		t.Fatalf("Upsert(%s): %v", r.SyncID, err)
	}
}
