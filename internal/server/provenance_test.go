package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/velion/omnia/internal/audit"
	"github.com/velion/omnia/internal/embed"
)

// decodeRecorderJSON decodes an httptest.ResponseRecorder body as JSON. The
// server_e2e_test.go file's decodeJSON helper is gated behind the "e2e" build
// tag and unavailable to normal `go test` runs, so these in-process handler
// tests (matching auto_embed_test.go's style) get their own small decode
// helper instead.
func decodeRecorderJSON[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.NewDecoder(rec.Result().Body).Decode(&out); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	return out
}

// newServerTestAutoEmbedWorker builds a Worker wired to a fresh, real
// embeddings store (no Ollama client needed — these tests never call
// Enqueue/process, they only reach the store via Worker.Store() to seed/
// inspect rows directly). Mirrors internal/mcp/provenance_test.go's
// newTestAutoEmbedWorker.
func newServerTestAutoEmbedWorker(t *testing.T) *embed.Worker {
	t.Helper()
	embStore, err := embed.OpenStore(filepath.Join(t.TempDir(), "emb.db"))
	if err != nil {
		t.Fatalf("embed.OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = embStore.Close() })
	return embed.NewWorker(embStore, nil, "test-model", 3, 0, nil)
}

// withIsolatedAuditLog points the real audit.Append/audit.Read at a temp
// HOME for the duration of the test (mirrors internal/audit's own test
// pattern, audit_test.go) so these tests can assert against the REAL audit
// log — internal/server has no injectable audit seam the way internal/mcp
// does — without touching the developer's actual
// ~/.local/state/omnia/audit.jsonl.
func withIsolatedAuditLog(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	t.Cleanup(func() { os.Setenv("HOME", origHome) })
}

// TestHandleDeleteObservation_HardDelete_FansOutEmbedPurgeAndAudits closes the
// coverage gap flagged in the omnia-provenance-foundation review (Blocking
// 1): before this fix, HTTP DELETE /observations/{id}?hard=true called
// store.DeleteObservation directly and orphaned the embedding vector with no
// audit entry at all. It must now purge the vector and audit exactly like
// MCP mem_delete does (internal/mcp/provenance_test.go's
// TestHandleDelete_HardDelete_FansOutEmbedPurgeAndAuditsHardDelete).
func TestHandleDeleteObservation_HardDelete_FansOutEmbedPurgeAndAudits(t *testing.T) {
	withIsolatedAuditLog(t)

	st := newServerTestStore(t)
	srv := New(st, 0)
	worker := newServerTestAutoEmbedWorker(t)
	srv.SetAutoEmbed(worker)
	h := srv.Handler()

	createReq := httptest.NewRequest(http.MethodPost, "/sessions", strings.NewReader(`{"id":"s-hard-purge","project":"engram","directory":"/work/engram"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	h.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("expected session create 201, got %d body=%s", createRec.Code, createRec.Body.String())
	}

	obsReq := httptest.NewRequest(http.MethodPost, "/observations", strings.NewReader(`{"session_id":"s-hard-purge","type":"decision","title":"embed purge fan-out via HTTP","content":"content to purge","project":"engram"}`))
	obsReq.Header.Set("Content-Type", "application/json")
	obsRec := httptest.NewRecorder()
	h.ServeHTTP(obsRec, obsReq)
	if obsRec.Code != http.StatusCreated {
		t.Fatalf("expected observation create 201, got %d body=%s", obsRec.Code, obsRec.Body.String())
	}
	created := decodeRecorderJSON[map[string]any](t, obsRec)
	obsIDFloat, ok := created["id"].(float64)
	if !ok {
		t.Fatalf("expected numeric id in create response, got %v", created["id"])
	}
	obsID := int64(obsIDFloat)

	obs, err := st.GetObservation(obsID)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	syncID := obs.SyncID

	ctx := context.Background()
	if err := worker.Store().Upsert(ctx, embed.Row{
		SyncID: syncID, ObsID: int(obsID), ContentHash: "h", Model: "test-model", Dim: 3,
		Vector: []float32{1, 0, 0}, EmbeddedAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed embed row: %v", err)
	}

	delReq := httptest.NewRequest(http.MethodDelete, "/observations/"+strconv.FormatInt(obsID, 10)+"?hard=true", nil)
	delRec := httptest.NewRecorder()
	h.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusOK {
		t.Fatalf("expected 200 hard delete, got %d body=%s", delRec.Code, delRec.Body.String())
	}

	// Embed vector must be physically purged.
	n, err := worker.Store().Count(ctx)
	if err != nil {
		t.Fatalf("embed Count: %v", err)
	}
	if n != 0 {
		t.Errorf("expected embed store count=0 after hard delete fan-out, got %d", n)
	}

	// The row itself must be gone.
	if _, err := st.GetObservation(obsID); err == nil {
		t.Fatal("expected observation row to be purged after hard delete")
	}

	// Exactly one ActionHardDelete audit entry, actor="http".
	entries, err := audit.EntriesForObservation(int(obsID))
	if err != nil {
		t.Fatalf("EntriesForObservation: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 audit entry, got %d: %+v", len(entries), entries)
	}
	got := entries[0]
	if got.Action != audit.ActionHardDelete {
		t.Errorf("Action = %q, want %q", got.Action, audit.ActionHardDelete)
	}
	if got.Actor != "http" {
		t.Errorf("Actor = %q, want %q", got.Actor, "http")
	}
	if got.SyncID != syncID {
		t.Errorf("SyncID = %q, want %q", got.SyncID, syncID)
	}
	if got.Result != "ok" {
		t.Errorf("Result = %q, want %q", got.Result, "ok")
	}
}

// TestHandleDeleteObservation_NoAutoEmbedWorker_HardDeleteStillSucceedsAndAudits
// pins the disabled (default) path explicitly: with no SetAutoEmbed call,
// a hard delete over HTTP must still succeed and still audit — the vector
// purge fan-out is a no-op, mirroring enqueueAutoEmbed's nil-guard
// convention.
func TestHandleDeleteObservation_NoAutoEmbedWorker_HardDeleteStillSucceedsAndAudits(t *testing.T) {
	withIsolatedAuditLog(t)

	st := newServerTestStore(t)
	h := New(st, 0).Handler()

	createReq := httptest.NewRequest(http.MethodPost, "/sessions", strings.NewReader(`{"id":"s-hard-noembed","project":"engram","directory":"/work/engram"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	h.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("expected session create 201, got %d body=%s", createRec.Code, createRec.Body.String())
	}

	obsReq := httptest.NewRequest(http.MethodPost, "/observations", strings.NewReader(`{"session_id":"s-hard-noembed","type":"decision","title":"nil auto-embed http test","content":"content","project":"engram"}`))
	obsReq.Header.Set("Content-Type", "application/json")
	obsRec := httptest.NewRecorder()
	h.ServeHTTP(obsRec, obsReq)
	if obsRec.Code != http.StatusCreated {
		t.Fatalf("expected observation create 201, got %d body=%s", obsRec.Code, obsRec.Body.String())
	}
	created := decodeRecorderJSON[map[string]any](t, obsRec)
	obsID := int64(created["id"].(float64))

	delReq := httptest.NewRequest(http.MethodDelete, "/observations/"+strconv.FormatInt(obsID, 10)+"?hard=true", nil)
	delRec := httptest.NewRecorder()
	h.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusOK {
		t.Fatalf("expected 200 hard delete, got %d body=%s", delRec.Code, delRec.Body.String())
	}

	entries, err := audit.EntriesForObservation(int(obsID))
	if err != nil {
		t.Fatalf("EntriesForObservation: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != audit.ActionHardDelete {
		t.Fatalf("expected 1 ActionHardDelete audit entry, got %+v", entries)
	}
	if entries[0].Result != "ok" {
		t.Errorf("Result = %q, want %q", entries[0].Result, "ok")
	}
}
