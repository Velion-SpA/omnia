package embed

import (
	"context"
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
