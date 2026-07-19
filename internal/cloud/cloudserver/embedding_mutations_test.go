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

// TestMutationPush_MalformedEmbeddingPayload_RejectedAtPush (CRITICAL bugfix,
// human-like-memory PR5, found by adversarial review) supersedes the old
// "TestMutationPush_MalformedEmbeddingPayload_SkippedNotFatal" test, which
// asserted this exact payload (missing vector) returned 200 and was merely
// skipped during best-effort materialization. That WAS the bug:
// validateMutationEntry had no case for "embedding", so a malformed embedding
// payload fell through to the lenient legacy validator, was accepted into
// the shared cloud_mutations journal at push time, and would later poison
// every peer that pulled it (see the pull-side ApplyPulledMutation fix in
// internal/store/store.go — that fix is a safety net for payloads that
// already made it into the journal; this push-time rejection is the actual
// front door). Now: a malformed embedding payload must be REJECTED with 400
// BEFORE InsertMutationBatch is ever called — nothing reaches the journal.
func TestMutationPush_MalformedEmbeddingPayload_RejectedAtPush(t *testing.T) {
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

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 (malformed embedding payload must be rejected at push, never reach the journal), got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(ms.mutations) != 0 {
		t.Fatalf("expected 0 mutations stored (rejected batch is atomic); got %d", len(ms.mutations))
	}
	if len(ms.upserted) != 0 {
		t.Fatalf("expected 0 UpsertEmbedding calls for a rejected payload; got %d", len(ms.upserted))
	}
}

// TestHandleMutationPush_ValidEmbedding_Returns200 verifies the happy path:
// a complete embedding payload with all required fields (sync_id, project,
// a decodable vector) returns HTTP 200 and is stored.
func TestHandleMutationPush_ValidEmbedding_Returns200(t *testing.T) {
	ms := newFakeMutationStore()
	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	entries := []MutationEntry{embeddingMutationEntry(t, "proj-a", "obs-emb-valid", []float32{0.1, 0.2, 0.3})}
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for a valid embedding payload, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(ms.mutations) != 1 {
		t.Fatalf("expected 1 mutation stored, got %d", len(ms.mutations))
	}
}

// TestHandleMutationPush_EmbeddingMissingEachRequiredField verifies each
// required embedding field (sync_id, project, vector), individually absent,
// is rejected with HTTP 400 and the correct field name in the response body
// — mirrors TestHandleMutationPush_RelationMissingEachRequiredField.
func TestHandleMutationPush_EmbeddingMissingEachRequiredField(t *testing.T) {
	requiredFields := []struct {
		name    string
		payload json.RawMessage
	}{
		{
			name: "sync_id",
			payload: mustMarshalEmbeddingPayload(t, map[string]any{
				"project": "proj-a",
				"vector":  encodeTestVector([]float32{0.1, 0.2}),
			}),
		},
		{
			name: "project",
			payload: mustMarshalEmbeddingPayload(t, map[string]any{
				"sync_id": "obs-emb-missing",
				"vector":  encodeTestVector([]float32{0.1, 0.2}),
			}),
		},
		{
			name: "vector",
			payload: mustMarshalEmbeddingPayload(t, map[string]any{
				"sync_id": "obs-emb-missing",
				"project": "proj-a",
			}),
		},
	}

	for _, tc := range requiredFields {
		t.Run("missing_"+tc.name, func(t *testing.T) {
			ms := newFakeMutationStore()
			srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

			entries := []MutationEntry{
				{Project: "proj-a", Entity: store.SyncEntityEmbedding, EntityKey: "obs-emb-missing", Op: store.SyncOpUpsert, Payload: tc.payload},
			}
			body := marshalPushRequest(t, entries)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
			req.Header.Set("Authorization", "Bearer secret")
			req.Header.Set("Content-Type", "application/json")
			srv.Handler().ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("missing %q: expected 400, got %d body=%q", tc.name, rec.Code, rec.Body.String())
			}
			if len(ms.mutations) != 0 {
				t.Fatalf("missing %q: expected 0 mutations stored, got %d", tc.name, len(ms.mutations))
			}
			var resp struct {
				Invalid []struct {
					Field  string `json:"field"`
					Index  int    `json:"index"`
					Entity string `json:"entity"`
				} `json:"invalid"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("missing %q: decode 400 body: %v; body=%q", tc.name, err, rec.Body.String())
			}
			if len(resp.Invalid) == 0 {
				t.Fatalf("missing %q: expected invalid list in 400 body, got none; body=%q", tc.name, rec.Body.String())
			}
			if resp.Invalid[0].Field != tc.name {
				t.Errorf("missing %q: expected field=%q in invalid[0], got %q", tc.name, tc.name, resp.Invalid[0].Field)
			}
		})
	}
}

// TestHandleMutationPush_EmbeddingUndecodableVector_Returns400 verifies that
// a vector field which IS present but does not decode to a valid
// little-endian float32 BLOB (byte length not a multiple of 4) is rejected
// at push time — this is the "undecodable vector" case named explicitly in
// the CRITICAL bug report, distinct from an outright missing vector field.
func TestHandleMutationPush_EmbeddingUndecodableVector_Returns400(t *testing.T) {
	ms := newFakeMutationStore()
	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	payload := mustMarshalEmbeddingPayload(t, map[string]any{
		"sync_id": "obs-emb-bad-vec",
		"project": "proj-a",
		"vector":  []byte{1, 2, 3}, // 3 bytes: not a multiple of 4 => undecodable
	})
	entries := []MutationEntry{
		{Project: "proj-a", Entity: store.SyncEntityEmbedding, EntityKey: "obs-emb-bad-vec", Op: store.SyncOpUpsert, Payload: payload},
	}
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an undecodable vector, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(ms.mutations) != 0 {
		t.Fatalf("expected 0 mutations stored, got %d", len(ms.mutations))
	}
	var resp struct {
		Invalid []struct {
			Field string `json:"field"`
		} `json:"invalid"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode 400 body: %v; body=%q", err, rec.Body.String())
	}
	if len(resp.Invalid) == 0 || resp.Invalid[0].Field != "vector" {
		t.Fatalf("expected invalid[0].field=%q, got %+v; body=%q", "vector", resp.Invalid, rec.Body.String())
	}
}

// mustMarshalEmbeddingPayload marshals an arbitrary field map into JSON,
// used to build deliberately incomplete embedding payloads for validation
// tests.
func mustMarshalEmbeddingPayload(t *testing.T, fields map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal embedding payload: %v", err)
	}
	return raw
}
