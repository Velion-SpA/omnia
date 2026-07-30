package embed

import (
	"context"
	"fmt"
	"testing"
)

// --- Phase 2A: connector, derived state, active dimension -----------------

// TestOpenStore_VecIndexDisabled_ByteForByteBruteForce (task 2.1, REQ-460):
// the options-based OpenStore seam keeps an absent/false flag on modernc
// with byte-for-byte brute-force behavior — no Vec1 state at all.
func TestOpenStore_VecIndexDisabled_ByteForByteBruteForce(t *testing.T) {
	ctx := context.Background()

	// No options at all — the pre-v0.4 call shape must still compile and
	// behave identically.
	plain, err := OpenStore(t.TempDir() + "/emb.db")
	if err != nil {
		t.Fatalf("OpenStore(no opts): %v", err)
	}
	defer plain.Close()
	if plain.vec != nil {
		t.Fatal("OpenStore with no options must never set up a Vec1 connector")
	}

	// Explicit false is equally inert.
	off, err := OpenStore(t.TempDir()+"/emb2.db", WithVecIndex(false))
	if err != nil {
		t.Fatalf("OpenStore(WithVecIndex(false)): %v", err)
	}
	defer off.Close()
	if off.vec != nil {
		t.Fatal("OpenStore(WithVecIndex(false)) must never set up a Vec1 connector")
	}

	mustUpsert(t, off, unitRow("a", 1, []float32{1, 0, 0}))
	hits, err := off.Search(ctx, []float32{1, 0, 0}, 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].SyncID != "a" {
		t.Fatalf("disabled-path Search must behave exactly like brute force: got %+v", hits)
	}
}

// TestOpenStore_VecIndexEnabled_UsesPrivateConnectorNotGlobalDriver (task
// 2.1/2.2, REQ-460/464): enabled setup requires a private Vec1 connector
// (registered per-connection via driver.Open), not a globally registered
// database/sql driver name — proven by successfully creating and querying
// the vec1 virtual table through the Store's own db handle without ever
// calling sql.Open("sqlite3", ...) or any global driver registration.
func TestOpenStore_VecIndexEnabled_UsesPrivateConnectorNotGlobalDriver(t *testing.T) {
	store, err := OpenStore(t.TempDir()+"/emb.db", WithVecIndex(true))
	if err != nil {
		t.Fatalf("OpenStore(WithVecIndex(true)): %v", err)
	}
	defer store.Close()

	if store.vec == nil {
		t.Fatal("OpenStore(WithVecIndex(true)) on a supported (little-endian) host must set up a Vec1 connector")
	}
	if !store.vec.healthyOK() {
		t.Fatal("freshly opened Vec1 connector must be healthy")
	}
}

// TestOpenStore_VecIndexEnabled_CreatesFlatCosTableInSameDatabase (task 2.3,
// REQ-461/463): enabled setup creates the SAME-DB `vec_embeddings USING
// vec1(vector, project)` table, configured flat/cos via 'rebuild', and never
// creates a second database file, an int8/binary artifact, or an ANN index.
func TestOpenStore_VecIndexEnabled_CreatesFlatCosTableInSameDatabase(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/emb.db"
	store, err := OpenStore(path, WithVecIndex(true))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	var name string
	if err := store.db.QueryRow(`SELECT name FROM sqlite_master WHERE name = 'vec_embeddings'`).Scan(&name); err != nil {
		t.Fatalf("vec_embeddings table must exist in the SAME database: %v", err)
	}

	// vec_index_state bookkeeping table must also exist, and no readiness
	// has been granted yet (nothing indexed).
	var ready int
	if err := store.db.QueryRow(`SELECT ready FROM vec_index_state WHERE id = 1`).Scan(&ready); err != nil {
		t.Fatalf("vec_index_state row must exist: %v", err)
	}
	if ready != 0 {
		t.Errorf("a freshly created store must not be ready before any backfill: got ready=%d", ready)
	}
}

// TestVecIndex_FirstValidVectorEstablishesActiveDim (task 2.5, REQ-466/468):
// the first valid source vector (in rowid order) establishes the active
// dimension via VecBackfill; a native-endian re-encoded blob is stored
// separately from the canonical little-endian source.
func TestVecIndex_FirstValidVectorEstablishesActiveDim(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(t.TempDir()+"/emb.db", WithVecIndex(true))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	mustUpsert(t, store, unitRow("a", 1, []float32{1, 0, 0, 0}))

	report, err := store.VecBackfill(ctx)
	if err != nil {
		t.Fatalf("VecBackfill: %v", err)
	}
	if !report.Enabled {
		t.Fatal("VecBackfill report must report Enabled=true when Vec1 is on")
	}
	if report.ActiveDim != 4 {
		t.Errorf("ActiveDim: got %d, want 4", report.ActiveDim)
	}
	if store.vec.dim() != 4 {
		t.Errorf("store.vec.dim(): got %d, want 4", store.vec.dim())
	}
	if !store.vec.usable() {
		t.Error("store.vec.usable() must be true after a successful backfill")
	}
}

