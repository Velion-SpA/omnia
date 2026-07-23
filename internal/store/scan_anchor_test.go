package store

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// ─── fakeAnchorProvider ───────────────────────────────────────────────────────

// fakeAnchorProvider returns pre-baked AnchorRecheckResult/error values keyed
// by anchor sync_id, so ScanProject{Source:anchor} tests are deterministic
// and never shell out to real git (design obs #1594's injectable-seam
// convention, mirrored from scan_semantic_test.go's verdictRunner).
//
// scanProjectAnchors' worker pool calls Recheck concurrently, so the fake's
// own bookkeeping (calls) is mutex-guarded — reads of the results/errs maps
// (populated once, before ScanProject runs, and never written concurrently)
// don't need the lock, but the shared calls slice does.
type fakeAnchorProvider struct {
	results map[string]AnchorRecheckResult
	errs    map[string]error

	mu    sync.Mutex
	calls []string
}

func newFakeAnchorProvider() *fakeAnchorProvider {
	return &fakeAnchorProvider{
		results: map[string]AnchorRecheckResult{},
		errs:    map[string]error{},
	}
}

func (f *fakeAnchorProvider) Recheck(_ context.Context, a MemoryAnchor) (AnchorRecheckResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, a.SyncID)
	f.mu.Unlock()

	if err, ok := f.errs[a.SyncID]; ok {
		return AnchorRecheckResult{}, err
	}
	if r, ok := f.results[a.SyncID]; ok {
		return r, nil
	}
	// Default (no fixture registered): report truly unchanged.
	return AnchorRecheckResult{
		Found:       true,
		LineStart:   a.LineStart,
		LineEnd:     a.LineEnd,
		BlameSHA:    a.BlameSHA,
		ContentHash: a.ContentHash,
	}, nil
}

// ─── query helpers ────────────────────────────────────────────────────────────

func anchorStatus(t *testing.T, s *Store, syncID string) string {
	t.Helper()
	var status string
	if err := s.db.QueryRow(`SELECT anchor_status FROM memory_anchors WHERE sync_id = ?`, syncID).Scan(&status); err != nil {
		t.Fatalf("query anchor_status: %v", err)
	}
	return status
}

func anchorRange(t *testing.T, s *Store, syncID string) (int, int) {
	t.Helper()
	var start, end int
	if err := s.db.QueryRow(`SELECT line_start, line_end FROM memory_anchors WHERE sync_id = ?`, syncID).Scan(&start, &end); err != nil {
		t.Fatalf("query anchor range: %v", err)
	}
	return start, end
}

func anchorNewBlameSHA(t *testing.T, s *Store, syncID string) *string {
	t.Helper()
	var sha *string
	if err := s.db.QueryRow(`SELECT new_blame_sha FROM memory_anchors WHERE sync_id = ?`, syncID).Scan(&sha); err != nil {
		t.Fatalf("query new_blame_sha: %v", err)
	}
	return sha
}

// ─── 4.2 — unchanged anchor is skipped, stays active ─────────────────────────

func TestScanProjectAnchor_Unchanged_SkipsStaysActive(t *testing.T) {
	s := setupRelationsStore(t)
	_, syncA := addTestObs(t, s, "Unchanged anchor memory", "bugfix", "testproject", "project")
	anchorID := addTestAnchor(t, s, syncA, "unchanged.go", "Unchanged")

	provider := newFakeAnchorProvider()
	// Default fixture already reports "same SHA/hash, same range" (truly unchanged).

	result, err := s.ScanProject(ScanOptions{
		Project: "testproject", Source: SourceAnchor, Apply: true, AnchorProvider: provider,
	})
	if err != nil {
		t.Fatalf("ScanProject: %v", err)
	}
	if result.AnchorsChecked != 1 {
		t.Errorf("AnchorsChecked: want 1; got %d", result.AnchorsChecked)
	}
	if result.AnchorsTraveled != 0 || result.AnchorsStaled != 0 {
		t.Errorf("expected no travel/stale; got traveled=%d staled=%d", result.AnchorsTraveled, result.AnchorsStaled)
	}
	if anchorStatus(t, s, anchorID) != AnchorStatusActive {
		t.Errorf("expected anchor to stay active")
	}
}

// ─── 4.7 — changed but non-material (whitespace-only) skipped, stays active ──

