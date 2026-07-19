package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/velion/omnia/internal/config"
)

// TestBuildRecallService_DisabledReturnsNil locks the D7 rollback guarantee
// at the cmdMCP construction seam (task 3.6): recall.enabled=false (the
// default) must yield a nil *recall.Service, so cmdMCP never opens the
// embeddings store or constructs an Ollama HTTP client on the default path.
func TestBuildRecallService_DisabledReturnsNil(t *testing.T) {
	s, err := storeNew(testConfig(t))
	if err != nil {
		t.Fatalf("storeNew: %v", err)
	}
	defer s.Close()

	got := buildRecallService(s, config.RecallConfig{Enabled: false}, config.EmbeddingsConfig{})
	if got != nil {
		t.Fatalf("buildRecallService(disabled) = %v, want nil (recall.enabled=false must not construct a Service)", got)
	}
}

// TestBuildRecallService_EnabledBuildsWiredService is the flag-ON
// counterpart: recall.enabled=true must produce a *recall.Service with both
// the lexical and semantic sides wired, using RecallConfig's fusion params.
func TestBuildRecallService_EnabledBuildsWiredService(t *testing.T) {
	s, err := storeNew(testConfig(t))
	if err != nil {
		t.Fatalf("storeNew: %v", err)
	}
	defer s.Close()

	embCfg := config.EmbeddingsConfig{
		BaseURL: "http://127.0.0.1:11434",
		Model:   "jina/jina-embeddings-v2-base-es",
		Dim:     768,
		DBPath:  filepath.Join(t.TempDir(), "embeddings.db"),
	}
	recallCfg := config.RecallConfig{
		Enabled:     true,
		RRFK:        60,
		DenseK:      5,
		StrongFloor: 0.65,
		BaseFloor:   0.55,
		MaxResults:  50,
	}

	got := buildRecallService(s, recallCfg, embCfg)
	if got == nil {
		t.Fatal("buildRecallService(enabled) = nil, want a non-nil *recall.Service")
	}
	if got.Lexical == nil {
		t.Error("expected non-nil Lexical (store-backed LexicalSearcher)")
	}
	if got.Semantic == nil {
		t.Error("expected non-nil Semantic (embed.Searcher) when the embeddings store opens successfully")
	}
	if got.Params.RRFK != recallCfg.RRFK ||
		got.Params.DenseK != recallCfg.DenseK ||
		got.Params.StrongFloor != recallCfg.StrongFloor ||
		got.Params.BaseFloor != recallCfg.BaseFloor ||
		got.Params.MaxResults != recallCfg.MaxResults {
		t.Errorf("Params = %+v, want fields copied from RecallConfig %+v", got.Params, recallCfg)
	}
}

// TestBuildRecallService_EnabledButStoreUnavailableReturnsNil covers the
// graceful-degradation branch: if the embeddings store can't even be
// opened, buildRecallService must fail closed to nil (routing mem_search
// back through the already-tested cfg.Recall == nil / FTS5-only path)
// rather than starting a half-wired recall.Service.
func TestBuildRecallService_EnabledButStoreUnavailableReturnsNil(t *testing.T) {
	s, err := storeNew(testConfig(t))
	if err != nil {
		t.Fatalf("storeNew: %v", err)
	}
	defer s.Close()

	// A regular file where a directory component is expected forces
	// embed.OpenStore's os.MkdirAll to fail deterministically.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}

	got := buildRecallService(s, config.RecallConfig{Enabled: true}, config.EmbeddingsConfig{
		DBPath: filepath.Join(blocker, "embeddings.db"),
	})
	if got != nil {
		t.Fatal("buildRecallService: expected nil when the embeddings store cannot be opened")
	}
}
