package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func newTimeTravelStore(t *testing.T, enabled bool, cap int) *Store {
	t.Helper()
	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()
	cfg.TimeTravelEnabled = enabled
	cfg.HistoryRevisionCap = cap
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.CreateSession("s1", "omnia", "/tmp/omnia"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return s
}

func addTimeTravelObservation(t *testing.T, s *Store, title, topic string) int64 {
	t.Helper()
	id, err := s.AddObservation(AddObservationParams{
		SessionID: "s1", Type: "decision", Title: title, Content: title + " content",
		Project: "omnia", Scope: "project", TopicKey: topic,
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	return id
}

func revisionCount(t *testing.T, s *Store, id int64) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`
		SELECT COUNT(*) FROM observation_revisions r
		JOIN observations o ON o.sync_id = r.obs_sync_id WHERE o.id = ?`, id).Scan(&count); err != nil {
		t.Fatalf("count revisions: %v", err)
	}
	return count
}

func TestTimeTravelCapturesOnlyEnabledBeforeImages(t *testing.T) {
	disabled := newTimeTravelStore(t, false, 0)
	disabledID := addTimeTravelObservation(t, disabled, "disabled old", "")
	disabledTitle := "disabled new"
	if _, err := disabled.UpdateObservation(disabledID, UpdateObservationParams{Title: &disabledTitle}); err != nil {
		t.Fatal(err)
	}
	if err := disabled.DeleteObservation(disabledID, false); err != nil {
		t.Fatal(err)
	}
	if got := revisionCount(t, disabled, disabledID); got != 0 {
		t.Fatalf("disabled capture count = %d, want 0", got)
	}

	enabled := newTimeTravelStore(t, true, 0)
	id := addTimeTravelObservation(t, enabled, "old title", "history/topic")
	if got := revisionCount(t, enabled, id); got != 0 {
		t.Fatalf("insert capture count = %d, want 0", got)
	}
	_, err := enabled.SaveObservation(AddObservationParams{
		SessionID: "s1", Type: "decision", Title: "new title", Content: "new content",
		Project: "omnia", Scope: "project", TopicKey: "history/topic",
	})
	if err != nil {
		t.Fatalf("revision SaveObservation: %v", err)
	}
	var op, snapshot string
	if err := enabled.db.QueryRow(`SELECT op, snapshot FROM observation_revisions LIMIT 1`).Scan(&op, &snapshot); err != nil {
		t.Fatalf("read revision: %v", err)
	}
	var before Observation
	if err := json.Unmarshal([]byte(snapshot), &before); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if op != "update" || before.Title != "old title" || before.SyncID == "" {
		t.Fatalf("revision = op %q snapshot %+v, want update before-image", op, before)
	}
}

func TestTimeTravelRetention(t *testing.T) {
	for _, tt := range []struct {
		name, title string
		cap, want   int
	}{
		{name: "unlimited", title: "unlimited", cap: 0, want: 3},
		{name: "capped", title: "capped", cap: 2, want: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newTimeTravelStore(t, true, tt.cap)
			id := addTimeTravelObservation(t, s, tt.title, "")
			for _, title := range []string{"edit one", "edit two", "edit three"} {
				if _, err := s.UpdateObservation(id, UpdateObservationParams{Title: &title}); err != nil {
					t.Fatalf("UpdateObservation: %v", err)
				}
			}
			if got := revisionCount(t, s, id); got != tt.want {
				t.Fatalf("revision count = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestTimeTravelSoftDeleteAndHardDeleteProof(t *testing.T) {
	s := newTimeTravelStore(t, true, 0)
	softID := addTimeTravelObservation(t, s, "soft before", "")
	if err := s.DeleteObservation(softID, false); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	var op, snapshot string
	if err := s.db.QueryRow(`SELECT op, snapshot FROM observation_revisions LIMIT 1`).Scan(&op, &snapshot); err != nil {
		t.Fatal(err)
	}
	if op != "soft_delete" || !strings.Contains(snapshot, `"title":"soft before"`) {
		t.Fatalf("soft-delete revision = %q %s", op, snapshot)
	}

	hardID := addTimeTravelObservation(t, s, "hard before", "")
	newTitle := "hard edited"
	if _, err := s.UpdateObservation(hardID, UpdateObservationParams{Title: &newTitle}); err != nil {
		t.Fatal(err)
	}
	hard, err := s.GetObservation(hardID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteObservationWithActor(hardID, true, "test"); err != nil {
		t.Fatalf("hard delete: %v", err)
	}
	var revisions, tombstones int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM observation_revisions WHERE obs_sync_id = ?`, hard.SyncID).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM deletion_tombstones WHERE sync_id = ? AND hard = 1 AND actor = 'test' AND content_hash != ''`, hard.SyncID).Scan(&tombstones); err != nil {
		t.Fatal(err)
	}
	if revisions != 0 || tombstones != 1 {
		t.Fatalf("hard-delete proof: revisions=%d tombstones=%d, want 0/1", revisions, tombstones)
	}
}

func TestTimeTravelCaptureFailureRollsBackMutation(t *testing.T) {
	s := newTimeTravelStore(t, true, 0)
	id := addTimeTravelObservation(t, s, "before rollback", "")
	originalExec := s.hooks.exec
	s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "INSERT INTO observation_revisions") {
			return nil, errors.New("capture failed")
		}
		return originalExec(db, query, args...)
	}
	title := "must not persist"
	if _, err := s.UpdateObservation(id, UpdateObservationParams{Title: &title}); err == nil {
		t.Fatal("UpdateObservation unexpectedly succeeded")
	}
	got, err := s.GetObservation(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "before rollback" {
		t.Fatalf("title = %q after failed capture, want original", got.Title)
	}
}

func TestTimeTravelRapidUpdatesShareStableBoundaries(t *testing.T) {
	first := nextRevisionTimestamp("")
	firstTime, firstErr := parseObservationTime(first)
	secondTime, secondErr := parseObservationTime(nextRevisionTimestamp(first))
	if firstErr != nil || secondErr != nil || !secondTime.After(firstTime) {
		t.Fatalf("helper output did not parse and advance: first=%q errors=%v/%v", first, firstErr, secondErr)
	}
	s := newTimeTravelStore(t, true, 0)
	id := addTimeTravelObservation(t, s, "rapid original", "")
	var previousBoundary string
	for _, title := range []string{"rapid one", "rapid two"} {
		live, err := s.UpdateObservation(id, UpdateObservationParams{Title: &title})
		if err != nil {
			t.Fatal(err)
		}
		var validTo string
		if err := s.db.QueryRow(`SELECT valid_to FROM observation_revisions ORDER BY id DESC LIMIT 1`).Scan(&validTo); err != nil {
			t.Fatal(err)
		}
		if validTo != live.UpdatedAt {
			t.Fatalf("revision valid_to %q != live updated_at %q", validTo, live.UpdatedAt)
		}
		if previousBoundary != "" && validTo <= previousBoundary {
			t.Fatalf("unstable interval: previous=%q valid_to=%q", previousBoundary, validTo)
		}
		previousBoundary = validTo
	}
}

func TestTimeTravelDeleteProjectPurgesHistoryAndWritesProofsAtomically(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		s := newTimeTravelStore(t, true, 0)
		var proofs []struct {
			syncID, contentHash string
		}
		for _, title := range []string{"bulk one", "bulk two"} {
			id := addTimeTravelObservation(t, s, title, "")
			edited := title + " edited"
			if _, err := s.UpdateObservation(id, UpdateObservationParams{Title: &edited}); err != nil {
				t.Fatal(err)
			}
			obs, err := s.GetObservation(id)
			if err != nil {
				t.Fatal(err)
			}
			if title == "bulk one" {
				if _, err := s.db.Exec(`UPDATE observations SET normalized_hash = NULL WHERE id = ?`, id); err != nil {
					t.Fatal(err)
				}
			}
			proofs = append(proofs, struct {
				syncID, contentHash string
			}{syncID: obs.SyncID, contentHash: hashNormalized(obs.Content)})
		}

		if _, err := s.DeleteProject("omnia", true); err != nil {
			t.Fatalf("DeleteProject: %v", err)
		}
		for _, proof := range proofs {
			var revisions int
			var contentHash sql.NullString
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM observation_revisions WHERE obs_sync_id = ?`, proof.syncID).Scan(&revisions); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRow(`
				SELECT content_hash FROM deletion_tombstones
				WHERE sync_id = ? AND hard = 1 AND reason = 'hard_delete'`,
				proof.syncID,
			).Scan(&contentHash); err != nil {
				t.Fatal(err)
			}
			if revisions != 0 || !contentHash.Valid || contentHash.String != proof.contentHash {
				t.Fatalf("%s: revisions=%d content_hash=%+v, want 0/%q", proof.syncID, revisions, contentHash, proof.contentHash)
			}
		}
	})

	t.Run("rollback", func(t *testing.T) {
		s := newTimeTravelStore(t, true, 0)
		id := addTimeTravelObservation(t, s, "bulk rollback", "")
		edited := "bulk rollback edited"
		if _, err := s.UpdateObservation(id, UpdateObservationParams{Title: &edited}); err != nil {
			t.Fatal(err)
		}
		obs, err := s.GetObservation(id)
		if err != nil {
			t.Fatal(err)
		}
		originalExec := s.hooks.exec
		s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
			if strings.Contains(query, "INSERT INTO deletion_tombstones") {
				return nil, errors.New("proof failed")
			}
			return originalExec(db, query, args...)
		}

		if _, err := s.DeleteProject("omnia", true); err == nil {
			t.Fatal("DeleteProject unexpectedly succeeded")
		}
		if _, err := s.GetObservation(id); err != nil {
			t.Fatalf("observation was not rolled back: %v", err)
		}
		if got := revisionCount(t, s, id); got != 1 {
			t.Fatalf("revision count after rollback = %d, want 1", got)
		}
		var tombstones int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM deletion_tombstones WHERE sync_id = ?`, obs.SyncID).Scan(&tombstones); err != nil {
			t.Fatal(err)
		}
		if tombstones != 0 {
			t.Fatalf("tombstones after rollback = %d, want 0", tombstones)
		}
	})
}

func TestTimeTravelDeleteProjectSoftDeletePreservesEditHistory(t *testing.T) {
	s := newTimeTravelStore(t, true, 0)
	id := addTimeTravelObservation(t, s, "soft-project original", "")
	newTitle := "soft-project edited"
	edited, err := s.UpdateObservation(id, UpdateObservationParams{Title: &newTitle})
	if err != nil {
		t.Fatalf("UpdateObservation: %v", err)
	}

	if _, err := s.DeleteProject("omnia", false); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}

	got, err := s.StateAsOf(id, edited.UpdatedAt)
	if err != nil {
		t.Fatalf("StateAsOf(%s) after project soft-delete: %v", edited.UpdatedAt, err)
	}
	if got.Title != newTitle {
		t.Fatalf("StateAsOf title = %q, want %q", got.Title, newTitle)
	}
}

func TestTimeTravelPulledUpdateAndSoftDeleteCaptureBeforeImages(t *testing.T) {
	s := newTimeTravelStore(t, true, 0)
	id := addTimeTravelObservation(t, s, "pulled original", "")
	original, err := s.GetObservation(id)
	if err != nil {
		t.Fatal(err)
	}
	updateAt := time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano)
	update := SyncMutation{
		Seq: 1, Entity: SyncEntityObservation, EntityKey: original.SyncID, Op: SyncOpUpsert,
		Payload: `{"sync_id":"` + original.SyncID + `","session_id":"s1","type":"decision","title":"pulled update","content":"updated remotely","project":"omnia","scope":"project","updated_at":"` + updateAt + `"}`,
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, update); err != nil {
		t.Fatalf("pulled update: %v", err)
	}
	updated, err := s.GetObservationBySyncID(original.SyncID)
	if err != nil {
		t.Fatal(err)
	}
	var updateOp, updateTo, updateSnapshot string
	if err := s.db.QueryRow(`
		SELECT op, valid_to, snapshot FROM observation_revisions
		WHERE obs_sync_id = ? ORDER BY id DESC LIMIT 1`, original.SyncID,
	).Scan(&updateOp, &updateTo, &updateSnapshot); err != nil {
		t.Fatal(err)
	}
	if updateOp != revisionOpUpdate || updateTo != updated.UpdatedAt || !strings.Contains(updateSnapshot, `"title":"pulled original"`) {
		t.Fatalf("update revision op=%q valid_to=%q live=%q snapshot=%s", updateOp, updateTo, updated.UpdatedAt, updateSnapshot)
	}

	deleteAt := time.Now().UTC().Add(2 * time.Minute).Format(time.RFC3339Nano)
	softDelete := SyncMutation{
		Seq: 2, Entity: SyncEntityObservation, EntityKey: original.SyncID, Op: SyncOpDelete,
		Payload: `{"sync_id":"` + original.SyncID + `","deleted":true,"deleted_at":"` + deleteAt + `"}`,
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, softDelete); err != nil {
		t.Fatalf("pulled soft delete: %v", err)
	}
	deleted, err := s.getObservationBySyncIDTxForTest(original.SyncID)
	if err != nil {
		t.Fatal(err)
	}
	var deleteOp, deleteTo, deleteSnapshot string
	if err := s.db.QueryRow(`
		SELECT op, valid_to, snapshot FROM observation_revisions
		WHERE obs_sync_id = ? ORDER BY id DESC LIMIT 1`, original.SyncID,
	).Scan(&deleteOp, &deleteTo, &deleteSnapshot); err != nil {
		t.Fatal(err)
	}
	if deleteOp != revisionOpSoftDelete || deleteTo != deleted.UpdatedAt || deleted.DeletedAt == nil || deleteTo != *deleted.DeletedAt || !strings.Contains(deleteSnapshot, `"title":"pulled update"`) {
		t.Fatalf("delete revision op=%q valid_to=%q live=%+v snapshot=%s", deleteOp, deleteTo, deleted, deleteSnapshot)
	}
}