func TestScanProjectAnchor_NonMaterialWhitespaceOnly_SkipsStaysActive(t *testing.T) {
	s := setupRelationsStore(t)
	_, syncA := addTestObs(t, s, "Whitespace touched anchor memory", "bugfix", "testproject", "project")
	anchorID := addTestAnchor(t, s, syncA, "whitespace.go", "Reindented")

	provider := newFakeAnchorProvider()
	// Same location, same ContentHash (RangeHash already normalizes whitespace —
	// REQ-003), but a DIFFERENT BlameSHA (a new commit touched only whitespace
	// in the range). Must still be treated as non-material: skip.
	provider.results[anchorID] = AnchorRecheckResult{
		Found: true, LineStart: 10, LineEnd: 20,
		BlameSHA:    "cccccccccccccccccccccccccccccccccccccccc", // differs from the stored "aaaa...a"
		ContentHash: "hash-v1",                                  // same as addTestAnchor's stored ContentHash
	}

	result, err := s.ScanProject(ScanOptions{
		Project: "testproject", Source: SourceAnchor, Apply: true, AnchorProvider: provider,
	})
	if err != nil {
		t.Fatalf("ScanProject: %v", err)
	}
	if result.AnchorsTraveled != 0 || result.AnchorsStaled != 0 {
		t.Errorf("expected no travel/stale for whitespace-only change; got traveled=%d staled=%d", result.AnchorsTraveled, result.AnchorsStaled)
	}
	if anchorStatus(t, s, anchorID) != AnchorStatusActive {
		t.Errorf("expected anchor to stay active")
	}
}

// ─── 4.4 — TRAVEL: same content-hash, new range ──────────────────────────────

func TestScanProjectAnchor_Travel_UpdatesRangeNotStale(t *testing.T) {
	s := setupRelationsStore(t)
	_, syncA := addTestObs(t, s, "Refactor-moved anchor memory", "bugfix", "testproject", "project")
	anchorID := addTestAnchor(t, s, syncA, "moved.go", "Moved")

	provider := newFakeAnchorProvider()
	provider.results[anchorID] = AnchorRecheckResult{
		Found: true, LineStart: 100, LineEnd: 110, // new location
		BlameSHA:    "dddddddddddddddddddddddddddddddddddddddd",
		ContentHash: "hash-v1", // SAME body as stored — this is what makes it travel
	}

	result, err := s.ScanProject(ScanOptions{
		Project: "testproject", Source: SourceAnchor, Apply: true, AnchorProvider: provider,
	})
	if err != nil {
		t.Fatalf("ScanProject: %v", err)
	}
	if result.AnchorsTraveled != 1 {
		t.Errorf("AnchorsTraveled: want 1; got %d", result.AnchorsTraveled)
	}
	if result.AnchorsStaled != 0 {
		t.Errorf("expected MarkAnchorStale NOT called on travel; AnchorsStaled=%d", result.AnchorsStaled)
	}
	if anchorStatus(t, s, anchorID) != AnchorStatusActive {
		t.Errorf("expected anchor to remain active after travel (never staled)")
	}
	start, end := anchorRange(t, s, anchorID)
	if start != 100 || end != 110 {
		t.Errorf("expected relocated range [100,110]; got [%d,%d]", start, end)
	}
}

// ─── 4.6 — symbol-not-found (Found=false) proceeds straight to staleness ────

func TestScanProjectAnchor_SymbolNotFound_MaterialStale(t *testing.T) {
	s := setupRelationsStore(t)
	_, syncA := addTestObs(t, s, "Symbol relocated nowhere", "bugfix", "testproject", "project")
	anchorID := addTestAnchor(t, s, syncA, "gone.go", "Gone")

	provider := newFakeAnchorProvider()
	provider.results[anchorID] = AnchorRecheckResult{Found: false} // relocation failed entirely

	result, err := s.ScanProject(ScanOptions{
		Project: "testproject", Source: SourceAnchor, Apply: true, AnchorProvider: provider,
	})
	if err != nil {
		t.Fatalf("ScanProject: %v", err)
	}
	if result.AnchorsStaled != 1 {
		t.Errorf("AnchorsStaled: want 1; got %d", result.AnchorsStaled)
	}
	if result.AnchorsTraveled != 0 {
		t.Errorf("expected no travel; got %d", result.AnchorsTraveled)
	}
	if anchorStatus(t, s, anchorID) != AnchorStatusStale {
		t.Errorf("expected anchor_status=stale")
	}
	obs, err := s.GetObservation(mustObsIntID(t, s, syncA))
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if obs.ReviewAfter == nil || *obs.ReviewAfter == "" {
		t.Errorf("expected review_after to be set")
	}
}

