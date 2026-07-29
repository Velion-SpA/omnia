package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
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