func TestTimeTravelPulledHardDeletePurgesHistory(t *testing.T) {
	s := newTimeTravelStore(t, true, 0)
	id := addTimeTravelObservation(t, s, "pulled hard delete", "")
	edited := "pulled hard delete edited"
	if _, err := s.UpdateObservation(id, UpdateObservationParams{Title: &edited}); err != nil {
		t.Fatal(err)
	}
	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatal(err)
	}
	mutation := SyncMutation{
		Seq: 1, Entity: SyncEntityObservation, EntityKey: obs.SyncID, Op: SyncOpDelete,
		Payload: `{"sync_id":"` + obs.SyncID + `","deleted":true,"hard_delete":true}`,
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, mutation); err != nil {
		t.Fatal(err)
	}
	var revisions, tombstones int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM observation_revisions WHERE obs_sync_id = ?`, obs.SyncID).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM deletion_tombstones WHERE sync_id = ? AND hard = 1`, obs.SyncID).Scan(&tombstones); err != nil {
		t.Fatal(err)
	}
	if revisions != 0 || tombstones != 1 {
		t.Fatalf("pulled hard delete revisions=%d tombstones=%d, want 0/1", revisions, tombstones)
	}
}

func (s *Store) getObservationBySyncIDTxForTest(syncID string) (*Observation, error) {
	row := s.db.QueryRow(`SELECT `+observationSelectColumns+` FROM observations WHERE sync_id = ? ORDER BY id DESC LIMIT 1`, syncID)
	var obs Observation
	if err := scanObservationRow(row, &obs); err != nil {
		return nil, err
	}
	return &obs, nil
}

