package store

import (
	"testing"
)

// ─── Helpers ─────────────────────────────────────────────────────────────────

// addTestAnchor upserts a minimal active anchor linked to obsSyncID and
// returns its sync_id.
func addTestAnchor(t *testing.T, s *Store, obsSyncID, file, symbol string) string {
	t.Helper()
	syncID, err := s.UpsertAnchor(UpsertAnchorParams{
		ObsSyncID:   obsSyncID,
		RepoRoot:    "/repo",
		FilePath:    file,
		Symbol:      symbol,
		LineStart:   10,
		LineEnd:     20,
		BlameSHA:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		BlameAt:     "2024-01-01T00:00:00Z",
		ContentHash: "hash-v1",
	})
	if err != nil {
		t.Fatalf("UpsertAnchor(%q): %v", file, err)
	}
	return syncID
}

// ─── 2.2 — TestUpsertAnchor_InsertsActiveRow ─────────────────────────────────

func TestUpsertAnchor_InsertsActiveRow(t *testing.T) {
	s := setupRelationsStore(t)
	_, syncA := addTestObs(t, s, "Auth middleware bugfix", "bugfix", "testproject", "project")

	syncID, err := s.UpsertAnchor(UpsertAnchorParams{
		ObsSyncID:   syncA,
		RepoRoot:    "/repo",
		FilePath:    "internal/auth/middleware.go",
		Symbol:      "Middleware",
		LineStart:   10,
		LineEnd:     25,
		BlameSHA:    "d8d74ffa1489ad18fa062f76a5455cf41f22ea9d",
		BlameAt:     "2024-01-01T00:00:00Z",
		ContentHash: "abc123",
	})
	if err != nil {
		t.Fatalf("UpsertAnchor: %v", err)
	}
	if !hasPrefix(syncID, "anc-") {
		t.Errorf("sync_id must start with 'anc-'; got %q", syncID)
	}

	var (
		obsSyncID, filePath, symbol, blameSHA, contentHash, status string
		lineStart, lineEnd                                         int
	)
	if err := s.db.QueryRow(
		`SELECT obs_sync_id, file_path, symbol, line_start, line_end, blame_sha, content_hash, anchor_status
		 FROM memory_anchors WHERE sync_id = ?`, syncID,
	).Scan(&obsSyncID, &filePath, &symbol, &lineStart, &lineEnd, &blameSHA, &contentHash, &status); err != nil {
		t.Fatalf("query inserted anchor: %v", err)
	}

	if obsSyncID != syncA {
		t.Errorf("obs_sync_id: want %q; got %q", syncA, obsSyncID)
	}
	if filePath != "internal/auth/middleware.go" {
		t.Errorf("file_path: got %q", filePath)
	}
	if symbol != "Middleware" {
		t.Errorf("symbol: got %q", symbol)
	}
	if lineStart != 10 || lineEnd != 25 {
		t.Errorf("line range: got [%d,%d]", lineStart, lineEnd)
	}
	if blameSHA != "d8d74ffa1489ad18fa062f76a5455cf41f22ea9d" {
		t.Errorf("blame_sha: got %q", blameSHA)
	}
	if contentHash != "abc123" {
		t.Errorf("content_hash: got %q", contentHash)
	}
	if status != AnchorStatusActive {
		t.Errorf("anchor_status: want %q; got %q", AnchorStatusActive, status)
	}
}

