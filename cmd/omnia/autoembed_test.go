package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/velion/omnia/internal/config"
)

// TestBuildAutoEmbedWorker_DisabledReturnsNil pins the byte-for-byte-today
// guarantee (human-like-memory PR4 review fix, closing the test-coverage
// asymmetry with buildRecallService): embeddings.enabled=false (the
// default) must yield a nil *embed.Worker, so neither cmdMCP nor cmdServe
// ever opens the embeddings store or constructs an Ollama HTTP client on
// the default path.
func TestBuildAutoEmbedWorker_DisabledReturnsNil(t *testing.T) {
	got := buildAutoEmbedWorker(config.EmbeddingsConfig{Enabled: false})
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

	got := buildAutoEmbedWorker(embCfg)
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
	})
	if got != nil {
		t.Fatal("buildAutoEmbedWorker: expected nil when the embeddings store cannot be opened")
	}
}
