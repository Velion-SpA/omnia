package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/embed"
	"github.com/velion/omnia/internal/store"
)

// TestBuildAutoEmbedWorker_DisabledReturnsNil pins the byte-for-byte-today
// guarantee (human-like-memory PR4 review fix, closing the test-coverage
// asymmetry with buildRecallService): embeddings.enabled=false (the
// default) must yield a nil *embed.Worker, so neither cmdMCP nor cmdServe
// ever opens the embeddings store or constructs an Ollama HTTP client on
// the default path.
func TestBuildAutoEmbedWorker_DisabledReturnsNil(t *testing.T) {
	got := buildAutoEmbedWorker(config.EmbeddingsConfig{Enabled: false}, nil, "")
	if got != nil {
		t.Fatalf("buildAutoEmbedWorker(disabled) = %v, want nil (embeddings.enabled=false must not construct a Worker)", got)
	}
}

// TestBuildAutoEmbedWorker_EnabledBuildsWorker is the flag-ON counterpart:
// embeddings.enabled=true with a valid DBPath must produce a non-nil
// *embed.Worker.
func TestBuildAutoEmbedWorker_EnabledBuildsWorker(t *testing.T) {
	embCfg := config.EmbeddingsConfig{
		Enabled: true,
		BaseURL: "http://127.0.0.1:11434",
		Model:   "jina/jina-embeddings-v2-base-es",
		Dim:     768,
		DBPath:  filepath.Join(t.TempDir(), "embeddings.db"),
	}

	got := buildAutoEmbedWorker(embCfg, nil, "")
	if got == nil {
		t.Fatal("buildAutoEmbedWorker(enabled) = nil, want a non-nil *embed.Worker")
	}
}

// TestBuildAutoEmbedWorker_EnabledButStoreUnavailableReturnsNil covers the
// graceful-degradation branch: if the embeddings store can't even be
// opened, buildAutoEmbedWorker must fail closed to nil (no worker starts,
// the periodic `omnia embed`/Reconcile run still catches anything saved
// meanwhile) rather than starting a half-wired worker.
func TestBuildAutoEmbedWorker_EnabledButStoreUnavailableReturnsNil(t *testing.T) {
	// A regular file where a directory component is expected forces
	// embed.OpenStore's os.MkdirAll to fail deterministically.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}

	got := buildAutoEmbedWorker(config.EmbeddingsConfig{
		Enabled: true,
		DBPath:  filepath.Join(blocker, "embeddings.db"),
	}, nil, "")
	if got != nil {
		t.Fatal("buildAutoEmbedWorker: expected nil when the embeddings store cannot be opened")
	}
}

// TestBuildAutoEmbedWorker_WithStoreStillBuildsWorker (human-like-memory PR5
// slice 2) asserts that passing a non-nil *store.Store does not change the
// enabled-path construction outcome — buildAutoEmbedWorker still returns a
// non-nil *embed.Worker (the hook is installed internally; its enqueue
// behavior is covered by TestBuildEmbeddingSyncHook_EnqueuesSyncMutation
// below, extracted so it does not depend on a real Ollama HTTP round trip).
func TestBuildAutoEmbedWorker_WithStoreStillBuildsWorker(t *testing.T) {
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	cfg.DataDir = t.TempDir()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	embCfg := config.EmbeddingsConfig{
		Enabled: true,
		BaseURL: "http://127.0.0.1:11434",
		Model:   "jina/jina-embeddings-v2-base-es",
		Dim:     3,
		DBPath:  filepath.Join(t.TempDir(), "embeddings.db"),
	}
	worker := buildAutoEmbedWorker(embCfg, s, "")
	if worker == nil {
		t.Fatal("buildAutoEmbedWorker: expected a non-nil *embed.Worker")
	}
}

// TestBuildEmbeddingSyncHook_EnqueuesSyncMutation (human-like-memory PR5
// slice 2) asserts the hook buildAutoEmbedWorker installs actually records a
// SyncEntityEmbedding sync mutation via store.EnqueueEmbeddingMutation for a
// successfully upserted row.
func TestBuildEmbeddingSyncHook_EnqueuesSyncMutation(t *testing.T) {
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	cfg.DataDir = t.TempDir()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	if err := s.CreateSession("ses-hook", "proj-hook", "/tmp/hook"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.EnrollProject("proj-hook"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}

	hook := buildEmbeddingSyncHook(s, nil)
	hook(embed.Row{
		SyncID:      "obs-hook-1",
		Project:     "proj-hook",
		Type:        "decision",
		Model:       "jina/jina-embeddings-v2-base-es",
		Dim:         3,
		Vector:      []float32{0.1, 0.2, 0.3},
		ContentHash: "hash-hook-1",
		UpdatedAt:   "2026-07-16 12:00:00",
	})

	pending, err := s.ListPendingSyncMutations(store.DefaultSyncTargetKey, 10)
	if err != nil {
		t.Fatalf("ListPendingSyncMutations: %v", err)
	}
	var found bool
	for _, m := range pending {
		if m.Entity == store.SyncEntityEmbedding && m.EntityKey == "obs-hook-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a pending embedding sync mutation for obs-hook-1; got %+v", pending)
	}
}