// ─── content changed at the SAME location -> stale (not travel) ────────────

func TestScanProjectAnchor_ContentChangedSameLocation_Stale(t *testing.T) {
	s := setupRelationsStore(t)
	_, syncA := addTestObs(t, s, "Directly edited anchor memory", "bugfix", "testproject", "project")
	anchorID := addTestAnchor(t, s, syncA, "edited.go", "Edited")

	provider := newFakeAnchorProvider()
	provider.results[anchorID] = AnchorRecheckResult{
		Found: true, LineStart: 10, LineEnd: 20, // SAME location as stored
		BlameSHA:    "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		ContentHash: "hash-v2", // DIFFERENT body
	}

	result, err := s.ScanProject(ScanOptions{
		Project: "testproject", Source: SourceAnchor, Apply: true, AnchorProvider: provider,
	})
	if err != nil {
		t.Fatalf("ScanProject: %v", err)
	}
	if result.AnchorsStaled != 1 || result.AnchorsTraveled != 0 {
		t.Errorf("expected staled=1 traveled=0; got staled=%d traveled=%d", result.AnchorsStaled, result.AnchorsTraveled)
	}
	if anchorStatus(t, s, anchorID) != AnchorStatusStale {
		t.Errorf("expected anchor_status=stale")
	}
}

// ─── receipt: new_blame_sha persisted at staling time ────────────────────────

func TestScanProjectAnchor_StaleReceiptSHAPersisted(t *testing.T) {
	s := setupRelationsStore(t)
	_, syncA := addTestObs(t, s, "Receipt SHA anchor memory", "bugfix", "testproject", "project")
	anchorID := addTestAnchor(t, s, syncA, "receipt.go", "Receipt")

	const newSHA = "ffffffffffffffffffffffffffffffffffffffff"
	provider := newFakeAnchorProvider()
	provider.results[anchorID] = AnchorRecheckResult{
		Found: true, LineStart: 10, LineEnd: 20,
		BlameSHA: newSHA, ContentHash: "hash-v2",
	}

	if _, err := s.ScanProject(ScanOptions{
		Project: "testproject", Source: SourceAnchor, Apply: true, AnchorProvider: provider,
	}); err != nil {
		t.Fatalf("ScanProject: %v", err)
	}

	got := anchorNewBlameSHA(t, s, anchorID)
	if got == nil || *got != newSHA {
		t.Errorf("expected new_blame_sha=%q; got %v", newSHA, got)
	}
}

// ─── AnchorsErrors: a Recheck error must not be miscounted as AnchorsChecked ─

// TestScanProjectAnchor_RecheckError_CountsAnchorsErrorsNotChecked verifies
// that a real Recheck error (e.g. a git error) increments AnchorsErrors and
// is NEVER folded into AnchorsChecked, mirroring how the sibling semantic
// pass counts runner.Compare errors in SemanticErrors. Before this fix, the
// error was counted as a clean AnchorsChecked and the anchor was silently
// dropped, making a git error indistinguishable from "confirmed unchanged".
func TestScanProjectAnchor_RecheckError_CountsAnchorsErrorsNotChecked(t *testing.T) {
	s := setupRelationsStore(t)
	_, syncA := addTestObs(t, s, "Recheck error anchor memory", "bugfix", "testproject", "project")
	anchorID := addTestAnchor(t, s, syncA, "erroring.go", "Erroring")

	provider := newFakeAnchorProvider()
	provider.errs[anchorID] = errors.New("simulated git error")

	result, err := s.ScanProject(ScanOptions{
		Project: "testproject", Source: SourceAnchor, Apply: true, AnchorProvider: provider,
	})
	if err != nil {
		t.Fatalf("ScanProject: %v", err)
	}
	if result.AnchorsErrors != 1 {
		t.Errorf("AnchorsErrors: want 1; got %d", result.AnchorsErrors)
	}
	if result.AnchorsChecked != 0 {
		t.Errorf("AnchorsChecked: want 0 (errored anchor must not count as clean-checked); got %d", result.AnchorsChecked)
	}
	if result.AnchorsTraveled != 0 || result.AnchorsStaled != 0 {
		t.Errorf("expected no travel/stale on recheck error; got traveled=%d staled=%d", result.AnchorsTraveled, result.AnchorsStaled)
	}
	if anchorStatus(t, s, anchorID) != AnchorStatusActive {
		t.Errorf("expected anchor to remain active/untouched after a recheck error")
	}
}

