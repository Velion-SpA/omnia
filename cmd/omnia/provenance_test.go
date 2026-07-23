package main

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/velion/omnia/internal/audit"
	"github.com/velion/omnia/internal/embed"
	"github.com/velion/omnia/internal/store"
)

// TestCmdDeleteObservation_HardDelete_FansOutEmbedPurge closes the CLI
// coverage gap flagged in the omnia-provenance-foundation review (Blocking
// 1): `omnia delete --hard` used to call store.DeleteObservation directly,
// silently orphaning the embedding vector. It must now purge the vector via
// the shared internal/purge.HardDeleteWithPurge helper — the same helper MCP
// mem_delete and HTTP DELETE ?hard=true go through — proving the CLI path is
// wired too, not just MCP.
//
// buildCLIEmbedPurgeStore is stubbed to point at a test-controlled
// embeddings.db (rather than depending on the developer machine's real
// ~/.config/omnia/config.yaml + ~/.local/share/omnia/embeddings.db) exactly
// like storeNew/loadAppConfigWithRecallAutodetect are stubbed elsewhere in
// this package.
func TestCmdDeleteObservation_HardDelete_FansOutEmbedPurge(t *testing.T) {
	// Isolate HOME: audit.Append writes to $HOME/.local/state/omnia/audit.jsonl.
	t.Setenv("HOME", t.TempDir())

	cfg := testConfig(t)

	seedStore, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := seedStore.CreateSession("s-cli-purge", "proj-cli-purge", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	id, err := seedStore.AddObservation(store.AddObservationParams{
		SessionID: "s-cli-purge",
		Type:      "decision",
		Title:     "cli hard delete purge",
		Content:   "content to purge",
		Project:   "proj-cli-purge",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	obs, err := seedStore.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	syncID := obs.SyncID
	if err := seedStore.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	embDBPath := filepath.Join(t.TempDir(), "emb.db")
	seedEmbStore, err := embed.OpenStore(embDBPath)
	if err != nil {
		t.Fatalf("embed.OpenStore: %v", err)
	}
	if err := seedEmbStore.Upsert(context.Background(), embed.Row{
		SyncID: syncID, ObsID: int(id), ContentHash: "h", Model: "test-model", Dim: 3,
		Vector: []float32{1, 0, 0}, EmbeddedAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed embed row: %v", err)
	}
	if err := seedEmbStore.Close(); err != nil {
		t.Fatalf("close seed embed store: %v", err)
	}

	oldBuild := buildCLIEmbedPurgeStore
	buildCLIEmbedPurgeStore = func(dataDir string) *embed.Store {
		es, err := embed.OpenStore(embDBPath)
		if err != nil {
			t.Fatalf("reopen embed store: %v", err)
		}
		return es
	}
	t.Cleanup(func() { buildCLIEmbedPurgeStore = oldBuild })

	withArgs(t, "engram", "delete", strconv.FormatInt(id, 10), "--hard")
	stdout, stderr := captureOutput(t, func() { cmdDelete(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, "hard-deleted") {
		t.Fatalf("expected hard-delete confirmation, got: %q", stdout)
	}

	// Embed vector must be physically purged.
	verifyEmbStore, err := embed.OpenStore(embDBPath)
	if err != nil {
		t.Fatalf("reopen embed store to verify: %v", err)
	}
	defer verifyEmbStore.Close()
	n, err := verifyEmbStore.Count(context.Background())
	if err != nil {
		t.Fatalf("embed Count: %v", err)
	}
	if n != 0 {
		t.Errorf("expected embed store count=0 after hard delete purge, got %d", n)
	}

	// Exactly one ActionHardDelete audit entry, actor="cli".
	entries, err := audit.EntriesForObservation(int(id))
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
	if got.Actor != "cli" {
		t.Errorf("Actor = %q, want %q", got.Actor, "cli")
	}
	if got.SyncID != syncID {
		t.Errorf("SyncID = %q, want %q", got.SyncID, syncID)
	}
	if got.Result != "ok" {
		t.Errorf("Result = %q, want %q", got.Result, "ok")
	}
}
