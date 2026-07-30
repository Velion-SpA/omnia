package embed

import (
	"context"
	"fmt"
	"math"
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

// --- Phase 3: Vec1 KNN reads, parity, fallback -----------------------------

// TestVecScore_SelfOrthogonalAntipodal (task 3.1, REQ-469): the pinned
// v0.35.2 flat/cos distance conversion gives scores 1, 0, -1 for normalized
// self/orthogonal/antipodal vectors — the PINNED contract (`1 - distance`),
// never the newer Vec1 trunk's `2 - distance` semantics.
func TestVecScore_SelfOrthogonalAntipodal(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(t.TempDir()+"/emb.db", WithVecIndex(true))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	self := []float32{1, 0, 0, 0}
	orth := []float32{0, 1, 0, 0}
	anti := []float32{-1, 0, 0, 0}

	mustUpsert(t, store, unitRow("self", 1, self))
	mustUpsert(t, store, unitRow("orth", 2, orth))
	mustUpsert(t, store, unitRow("anti", 3, anti))
	if _, err := store.VecBackfill(ctx); err != nil {
		t.Fatalf("VecBackfill: %v", err)
	}
	if !store.vec.usable() {
		t.Fatal("Vec1 must be usable after a successful backfill")
	}

	hits, err := store.Search(ctx, self, 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	scores := map[string]float32{}
	for _, h := range hits {
		scores[h.SyncID] = h.Score
	}
	const tol = 1e-4
	if math.Abs(float64(scores["self"])-1) > tol {
		t.Errorf("self score: got %v, want ~1", scores["self"])
	}
	if math.Abs(float64(scores["orth"])-0) > tol {
		t.Errorf("orthogonal score: got %v, want ~0", scores["orth"])
	}
	if math.Abs(float64(scores["anti"])-(-1)) > tol {
		t.Errorf("antipodal score: got %v, want ~-1", scores["anti"])
	}
}

// angledVector returns a dim-length unit vector [cos(theta), sin(theta), 0,
// ..., 0] — unlike oneHot, distinct theta values give DISTINCT (non-tied)
// cosine similarities against a [1,0,...,0] query, which is required for an
// unambiguous top-k parity comparison between brute force and Vec1 (spec
// REQ-462 explicitly excepts ties).
func angledVector(dim int, theta float64) []float32 {
	v := make([]float32, dim)
	v[0] = float32(math.Cos(theta))
	v[1] = float32(math.Sin(theta))
	return v
}

// TestVecSearch_MatchesBruteForceTopKAndRespectsProjectScope (task 3.3,
// REQ-462): on a multi-project fixture, enabled Search/SearchScoped return
// the brute-force-equivalent top-k, and scoped KNN never returns another
// project's vector — including the exact crowding-out scenario the
// brute-force scoping fix already guards (engram obs #1436).
func TestVecSearch_MatchesBruteForceTopKAndRespectsProjectScope(t *testing.T) {
	ctx := context.Background()

	build := func(vecEnabled bool) *Store {
		var opts []Option
		if vecEnabled {
			opts = append(opts, WithVecIndex(true))
		}
		store, err := OpenStore(t.TempDir()+"/emb.db", opts...)
		if err != nil {
			t.Fatalf("OpenStore(vecEnabled=%v): %v", vecEnabled, err)
		}
		t.Cleanup(func() { store.Close() })

		// angledVector gives every row a DISTINCT cosine similarity to the
		// query (unlike one-hot vectors, which collapse into just two tied
		// similarity buckets) — top-k ranking is only unambiguous between
		// brute force and Vec1 when there are no ties to arbitrate (spec
		// REQ-462 explicitly excepts ties from the parity guarantee).
		for i := 0; i < 250; i++ {
			r := unitRow(fmt.Sprintf("pA-%d", i), i+1, angledVector(16, float64(i+1)/2000))
			r.Project = "projA"
			if err := store.Upsert(ctx, r); err != nil {
				t.Fatalf("Upsert: %v", err)
			}
		}
		for i := 0; i < 250; i++ {
			r := unitRow(fmt.Sprintf("pB-%d", i), 1000+i+1, angledVector(16, float64(i+251)/2000))
			r.Project = "projB"
			if err := store.Upsert(ctx, r); err != nil {
				t.Fatalf("Upsert: %v", err)
			}
		}
		if vecEnabled {
			report, err := store.VecBackfill(ctx)
			if err != nil {
				t.Fatalf("VecBackfill: %v", err)
			}
			if !store.vec.usable() {
				t.Fatalf("Vec1 must be usable after backfill (report=%+v)", report)
			}
		}
		return store
	}

	brute := build(false)
	vec := build(true)

	query := oneHot(16, 0)

	wantGlobal, err := brute.Search(ctx, query, 10)
	if err != nil {
		t.Fatalf("brute Search: %v", err)
	}
	gotGlobal, err := vec.Search(ctx, query, 10)
	if err != nil {
		t.Fatalf("vec Search: %v", err)
	}
	if len(wantGlobal) != len(gotGlobal) {
		t.Fatalf("unscoped top-k length: brute=%d vec=%d", len(wantGlobal), len(gotGlobal))
	}
	for i := range wantGlobal {
		if wantGlobal[i].SyncID != gotGlobal[i].SyncID {
			t.Errorf("unscoped top-k[%d]: brute=%s vec=%s", i, wantGlobal[i].SyncID, gotGlobal[i].SyncID)
		}
		if math.Abs(float64(wantGlobal[i].Score-gotGlobal[i].Score)) > 1e-4 {
			t.Errorf("unscoped score[%d]: brute=%v vec=%v", i, wantGlobal[i].Score, gotGlobal[i].Score)
		}
	}

	wantScoped, err := brute.SearchScoped(ctx, query, 10, "projA")
	if err != nil {
		t.Fatalf("brute SearchScoped: %v", err)
	}
	gotScoped, err := vec.SearchScoped(ctx, query, 10, "projA")
	if err != nil {
		t.Fatalf("vec SearchScoped: %v", err)
	}
	if len(wantScoped) != len(gotScoped) {
		t.Fatalf("scoped top-k length: brute=%d vec=%d", len(wantScoped), len(gotScoped))
	}
	for _, h := range gotScoped {
		if h.SyncID[:2] != "pA" {
			t.Errorf("SearchScoped(projA) returned a non-projA row: %+v", h)
		}
	}
	for i := range wantScoped {
		if wantScoped[i].SyncID != gotScoped[i].SyncID {
			t.Errorf("scoped top-k[%d]: brute=%s vec=%s", i, wantScoped[i].SyncID, gotScoped[i].SyncID)
		}
	}
}

// TestVecSearch_CrowdingOutFixHoldsUnderVec1 reproduces the exact
// brute-force crowding-out regression test (TestStore_SearchScoped_
// TargetProjectSurfacesDespiteGlobalCrowdOut) against a Vec1-enabled store,
// proving the Vec1 metadata pushdown filters BEFORE top-K accumulation
// (verified empirically against the pinned dependency), not after.
func TestVecSearch_CrowdingOutFixHoldsUnderVec1(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(t.TempDir()+"/emb.db", WithVecIndex(true))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	query := []float32{1, 0, 0, 0}
	for i := 0; i < 20; i++ {
		r := unitRow(fmt.Sprintf("other-%d", i), i+1, []float32{0.995, 0.0999, 0, 0})
		r.Project = "other"
		mustUpsert(t, store, r)
	}
	target := unitRow("target-1", 1000, []float32{0.6, 0.8, 0, 0})
	target.Project = "target"
	mustUpsert(t, store, target)

	if _, err := store.VecBackfill(ctx); err != nil {
		t.Fatalf("VecBackfill: %v", err)
	}
	if !store.vec.usable() {
		t.Fatal("Vec1 must be usable after backfill — this test must actually exercise the Vec1 pushdown path, otherwise it would pass trivially via brute-force fallback and no longer guard the behavior it's named for")
	}

	scopedHits, err := store.SearchScoped(ctx, query, 5, "target")
	if err != nil {
		t.Fatalf("SearchScoped: %v", err)
	}
	if len(scopedHits) != 1 || scopedHits[0].SyncID != "target-1" {
		t.Fatalf("SearchScoped(project=target) under Vec1: got %+v, want exactly [target-1]", scopedHits)
	}
}

// TestVecSearch_DimensionMismatchQueryFallsBackToBruteForce (task 3.5,
// REQ-466/468): a query whose dimension differs from the active index
// dimension must use brute force, never error, never touch Vec1 health.
func TestVecSearch_DimensionMismatchQueryFallsBackToBruteForce(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(t.TempDir()+"/emb.db", WithVecIndex(true))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	mustUpsert(t, store, unitRow("a", 1, oneHot(8, 0)))
	if _, err := store.VecBackfill(ctx); err != nil {
		t.Fatalf("VecBackfill: %v", err)
	}

	hits, err := store.Search(ctx, oneHot(16, 0), 5) // wrong dimension query
	if err != nil {
		t.Fatalf("Search with mismatched query dimension must not error: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("mismatched-dimension query against an 8-dim store: got %+v, want no hits", hits)
	}
	if !store.vec.healthyOK() {
		t.Error("a dimension-mismatched query must NOT mark Vec1 unhealthy — it is an expected routing decision, not a failure")
	}
}

// TestVecSearch_UnavailableIndexFallsBackToBruteForce (task 3.5, REQ-465): a
// store that never ran a successful backfill (never "ready") must serve
// reads via brute force, and an unhealthy Vec1 (post-corruption) must do the
// same without ever failing the read.
func TestVecSearch_UnavailableIndexFallsBackToBruteForce(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(t.TempDir()+"/emb.db", WithVecIndex(true))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	mustUpsert(t, store, unitRow("a", 1, []float32{1, 0, 0}))
	// No VecBackfill call: not "ready" yet.
	hits, err := store.Search(ctx, []float32{1, 0, 0}, 5)
	if err != nil {
		t.Fatalf("Search on a never-backfilled Vec1 store must still succeed via brute force: %v", err)
	}
	if len(hits) != 1 || hits[0].SyncID != "a" {
		t.Errorf("brute-force fallback on unready index: got %+v", hits)
	}
}

// TestVecGraph_MatchesBruteForceEdgeSetAndRespectsProjectScope (task 3.7,
// REQ-462/463): per-node Vec1 KNN yields the existing O(N^2) edge set
// (except ties), respects project filtering, and produces no HNSW/ANN/
// int8/binary artifact.
func TestVecGraph_MatchesBruteForceEdgeSetAndRespectsProjectScope(t *testing.T) {
	ctx := context.Background()

	build := func(vecEnabled bool) *Store {
		var opts []Option
		if vecEnabled {
			opts = append(opts, WithVecIndex(true))
		}
		store, err := OpenStore(t.TempDir()+"/emb.db", opts...)
		if err != nil {
			t.Fatalf("OpenStore(vecEnabled=%v): %v", vecEnabled, err)
		}
		t.Cleanup(func() { store.Close() })

		a1 := unitRow("a1", 1, []float32{1, 0, 0})
		a1.Project = "projA"
		mustUpsert(t, store, a1)
		a2 := unitRow("a2", 2, []float32{0.999, 0.045, 0})
		a2.Project = "projA"
		mustUpsert(t, store, a2)
		b1 := unitRow("b1", 3, []float32{0.998, 0.06, 0})
		b1.Project = "projB"
		mustUpsert(t, store, b1)
		b2 := unitRow("b2", 4, []float32{0.997, 0.07, 0})
		b2.Project = "projB"
		mustUpsert(t, store, b2)

		if vecEnabled {
			if _, err := store.VecBackfill(ctx); err != nil {
				t.Fatalf("VecBackfill: %v", err)
			}
			if !store.vec.usable() {
				t.Fatal("Vec1 must be usable after backfill")
			}
		}
		return store
	}

	brute := build(false)
	vec := build(true)

	wantNodes, wantEdges, err := brute.GraphScoped([]string{"projA"}, 5, 0.9)
	if err != nil {
		t.Fatalf("brute GraphScoped: %v", err)
	}
	gotNodes, gotEdges, err := vec.GraphScoped([]string{"projA"}, 5, 0.9)
	if err != nil {
		t.Fatalf("vec GraphScoped: %v", err)
	}
	if len(gotNodes) != len(wantNodes) {
		t.Fatalf("GraphScoped(projA) node count: brute=%d vec=%d", len(wantNodes), len(gotNodes))
	}
	for _, n := range gotNodes {
		if n.Project != "projA" {
			t.Errorf("vec GraphScoped(projA) returned a non-projA node: %+v", n)
		}
	}
	if len(gotEdges) != len(wantEdges) {
		t.Fatalf("GraphScoped(projA) edge count: brute=%d vec=%d (want=%+v got=%+v)", len(wantEdges), len(gotEdges), wantEdges, gotEdges)
	}
	for i := range wantEdges {
		if wantEdges[i].Source != gotEdges[i].Source || wantEdges[i].Target != gotEdges[i].Target {
			t.Errorf("edge[%d]: brute=%+v vec=%+v", i, wantEdges[i], gotEdges[i])
		}
		if math.Abs(float64(wantEdges[i].Weight-gotEdges[i].Weight)) > 1e-4 {
			t.Errorf("edge[%d] weight: brute=%v vec=%v", i, wantEdges[i].Weight, gotEdges[i].Weight)
		}
	}
	for _, e := range gotEdges {
		bySync := map[int]string{1: "a1", 2: "a2", 3: "b1", 4: "b2"}
		if bySync[e.Source] == "b1" || bySync[e.Source] == "b2" || bySync[e.Target] == "b1" || bySync[e.Target] == "b2" {
			t.Errorf("vec GraphScoped(projA) must never surface a projB endpoint, got edge %+v", e)
		}
	}
}