// ─── 4.8 — mixed-batch counter consistency ───────────────────────────────────

func TestScanProjectAnchor_MixedBatchCounterConsistency(t *testing.T) {
	s := setupRelationsStore(t)
	_, syncA := addTestObs(t, s, "Mixed batch unchanged", "bugfix", "testproject", "project")
	_, syncB := addTestObs(t, s, "Mixed batch travel", "bugfix", "testproject", "project")
	_, syncC := addTestObs(t, s, "Mixed batch stale", "bugfix", "testproject", "project")

	unchangedID := addTestAnchor(t, s, syncA, "u.go", "U")
	travelID := addTestAnchor(t, s, syncB, "t.go", "T")
	staleID := addTestAnchor(t, s, syncC, "s.go", "S")

	provider := newFakeAnchorProvider()
	// unchangedID: default fixture (truly unchanged).
	provider.results[travelID] = AnchorRecheckResult{
		Found: true, LineStart: 200, LineEnd: 210, BlameSHA: "1111111111111111111111111111111111111111", ContentHash: "hash-v1",
	}
	provider.results[staleID] = AnchorRecheckResult{Found: false}

	result, err := s.ScanProject(ScanOptions{
		Project: "testproject", Source: SourceAnchor, Apply: true, AnchorProvider: provider,
	})
	if err != nil {
		t.Fatalf("ScanProject: %v", err)
	}
	if result.AnchorsChecked != 3 {
		t.Errorf("AnchorsChecked: want 3; got %d", result.AnchorsChecked)
	}
	if result.AnchorsTraveled != 1 {
		t.Errorf("AnchorsTraveled: want 1; got %d", result.AnchorsTraveled)
	}
	if result.AnchorsStaled != 1 {
		t.Errorf("AnchorsStaled: want 1; got %d", result.AnchorsStaled)
	}
	if anchorStatus(t, s, unchangedID) != AnchorStatusActive {
		t.Errorf("unchanged anchor should stay active")
	}
	if anchorStatus(t, s, travelID) != AnchorStatusActive {
		t.Errorf("traveled anchor should stay active")
	}
	if anchorStatus(t, s, staleID) != AnchorStatusStale {
		t.Errorf("staled anchor should be stale")
	}
}

// ─── dry-run: counters populate, no writes ───────────────────────────────────

func TestScanProjectAnchor_DryRun_NoWrites(t *testing.T) {
	s := setupRelationsStore(t)
	_, syncA := addTestObs(t, s, "Dry-run travel candidate", "bugfix", "testproject", "project")
	_, syncB := addTestObs(t, s, "Dry-run stale candidate", "bugfix", "testproject", "project")

	travelID := addTestAnchor(t, s, syncA, "dry-travel.go", "DryTravel")
	staleID := addTestAnchor(t, s, syncB, "dry-stale.go", "DryStale")

	provider := newFakeAnchorProvider()
	provider.results[travelID] = AnchorRecheckResult{
		Found: true, LineStart: 300, LineEnd: 310, BlameSHA: "2222222222222222222222222222222222222222", ContentHash: "hash-v1",
	}
	provider.results[staleID] = AnchorRecheckResult{Found: false}

	result, err := s.ScanProject(ScanOptions{
		Project: "testproject", Source: SourceAnchor, Apply: false, AnchorProvider: provider,
	})
	if err != nil {
		t.Fatalf("ScanProject: %v", err)
	}
	if !result.DryRun {
		t.Errorf("expected DryRun=true")
	}
	if result.AnchorsTraveled != 1 || result.AnchorsStaled != 1 {
		t.Errorf("expected dry-run to still report traveled=1 staled=1; got traveled=%d staled=%d", result.AnchorsTraveled, result.AnchorsStaled)
	}

	// No writes: both anchors must be untouched.
	if anchorStatus(t, s, travelID) != AnchorStatusActive {
		t.Errorf("dry-run must not write: travel anchor status changed")
	}
	start, end := anchorRange(t, s, travelID)
	if start != 10 || end != 20 {
		t.Errorf("dry-run must not write: travel anchor range changed to [%d,%d]", start, end)
	}
	if anchorStatus(t, s, staleID) != AnchorStatusActive {
		t.Errorf("dry-run must not write: stale-candidate anchor was marked stale")
	}
}