func TestTimeTravelPulledCaptureRespectsDisabledRetentionAndRollback(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		s := newTimeTravelStore(t, false, 0)
		id := addTimeTravelObservation(t, s, "disabled pull", "")
		obs, _ := s.GetObservation(id)
		mutation := SyncMutation{Seq: 1, Entity: SyncEntityObservation, EntityKey: obs.SyncID, Op: SyncOpUpsert,
			Payload: `{"sync_id":"` + obs.SyncID + `","session_id":"s1","type":"decision","title":"changed","content":"changed","project":"omnia","scope":"project"}`}
		if err := s.ApplyPulledMutation(DefaultSyncTargetKey, mutation); err != nil {
			t.Fatal(err)
		}
		live, err := s.GetObservation(id)
		if err != nil {
			t.Fatal(err)
		}
		if live.UpdatedAt != obs.UpdatedAt {
			t.Fatalf("disabled missing updated_at changed from %q to %q", obs.UpdatedAt, live.UpdatedAt)
		}
		equalTimestamp := SyncMutation{Seq: 2, Entity: SyncEntityObservation, EntityKey: obs.SyncID, Op: SyncOpUpsert,
			Payload: `{"sync_id":"` + obs.SyncID + `","session_id":"s1","type":"decision","title":"changed again","content":"changed","project":"omnia","scope":"project","updated_at":"` + obs.UpdatedAt + `"}`}
		if err := s.ApplyPulledMutation(DefaultSyncTargetKey, equalTimestamp); err != nil {
			t.Fatal(err)
		}
		live, err = s.GetObservation(id)
		if err != nil {
			t.Fatal(err)
		}
		if live.UpdatedAt != obs.UpdatedAt {
			t.Fatalf("disabled equal updated_at changed from %q to %q", obs.UpdatedAt, live.UpdatedAt)
		}

		deleteAt := "2024-01-02 03:04:05"
		beforeDelete := Now()
		softDelete := SyncMutation{Seq: 3, Entity: SyncEntityObservation, EntityKey: obs.SyncID, Op: SyncOpDelete,
			Payload: `{"sync_id":"` + obs.SyncID + `","deleted":true,"deleted_at":"` + deleteAt + `"}`}
		if err := s.ApplyPulledMutation(DefaultSyncTargetKey, softDelete); err != nil {
			t.Fatal(err)
		}
		afterDelete := Now()
		deleted, err := s.getObservationBySyncIDTxForTest(obs.SyncID)
		if err != nil {
			t.Fatal(err)
		}
		if deleted.DeletedAt == nil || *deleted.DeletedAt != deleteAt || deleted.UpdatedAt < beforeDelete || deleted.UpdatedAt > afterDelete {
			t.Fatalf("disabled soft-delete timestamps deleted_at=%v updated_at=%q, want %q and [%q,%q]", deleted.DeletedAt, deleted.UpdatedAt, deleteAt, beforeDelete, afterDelete)
		}
		if got := revisionCount(t, s, id); got != 0 {
			t.Fatalf("disabled pulled revisions = %d, want 0", got)
		}
	})

	t.Run("retention", func(t *testing.T) {
		s := newTimeTravelStore(t, true, 1)
		id := addTimeTravelObservation(t, s, "retained original", "")
		obs, _ := s.GetObservation(id)
		for seq, title := range []string{"retained one", "retained two"} {
			mutation := SyncMutation{Seq: int64(seq + 1), Entity: SyncEntityObservation, EntityKey: obs.SyncID, Op: SyncOpUpsert,
				Payload: `{"sync_id":"` + obs.SyncID + `","session_id":"s1","type":"decision","title":"` + title + `","content":"changed","project":"omnia","scope":"project"}`}
			if err := s.ApplyPulledMutation(DefaultSyncTargetKey, mutation); err != nil {
				t.Fatal(err)
			}
		}
		if got := revisionCount(t, s, id); got != 1 {
			t.Fatalf("retained pulled revisions = %d, want 1", got)
		}
	})

	t.Run("rollback", func(t *testing.T) {
		s := newTimeTravelStore(t, true, 0)
		id := addTimeTravelObservation(t, s, "pull rollback", "")
		obs, _ := s.GetObservation(id)
		originalExec := s.hooks.exec
		s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
			if strings.Contains(query, "INSERT INTO observation_revisions") {
				return nil, errors.New("capture failed")
			}
			return originalExec(db, query, args...)
		}
		mutation := SyncMutation{Seq: 1, Entity: SyncEntityObservation, EntityKey: obs.SyncID, Op: SyncOpUpsert,
			Payload: `{"sync_id":"` + obs.SyncID + `","session_id":"s1","type":"decision","title":"must rollback","content":"changed","project":"omnia","scope":"project"}`}
		if err := s.ApplyPulledMutation(DefaultSyncTargetKey, mutation); err == nil {
			t.Fatal("pulled update unexpectedly succeeded")
		}
		got, err := s.GetObservation(id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Title != "pull rollback" {
			t.Fatalf("title after rollback = %q", got.Title)
		}
		state, err := s.GetSyncState(DefaultSyncTargetKey)
		if err != nil {
			t.Fatal(err)
		}
		if state.LastPulledSeq != 0 {
			t.Fatalf("pull cursor after rollback = %d, want 0", state.LastPulledSeq)
		}
	})
}

