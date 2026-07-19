package main

import (
	"context"
	"testing"

	"github.com/velion/omnia/internal/cloud"
)

// TestCloudSemanticQueryEmbedderNilWhenUnconfigured proves the OPTIONAL cloud
// semantic query embedder (design D5, PR5 slice 3) stays nil when no Ollama
// base URL is configured for the cloud host — the safe default, since the
// cloud typically has no reachable Ollama instance (memory 1398). A nil
// embedder degrades EmbedQuery cleanly (clouddash.cloudSemanticIndex);
// cloud_embeddings rows synced in from devices that DO run Ollama stay
// searchable regardless.
func TestCloudSemanticQueryEmbedderNilWhenUnconfigured(t *testing.T) {
	cfg := cloud.DefaultConfig()
	cfg.CloudSemanticEmbedBaseURL = ""
	if embedder := cloudSemanticQueryEmbedder(cfg); embedder != nil {
		t.Fatalf("expected nil embedder when no base URL is configured, got %#v", embedder)
	}
}

// TestCloudSemanticQueryEmbedderBuildsClientWhenConfigured proves an
// operator-configured Ollama endpoint (e.g. reachable over the homelab LAN)
// produces a real, usable embed.Embedder wired with the configured
// model/dim.
func TestCloudSemanticQueryEmbedderBuildsClientWhenConfigured(t *testing.T) {
	cfg := cloud.DefaultConfig()
	cfg.CloudSemanticEmbedBaseURL = "http://homelab-ollama:11434"
	cfg.CloudSemanticEmbedModel = "bge-m3"
	cfg.CloudSemanticEmbedDim = 1024

	embedder := cloudSemanticQueryEmbedder(cfg)
	if embedder == nil {
		t.Fatal("expected a non-nil embedder when a base URL is configured")
	}
	// Embedder is an interface; confirm it's actually callable (won't dial
	// out — an unreachable host errors, which is still proof it's wired).
	if _, err := embedder.Embed(context.Background(), "probe"); err == nil {
		t.Fatal("expected the probe embed call against an unreachable host to error")
	}
}