func TestUpsertAnchor_UpdatesExistingRowForSameFileAndSymbol(t *testing.T) {
	s := setupRelationsStore(t)
	_, syncA := addTestObs(t, s, "Auth middleware bugfix", "bugfix", "testproject", "project")

	first, err := s.UpsertAnchor(UpsertAnchorParams{
		ObsSyncID: syncA, FilePath: "foo.go", Symbol: "Foo",
		LineStart: 1, LineEnd: 5, BlameSHA: "sha1", ContentHash: "hash1",
	})
	if err != nil {
		t.Fatalf("UpsertAnchor (first): %v", err)
	}

	second, err := s.UpsertAnchor(UpsertAnchorParams{
		ObsSyncID: syncA, FilePath: "foo.go", Symbol: "Foo",
		LineStart: 3, LineEnd: 8, BlameSHA: "sha2", ContentHash: "hash2",
	})
	if err != nil {
		t.Fatalf("UpsertAnchor (second): %v", err)
	}

	if first != second {
		t.Errorf("expected same sync_id on upsert for the same (obs,file,symbol); got %q and %q", first, second)
	}

	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM memory_anchors WHERE obs_sync_id = ?`, syncA).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly 1 row after upsert; got %d", count)
	}

	var lineStart, lineEnd int
	var hash string
	if err := s.db.QueryRow(`SELECT line_start, line_end, content_hash FROM memory_anchors WHERE sync_id = ?`, second).
		Scan(&lineStart, &lineEnd, &hash); err != nil {
		t.Fatalf("query updated row: %v", err)
	}
	if lineStart != 3 || lineEnd != 8 || hash != "hash2" {
		t.Errorf("expected updated fields [3,8,hash2]; got [%d,%d,%s]", lineStart, lineEnd, hash)
	}
}

// ─── 2.4 — TestListActiveAnchors_ReturnsOnlyActiveStatus ─────────────────────

func TestListActiveAnchors_ReturnsOnlyActiveStatus(t *testing.T) {
	s := setupRelationsStore(t)
	_, syncA := addTestObs(t, s, "Active anchor memory", "bugfix", "testproject", "project")
	_, syncB := addTestObs(t, s, "Stale anchor memory", "bugfix", "testproject", "project")

	activeID := addTestAnchor(t, s, syncA, "active.go", "Active")
	staleID := addTestAnchor(t, s, syncB, "stale.go", "Stale")
	if err := s.MarkAnchorStale(staleID, nil); err != nil {
		t.Fatalf("MarkAnchorStale: %v", err)
	}

	anchors, err := s.ListActiveAnchors("testproject")
	if err != nil {
		t.Fatalf("ListActiveAnchors: %v", err)
	}
	if len(anchors) != 1 {
		t.Fatalf("expected 1 active anchor; got %d", len(anchors))
	}
	if anchors[0].SyncID != activeID {
		t.Errorf("expected active anchor %q; got %q", activeID, anchors[0].SyncID)
	}
	if anchors[0].AnchorStatus != AnchorStatusActive {
		t.Errorf("expected status active; got %q", anchors[0].AnchorStatus)
	}
}

func TestListActiveAnchors_ScopesByProject(t *testing.T) {
	s := setupRelationsStore(t)
	_, syncA := addTestObs(t, s, "Project A anchor", "bugfix", "project-a", "project")
	_, syncB := addTestObs(t, s, "Project B anchor", "bugfix", "project-b", "project")

	addTestAnchor(t, s, syncA, "a.go", "A")
	addTestAnchor(t, s, syncB, "b.go", "B")

	anchors, err := s.ListActiveAnchors("project-a")
	if err != nil {
		t.Fatalf("ListActiveAnchors: %v", err)
	}
	if len(anchors) != 1 || anchors[0].FilePath != "a.go" {
		t.Fatalf("expected only project-a's anchor; got %+v", anchors)
	}
}

// ─── 2.6 — TestUpdateAnchorRange (travel) ────────────────────────────────────

func TestUpdateAnchorRange_UpdatesRangeAndBlameFields(t *testing.T) {
	s := setupRelationsStore(t)
	_, syncA := addTestObs(t, s, "Traveled anchor memory", "bugfix", "testproject", "project")
	anchorID := addTestAnchor(t, s, syncA, "moved.go", "Moved")

	if err := s.UpdateAnchorRange(UpdateAnchorRangeParams{
		SyncID:      anchorID,
		LineStart:   50,
		LineEnd:     65,
		BlameSHA:    "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		ContentHash: "hash-v2",
	}); err != nil {
		t.Fatalf("UpdateAnchorRange: %v", err)
	}

	var lineStart, lineEnd int
	var blameSHA, contentHash, status string
	var checkedAt *string
	if err := s.db.QueryRow(
		`SELECT line_start, line_end, blame_sha, content_hash, anchor_status, checked_at FROM memory_anchors WHERE sync_id = ?`,
		anchorID,
	).Scan(&lineStart, &lineEnd, &blameSHA, &contentHash, &status, &checkedAt); err != nil {
		t.Fatalf("query updated anchor: %v", err)
	}

	if lineStart != 50 || lineEnd != 65 {
		t.Errorf("expected traveled range [50,65]; got [%d,%d]", lineStart, lineEnd)
	}
	if blameSHA != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Errorf("expected updated blame_sha; got %q", blameSHA)
	}
	if contentHash != "hash-v2" {
		t.Errorf("expected updated content_hash; got %q", contentHash)
	}
	if status != AnchorStatusActive {
		t.Errorf("expected anchor to remain active after travel; got %q", status)
	}
	if checkedAt == nil || *checkedAt == "" {
		t.Errorf("expected checked_at to be set")
	}
}

// ─── 2.8 — TestMarkAnchorStale idempotency ───────────────────────────────────

func TestMarkAnchorStale_SetsStatusAndReviewAfter(t *testing.T) {
	s := setupRelationsStore(t)
	_, syncA := addTestObs(t, s, "Anchor going stale", "bugfix", "testproject", "project")
	anchorID := addTestAnchor(t, s, syncA, "gone.go", "Gone")

	if err := s.MarkAnchorStale(anchorID, nil); err != nil {
		t.Fatalf("MarkAnchorStale: %v", err)
	}

	var status string
	var staledAt *string
	if err := s.db.QueryRow(`SELECT anchor_status, staled_at FROM memory_anchors WHERE sync_id = ?`, anchorID).
		Scan(&status, &staledAt); err != nil {
		t.Fatalf("query anchor: %v", err)
	}
	if status != AnchorStatusStale {
		t.Errorf("anchor_status: want %q; got %q", AnchorStatusStale, status)
	}
	if staledAt == nil || *staledAt == "" {
		t.Errorf("expected staled_at to be set")
	}

	obs, err := s.GetObservation(mustObsIntID(t, s, syncA))
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if obs.ReviewAfter == nil || *obs.ReviewAfter == "" {
		t.Errorf("expected obs.review_after to be set to now")
	}
}

func TestMarkAnchorStale_IsIdempotent(t *testing.T) {
	s := setupRelationsStore(t)
	_, syncA := addTestObs(t, s, "Anchor going stale twice", "bugfix", "testproject", "project")
	anchorID := addTestAnchor(t, s, syncA, "gone2.go", "Gone2")

	if err := s.MarkAnchorStale(anchorID, nil); err != nil {
		t.Fatalf("MarkAnchorStale (first): %v", err)
	}
	if err := s.MarkAnchorStale(anchorID, nil); err != nil {
		t.Fatalf("MarkAnchorStale (second): %v", err)
	}

	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM memory_anchors WHERE sync_id = ?`, anchorID).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly 1 anchor row after repeated MarkAnchorStale; got %d", count)
	}
}