func TestStateAsOfRecordedTimeBoundariesAndVisibility(t *testing.T) {
	s := newTimeTravelStore(t, true, 0)
	id := addTimeTravelObservation(t, s, "original", "")
	original, _ := s.GetObservation(id)
	editedTitle, editedContent, editedType, editedProject, editedScope := "edited", "current-only token", "pattern", "other", "personal"
	edited, _ := s.UpdateObservation(id, UpdateObservationParams{Title: &editedTitle, Content: &editedContent, Type: &editedType, Project: &editedProject, Scope: &editedScope})
	for _, tt := range []struct {
		name, at, want string
	}{
		{"before edit", original.UpdatedAt, "original"},
		{"exact edit boundary", edited.UpdatedAt, "edited"},
		{"future resolves live", time.Now().Add(time.Hour).Format(time.RFC3339Nano), "edited"},
		{"within clock-skew tolerance resolves live", time.Now().Add(2 * time.Second).Format(time.RFC3339Nano), "edited"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.StateAsOf(id, tt.at)
			if err != nil || got.Title != tt.want {
				t.Fatalf("StateAsOf(%q) = %+v, %v; want %q", tt.at, got, err, tt.want)
			}
		})
	}
	if err := s.DeleteObservation(id, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StateAsOf(id, edited.UpdatedAt); err != nil {
		t.Fatalf("pre-delete state unavailable: %v", err)
	}
	deleted, _ := s.getObservationIncludingDeleted(id)
	if _, err := s.StateAsOf(id, *deleted.DeletedAt); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("at delete boundary error = %v, want not visible", err)
	}
	disabled := newTimeTravelStore(t, false, 0)
	disabledID := addTimeTravelObservation(t, disabled, "disabled old", "")
	old, _ := disabled.GetObservation(disabledID)
	current := "disabled current"
	_, _ = disabled.UpdateObservation(disabledID, UpdateObservationParams{Title: &current})
	got, err := disabled.StateAsOf(disabledID, old.UpdatedAt)
	if err != nil || got.Title != current {
		t.Fatalf("disabled as-of must be ignored: %+v, %v", got, err)
	}
}

