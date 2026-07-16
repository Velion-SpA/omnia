package store

import (
	"database/sql"
	"encoding/json"
	"errors"
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

// ─── PR5 slice 2 — pull side ─────────────────────────────────────────────────
//
// applyEmbeddingUpsertTx decodes and validates a pulled embedding mutation and
// (if a hook is installed) hands the decoded vector to it. It never writes to
// engram.db itself — see applyEmbeddingUpsertTx's doc comment for why.

// buildEmbeddingMutation builds a SyncMutation for entity=SyncEntityEmbedding
// from a syncEmbeddingPayload, mirroring buildRelationMutation.
func buildEmbeddingMutation(t *testing.T, p syncEmbeddingPayload) SyncMutation {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("buildEmbeddingMutation: marshal: %v", err)
	}
	return SyncMutation{
		Entity:    SyncEntityEmbedding,
		EntityKey: p.SyncID,
		Op:        SyncOpUpsert,
		Payload:   string(raw),
		Source:    SyncSourceRemote,
		Project:   p.Project,
	}
}

// applyEmbeddingMutation calls applyPulledMutationTx inside a transaction,
// mirroring applyRelationMutation.
func applyEmbeddingMutation(t *testing.T, s *Store, m SyncMutation) error {
	t.Helper()
	return s.withTx(func(tx *sql.Tx) error {
		return s.applyPulledMutationTx(tx, m)
	})
}

// TestApplyPulledEmbedding_InvokesHookWithDecodedVector (RED) asserts a valid
// pulled embedding mutation decodes without error and, when a hook is
// installed, the hook receives the exact vector bytes that were pushed.
func TestApplyPulledEmbedding_InvokesHookWithDecodedVector(t *testing.T) {
	s := newTestStore(t)

	vec := []float32{0.1, 0.2, 0.3, -0.4}
	blob, err := encodeSyncVectorFloat32(vec)
	if err != nil {
		t.Fatalf("encodeSyncVectorFloat32: %v", err)
	}
	m := buildEmbeddingMutation(t, syncEmbeddingPayload{
		SyncID:      "obs-pull-1",
		Project:     "proj-pull",
		Type:        "decision",
		Model:       "jina/jina-embeddings-v2-base-es",
		Dim:         4,
		Vector:      blob,
		ContentHash: "hash-pull-1",
		UpdatedAt:   "2026-07-16 11:00:00",
	})

	var got []EmbeddingPulled
	s.SetEmbeddingPulledHook(func(p EmbeddingPulled) { got = append(got, p) })

	if err := applyEmbeddingMutation(t, s, m); err != nil {
		t.Fatalf("applyPulledMutationTx: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("expected hook to be called exactly once; got %d calls", len(got))
	}
	if got[0].SyncID != "obs-pull-1" || got[0].Project != "proj-pull" || got[0].Model != "jina/jina-embeddings-v2-base-es" || got[0].Dim != 4 {
		t.Errorf("hook payload fields mismatch: %+v", got[0])
	}
	if len(got[0].Vector) != len(vec) {
		t.Fatalf("vector length: want %d, got %d", len(vec), len(got[0].Vector))
	}
	for i := range vec {
		if got[0].Vector[i] != vec[i] {
			t.Errorf("vector[%d]: want %v, got %v", i, vec[i], got[0].Vector[i])
		}
	}
}

// TestApplyPulledEmbedding_IdempotentAcrossRepeatedApply (RED, triangulation)
// asserts applying the identical mutation twice is safe (no error either
// time) and invokes the hook once per call with byte-identical data —
// decode-and-notify has no local persistent state to duplicate, so repeated
// application is trivially idempotent (mirrors the SPIRIT of
// TestApplyPulledRelation_IdempotentOnSyncID, adapted to a decode-only apply).
func TestApplyPulledEmbedding_IdempotentAcrossRepeatedApply(t *testing.T) {
	s := newTestStore(t)

	vec := []float32{1, 0, 0}
	blob, err := encodeSyncVectorFloat32(vec)
	if err != nil {
		t.Fatalf("encodeSyncVectorFloat32: %v", err)
	}
	m := buildEmbeddingMutation(t, syncEmbeddingPayload{
		SyncID: "obs-pull-idem", Project: "proj-pull", Model: "m", Dim: 3, Vector: blob, ContentHash: "h",
	})

	calls := 0
	s.SetEmbeddingPulledHook(func(EmbeddingPulled) { calls++ })

	if err := applyEmbeddingMutation(t, s, m); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if err := applyEmbeddingMutation(t, s, m); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected hook invoked once per apply call (2 total); got %d", calls)
	}
}

// TestApplyPulledEmbedding_NilHookIsSafe (RED, triangulation) asserts the
// default (no hook installed) path — the common case for a device that
// doesn't wire pulled vectors anywhere — does not error.
func TestApplyPulledEmbedding_NilHookIsSafe(t *testing.T) {
	s := newTestStore(t)
	vec := []float32{1, 2}
	blob, _ := encodeSyncVectorFloat32(vec)
	m := buildEmbeddingMutation(t, syncEmbeddingPayload{SyncID: "obs-pull-nil", Vector: blob, Dim: 2})

	if err := applyEmbeddingMutation(t, s, m); err != nil {
		t.Fatalf("expected nil-hook apply to succeed; got %v", err)
	}
}

// TestApplyPulledEmbedding_DecodeErrorReturnsErrApplyDead (RED, triangulation)
// asserts a permanently malformed payload (invalid JSON) returns ErrApplyDead
// — matching applyRelationUpsertTx's contract for undecodable payloads.
func TestApplyPulledEmbedding_DecodeErrorReturnsErrApplyDead(t *testing.T) {
	s := newTestStore(t)
	m := SyncMutation{Entity: SyncEntityEmbedding, EntityKey: "obs-bad", Op: SyncOpUpsert, Payload: "{not-json"}

	err := applyEmbeddingMutation(t, s, m)
	if err == nil {
		t.Fatal("expected an error for malformed JSON payload")
	}
	if !errors.Is(err, ErrApplyDead) {
		t.Errorf("expected ErrApplyDead; got %v", err)
	}
}

// TestApplyPulledEmbedding_MissingVectorReturnsErrApplyDead (RED,
// triangulation) asserts a payload with no vector bytes is permanently
// invalid, not retryable.
func TestApplyPulledEmbedding_MissingVectorReturnsErrApplyDead(t *testing.T) {
	s := newTestStore(t)
	m := buildEmbeddingMutation(t, syncEmbeddingPayload{SyncID: "obs-no-vec", Dim: 3})

	err := applyEmbeddingMutation(t, s, m)
	if err == nil {
		t.Fatal("expected an error when vector is empty")
	}
	if !errors.Is(err, ErrApplyDead) {
		t.Errorf("expected ErrApplyDead; got %v", err)
	}
}
