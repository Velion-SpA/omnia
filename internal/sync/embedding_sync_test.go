package sync

import (
	"encoding/json"
	"testing"

	"github.com/velion/omnia/internal/store"
)

// ─── SyncEntityEmbedding: local queue, no cloud replication ──────────────────
//
// Embeddings were originally replicated through the cloud chunk alongside
// relations. No cloud server ever accepted the entity — validateSupportedMutation
// only allows session/observation/prompt/relation — so a single embedding
// mutation made the whole push fail with
// `unsupported mutation "embedding"/"upsert"` and gated every incremental sync
// for the project (#251).
//
// The replication was dropped rather than extended: a vector is derived data,
// rebuilt from the observation's content by `omnia embed`, and only usable by a
// store running the same model at the same dimension. Consumers already
// re-embed on their own, so shipping vectors cost a few KB per memory and
// bought nothing.
//
// The PULL side is deliberately kept: chunks produced by older clients still
// carry embedding mutations, and applying them must remain safe.

// A locally enqueued embedding must NOT reach the cloud chunk, and its sequence
// must still be acked — otherwise the row sits pending forever and re-breaks
// every subsequent sync.
func TestEmbeddingSync_ExcludedFromCloudMutationExport(t *testing.T) {
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

	if err := s.EnqueueEmbeddingMutation(store.EmbeddingSyncInput{
		SyncID:      "obs-sync-emb-1",
		Project:     "proj-emb-sync",
		Type:        "decision",
		Model:       "jina/jina-embeddings-v2-base-es",
		Dim:         3,
		Vector:      []float32{0.4, -0.2, 0.9},
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

	for _, m := range chunk.Mutations {
		if m.Entity == store.SyncEntityEmbedding {
			t.Fatalf("embedding mutation reached the cloud chunk; the push would be rejected: %+v", m)
		}
	}
	if len(seqs) == 0 {
		t.Fatal("the excluded embedding's seq must still be acked, or it stays pending forever")
	}
}

// The pull side stays supported so chunks from older clients keep applying
// cleanly, and applying the same seq twice remains a safe no-op. The mutation is
// read straight from the queue rather than from an export, because export no
// longer emits it — this asserts the receiving contract, not the sending one.
func TestEmbeddingSync_PullApplyIdempotentAcrossRepeatedTransfer(t *testing.T) {
	resetSyncTestHooks(t)
	src := newTestStore(t)

	if err := src.EnrollProject("proj-emb-pull"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}
	if err := src.CreateSession("ses-emb-pull", "proj-emb-pull", "/tmp/emb-pull"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := src.EnqueueEmbeddingMutation(store.EmbeddingSyncInput{
		SyncID: "obs-pull-emb-1", Project: "proj-emb-pull", Model: "m", Dim: 2,
		Vector: []float32{1, 2}, ContentHash: "h",
	}); err != nil {
		t.Fatalf("EnqueueEmbeddingMutation: %v", err)
	}

	// Read the row an older client would have shipped, straight from the queue.
	pending, err := src.ListPendingSyncMutations(store.DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("ListPendingSyncMutations: %v", err)
	}
	var mutation store.SyncMutation
	for _, m := range pending {
		if m.Entity == store.SyncEntityEmbedding {
			mutation = m
		}
	}
	if mutation.Entity == "" {
		t.Fatal("expected an embedding mutation in the pending queue")
	}

	// Payload shape is part of the receiving contract: a decoder on the other
	// side has to find these fields.
	var p struct {
		SyncID string `json:"sync_id"`
		Model  string `json:"model"`
		Dim    int    `json:"dim"`
	}
	if err := json.Unmarshal([]byte(mutation.Payload), &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.SyncID != "obs-pull-emb-1" || p.Model != "m" || p.Dim != 2 {
		t.Errorf("payload fields mismatch: %+v", p)
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