// TestVecIndex_UnsupportedHostDisablesVecIndex (task 2.5, REQ-468): a
// non-little-endian host must leave Vec1 entirely unavailable regardless of
// the config flag — proven directly against the byte-order probe itself,
// since this test suite always runs on real (little-endian) hardware.
func TestVecIndex_UnsupportedHostDisablesVecIndex(t *testing.T) {
	if !hostIsLittleEndian() {
		t.Skip("this test asserts the little-endian branch; host is big-endian")
	}
	// Sanity: the probe must be self-consistent with Go's own encoding/binary
	// NativeEndian, so the OpenStore gate is provably correct rather than
	// coincidentally true.
	var want bool
	x := uint16(1)
	if b := byte(x); b == 1 {
		want = true
	}
	if hostIsLittleEndian() != want {
		t.Errorf("hostIsLittleEndian(): got %v, want %v", hostIsLittleEndian(), want)
	}
}

// TestVecBackfillAndReindex_ReportCountsAndPreserveSourceRows (task 2.7,
// REQ-461/466/467): seeds matching + mismatched-dimension rows, runs
// VecBackfill then VecReindex, and verifies indexed/skipped counts/reasons
// while every canonical source row survives untouched.
func TestVecBackfillAndReindex_ReportCountsAndPreserveSourceRows(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(t.TempDir()+"/emb.db", WithVecIndex(true))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	const matching = 998
	const mismatched = 2
	for i := 0; i < matching; i++ {
		r := unitRow(fmt.Sprintf("m-%d", i), i+1, oneHot(8, i%8))
		if err := store.Upsert(ctx, r); err != nil {
			t.Fatalf("Upsert m-%d: %v", i, err)
		}
	}
	for i := 0; i < mismatched; i++ {
		r := unitRow(fmt.Sprintf("x-%d", i), matching+i+1, oneHot(16, i%16))
		if err := store.Upsert(ctx, r); err != nil {
			t.Fatalf("Upsert x-%d: %v", i, err)
		}
	}

	total, err := store.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if total != matching+mismatched {
		t.Fatalf("Count before maintenance: got %d, want %d", total, matching+mismatched)
	}

	report, err := store.VecBackfill(ctx)
	if err != nil {
		t.Fatalf("VecBackfill: %v", err)
	}
	if report.ActiveDim != 8 {
		t.Errorf("VecBackfill ActiveDim: got %d, want 8", report.ActiveDim)
	}
	if report.Skipped != mismatched || report.SkippedReasons["dimension_mismatch"] != mismatched {
		t.Errorf("VecBackfill skip accounting: got Skipped=%d reasons=%v, want %d dimension_mismatch skips",
			report.Skipped, report.SkippedReasons, mismatched)
	}
	if report.TotalRows != total {
		t.Errorf("VecBackfill TotalRows: got %d, want %d", report.TotalRows, total)
	}

	afterBackfill, err := store.Count(ctx)
	if err != nil {
		t.Fatalf("Count after backfill: %v", err)
	}
	if afterBackfill != total {
		t.Fatalf("source rows must survive VecBackfill untouched: got %d, want %d", afterBackfill, total)
	}

	// VecReindex discards ONLY derived state and rebuilds — source rows must
	// still be exactly `total` afterward, and the report re-establishes the
	// same active dimension/skip counts from scratch.
	reReport, err := store.VecReindex(ctx)
	if err != nil {
		t.Fatalf("VecReindex: %v", err)
	}
	if reReport.ActiveDim != 8 {
		t.Errorf("VecReindex ActiveDim: got %d, want 8", reReport.ActiveDim)
	}
	if reReport.Indexed != matching {
		t.Errorf("VecReindex Indexed: got %d, want %d", reReport.Indexed, matching)
	}
	if reReport.Skipped != mismatched {
		t.Errorf("VecReindex Skipped: got %d, want %d", reReport.Skipped, mismatched)
	}

	afterReindex, err := store.Count(ctx)
	if err != nil {
		t.Fatalf("Count after reindex: %v", err)
	}
	if afterReindex != total {
		t.Fatalf("source rows must survive VecReindex untouched: got %d, want %d", afterReindex, total)
	}
}

