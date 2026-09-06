package cloudserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velion/omnia/internal/cloud/chunkcodec"
)

// TestMutationPushInvalidPayloadIsBadRequest locks the status class for a
// payload the client can never make valid.
//
// validateMutationEntry applies only the lenient floor to the legacy entities,
// so an empty-content prompt upsert passes it and fails later in the strict
// canonicalization inside InsertMutationBatch. That failure used to reach the
// generic error branch and answer 500. A 5xx is retryable, so the client
// classified it transport_failed: project "umbral" retried the same doomed
// batch indefinitely with `status 500: insert mutations: cloudstore:
// canonicalize materialized mutation batch chunk: mutations[17]: prompt payload
// content is required for upsert`.
func TestMutationPushInvalidPayloadIsBadRequest(t *testing.T) {
	ms := newFakeMutationStore()
	// Exactly how the real chain surfaces it: cloudstore wraps the chunkcodec
	// error with %w on the way out of InsertMutationBatch.
	ms.errInsert = fmt.Errorf("cloudstore: canonicalize materialized mutation batch chunk: %w",
		fmt.Errorf("mutations[17]: %w: prompt payload content is required for upsert", chunkcodec.ErrInvalidPayload))
	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	body := marshalPushRequest(t, makeMutationEntries(1, "proj-a"))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a client payload error, got %d body=%q", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := resp["reason_code"]; got != "validation_error" {
		t.Errorf("reason_code = %v, want validation_error", got)
	}
}

// TestMutationPushGenuineServerErrorStaysInternal is the other half: a real
// server fault must NOT be downgraded to 400, or a broken cloud would look
// like bad client input and clients would stop retrying something they should.
func TestMutationPushGenuineServerErrorStaysInternal(t *testing.T) {
	ms := newFakeMutationStore()
	ms.errInsert = fmt.Errorf("cloudstore: database is locked")
	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	body := marshalPushRequest(t, makeMutationEntries(1, "proj-a"))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for a genuine server fault, got %d body=%q", rec.Code, rec.Body.String())
	}
}
