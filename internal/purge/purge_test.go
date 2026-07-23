package purge

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/velion/omnia/internal/audit"
	"github.com/velion/omnia/internal/embed"
	"github.com/velion/omnia/internal/store"
)

func newPurgeTestStore(t *testing.T) *store.Store {
	t.Helper()
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	cfg.DataDir = t.TempDir()

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newPurgeTestEmbedStore(t *testing.T) *embed.Store {
	t.Helper()
	embStore, err := embed.OpenStore(filepath.Join(t.TempDir(), "emb.db"))
	if err != nil {
		t.Fatalf("embed.OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = embStore.Close() })
	return embStore
}

// captureAudit returns an AuditAppender that records entries in call order,
// plus a pointer to the slice it appends into.
func captureAudit() (AuditAppender, *[]audit.Entry) {
	var entries []audit.Entry
	return func(e audit.Entry) { entries = append(entries, e) }, &entries
}

// failingEmbedPurger is an EmbedPurger test double whose DeleteBySyncID
// always fails — used to prove Blocking 2's error-propagation contract
// without needing to break a real *embed.Store's underlying sqlite
// connection to force a failure.
type failingEmbedPurger struct {
	err error
}

func (f failingEmbedPurger) DeleteBySyncID(ctx context.Context, syncID string) (int, error) {
	return 0, f.err
}

func seedObservation(t *testing.T, s *store.Store, title string) (id int64, syncID string) {
	t.Helper()
	if err := s.CreateSession("purge-sess-"+title, "purge-proj", "/tmp/purge"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	id, err := s.AddObservation(store.AddObservationParams{
		SessionID: "purge-sess-" + title,
		Type:      "manual",
		Title:     title,
		Content:   "content for " + title,
		Project:   "purge-proj",
		Source:    "user",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	return id, obs.SyncID
}

// TestHardDeleteWithPurge_Success proves the happy path required by Blocking
// 1: the row is physically purged, its embedding vector is purged via the
// fan-out, and exactly one audit entry with Result "ok" is recorded.
func TestHardDeleteWithPurge_Success(t *testing.T) {
	s := newPurgeTestStore(t)
	id, syncID := seedObservation(t, s, "hard-delete-success")

	embStore := newPurgeTestEmbedStore(t)
	ctx := context.Background()
	if err := embStore.Upsert(ctx, embed.Row{
		SyncID: syncID, ObsID: int(id), ContentHash: "h", Model: "test-model", Dim: 3,
		Vector: []float32{1, 0, 0}, EmbeddedAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed embed row: %v", err)
	}

	appendAudit, entries := captureAudit()
	if err := HardDeleteWithPurge(ctx, s, embStore, appendAudit, "http", id); err != nil {
		t.Fatalf("HardDeleteWithPurge: %v", err)
	}

	if _, err := s.GetObservation(id); err == nil {
		t.Fatal("expected observation row to be purged")
	}

	n, err := embStore.Count(ctx)
	if err != nil {
		t.Fatalf("embed Count: %v", err)
	}
	if n != 0 {
		t.Errorf("expected embed store count=0 after purge, got %d", n)
	}

	if len(*entries) != 1 {
		t.Fatalf("expected exactly 1 audit entry, got %d: %+v", len(*entries), *entries)
	}
	got := (*entries)[0]
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
	if got.Source != "user" {
		t.Errorf("Source = %q, want %q", got.Source, "user")
	}

	// The deletion_tombstones proof row must carry the actor too
	// (should-fix #5).
	var tombActor string
	if err := s.DB().QueryRow(`SELECT actor FROM deletion_tombstones WHERE sync_id = ?`, syncID).Scan(&tombActor); err != nil {
		t.Fatalf("query tombstone actor: %v", err)
	}
	if tombActor != "http" {
		t.Errorf("tombstone actor = %q, want %q", tombActor, "http")
	}
}

// TestHardDeleteWithPurge_NilEmbedStore_NoOpPurge_StillAudits mirrors
// enqueueAutoEmbed's nil-guard convention: when embeddings are disabled
// (embedStore == nil), the purge fan-out is a no-op — the delete still
// succeeds and still audits.
func TestHardDeleteWithPurge_NilEmbedStore_NoOpPurge_StillAudits(t *testing.T) {
	s := newPurgeTestStore(t)
	id, _ := seedObservation(t, s, "nil-embed-store")

	appendAudit, entries := captureAudit()
	if err := HardDeleteWithPurge(context.Background(), s, nil, appendAudit, "cli", id); err != nil {
		t.Fatalf("HardDeleteWithPurge: %v", err)
	}

	if _, err := s.GetObservation(id); err == nil {
		t.Fatal("expected observation row to be purged")
	}
	if len(*entries) != 1 || (*entries)[0].Result != "ok" {
		t.Fatalf("expected 1 audit entry with Result=ok, got %+v", *entries)
	}
}

// TestHardDeleteWithPurge_EmbedPurgeFails_SurfacesErrorAndAuditReflectsIt is
// the Blocking 2 regression guard: an embed purge failure must be
// propagated to the caller (errors.Is ErrEmbedPurgeFailed) AND recorded
// honestly in the audit entry's Result — never silently logged-and-ignored
// with Result unconditionally "ok". The row delete + tombstone must still
// have succeeded (not rolled back).
func TestHardDeleteWithPurge_EmbedPurgeFails_SurfacesErrorAndAuditReflectsIt(t *testing.T) {
	s := newPurgeTestStore(t)
	id, syncID := seedObservation(t, s, "embed-purge-fails")

	boom := errors.New("boom: embeddings db unavailable")
	appendAudit, entries := captureAudit()

	err := HardDeleteWithPurge(context.Background(), s, failingEmbedPurger{err: boom}, appendAudit, "mcp", id)
	if err == nil {
		t.Fatal("expected HardDeleteWithPurge to return a non-nil error when the embed purge fails")
	}
	if !errors.Is(err, ErrEmbedPurgeFailed) {
		t.Errorf("expected errors.Is(err, ErrEmbedPurgeFailed), got: %v", err)
	}

	// The store-level hard delete already committed — not rolled back.
	if _, getErr := s.GetObservation(id); getErr == nil {
		t.Fatal("expected observation row to still be purged despite the embed purge failure")
	}
	var tombCount int
	if qerr := s.DB().QueryRow(`SELECT COUNT(*) FROM deletion_tombstones WHERE sync_id = ?`, syncID).Scan(&tombCount); qerr != nil {
		t.Fatalf("query deletion_tombstones: %v", qerr)
	}
	if tombCount != 1 {
		t.Errorf("expected tombstone to still be written, got count=%d", tombCount)
	}

	if len(*entries) != 1 {
		t.Fatalf("expected exactly 1 audit entry, got %d: %+v", len(*entries), *entries)
	}
	if (*entries)[0].Result != "embed_purge_failed" {
		t.Errorf("audit Result = %q, want %q", (*entries)[0].Result, "embed_purge_failed")
	}
}

// TestHardDeleteWithPurge_NotFound_NoAudit proves a store-level delete
// failure (observation never existed) is returned as-is and never reaches
// the embed-purge or audit steps.
func TestHardDeleteWithPurge_NotFound_NoAudit(t *testing.T) {
	s := newPurgeTestStore(t)

	appendAudit, entries := captureAudit()
	err := HardDeleteWithPurge(context.Background(), s, nil, appendAudit, "cli", 999999)
	if err == nil {
		t.Fatal("expected an error for a non-existent observation id")
	}
	if !errors.Is(err, store.ErrObservationNotFound) {
		t.Errorf("expected errors.Is(err, store.ErrObservationNotFound), got: %v", err)
	}
	if len(*entries) != 0 {
		t.Errorf("expected no audit entries when the delete itself fails, got %+v", *entries)
	}
}
