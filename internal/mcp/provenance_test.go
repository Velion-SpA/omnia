package mcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	mcppkg "github.com/mark3labs/mcp-go/mcp"
	"github.com/velion/omnia/internal/audit"
	"github.com/velion/omnia/internal/embed"
	"github.com/velion/omnia/internal/store"
)

// captureAudit overrides the auditAppend seam for the duration of the test
// and returns the entries recorded, in call order.
func captureAudit(t *testing.T) *[]audit.Entry {
	t.Helper()
	var entries []audit.Entry
	orig := auditAppend
	auditAppend = func(e audit.Entry) {
		entries = append(entries, e)
	}
	t.Cleanup(func() { auditAppend = orig })
	return &entries
}

// ─── mem_save: write-time provenance + audit (omnia-provenance-foundation, phase 3) ───

func TestHandleSave_ClassifiesSourceAndAppendsWriteAudit(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	entries := captureAudit(t)
	h := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	res, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "Provenance write test",
		"content": "content body",
		"type":    "manual",
		"project": "engram",
		"source":  "ingest:web",
	}}})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected save error: %s", callResultText(t, res))
	}

	obs, err := s.RecentObservations("engram", "project", 5)
	if err != nil {
		t.Fatalf("recent observations: %v", err)
	}
	if len(obs) != 1 {
		t.Fatalf("expected 1 observation, got %d", len(obs))
	}
	if obs[0].Source == nil || *obs[0].Source != "ingest:web" {
		t.Fatalf("persisted Source = %v, want %q", obs[0].Source, "ingest:web")
	}
	if obs[0].TrustTag == nil || *obs[0].TrustTag != "ingest:web" {
		t.Fatalf("persisted TrustTag = %v, want %q", obs[0].TrustTag, "ingest:web")
	}

	if len(*entries) != 1 {
		t.Fatalf("expected exactly 1 audit entry, got %d: %+v", len(*entries), *entries)
	}
	got := (*entries)[0]
	if got.Action != audit.ActionWrite {
		t.Errorf("Action = %q, want %q", got.Action, audit.ActionWrite)
	}
	if got.Source != "ingest:web" {
		t.Errorf("audit Source = %q, want %q", got.Source, "ingest:web")
	}
	if got.TrustTag != "ingest:web" {
		t.Errorf("audit TrustTag = %q, want %q", got.TrustTag, "ingest:web")
	}
	if got.SyncID == "" {
		t.Error("audit SyncID must not be empty")
	}
	if got.Result != "ok" {
		t.Errorf("audit Result = %q, want %q", got.Result, "ok")
	}
}

func TestHandleSave_MissingSource_AuditsUnverified(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	entries := captureAudit(t)
	h := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	res, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "Provenance default test",
		"content": "content body",
		"type":    "manual",
		"project": "engram",
	}}})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected save error: %s", callResultText(t, res))
	}

	if len(*entries) != 1 {
		t.Fatalf("expected exactly 1 audit entry, got %d", len(*entries))
	}
	if (*entries)[0].TrustTag != "unverified" {
		t.Errorf("TrustTag = %q, want %q", (*entries)[0].TrustTag, "unverified")
	}
	if (*entries)[0].Source != "" {
		t.Errorf("Source = %q, want empty (no source provided)", (*entries)[0].Source)
	}
}

// TestHandleSave_AuditAppendFailure_DoesNotBlockSave (omnia-provenance-foundation,
// phase 3.3): points the REAL audit.Append (not the test seam) at an
// unwritable log directory (HOME resolves under a path component that is a
// regular file, so os.MkdirAll fails) and asserts the save still succeeds —
// an audit-append failure must never block or roll back mem_save.
func TestHandleSave_AuditAppendFailure_DoesNotBlockSave(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("create blocker file: %v", err)
	}
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", blocker) // HOME is a FILE, not a dir: mkdir under it always fails
	t.Cleanup(func() { os.Setenv("HOME", origHome) })

	s := newMCPTestStore(t)
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	h := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	res, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "Audit failure resilience test",
		"content": "content body",
		"type":    "manual",
		"project": "engram",
		"source":  "user",
	}}})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("save must succeed even when the audit log is unwritable: %s", callResultText(t, res))
	}

	obs, err := s.RecentObservations("engram", "project", 5)
	if err != nil {
		t.Fatalf("recent observations: %v", err)
	}
	if len(obs) != 1 {
		t.Fatalf("expected the observation to be persisted despite the audit failure, got %d", len(obs))
	}
}

// ─── mem_delete: embed purge fan-out + audit (omnia-provenance-foundation, phase 5) ───

