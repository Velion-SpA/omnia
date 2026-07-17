package cloudserver

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	cloudauth "github.com/velion/omnia/internal/cloud/auth"
	"github.com/velion/omnia/internal/cloud/cloudstore"
	"github.com/velion/omnia/internal/store"
)

// ─── human-like-memory PR5 slice 2 — server-side embedding materialization ───
//
// A pushed SyncEntityEmbedding mutation must be inserted into cloud_mutations
// (unchanged, generic path — already covered by the existing mutation-push
// tests) AND, when the caller authenticates via RBAC account claims,
// materialized into cloud_embeddings via cloudstore.UpsertEmbedding so PR5
// slice 3's cloud semantic search can query it directly.

// fakeRBACEmbeddingStore extends fakeMembershipStore with UpsertEmbedding
// call capture, satisfying EmbeddingMutationStore for the RBAC-account tests.
type fakeRBACEmbeddingStore struct {
	fakeMembershipStore
	upserted []cloudstore.EmbeddingRow
}

func newFakeRBACEmbeddingStore() *fakeRBACEmbeddingStore {
	return &fakeRBACEmbeddingStore{fakeMembershipStore: *newFakeMembershipStore()}
}

func (s *fakeRBACEmbeddingStore) UpsertEmbedding(_ context.Context, row cloudstore.EmbeddingRow) error {
	s.upserted = append(s.upserted, row)
	return nil
}

// fakeLegacyEmbeddingStore extends the plain fakeMutationStore (NOT
// membership-capable, so New(...) never wires RBAC) with UpsertEmbedding call
// capture, for the legacy shared-token test.
type fakeLegacyEmbeddingStore struct {
	fakeMutationStore
	upserted []cloudstore.EmbeddingRow
}

func newFakeLegacyEmbeddingStore() *fakeLegacyEmbeddingStore {
	return &fakeLegacyEmbeddingStore{fakeMutationStore: *newFakeMutationStore()}
}

func (s *fakeLegacyEmbeddingStore) UpsertEmbedding(_ context.Context, row cloudstore.EmbeddingRow) error {
	s.upserted = append(s.upserted, row)
	return nil
}

// encodeTestVector little-endian float32 encodes v — mirrors internal/store's
// encodeSyncVectorFloat32, duplicated here for test payload construction.
func encodeTestVector(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, x := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(x))
	}
	return buf
}

func embeddingMutationEntry(t *testing.T, project, syncID string, vec []float32) MutationEntry {
	t.Helper()
	payload := map[string]any{
		"sync_id":      syncID,
		"project":      project,
		"type":         "decision",
		"model":        "jina/jina-embeddings-v2-base-es",
		"dim":          len(vec),
		"vector":       encodeTestVector(vec),
		"content_hash": "hash-1",
		"updated_at":   "2026-07-16 12:00:00",
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal embedding payload: %v", err)
	}
	return MutationEntry{Project: project, Entity: store.SyncEntityEmbedding, EntityKey: syncID, Op: store.SyncOpUpsert, Payload: raw}
}