// ─── 2.9 — supersedes relation row only when a newer memory exists ──────────

func TestMarkAnchorStale_NoSupersedesRowWithoutNewerMemory(t *testing.T) {
	s := setupRelationsStore(t)
	_, syncA := addTestObs(t, s, "Stale, no replacement", "bugfix", "testproject", "project")
	anchorID := addTestAnchor(t, s, syncA, "solo.go", "Solo")

	if err := s.MarkAnchorStale(anchorID, nil); err != nil {
		t.Fatalf("MarkAnchorStale: %v", err)
	}

	var count int
	if err := s.db.QueryRow(
		`SELECT count(*) FROM memory_relations WHERE target_id = ? AND relation = 'supersedes'`, syncA,
	).Scan(&count); err != nil {
		t.Fatalf("count supersedes rows: %v", err)
	}
	if count != 0 {
		t.Errorf("expected no supersedes row when no newer memory exists; got %d", count)
	}
}

func TestMarkAnchorStale_WritesSupersedesRowWhenNewerMemoryExists(t *testing.T) {
	s := setupRelationsStore(t)
	_, syncOld := addTestObs(t, s, "Old memory about the anchored code", "bugfix", "testproject", "project")
	_, syncNew := addTestObs(t, s, "New memory replacing the old one", "bugfix", "testproject", "project")
	anchorID := addTestAnchor(t, s, syncOld, "replaced.go", "Replaced")

	if err := s.MarkAnchorStale(anchorID, &syncNew); err != nil {
		t.Fatalf("MarkAnchorStale: %v", err)
	}

	var relSourceID, relTargetID, relation string
	var markedByActor, markedByKind *string
	if err := s.db.QueryRow(
		`SELECT source_id, target_id, relation, marked_by_actor, marked_by_kind
		 FROM memory_relations WHERE target_id = ? AND relation = 'supersedes'`, syncOld,
	).Scan(&relSourceID, &relTargetID, &relation, &markedByActor, &markedByKind); err != nil {
		t.Fatalf("query supersedes row: %v", err)
	}

	if relSourceID != syncNew {
		t.Errorf("supersedes source_id: want %q (newer memory); got %q", syncNew, relSourceID)
	}
	if relTargetID != syncOld {
		t.Errorf("supersedes target_id: want %q (staled memory); got %q", syncOld, relTargetID)
	}
	if markedByActor == nil || *markedByActor != "engram" {
		t.Errorf("marked_by_actor: want %q; got %v", "engram", markedByActor)
	}
	if markedByKind == nil || *markedByKind != "system" {
		t.Errorf("marked_by_kind: want %q; got %v", "system", markedByKind)
	}
}

func TestMarkAnchorStale_NotFoundReturnsError(t *testing.T) {
	s := setupRelationsStore(t)
	if err := s.MarkAnchorStale("anc-does-not-exist", nil); err == nil {
		t.Fatalf("expected error for unknown anchor sync_id")
	}
}

// mustObsIntID resolves an observation's integer id from its sync_id for
// tests that only have the sync_id in hand.
func mustObsIntID(t *testing.T, s *Store, syncID string) int64 {
	t.Helper()
	var id int64
	if err := s.db.QueryRow(`SELECT id FROM observations WHERE sync_id = ?`, syncID).Scan(&id); err != nil {
		t.Fatalf("resolve observation id for %q: %v", syncID, err)
	}
	return id
}
