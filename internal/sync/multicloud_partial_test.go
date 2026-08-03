package sync

import (
	"testing"

	"github.com/velion/omnia/internal/store"
)

// TestCloudWithStaleRemoteChunksStillReceivesBacklog covers the case the first
// #242 fix missed.
//
// That fix keyed "this target never received anything" on knownChunks being
// empty, which merges the locally recorded deliveries with the REMOTE manifest.
// On the real install the remote already held one old chunk from months earlier,
// so knownChunks was non-empty, the export fell back to the mutation diff, and
// the CLI reported "all memories already exported" again — with the cloud still
// six weeks behind.
//
// The remote manifest is not evidence that THIS store delivered anything: the
// chunk can predate this machine, or come from another client entirely. The
// authoritative record of what this store has delivered to this target is its
// own sync_chunks rows.
func TestCloudWithStaleRemoteChunksStillReceivesBacklog(t *testing.T) {
	const project = "proj-partial"

	s := newTestStore(t)
	if err := s.EnrollProject(project); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	if err := s.CreateSession("sess-partial", project, "/tmp/proj-partial"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	const obsTitle = "memory the stale cloud is missing"
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess-partial",
		Type:      "decision",
		Title:     obsTitle,
		Content:   "the remote already holds an unrelated older chunk",
		Project:   project,
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add observation: %v", err)
	}

	// Default cloud drains the legacy queue, as it would on a real install.
	defaultCloud := NewCloudWithTransport(s, newFakeCloudTransport(), project)
	defaultCloud.SetCloudTargetKeys("", "")
	if _, err := defaultCloud.Export("alice", project); err != nil {
		t.Fatalf("default export: %v", err)
	}

	// A cloud added later, whose remote ALREADY holds an old chunk this store
	// never sent — exactly the shape of the reported install.
	if err := s.ReplaceCloudSyncTargets([]string{"stale"}); err != nil {
		t.Fatalf("register target: %v", err)
	}
	staleKey := "stale:" + project
	staleTransport := newFakeCloudTransport()
	staleTransport.manifest = &Manifest{
		Version: 1,
		Chunks: []ChunkEntry{{
			ID:        "old0chunk",
			CreatedBy: "some-other-machine",
			CreatedAt: "2026-06-13T06:57:29Z",
			Sessions:  1,
			Memories:  6,
		}},
	}

	staleCloud := NewCloudWithTransport(s, staleTransport, project)
	staleCloud.SetCloudTargetKeys(staleKey, staleKey)

	result, err := staleCloud.Export("alice", project)
	if err != nil {
		t.Fatalf("stale export: %v", err)
	}
	if result.IsEmpty {
		t.Fatal("a cloud holding only an unrelated old chunk reported nothing to sync — the backlog never left this machine")
	}
	assertRemoteHasObservation(t, "stale", staleTransport, obsTitle)
}
