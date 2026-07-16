package store

import (
	"encoding/json"
	"testing"
)

// ─── PR5 slice 2 — push side (human-like-memory cloud semantic parity) ───────
//
// EnqueueEmbeddingMutation mirrors JudgeRelation's enqueue call: a locally
// computed vector is recorded as a sync_mutations row with entity='embedding'
// so autosync propagates it toward cloud_embeddings (PR5 slice 1). Unlike
// relations, embeddings are always enqueued (no enrollment gate) — matching
// how observation/prompt mutations are unconditionally enqueued today.

// countEmbeddingMutations returns the number of sync_mutations rows with
// entity='embedding' and the given project value.
func countEmbeddingMutations(t *testing.T, s *Store, project string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(
		`SELECT count(*) FROM sync_mutations WHERE entity = ? AND project = ?`,
		SyncEntityEmbedding, project,
	).Scan(&n); err != nil {
		t.Fatalf("countEmbeddingMutations: %v", err)
	}
	return n
}

// TestEnqueueEmbeddingMutation_CreatesSyncMutationWithVector (RED) asserts that
// calling EnqueueEmbeddingMutation inserts exactly one sync_mutations row whose
// decoded payload carries the vector, project, model, dim, and content hash
// unchanged.
func TestEnqueueEmbeddingMutation_CreatesSyncMutationWithVector(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("ses-emb-a", "proj-emb", "/tmp/emb-a"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.EnrollProject("proj-emb"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}

	vec := []float32{0.25, -0.5, 1.0}
	if err := s.EnqueueEmbeddingMutation(EmbeddingSyncInput{
		SyncID:      "obs-emb-1",
		Project:     "proj-emb",
		Type:        "decision",
		Model:       "jina/jina-embeddings-v2-base-es",
		Dim:         3,
		Vector:      vec,
		ContentHash: "hash-1",
		UpdatedAt:   "2026-07-16 10:00:00",
	}); err != nil {
		t.Fatalf("EnqueueEmbeddingMutation: %v", err)
	}

	if n := countEmbeddingMutations(t, s, "proj-emb"); n != 1 {
		t.Fatalf("expected exactly 1 embedding sync_mutations row for proj-emb; got %d", n)
	}

	pending, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 10)
	if err != nil {
		t.Fatalf("ListPendingSyncMutations: %v", err)
	}
	var found *SyncMutation
	for i := range pending {
		if pending[i].Entity == SyncEntityEmbedding && pending[i].EntityKey == "obs-emb-1" {
			found = &pending[i]
		}
	}
	if found == nil {
		t.Fatalf("expected a pending embedding mutation with entity_key=obs-emb-1; got %+v", pending)
	}
	if found.Op != SyncOpUpsert {
		t.Errorf("op: want %q, got %q", SyncOpUpsert, found.Op)
	}
	if found.Project != "proj-emb" {
		t.Errorf("project: want %q, got %q", "proj-emb", found.Project)
	}

	var p syncEmbeddingPayload
	if err := json.Unmarshal([]byte(found.Payload), &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.SyncID != "obs-emb-1" || p.Model != "jina/jina-embeddings-v2-base-es" || p.Dim != 3 || p.ContentHash != "hash-1" {
		t.Errorf("payload fields mismatch: %+v", p)
	}
	gotVec, err := decodeSyncVectorFloat32(p.Vector)
	if err != nil {
		t.Fatalf("decodeSyncVectorFloat32: %v", err)
	}
	if len(gotVec) != len(vec) {
		t.Fatalf("vector length: want %d, got %d", len(vec), len(gotVec))
	}
	for i := range vec {
		if gotVec[i] != vec[i] {
			t.Errorf("vector[%d]: want %v, got %v", i, vec[i], gotVec[i])
		}
	}
}

// TestEnqueueEmbeddingMutation_RequiresSyncIDAndVector (RED, triangulation)
// asserts the required-field guard rejects empty sync_id / empty vector before
// any row is written — mirroring relation's own required-field validation.
func TestEnqueueEmbeddingMutation_RequiresSyncIDAndVector(t *testing.T) {
	s := newTestStore(t)

	if err := s.EnqueueEmbeddingMutation(EmbeddingSyncInput{
		SyncID: "",
		Vector: []float32{1, 2, 3},
	}); err == nil {
		t.Fatal("expected error when sync_id is empty")
	}

	if err := s.EnqueueEmbeddingMutation(EmbeddingSyncInput{
		SyncID: "obs-emb-missing-vec",
		Vector: nil,
	}); err == nil {
		t.Fatal("expected error when vector is empty")
	}

	if n := countEmbeddingMutations(t, s, ""); n != 0 {
		t.Fatalf("expected 0 embedding mutations after rejected calls; got %d", n)
	}
}