// TestAsOfIsFutureHonorsClockSkewTolerance is the direct unit test for the
// shared future-detection primitive behind every --as-of comparison
// (NormalizeAsOf, StateAsOf, SearchAsOf, FormatContextAsOf). Without a small
// tolerance band, a server whose wall clock runs even slightly behind true
// time would classify a genuinely-past --as-of request as "future" (because
// the request's timestamp, produced by a correctly-synced client, ends up
// numerically ahead of this server's lagging time.Now()) and silently
// resolve it to live/current data instead of the requested recorded-time
// state. asOfClockSkewTolerance absorbs that class of skew: only timestamps
// clearly beyond the tolerance band are treated as "the future".
func TestAsOfIsFutureHonorsClockSkewTolerance(t *testing.T) {
	now := time.Now().UTC()
	for _, tt := range []struct {
		name string
		at   time.Time
		want bool
	}{
		{"a couple seconds in the past is never future", now.Add(-2 * time.Second), false},
		{"exactly now is not future", now, false},
		{"within tolerance ahead of now (simulated clock skew) is not future", now.Add(2 * time.Second), false},
		{"at the tolerance boundary is not yet future", now.Add(asOfClockSkewTolerance), false},
		{"clearly beyond tolerance is future", now.Add(asOfClockSkewTolerance + time.Second), true},
		{"far future is still future", now.Add(time.Hour), true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := asOfIsFuture(tt.at); got != tt.want {
				t.Fatalf("asOfIsFuture(now%+v) = %v, want %v", tt.at.Sub(now), got, tt.want)
			}
		})
	}
}