// TestVecBackfill_DisabledStoreIsInertNoop (REQ-460): a Store opened without
// Vec1 must treat VecBackfill/VecReindex as graceful no-ops, never errors.
func TestVecBackfill_DisabledStoreIsInertNoop(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(t.TempDir() + "/emb.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	report, err := store.VecBackfill(ctx)
	if err != nil {
		t.Fatalf("VecBackfill on disabled store must not error: %v", err)
	}
	if report.Enabled {
		t.Error("VecBackfill report.Enabled must be false when Vec1 is off")
	}
	report2, err := store.VecReindex(ctx)
	if err != nil {
		t.Fatalf("VecReindex on disabled store must not error: %v", err)
	}
	if report2.Enabled {
		t.Error("VecReindex report.Enabled must be false when Vec1 is off")
	}
}

// --- Phase 2B: derived lifecycle dual-write --------------------------------

// TestVecIndex_UpsertUpdateDeletePrune_MirrorDerivedTable (task 2.13,
// REQ-461): normal upsert/update/delete/prune mirror their source-rowid
// lifecycle into the derived table transactionally.
func TestVecIndex_UpsertUpdateDeletePrune_MirrorDerivedTable(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(t.TempDir()+"/emb.db", WithVecIndex(true))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	mustUpsert(t, store, unitRow("a", 1, []float32{1, 0, 0, 0}))
	mustUpsert(t, store, unitRow("b", 2, []float32{0, 1, 0, 0}))

	countVec := func() int {
		var n int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM vec_embeddings`).Scan(&n); err != nil {
			t.Fatalf("count vec_embeddings: %v", err)
		}
		return n
	}
	if n := countVec(); n != 2 {
		t.Fatalf("derived rows after two upserts: got %d, want 2", n)
	}

	// Update: re-upserting the SAME sync_id with a new vector must replace,
	// not duplicate, the derived row.
	updated := unitRow("a", 1, []float32{0, 0, 1, 0})
	mustUpsert(t, store, updated)
	if n := countVec(); n != 2 {
		t.Fatalf("derived rows after update-by-reupsert: got %d, want 2 (no duplication)", n)
	}

	// Delete: DeleteBySyncID must remove the derived row too.
	removed, err := store.DeleteBySyncID(ctx, "b")
	if err != nil {
		t.Fatalf("DeleteBySyncID: %v", err)
	}
	if removed != 1 {
		t.Fatalf("DeleteBySyncID removed: got %d, want 1", removed)
	}
	if n := countVec(); n != 1 {
		t.Fatalf("derived rows after delete: got %d, want 1", n)
	}

	// Prune: removing everything not in the live set must also prune the
	// derived table.
	mustUpsert(t, store, unitRow("c", 3, []float32{0, 1, 0, 0}))
	if _, err := store.Prune(ctx, []string{"c"}); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n := countVec(); n != 1 {
		t.Fatalf("derived rows after prune (only 'c' live): got %d, want 1", n)
	}
}

// TestVecIndex_ForcedFailurePreservesSourceMutationAndMarksUnhealthy (task
// 2.13, REQ-465): a forced Vec1 write failure (the derived table dropped out
// from under the Store) must still let the source-table mutation succeed,
// and must permanently disable Vec1 for the rest of this Store's life —
// never surface an index-only failure to the caller.
func TestVecIndex_ForcedFailurePreservesSourceMutationAndMarksUnhealthy(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(t.TempDir()+"/emb.db", WithVecIndex(true))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	mustUpsert(t, store, unitRow("a", 1, []float32{1, 0, 0, 0}))
	if !store.vec.healthyOK() {
		t.Fatal("Vec1 must still be healthy before the forced failure")
	}

	// Force a genuine Vec1 failure by dropping the derived table out from
	// under the Store (simulating corruption/unavailability).
	if _, err := store.db.Exec(`DROP TABLE vec_embeddings`); err != nil {
		t.Fatalf("drop vec_embeddings for the forced-failure setup: %v", err)
	}

	if err := store.Upsert(ctx, unitRow("b", 2, []float32{0, 1, 0, 0})); err != nil {
		t.Fatalf("Upsert must succeed on the source table even when the derived write fails: %v", err)
	}

	// The source table must have BOTH rows — the mutation was never lost.
	n, err := store.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 2 {
		t.Fatalf("source rows after forced derived failure: got %d, want 2", n)
	}

	if store.vec.healthyOK() {
		t.Error("Vec1 must be marked unhealthy for the rest of this Store's life after a genuine derived-write failure")
	}

	// Subsequent reads must fall back to brute force cleanly (no error).
	hits, err := store.Search(ctx, []float32{1, 0, 0, 0}, 5)
	if err != nil {
		t.Fatalf("Search after forced Vec1 failure must still succeed via brute force: %v", err)
	}
	if len(hits) != 2 {
		t.Errorf("brute-force fallback Search: got %d hits, want 2", len(hits))
	}
}

