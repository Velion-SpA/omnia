package sync

import (
	"encoding/json"
	"testing"

	"github.com/velion/omnia/internal/store"
)

// ─── human-like-memory PR5 slice 2 — SyncEntityEmbedding push/pull round trip ─
//
// Mirrors the relation cloud-mode export tests (see
// TestCloudExportKnownChunkReconcileFailureDoesNotAckMutations): a locally
// enqueued embedding mutation must surface, byte-for-byte, in the
// cloud-mode filterByPendingMutations chunk (the same generic path relation
// mutations already flow through — no entity-specific code needed there),
// and applying the resulting mutation on a destination store must be
// idempotent across repeated pulls.

// TestEmbeddingSync_SurfacesInCloudModeMutationExport (RED) asserts a locally
// enqueued embedding mutation appears in the cloud-mode export's
// chunk.Mutations with its payload intact.
func TestEmbeddingSync_SurfacesInCloudModeMutationExport(t *testing.T) {
	resetSyncTestHooks(t)
	s := newTestStore(t)
	transport := newFakeCloudTransport()
	sy := NewCloudWithTransport(s, transport, "proj-emb-sync")

	if err := s.EnrollProject("proj-emb-sync"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}
	if err := s.CreateSession("ses-emb-sync", "proj-emb-sync", "/tmp/emb-sync"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	vec := []float32{0.4, -0.2, 0.9}
	if err := s.EnqueueEmbeddingMutation(store.EmbeddingSyncInput{
		SyncID:      "obs-sync-emb-1",
		Project:     "proj-emb-sync",
		Type:        "decision",
		Model:       "jina/jina-embeddings-v2-base-es",
		Dim:         3,
		Vector:      vec,
		ContentHash: "hash-sync-1",
		UpdatedAt:   "2026-07-16 12:00:00",
	}); err != nil {
		t.Fatalf("EnqueueEmbeddingMutation: %v", err)
	}

	data, err := s.ExportProject("proj-emb-sync")
	if err != nil {
		t.Fatalf("ExportProject: %v", err)
	}
	chunk, seqs, err := sy.filterByPendingMutations(data, "proj-emb-sync")
	if err != nil {
		t.Fatalf("filterByPendingMutations: %v", err)
	}
	if len(seqs) == 0 {
		t.Fatal("expected at least 1 pending mutation seq")
	}

	var found *store.SyncMutation
	for i := range chunk.Mutations {
		if chunk.Mutations[i].Entity == store.SyncEntityEmbedding && chunk.Mutations[i].EntityKey == "obs-sync-emb-1" {
			found = &chunk.Mutations[i]
		}
	}
	if found == nil {
		t.Fatalf("expected chunk.Mutations to contain the embedding mutation; got %+v", chunk.Mutations)
	}
	if found.Op != store.SyncOpUpsert {
		t.Errorf("op: want %q, got %q", store.SyncOpUpsert, found.Op)
	}

	var p struct {
		SyncID string `json:"sync_id"`
		Model  string `json:"model"`
		Dim    int    `json:"dim"`
	}
	if err := json.Unmarshal([]byte(found.Payload), &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.SyncID != "obs-sync-emb-1" || p.Model != "jina/jina-embeddings-v2-base-es" || p.Dim != 3 {
		t.Errorf("payload fields mismatch: %+v", p)
	}
}

// TestEmbeddingSync_PullApplyIdempotentAcrossRepeatedTransfer (RED,
// triangulation) asserts the exported embedding mutation, applied to an
// independent destination store via ApplyPulledMutation, does not error and
// is safe to apply twice — mirroring the cross-machine round trip relations
// use (TestRelationSync_PushPull_CrossMachine), adapted to embedding's
// decode-only pull-apply (see applyEmbeddingUpsertTx doc: idempotency here
// means "no local state to duplicate", not "one row upserted").
func TestEmbeddingSync_PullApplyIdempotentAcrossRepeatedTransfer(t *testing.T) {
	resetSyncTestHooks(t)
	src := newTestStore(t)
	transport := newFakeCloudTransport()
	sy := NewCloudWithTransport(src, transport, "proj-emb-pull")

	if err := src.EnrollProject("proj-emb-pull"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}
	if err := src.CreateSession("ses-emb-pull", "proj-emb-pull", "/tmp/emb-pull"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := src.EnqueueEmbeddingMutation(store.EmbeddingSyncInput{
		SyncID: "obs-pull-emb-1", Project: "proj-emb-pull", Model: "m", Dim: 2, Vector: []float32{1, 2}, ContentHash: "h",
	}); err != nil {
		t.Fatalf("EnqueueEmbeddingMutation: %v", err)
	}

	data, err := src.ExportProject("proj-emb-pull")
	if err != nil {
		t.Fatalf("ExportProject: %v", err)
	}
	chunk, _, err := sy.filterByPendingMutations(data, "proj-emb-pull")
	if err != nil {
		t.Fatalf("filterByPendingMutations: %v", err)
	}
	var mutation store.SyncMutation
	for _, m := range chunk.Mutations {
		if m.Entity == store.SyncEntityEmbedding {
			mutation = m
		}
	}
	if mutation.Entity == "" {
		t.Fatal("expected an embedding mutation in the exported chunk")
	}

	dst := newTestStore(t)
	// ApplyPulledMutation's underlying getSyncStateTx auto-creates the
	// sync_state row (INSERT OR IGNORE) on first use — no separate init call
	// needed.
	if err := dst.ApplyPulledMutation(store.DefaultSyncTargetKey, mutation); err != nil {
		t.Fatalf("first ApplyPulledMutation: %v", err)
	}
	// Re-apply the SAME seq: ApplyPulledMutation's own cursor guard
	// (mutation.Seq <= last_pulled_seq) already short-circuits a repeat, so
	// this must be a safe no-op, not an error.
	if err := dst.ApplyPulledMutation(store.DefaultSyncTargetKey, mutation); err != nil {
		t.Fatalf("second ApplyPulledMutation (idempotent re-apply): %v", err)
	}
}