// TestNormalizeAsOfClockSkewTolerance reproduces the --as-of CLI-facing
// symptom directly: a timestamp that is only a couple of seconds ahead of
// "now" (simulating minor clock skew between the machine that produced the
// timestamp and this server) must NOT be misclassified as a future request
// and normalized away to the live sentinel. A timestamp clearly and far in
// the future must still normalize to the live sentinel exactly as before.
func TestNormalizeAsOfClockSkewTolerance(t *testing.T) {
	nearNow := time.Now().UTC().Add(2 * time.Second).Format(time.RFC3339Nano)
	got, err := NormalizeAsOf(nearNow)
	if err != nil {
		t.Fatalf("NormalizeAsOf(%q) unexpected error: %v", nearNow, err)
	}
	if got != nearNow {
		t.Fatalf("NormalizeAsOf(%q) = %q, want unchanged (within clock-skew tolerance)", nearNow, got)
	}

	farFuture := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	got, err = NormalizeAsOf(farFuture)
	if err != nil {
		t.Fatalf("NormalizeAsOf(%q) unexpected error: %v", farFuture, err)
	}
	if got != "" {
		t.Fatalf("NormalizeAsOf(%q) = %q, want empty (live sentinel) for far-future timestamp", farFuture, got)
	}
}

func TestStateAsOfRetentionAndHardDelete(t *testing.T) {
	s := newTimeTravelStore(t, true, 1)
	id := addTimeTravelObservation(t, s, "first", "")
	first, _ := s.GetObservation(id)
	for _, content := range []string{"middle", "current searchable"} {
		if _, err := s.UpdateObservation(id, UpdateObservationParams{Content: &content}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.StateAsOf(id, first.UpdatedAt); err == nil || !strings.Contains(err.Error(), "history unavailable before") {
		t.Fatalf("retained-history error = %v", err)
	}
	if err := s.DeleteObservation(id, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StateAsOf(id, time.Now().Add(-time.Hour).Format(time.RFC3339Nano)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("hard-deleted state resurrected: %v", err)
	}
}

func TestStateAsOfRecordingBoundaryAndUnmodifiedLegacyRows(t *testing.T) {
	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()
	legacy, _ := New(cfg)
	_ = legacy.CreateSession("s1", "omnia", "/tmp/omnia")
	editedID := addTimeTravelObservation(t, legacy, "legacy edited", "")
	stableID := addTimeTravelObservation(t, legacy, "legacy stable", "")
	stable, _ := legacy.GetObservation(stableID)
	_ = legacy.Close()

	cfg.TimeTravelEnabled = true
	s, _ := New(cfg)
	defer s.Close()
	startedAt := nextRevisionTimestamp("")
	title := "edited after enable"
	_, _ = s.UpdateObservation(editedID, UpdateObservationParams{Title: &title})
	if _, err := s.StateAsOf(editedID, stable.UpdatedAt); err == nil || !strings.Contains(err.Error(), "history unavailable before") {
		t.Fatalf("pre-enable edited history error = %v", err)
	}
	unrelatedID := addTimeTravelObservation(t, s, "unrelated", "")
	changed := "unrelated changed"
	_, _ = s.UpdateObservation(unrelatedID, UpdateObservationParams{Title: &changed})
	got, err := s.StateAsOf(stableID, stable.UpdatedAt)
	if err != nil || got.Title != "legacy stable" {
		t.Fatalf("unmodified legacy state rejected by unrelated history: %+v, %v (enabled %s)", got, err, startedAt)
	}
}

func TestSearchAsOfRecordedFiltersRetentionAndMixedCaseProject(t *testing.T) {
	s := newTimeTravelStore(t, true, 0)
	id := addTimeTravelObservation(t, s, "original", "")
	if _, err := s.db.Exec(`UPDATE observations SET project = 'OmNiA' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	same := "original content"
	original, _ := s.UpdateObservation(id, UpdateObservationParams{Content: &same})
	typ, project, scope, content := "pattern", "other", "personal", "current searchable"
	if _, err := s.UpdateObservation(id, UpdateObservationParams{
		Type: &typ, Project: &project, Scope: &scope, Content: &content,
	}); err != nil {
		t.Fatal(err)
	}
	hits, err := s.SearchAsOf("searchable", SearchOptions{
		Type: "decision", Project: "OMNIA", Scope: "project",
	}, original.UpdatedAt)
	if err != nil || len(hits) != 1 || hits[0].Title != "original" {
		t.Fatalf("historical mixed-case filter = %#v, %v", hits, err)
	}
	rejected, err := s.SearchAsOf("searchable", SearchOptions{
		Type: typ, Project: project, Scope: scope,
	}, original.UpdatedAt)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("historically invalid filters returned %#v, %v", rejected, err)
	}
}

func TestSearchResultsAsOfRetentionGap(t *testing.T) {
	s := newTimeTravelStore(t, true, 1)
	id := addTimeTravelObservation(t, s, "retained search", "")
	first, _ := s.GetObservation(id)
	for _, content := range []string{"middle", "current searchable"} {
		if _, err := s.UpdateObservation(id, UpdateObservationParams{Content: &content}); err != nil {
			t.Fatal(err)
		}
	}
	current, _ := s.Search("searchable", SearchOptions{})
	if historical, err := s.SearchResultsAsOf(current, first.UpdatedAt); err == nil || historical != nil {
		t.Fatalf("pruned search = %#v, %v; want retention error", historical, err)
	}
	oldOnly, err := s.Search("middle", SearchOptions{})
	if err != nil || len(oldOnly) != 0 {
		t.Fatalf("historical-only text must not be indexed: %#v, %v", oldOnly, err)
	}
}

func TestSearchAsOfRelaxesAfterHistoricalFiltering(t *testing.T) {
	s := newTimeTravelStore(t, true, 0)
	eligible := addTimeTravelObservation(t, s, "eligible memory", "")
	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "s1", Type: "decision", Title: "eligible and memory",
		Content: "strict out of scope", Project: "other", Scope: "project",
	}); err != nil {
		t.Fatal(err)
	}
	var diag SearchDiag
	hits, err := s.SearchAsOf("eligible and memory", SearchOptions{
		Project: "omnia", Scope: "project", Diag: &diag,
	}, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil || len(hits) != 1 || hits[0].ID != eligible {
		t.Fatalf("historically filtered relaxation = %#v, %v", hits, err)
	}
	if !diag.Relaxed || diag.Step != 1 || diag.Exhausted {
		t.Fatalf("historical relaxation diag = %+v", diag)
	}
}

func TestSearchAsOfZeroHitBeforeRecordingBoundary(t *testing.T) {
	s := newTimeTravelStore(t, true, 0)
	var startedAt string
	if err := s.db.QueryRow(`SELECT started_at FROM time_travel_metadata WHERE id = 1`).Scan(&startedAt); err != nil {
		t.Fatal(err)
	}
	start, _ := parseObservationTime(startedAt)
	_, err := s.SearchAsOf("no-candidate", SearchOptions{}, start.Add(-time.Second).Format(time.RFC3339Nano))
	if err == nil || !strings.Contains(err.Error(), startedAt) {
		t.Fatalf("zero-hit boundary error = %v, want exact persisted %s", err, startedAt)
	}
}
