package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/velion/omnia/internal/config"
)

func writeTempConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}

func TestEmbeddings_DefaultsDisabled(t *testing.T) {
	// A config with no embeddings section must default to disabled, with the
	// standard local Ollama defaults filled in.
	path := writeTempConfig(t, "engram:\n  base_url: http://127.0.0.1:7437\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Embeddings.Enabled {
		t.Error("Embeddings.Enabled: got true, want false by default")
	}
	if cfg.Embeddings.BaseURL != "http://localhost:11434" {
		t.Errorf("BaseURL default: got %q", cfg.Embeddings.BaseURL)
	}
	// jina-embeddings-v2-base-es replaces nomic-embed-text as the default: a
	// purpose-built ES<->EN shared-space model with 768-dim (same as nomic, so
	// no vector-store schema change) and real 8192-token context. See engram
	// obs #1401 (supersedes design.md D3's original nomic pick).
	if cfg.Embeddings.Model != "jina/jina-embeddings-v2-base-es" {
		t.Errorf("Model default: got %q, want jina/jina-embeddings-v2-base-es", cfg.Embeddings.Model)
	}
	if cfg.Embeddings.Dim != 768 {
		t.Errorf("Dim default: got %d, want 768", cfg.Embeddings.Dim)
	}
	if cfg.Embeddings.DBPath == "" {
		t.Error("DBPath default: got empty")
	}
}

func TestEmbeddings_ParsesEnabled(t *testing.T) {
	path := writeTempConfig(t, "embeddings:\n  enabled: true\n  model: mxbai-embed-large\n  dim: 1024\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Embeddings.Enabled {
		t.Error("Embeddings.Enabled: got false, want true")
	}
	if cfg.Embeddings.Model != "mxbai-embed-large" {
		t.Errorf("Model: got %q", cfg.Embeddings.Model)
	}
	if cfg.Embeddings.Dim != 1024 {
		t.Errorf("Dim: got %d, want 1024", cfg.Embeddings.Dim)
	}
}
