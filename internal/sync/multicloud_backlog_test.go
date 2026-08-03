package sync

import (
	"testing"

	"github.com/velion/omnia/internal/store"
)

// TestCloudAddedAfterWritesReceivesExistingMemories is the issue #242 regression test.
//
// The OBL-06 fan-out test registers the second cloud BEFORE the local write, so the
// write fans out into that cloud's queue. Real installs do the opposite: memories
// accumulate for months against one cloud, and a second cloud is added later. That
// cloud has NO pending mutations — they were enqueued before it existed — so the
// export produced an empty chunk and the CLI reported
// "Nothing new to sync — all memories already exported" while the remote stayed empty.
//
// A cloud that has never received anything must get a full project export, not a
// mutation diff against a queue that could not have been populated.
func TestCloudAddedAfterWritesReceivesExistingMemories(t *testing.T) {
	const project = "proj-backlog"

	s := newTestStore(t)
	if err := s.EnrollProject(project); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	// Writes happen FIRST, with only the default cloud in play.
	if err := s.CreateSession("sess-backlog", project, "/tmp/proj-backlog"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	const obsTitle = "memory written before the second cloud existed"
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess-backlog",
		Type:      "decision",
		Title:     obsTitle,
		Content:   "must still reach a cloud added later",
		Project:   project,
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add observation: %v", err)
	}

	// The default cloud syncs normally and drains the legacy queue.
	defaultTransport := newFakeCloudTransport()
	defaultCloud := NewCloudWithTransport(s, defaultTransport, project)
	defaultCloud.SetCloudTargetKeys("", "")
	defaultResult, err := defaultCloud.Export("alice", project)
	if err != nil {
		t.Fatalf("default export: %v", err)
	}
	if defaultResult.IsEmpty {
		t.Fatal("expected the default cloud to receive the local write")
	}

	// NOW a second cloud is added — after the fact, exactly like adding a personal
	// cloud to an install that has been syncing to work for months.
	if err := s.ReplaceCloudSyncTargets([]string{"late"}); err != nil {
		t.Fatalf("register late fan-out target: %v", err)
	}

	lateKey := "late:" + project
	lateTransport := newFakeCloudTransport()
	lateCloud := NewCloudWithTransport(s, lateTransport, project)
	lateCloud.SetCloudTargetKeys(lateKey, lateKey)

	lateResult, err := lateCloud.Export("alice", project)
	if err != nil {
		t.Fatalf("late export: %v", err)
	}
	if lateResult.IsEmpty {
		t.Fatal("a cloud added after the writes reported nothing to sync and delivered nothing — issue #242")
	}
	if lateResult.ObservationsExported == 0 {
		t.Fatalf("expected the pre-existing observation to be exported, got %d", lateResult.ObservationsExported)
	}

	// The remote must actually hold the memory, not just report success.
	assertRemoteHasObservation(t, "late", lateTransport, obsTitle)
}