// ─── stale-without-newer-memory writes no supersedes row (delegates to 2.9) ─

func TestScanProjectAnchor_StaleWithoutNewerMemory_NoSupersedesRow(t *testing.T) {
	s := setupRelationsStore(t)
	_, syncA := addTestObs(t, s, "Solo stale via scan", "bugfix", "testproject", "project")
	anchorID := addTestAnchor(t, s, syncA, "solo-scan.go", "SoloScan")

	provider := newFakeAnchorProvider()
	provider.results[anchorID] = AnchorRecheckResult{Found: false}

	if _, err := s.ScanProject(ScanOptions{
		Project: "testproject", Source: SourceAnchor, Apply: true, AnchorProvider: provider,
	}); err != nil {
		t.Fatalf("ScanProject: %v", err)
	}

	var count int
	if err := s.db.QueryRow(
		`SELECT count(*) FROM memory_relations WHERE target_id = ? AND relation = 'supersedes'`, syncA,
	).Scan(&count); err != nil {
		t.Fatalf("count supersedes rows: %v", err)
	}
	if count != 0 {
		t.Errorf("expected no supersedes row; got %d", count)
	}
}

// ─── AnchorProvider required when Source==anchor ─────────────────────────────

func TestScanProject_AnchorProviderRequired_ReturnsErr(t *testing.T) {
	s := setupRelationsStore(t)
	_, err := s.ScanProject(ScanOptions{Project: "testproject", Source: SourceAnchor})
	if !errors.Is(err, ErrAnchorProviderRequired) {
		t.Errorf("expected ErrAnchorProviderRequired; got %v", err)
	}
}

// ─── Repo filter ──────────────────────────────────────────────────────────────

func TestScanProjectAnchor_RepoFilter(t *testing.T) {
	s := setupRelationsStore(t)
	_, syncA := addTestObs(t, s, "Repo A anchor", "bugfix", "testproject", "project")
	_, syncB := addTestObs(t, s, "Repo B anchor", "bugfix", "testproject", "project")

	idA, err := s.UpsertAnchor(UpsertAnchorParams{
		ObsSyncID: syncA, RepoRoot: "/repo-a", FilePath: "a.go", Symbol: "A",
		LineStart: 1, LineEnd: 5, ContentHash: "hash-a",
	})
	if err != nil {
		t.Fatalf("UpsertAnchor A: %v", err)
	}
	idB, err := s.UpsertAnchor(UpsertAnchorParams{
		ObsSyncID: syncB, RepoRoot: "/repo-b", FilePath: "b.go", Symbol: "B",
		LineStart: 1, LineEnd: 5, ContentHash: "hash-b",
	})
	if err != nil {
		t.Fatalf("UpsertAnchor B: %v", err)
	}

	provider := newFakeAnchorProvider()
	result, err := s.ScanProject(ScanOptions{
		Project: "testproject", Source: SourceAnchor, Apply: true, AnchorProvider: provider, Repo: "/repo-a",
	})
	if err != nil {
		t.Fatalf("ScanProject: %v", err)
	}
	if result.AnchorsChecked != 1 {
		t.Errorf("expected only repo-a's anchor checked; got %d", result.AnchorsChecked)
	}
	found := false
	for _, id := range provider.calls {
		if id == idB {
			found = true
		}
	}
	if found {
		t.Errorf("expected repo-b's anchor NOT to be rechecked")
	}
	_ = idA
}

// ─── default Source ("") behaves like today (regression) ────────────────────

func TestScanProject_DefaultSourceUnaffectedByAnchorBranch(t *testing.T) {
	s := setupRelationsStore(t)
	idA, syncA, idB, _ := seedSimilarPair(t, s, "testproject")
	_ = idB

	result, err := s.ScanProject(ScanOptions{Project: "testproject"})
	if err != nil {
		t.Fatalf("ScanProject: %v", err)
	}
	if result.AnchorsChecked != 0 || result.AnchorsTraveled != 0 || result.AnchorsStaled != 0 {
		t.Errorf("expected zero-value anchor counters on the default FTS path; got %+v", result)
	}
	_ = idA
	_ = syncA
}