// TestMutationPush_RBACAccount_MaterializesEmbedding (RED) asserts that an
// embedding mutation pushed by an RBAC-authenticated account is materialized
// into cloud_embeddings with the account's ID and the decoded vector intact.
func TestMutationPush_RBACAccount_MaterializesEmbedding(t *testing.T) {
	ms := newFakeRBACEmbeddingStore()
	authSvc := &fakeRBACAuth{
		accounts: map[string]*cloudauth.AccountClaims{
			"token-alice": {AccountID: "alice", Username: "alice"},
		},
	}
	ms.grant("alice", "proj-a", int(cloudauth.PermInsert|cloudauth.PermDelete), "owner")
	srv := newRBACTestServer(&ms.fakeMembershipStore, authSvc)
	// newRBACTestServer wires RBAC off the *fakeMembershipStore pointer we
	// pass; re-point s.store at the embedding-capable outer fake so
	// materializeEmbeddingMutations' type assertion succeeds too.
	srv.store = ms

	vec := []float32{0.1, 0.2, 0.3}
	entries := []MutationEntry{embeddingMutationEntry(t, "proj-a", "obs-cloud-1", vec)}
	body, err := json.Marshal(map[string]any{"entries": entries})
	if err != nil {
		t.Fatalf("marshal push body: %v", err)
	}

	rec := httptest.NewRecorder()
	req := makeAccountRequest(http.MethodPost, "/sync/mutations/push", "token-alice", body)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(ms.upserted) != 1 {
		t.Fatalf("expected exactly 1 UpsertEmbedding call; got %d", len(ms.upserted))
	}
	got := ms.upserted[0]
	if got.AccountID != "alice" || got.Project != "proj-a" || got.SyncID != "obs-cloud-1" {
		t.Errorf("embedding row identity mismatch: %+v", got)
	}
	if len(got.Vector) != len(vec) {
		t.Fatalf("vector length: want %d, got %d", len(vec), len(got.Vector))
	}
	for i := range vec {
		if got.Vector[i] != vec[i] {
			t.Errorf("vector[%d]: want %v, got %v", i, vec[i], got.Vector[i])
		}
	}
}

// TestMutationPush_LegacySharedToken_SkipsEmbeddingMaterialization (RED,
// triangulation) asserts that a legacy shared-token push (no account claims,
// no RBAC wiring at all) still succeeds and stores the mutation in
// cloud_mutations generically, but does NOT call UpsertEmbedding — there is
// no account boundary to scope it by (see materializeEmbeddingMutations doc).
func TestMutationPush_LegacySharedToken_SkipsEmbeddingMaterialization(t *testing.T) {
	ms := newFakeLegacyEmbeddingStore()
	srv := New(ms, multiProjectAuth{token: "secret", projects: []string{"proj-a"}}, 0)

	entries := []MutationEntry{embeddingMutationEntry(t, "proj-a", "obs-legacy-1", []float32{1, 2})}
	body, err := json.Marshal(map[string]any{"entries": entries})
	if err != nil {
		t.Fatalf("marshal push body: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(ms.mutations) != 1 {
		t.Fatalf("expected the mutation to still be stored generically; got %d", len(ms.mutations))
	}
	if len(ms.upserted) != 0 {
		t.Fatalf("expected 0 UpsertEmbedding calls for a legacy shared-token push; got %d", len(ms.upserted))
	}
}

// TestMutationPush_MalformedEmbeddingPayload_SkippedNotFatal (RED,
// triangulation) asserts a malformed embedding payload (missing vector) is
// skipped during materialization without failing the push — the mutation is
// already durably stored by InsertMutationBatch.
func TestMutationPush_MalformedEmbeddingPayload_SkippedNotFatal(t *testing.T) {
	ms := newFakeRBACEmbeddingStore()
	authSvc := &fakeRBACAuth{
		accounts: map[string]*cloudauth.AccountClaims{
			"token-bob": {AccountID: "bob", Username: "bob"},
		},
	}
	ms.grant("bob", "proj-a", int(cloudauth.PermInsert|cloudauth.PermDelete), "owner")
	srv := newRBACTestServer(&ms.fakeMembershipStore, authSvc)
	srv.store = ms

	raw, err := json.Marshal(map[string]any{"sync_id": "obs-bad", "project": "proj-a"}) // no vector
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	entries := []MutationEntry{{Project: "proj-a", Entity: store.SyncEntityEmbedding, EntityKey: "obs-bad", Op: store.SyncOpUpsert, Payload: raw}}
	body, err := json.Marshal(map[string]any{"entries": entries})
	if err != nil {
		t.Fatalf("marshal push body: %v", err)
	}

	rec := httptest.NewRecorder()
	req := makeAccountRequest(http.MethodPost, "/sync/mutations/push", "token-bob", body)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (malformed embedding payload must not fail the push), got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(ms.upserted) != 0 {
		t.Fatalf("expected 0 UpsertEmbedding calls for a malformed payload; got %d", len(ms.upserted))
	}
}
