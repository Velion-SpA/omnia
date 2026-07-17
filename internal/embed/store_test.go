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

func mustUpsert(t *testing.T, store *Store, r Row) {
	t.Helper()
	if err := store.Upsert(context.Background(), r); err != nil {
		t.Fatalf("Upsert(%s): %v", r.SyncID, err)
	}
}