// newTestAutoEmbedWorker builds a Worker wired to a fresh, real embeddings
// store (no Ollama client needed — this test never calls Enqueue/process, it
// only reaches the store via Worker.Store() to seed/inspect rows directly).
func newTestAutoEmbedWorker(t *testing.T) *embed.Worker {
	t.Helper()
	embStore, err := embed.OpenStore(filepath.Join(t.TempDir(), "emb.db"))
	if err != nil {
		t.Fatalf("embed.OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = embStore.Close() })
	return embed.NewWorker(embStore, nil, "test-model", 3, 0, nil)
}

func TestHandleDelete_HardDelete_FansOutEmbedPurgeAndAuditsHardDelete(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	saveH := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
	saveRes, err := saveH(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "embed purge fan-out test",
		"content": "content to purge",
		"type":    "manual",
		"project": "engram",
	}}})
	if err != nil || saveRes.IsError {
		t.Fatalf("save setup failed: err=%v res=%v", err, saveRes)
	}
	obs, err := s.RecentObservations("engram", "project", 1)
	if err != nil || len(obs) != 1 {
		t.Fatalf("recent observations: %v (len=%d)", err, len(obs))
	}
	id := obs[0].ID
	syncID := obs[0].SyncID

	worker := newTestAutoEmbedWorker(t)
	ctx := context.Background()
	if err := worker.Store().Upsert(ctx, embed.Row{
		SyncID: syncID, ObsID: int(id), ContentHash: "h", Model: "test-model", Dim: 3,
		Vector: []float32{1, 0, 0}, EmbeddedAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed embed row: %v", err)
	}

	entries := captureAudit(t)
	deleteH := handleDelete(s, MCPConfig{AutoEmbed: worker})
	delRes, err := deleteH(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"id":          float64(id),
		"hard_delete": true,
	}}})
	if err != nil {
		t.Fatalf("handleDelete error: %v", err)
	}
	if delRes.IsError {
		t.Fatalf("unexpected delete error: %s", callResultText(t, delRes))
	}

	// Embed vector must be physically purged.
	n, err := worker.Store().Count(ctx)
	if err != nil {
		t.Fatalf("embed Count: %v", err)
	}
	if n != 0 {
		t.Errorf("expected embed store count=0 after hard delete fan-out, got %d", n)
	}

	// Exactly one ActionHardDelete audit entry.
	if len(*entries) != 1 {
		t.Fatalf("expected exactly 1 audit entry, got %d: %+v", len(*entries), *entries)
	}
	got := (*entries)[0]
	if got.Action != audit.ActionHardDelete {
		t.Errorf("Action = %q, want %q", got.Action, audit.ActionHardDelete)
	}
	if got.SyncID != syncID {
		t.Errorf("audit SyncID = %q, want %q", got.SyncID, syncID)
	}
	if got.Result != "ok" {
		t.Errorf("audit Result = %q, want %q", got.Result, "ok")
	}
}

func TestHandleDelete_SoftDelete_DoesNotFanOutEmbedPurge_ButAuditsSoftDelete(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	saveH := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
	saveRes, err := saveH(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "soft delete no fan-out test",
		"content": "content that stays embedded",
		"type":    "manual",
		"project": "engram",
	}}})
	if err != nil || saveRes.IsError {
		t.Fatalf("save setup failed: err=%v res=%v", err, saveRes)
	}
	obs, err := s.RecentObservations("engram", "project", 1)
	if err != nil || len(obs) != 1 {
		t.Fatalf("recent observations: %v (len=%d)", err, len(obs))
	}
	id := obs[0].ID
	syncID := obs[0].SyncID

	worker := newTestAutoEmbedWorker(t)
	ctx := context.Background()
	if err := worker.Store().Upsert(ctx, embed.Row{
		SyncID: syncID, ObsID: int(id), ContentHash: "h", Model: "test-model", Dim: 3,
		Vector: []float32{1, 0, 0}, EmbeddedAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed embed row: %v", err)
	}

	entries := captureAudit(t)
	deleteH := handleDelete(s, MCPConfig{AutoEmbed: worker})
	delRes, err := deleteH(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"id": float64(id),
	}}})
	if err != nil {
		t.Fatalf("handleDelete error: %v", err)
	}
	if delRes.IsError {
		t.Fatalf("unexpected delete error: %s", callResultText(t, delRes))
	}

	// Embed vector must be UNTOUCHED for a soft delete.
	n, err := worker.Store().Count(ctx)
	if err != nil {
		t.Fatalf("embed Count: %v", err)
	}
	if n != 1 {
		t.Errorf("expected embed store count=1 (untouched) after soft delete, got %d", n)
	}

	if len(*entries) != 1 {
		t.Fatalf("expected exactly 1 audit entry, got %d: %+v", len(*entries), *entries)
	}
	if (*entries)[0].Action != audit.ActionSoftDelete {
		t.Errorf("Action = %q, want %q", (*entries)[0].Action, audit.ActionSoftDelete)
	}
}

// TestHandleDelete_NilAutoEmbed_HardDeleteStillSucceeds (omnia-provenance-foundation,
// phase 5): when embeddings are disabled (cfg.AutoEmbed nil), a hard delete
// must still succeed and still audit — the fan-out is a no-op, mirroring
// enqueueAutoEmbed's existing nil-guard convention.
func TestHandleDelete_NilAutoEmbed_HardDeleteStillSucceeds(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	if err := s.CreateSession("sess-nilembed", "engram", "/tmp/nilembed"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	id, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess-nilembed",
		Type:      "manual",
		Title:     "nil auto-embed test",
		Content:   "content",
		Project:   "engram",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	entries := captureAudit(t)
	deleteH := handleDelete(s, MCPConfig{})
	delRes, err := deleteH(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"id":          float64(id),
		"hard_delete": true,
	}}})
	if err != nil {
		t.Fatalf("handleDelete error: %v", err)
	}
	if delRes.IsError {
		t.Fatalf("unexpected delete error: %s", callResultText(t, delRes))
	}
	if len(*entries) != 1 || (*entries)[0].Action != audit.ActionHardDelete {
		t.Fatalf("expected 1 ActionHardDelete audit entry, got %+v", *entries)
	}
}
