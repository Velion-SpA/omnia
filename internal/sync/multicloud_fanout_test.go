package sync

import (
	"encoding/json"
	"testing"

	"github.com/velion/omnia/internal/store"
)

// TestCloudFanoutDeliversSingleWriteToEveryCloud is the OBL-06 regression test.
//
// A single local write, one local DB, and TWO configured clouds:
//   - the default cloud ("personal") drains the legacy global "cloud" queue and
//     tracks state under "cloud:<project>", exactly like a single-cloud install;
//   - a second named cloud ("work") is registered for fan-out and drains its OWN
//     alias-scoped queue "work:<project>".
//
// Before the fix, the first alias drained and acked the shared "cloud" queue, so
// the second alias saw nothing pending, exported an empty chunk, and was still
// marked healthy — the second cloud silently never received the data. This test
// exercises the REAL internal/sync.Syncer.Export path (no stubbed syncExport) and
// asserts BOTH remotes receive the write and BOTH sync_state rows advance.
func TestCloudFanoutDeliversSingleWriteToEveryCloud(t *testing.T) {
	const project = "proj-a"

	s := newTestStore(t)
	if err := s.EnrollProject(project); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	// Register the non-default "work" cloud BEFORE the write so the local mutation
	// fans out into its dedicated queue. The default cloud stays implicit.
	if err := s.ReplaceCloudSyncTargets([]string{"work"}); err != nil {
		t.Fatalf("register fan-out target: %v", err)
	}

	// One local write.
	if err := s.CreateSession("sess-a", project, "/tmp/proj-a"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	const obsTitle = "fanned-out decision"
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess-a",
		Type:      "decision",
		Title:     obsTitle,
		Content:   "one local write, delivered to every cloud",
		Project:   project,
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add observation: %v", err)
	}

	// Default cloud ("personal"): legacy "cloud" mutation queue + "cloud:proj-a" state.
	personalTransport := newFakeCloudTransport()
	personal := NewCloudWithTransport(s, personalTransport, project)
	personal.SetCloudTargetKeys("", "") // empty => default keys

	// Non-default cloud ("work"): its own alias-scoped fan-out queue.
	workKey := "work:" + project
	workTransport := newFakeCloudTransport()
	work := NewCloudWithTransport(s, workTransport, project)
	work.SetCloudTargetKeys(workKey, workKey)

	// Export to the DEFAULT cloud FIRST. This drains and acks the shared "cloud"
	// queue — the exact action that used to starve every sibling alias.
	personalResult, err := personal.Export("alice", project)
	if err != nil {
		t.Fatalf("personal export: %v", err)
	}
	if personalResult.IsEmpty {
		t.Fatal("expected the default cloud to receive the local write")
	}

	// Export to the WORK cloud. With the fix it drains its OWN queue and still
	// receives the write; before the fix it returned IsEmpty and delivered nothing.
	workResult, err := work.Export("alice", project)
	if err != nil {
		t.Fatalf("work export: %v", err)
	}
	if workResult.IsEmpty {
		t.Fatal("regression: work cloud received nothing — shared queue was drained by the default cloud")
	}

	// BOTH remotes must physically hold a chunk carrying the observation.
	assertRemoteHasObservation(t, "personal", personalTransport, obsTitle)
	assertRemoteHasObservation(t, "work", workTransport, obsTitle)

	// BOTH sync_state rows must advance last_acked_seq independently.
	defaultState, err := s.GetSyncState("cloud:" + project)
	if err != nil {
		t.Fatalf("read default sync_state: %v", err)
	}
	if defaultState.LastAckedSeq <= 0 {
		t.Fatalf("expected default cloud sync_state to advance, got last_acked_seq=%d", defaultState.LastAckedSeq)
	}

	workState, err := s.GetSyncState(workKey)
	if err != nil {
		t.Fatalf("read work sync_state: %v", err)
	}
	if workState.LastAckedSeq <= 0 {
		t.Fatalf("expected work cloud sync_state to advance, got last_acked_seq=%d", workState.LastAckedSeq)
	}

	// Neither cloud may still report its own queue as pending after delivery, and
	// draining one must never mark the other healthy without its own delivery.
	if pending, err := s.HasPendingSyncMutationsForProject(project); err != nil {
		t.Fatalf("default pending check: %v", err)
	} else if pending {
		t.Fatal("expected default cloud queue drained after export")
	}
	if pending, err := s.HasPendingSyncMutationsForTarget(workKey, project); err != nil {
		t.Fatalf("work pending check: %v", err)
	} else if pending {
		t.Fatal("expected work cloud queue drained after export")
	}
}

// TestCloudFanoutIsIndependentAcrossAliases proves the two queues do not share an
// ack cursor: draining one leaves the other fully pending until it syncs.
func TestCloudFanoutIsIndependentAcrossAliases(t *testing.T) {
	const project = "proj-a"

	s := newTestStore(t)
	if err := s.EnrollProject(project); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	if err := s.ReplaceCloudSyncTargets([]string{"work"}); err != nil {
		t.Fatalf("register fan-out target: %v", err)
	}
	if err := s.CreateSession("sess-a", project, "/tmp/proj-a"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess-a",
		Type:      "decision",
		Title:     "independent",
		Content:   "independent queues",
		Project:   project,
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add observation: %v", err)
	}

	workKey := "work:" + project

	// Sync ONLY the default cloud.
	personal := NewCloudWithTransport(s, newFakeCloudTransport(), project)
	personal.SetCloudTargetKeys("", "")
	if _, err := personal.Export("alice", project); err != nil {
		t.Fatalf("personal export: %v", err)
	}

	// The work queue must STILL be pending — the default sync must not have touched it.
	pending, err := s.HasPendingSyncMutationsForTarget(workKey, project)
	if err != nil {
		t.Fatalf("work pending check: %v", err)
	}
	if !pending {
		t.Fatal("expected work cloud queue to remain pending after only the default cloud synced")
	}
}

func assertRemoteHasObservation(t *testing.T, label string, transport *fakeCloudTransport, wantTitle string) {
	t.Helper()
	if transport.writeChunkCalls == 0 || len(transport.chunks) == 0 {
		t.Fatalf("%s cloud: no chunk was written to the remote", label)
	}
	for _, payload := range transport.chunks {
		var chunk ChunkData
		if err := json.Unmarshal(payload, &chunk); err != nil {
			t.Fatalf("%s cloud: decode chunk payload: %v", label, err)
		}
		for _, obs := range chunk.Observations {
			if obs.Title == wantTitle {
				return
			}
		}
	}
	t.Fatalf("%s cloud: chunk did not contain observation %q", label, wantTitle)
}
