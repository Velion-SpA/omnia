package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/velion/omnia/internal/datadir"
)

// TestAddObservation_PersistsSourceAndTrustTag (omnia-provenance-foundation,
// phase 2): AddObservation must persist an explicit Source and its derived
// TrustTag on a brand-new observation.
func TestAddObservation_PersistsSourceAndTrustTag(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("prov-sess", "prov-proj", "/tmp/prov"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	id, err := s.AddObservation(AddObservationParams{
		SessionID: "prov-sess",
		Type:      "manual",
		Title:     "provenance source test",
		Content:   "some content",
		Project:   "prov-proj",
		Source:    "ingest:web",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if obs.Source == nil || *obs.Source != "ingest:web" {
		t.Fatalf("Source = %v, want %q", obs.Source, "ingest:web")
	}
	if obs.TrustTag == nil || *obs.TrustTag != TrustTagIngestWeb {
		t.Fatalf("TrustTag = %v, want %q", obs.TrustTag, TrustTagIngestWeb)
	}
}

// TestAddObservation_MissingSourceDefaultsUnverified (omnia-provenance-foundation,
// phase 2): a save with no Source must persist trust_tag="unverified" — the
// legacy/no-source read path.
func TestAddObservation_MissingSourceDefaultsUnverified(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("prov-sess2", "prov-proj2", "/tmp/prov2"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	id, err := s.AddObservation(AddObservationParams{
		SessionID: "prov-sess2",
		Type:      "manual",
		Title:     "no source test",
		Content:   "some content",
		Project:   "prov-proj2",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if obs.TrustTag == nil || *obs.TrustTag != TrustTagUnverified {
		t.Fatalf("TrustTag = %v, want %q", obs.TrustTag, TrustTagUnverified)
	}
}

// TestAddObservation_TopicKeyRevision_PreservesSourceWhenAbsent
// (omnia-provenance-foundation, phase 2): a topic_key revision save that
// omits Source must PRESERVE the previously stored source/trust_tag rather
// than clearing it — mirrors error_signature/outcome's
// COALESCE(NULLIF(?, ”), col) convention.
func TestAddObservation_TopicKeyRevision_PreservesSourceWhenAbsent(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("prov-sess3", "prov-proj3", "/tmp/prov3"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	id, err := s.AddObservation(AddObservationParams{
		SessionID: "prov-sess3",
		Type:      "decision",
		Title:     "revision preserves source v1",
		Content:   "content v1",
		Project:   "prov-proj3",
		TopicKey:  "provenance/revision-test",
		Source:    "agent",
	})
	if err != nil {
		t.Fatalf("AddObservation (initial): %v", err)
	}

	// Revision save: same topic_key, no Source provided.
	revisedID, err := s.AddObservation(AddObservationParams{
		SessionID: "prov-sess3",
		Type:      "decision",
		Title:     "revision preserves source v2",
		Content:   "content v2",
		Project:   "prov-proj3",
		TopicKey:  "provenance/revision-test",
	})
	if err != nil {
		t.Fatalf("AddObservation (revision): %v", err)
	}
	if revisedID != id {
		t.Fatalf("expected topic_key revision to reuse id %d, got %d", id, revisedID)
	}

	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if obs.Source == nil || *obs.Source != "agent" {
		t.Fatalf("Source after revision = %v, want preserved %q", obs.Source, "agent")
	}
	if obs.TrustTag == nil || *obs.TrustTag != TrustTagAgent {
		t.Fatalf("TrustTag after revision = %v, want preserved %q", obs.TrustTag, TrustTagAgent)
	}

	// Revision WITH a new Source must overwrite.
	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "prov-sess3",
		Type:      "decision",
		Title:     "revision overwrites source v3",
		Content:   "content v3",
		Project:   "prov-proj3",
		TopicKey:  "provenance/revision-test",
		Source:    "ingest:doc",
	}); err != nil {
		t.Fatalf("AddObservation (revision overwrite): %v", err)
	}
	obs2, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation (after overwrite): %v", err)
	}
	if obs2.Source == nil || *obs2.Source != "ingest:doc" {
		t.Fatalf("Source after overwrite = %v, want %q", obs2.Source, "ingest:doc")
	}
	if obs2.TrustTag == nil || *obs2.TrustTag != TrustTagIngestDoc {
		t.Fatalf("TrustTag after overwrite = %v, want %q", obs2.TrustTag, TrustTagIngestDoc)
	}
}

// TestGetObservation_NormalizesNullTrustTagToUnverified
// (omnia-provenance-foundation review fix, should-fix #4): classifyTrust only
// ever runs at AddObservation write time. A row that was never written
// through AddObservation — a pre-migration/legacy row, or (as simulated
// here) a raw INSERT bypassing the write path entirely — has a genuinely
// NULL trust_tag in the database. GetObservation must normalize that to
// "unverified" ON READ (spec scenario: "Legacy rows read as unverified"),
// not surface a nil *string or an empty string.
func TestGetObservation_NormalizesNullTrustTagToUnverified(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("prov-null-sess", "prov-null-proj", "/tmp/prov-null"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Bypass AddObservation entirely: a raw INSERT leaves source/trust_tag
	// genuinely NULL, exactly like a pre-migration row would.
	if _, err := s.db.Exec(
		`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"obs-legacy-null-trust", "prov-null-sess", "manual", "legacy row with no trust_tag", "some content", "prov-null-proj", "project",
	); err != nil {
		t.Fatalf("raw insert: %v", err)
	}

	var id int64
	if err := s.db.QueryRow(`SELECT id FROM observations WHERE sync_id = ?`, "obs-legacy-null-trust").Scan(&id); err != nil {
		t.Fatalf("query inserted id: %v", err)
	}

	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if obs.TrustTag == nil {
		t.Fatal("expected TrustTag to be normalized to a non-nil pointer, got nil")
	}
	if *obs.TrustTag != TrustTagUnverified {
		t.Errorf("TrustTag = %q, want %q", *obs.TrustTag, TrustTagUnverified)
	}
	// Source has no documented read-time default (unlike trust_tag) — a
	// genuinely absent source stays nil, an honest signal that no
	// attribution was ever recorded.
	if obs.Source != nil {
		t.Errorf("Source = %v, want nil (no attribution was ever recorded for this row)", obs.Source)
	}
}

// ─── Phase 4: tombstone + hard delete ───────────────────────────────────────

// TestDeleteObservation_HardDelete_WritesTombstone (omnia-provenance-foundation,
// phase 4): a local hard delete must physically purge the observations row
// (and its FTS entry, via the existing obs_fts_delete trigger) AND write
// exactly one deletion_tombstones row for that observation's sync_id — a
// durable proof independent of the (prunable) sync_mutations journal.
func TestDeleteObservation_HardDelete_WritesTombstone(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("tomb-sess", "tomb-proj", "/tmp/tomb"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	id, err := s.AddObservation(AddObservationParams{
		SessionID: "tomb-sess",
		Type:      "manual",
		Title:     "tombstone hard delete test",
		Content:   "content to be purged",
		Project:   "tomb-proj",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	syncID := obs.SyncID

	if err := s.DeleteObservation(id, true); err != nil {
		t.Fatalf("DeleteObservation(hard=true): %v", err)
	}

	// Row must be physically gone.
	if _, err := s.GetObservation(id); err == nil {
		t.Fatal("expected observation row to be purged after hard delete")
	}
	// FTS entry must be gone too (obs_fts_delete trigger).
	var ftsCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM observations_fts WHERE rowid = ?`, id).Scan(&ftsCount); err != nil {
		t.Fatalf("query observations_fts: %v", err)
	}
	if ftsCount != 0 {
		t.Errorf("expected FTS entry purged, found %d rows", ftsCount)
	}

	// Exactly one tombstone row for this sync_id.
	var tombCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM deletion_tombstones WHERE sync_id = ?`, syncID).Scan(&tombCount); err != nil {
		t.Fatalf("query deletion_tombstones: %v", err)
	}
	if tombCount != 1 {
		t.Fatalf("expected exactly 1 tombstone row for sync_id %q, got %d", syncID, tombCount)
	}

	var hard int
	if err := s.db.QueryRow(`SELECT hard FROM deletion_tombstones WHERE sync_id = ?`, syncID).Scan(&hard); err != nil {
		t.Fatalf("query tombstone hard flag: %v", err)
	}
	if hard != 1 {
		t.Errorf("tombstone hard flag = %d, want 1", hard)
	}
}

// TestDeleteObservation_SoftDelete_WritesNoTombstone (omnia-provenance-foundation,
// phase 4): soft delete must remain out of physical-purge scope — no
// tombstone is written, and the row (with deleted_at set) is left intact.
// Regression guard on the existing soft-delete branch.
func TestDeleteObservation_SoftDelete_WritesNoTombstone(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("tomb-sess2", "tomb-proj2", "/tmp/tomb2"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	id, err := s.AddObservation(AddObservationParams{
		SessionID: "tomb-sess2",
		Type:      "manual",
		Title:     "soft delete no tombstone test",
		Content:   "content that stays",
		Project:   "tomb-proj2",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	syncID := obs.SyncID

	if err := s.DeleteObservation(id, false); err != nil {
		t.Fatalf("DeleteObservation(hard=false): %v", err)
	}

	var tombCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM deletion_tombstones WHERE sync_id = ?`, syncID).Scan(&tombCount); err != nil {
		t.Fatalf("query deletion_tombstones: %v", err)
	}
	if tombCount != 0 {
		t.Fatalf("soft delete must not write a tombstone, found %d rows", tombCount)
	}

	// Row still exists with deleted_at set (soft-delete semantics unchanged).
	var deletedAt sql.NullString
	if err := s.db.QueryRow(`SELECT deleted_at FROM observations WHERE id = ?`, id).Scan(&deletedAt); err != nil {
		t.Fatalf("query observations after soft delete: %v", err)
	}
	if !deletedAt.Valid || deletedAt.String == "" {
		t.Error("expected deleted_at to be set after soft delete")
	}
}

// TestDeleteObservationWithActor_PopulatesTombstoneActorAndReason
// (omnia-provenance-foundation review fix, should-fix #5): the
// deletion_tombstones table has had actor/reason columns since the migration
// that created it, but nothing ever populated them. DeleteObservationWithActor
// must record the given actor plus the constant reason "hard_delete" on the
// local (push-side) code path.
func TestDeleteObservationWithActor_PopulatesTombstoneActorAndReason(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("tomb-actor-sess", "tomb-actor-proj", "/tmp/tomb-actor"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	id, syncID := addTestObsSession(t, s, "tomb-actor-sess", "actor/reason tombstone test", "manual", "tomb-actor-proj", "project")

	if err := s.DeleteObservationWithActor(id, true, "http"); err != nil {
		t.Fatalf("DeleteObservationWithActor: %v", err)
	}

	var actor, reason string
	if err := s.db.QueryRow(`SELECT actor, reason FROM deletion_tombstones WHERE sync_id = ?`, syncID).Scan(&actor, &reason); err != nil {
		t.Fatalf("query tombstone actor/reason: %v", err)
	}
	if actor != "http" {
		t.Errorf("tombstone actor = %q, want %q", actor, "http")
	}
	if reason != "hard_delete" {
		t.Errorf("tombstone reason = %q, want %q", reason, "hard_delete")
	}
}

// TestDeleteObservation_PlainCall_LeavesTombstoneActorNull is the regression
// guard for callers that have no actor to report: plain DeleteObservation
// (unchanged signature, used by every pre-existing call site) must keep
// storing a NULL actor — should-fix #5 is additive, not a behavior change
// for callers that don't know an actor.
func TestDeleteObservation_PlainCall_LeavesTombstoneActorNull(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("tomb-noactor-sess", "tomb-noactor-proj", "/tmp/tomb-noactor"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	id, syncID := addTestObsSession(t, s, "tomb-noactor-sess", "no actor tombstone test", "manual", "tomb-noactor-proj", "project")

	if err := s.DeleteObservation(id, true); err != nil {
		t.Fatalf("DeleteObservation: %v", err)
	}

	var actor sql.NullString
	var reason string
	if err := s.db.QueryRow(`SELECT actor, reason FROM deletion_tombstones WHERE sync_id = ?`, syncID).Scan(&actor, &reason); err != nil {
		t.Fatalf("query tombstone actor/reason: %v", err)
	}
	if actor.Valid {
		t.Errorf("tombstone actor = %q, want NULL for a plain DeleteObservation call", actor.String)
	}
	if reason != "hard_delete" {
		t.Errorf("tombstone reason = %q, want %q", reason, "hard_delete")
	}
}

// TestApplyObservationDeleteTx_PopulatesTombstoneActorAndReason
// (omnia-provenance-foundation review fix, should-fix #5): the pull-side
// tombstone insert must also record its constant actor/reason
// ("cloud_sync"/"cloud_pull_delete") — every row written on this path was
// applied by the cloud-pull code path, never by a locally-identified actor.
func TestApplyObservationDeleteTx_PopulatesTombstoneActorAndReason(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("pull-actor-sess", "pull-actor-proj", "/tmp/pull-actor"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	_, syncID := addTestObsSession(t, s, "pull-actor-sess", "pulled tombstone actor test", "manual", "pull-actor-proj", "project")

	payload := syncObservationPayload{
		SyncID:     syncID,
		SessionID:  "pull-actor-sess",
		Project:    strPtr("pull-actor-proj"),
		Deleted:    true,
		HardDelete: true,
	}
	if err := s.withTx(func(tx *sql.Tx) error {
		return s.applyObservationDeleteTx(tx, payload)
	}); err != nil {
		t.Fatalf("applyObservationDeleteTx: %v", err)
	}

	var actor, reason string
	if err := s.db.QueryRow(`SELECT actor, reason FROM deletion_tombstones WHERE sync_id = ?`, syncID).Scan(&actor, &reason); err != nil {
		t.Fatalf("query tombstone actor/reason: %v", err)
	}
	if actor != "cloud_sync" {
		t.Errorf("tombstone actor = %q, want %q", actor, "cloud_sync")
	}
	if reason != "cloud_pull_delete" {
		t.Errorf("tombstone reason = %q, want %q", reason, "cloud_pull_delete")
	}
}

// ─── Phase 6: sync pull tombstone replication ───────────────────────────────

// TestApplyObservationDeleteTx_HardDelete_WritesTombstone
// (omnia-provenance-foundation, phase 6): applying a pulled
// SyncOpDelete{HardDelete:true} mutation via applyObservationDeleteTx must
// purge the local row AND write its OWN deletion_tombstones row — the proof
// replicates on the pull path independent of whatever happened on the
// pushing side.
func TestApplyObservationDeleteTx_HardDelete_WritesTombstone(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("pull-sess", "pull-proj", "/tmp/pull"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	_, syncID := addTestObsSession(t, s, "pull-sess", "pulled hard delete test", "manual", "pull-proj", "project")

	payload := syncObservationPayload{
		SyncID:     syncID,
		SessionID:  "pull-sess",
		Project:    strPtr("pull-proj"),
		Deleted:    true,
		HardDelete: true,
	}
	if err := s.withTx(func(tx *sql.Tx) error {
		return s.applyObservationDeleteTx(tx, payload)
	}); err != nil {
		t.Fatalf("applyObservationDeleteTx: %v", err)
	}

	if _, err := s.GetObservationBySyncID(syncID); err == nil {
		t.Fatal("expected pulled hard delete to purge the local row")
	}

	var tombCount, hard int
	if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(hard), 0) FROM deletion_tombstones WHERE sync_id = ?`, syncID).Scan(&tombCount, &hard); err != nil {
		t.Fatalf("query deletion_tombstones: %v", err)
	}
	if tombCount != 1 {
		t.Fatalf("expected exactly 1 tombstone row for sync_id %q, got %d", syncID, tombCount)
	}
	if hard != 1 {
		t.Errorf("tombstone hard flag = %d, want 1", hard)
	}
}

// TestApplyObservationDeleteTx_SoftDelete_WritesNoTombstone
// (omnia-provenance-foundation, phase 6): a pulled soft-delete mutation must
// NOT write a tombstone — physical-purge proof is hard-delete only, on
// either path.
func TestApplyObservationDeleteTx_SoftDelete_WritesNoTombstone(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("pull-sess2", "pull-proj2", "/tmp/pull2"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	_, syncID := addTestObsSession(t, s, "pull-sess2", "pulled soft delete test", "manual", "pull-proj2", "project")

	payload := syncObservationPayload{
		SyncID:    syncID,
		SessionID: "pull-sess2",
		Project:   strPtr("pull-proj2"),
		Deleted:   true,
	}
	if err := s.withTx(func(tx *sql.Tx) error {
		return s.applyObservationDeleteTx(tx, payload)
	}); err != nil {
		t.Fatalf("applyObservationDeleteTx: %v", err)
	}

	var tombCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM deletion_tombstones WHERE sync_id = ?`, syncID).Scan(&tombCount); err != nil {
		t.Fatalf("query deletion_tombstones: %v", err)
	}
	if tombCount != 0 {
		t.Fatalf("pulled soft delete must not write a tombstone, found %d rows", tombCount)
	}
}

// TestDeletionTombstone_PushPullSymmetry (omnia-provenance-foundation, phase
// 6.3): the SAME tombstone-row assertion holds for a LOCAL hard delete
// (DeleteObservation, the push side) and a PULLED hard delete
// (applyObservationDeleteTx, the pull side) — proving the proof replicates
// identically regardless of which side of sync initiated the delete.
func TestDeletionTombstone_PushPullSymmetry(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("sym-sess", "sym-proj", "/tmp/sym"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Push side: local DeleteObservation(hard=true).
	pushID, pushSyncID := addTestObsSession(t, s, "sym-sess", "push side hard delete", "manual", "sym-proj", "project")
	if err := s.DeleteObservation(pushID, true); err != nil {
		t.Fatalf("DeleteObservation (push side): %v", err)
	}

	// Pull side: applyObservationDeleteTx with HardDelete=true.
	_, pullSyncID := addTestObsSession(t, s, "sym-sess", "pull side hard delete", "manual", "sym-proj", "project")
	if err := s.withTx(func(tx *sql.Tx) error {
		return s.applyObservationDeleteTx(tx, syncObservationPayload{
			SyncID:     pullSyncID,
			SessionID:  "sym-sess",
			Project:    strPtr("sym-proj"),
			Deleted:    true,
			HardDelete: true,
		})
	}); err != nil {
		t.Fatalf("applyObservationDeleteTx (pull side): %v", err)
	}

	for _, syncID := range []string{pushSyncID, pullSyncID} {
		var tombCount, hard int
		if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(hard), 0) FROM deletion_tombstones WHERE sync_id = ?`, syncID).Scan(&tombCount, &hard); err != nil {
			t.Fatalf("query deletion_tombstones for %q: %v", syncID, err)
		}
		if tombCount != 1 {
			t.Errorf("sync_id %q: expected exactly 1 tombstone row, got %d", syncID, tombCount)
		}
		if hard != 1 {
			t.Errorf("sync_id %q: tombstone hard flag = %d, want 1", syncID, hard)
		}
	}
}

func strPtr(s string) *string { return &s }

// ─── Phase 7: store permissions hardening ───────────────────────────────────

// TestNew_CreatesStoreWithLockedDownPermissions (omnia-provenance-foundation,
// phase 7): a fresh store's data directory and database file must be
// owner-only — no group or world read/write bits. This only governs a
// FRESH install: an already-existing (looser) directory from before this
// slice is left as-is and flagged by internal/diagnostic's StoreExposureCheck
// instead (upgrade path, not silent re-permissioning of an existing store).
func TestNew_CreatesStoreWithLockedDownPermissions(t *testing.T) {
	cfg := mustDefaultConfig(t)
	// A NOT-yet-existing subdirectory — MkdirAll must create it fresh so this
	// test actually exercises the requested mode, not an inherited one.
	cfg.DataDir = filepath.Join(t.TempDir(), "fresh-store")
	cfg.DedupeWindow = time.Hour

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	dirInfo, err := os.Stat(cfg.DataDir)
	if err != nil {
		t.Fatalf("stat data dir: %v", err)
	}
	if dirInfo.Mode().Perm()&0o077 != 0 {
		t.Errorf("data dir perm = %o, want no group/world bits", dirInfo.Mode().Perm())
	}

	dbPath := datadir.DBPath(cfg.DataDir)
	fileInfo, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat db file: %v", err)
	}
	if fileInfo.Mode().Perm()&0o077 != 0 {
		t.Errorf("db file perm = %o, want no group/world bits", fileInfo.Mode().Perm())
	}
}

// TestClassifyTrust (omnia-provenance-foundation, phase 2): classifyTrust is a
// pure function that maps a write-time `source` argument to its trust class.
// This is ATTRIBUTION, not authentication — classifyTrust never rejects a
// save; unrecognized or absent source always degrades to "unverified".
func TestClassifyTrust(t *testing.T) {
	cases := []struct {
		name   string
		source string
		want   string
	}{
		{name: "user", source: "user", want: TrustTagUser},
		{name: "agent", source: "agent", want: TrustTagAgent},
		{name: "ingest tool", source: "ingest:tool", want: TrustTagIngestTool},
		{name: "ingest web", source: "ingest:web", want: TrustTagIngestWeb},
		{name: "ingest doc", source: "ingest:doc", want: TrustTagIngestDoc},
		{name: "empty defaults unverified", source: "", want: TrustTagUnverified},
		{name: "unrecognized defaults unverified", source: "ingest:carrier-pigeon", want: TrustTagUnverified},
		{name: "whitespace-only defaults unverified", source: "   ", want: TrustTagUnverified},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyTrust(tc.source)
			if got != tc.want {
				t.Errorf("classifyTrust(%q) = %q, want %q", tc.source, got, tc.want)
			}
		})
	}
}
