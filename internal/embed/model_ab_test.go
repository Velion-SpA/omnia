//go:build ollama_live

// This file is the bilingual model-selection GATE described in design.md's
// "Bilingual via embeddings first, gated empirically" decision (superseded by
// engram obs #1401 to compare jina-embeddings-v2-base-es vs bge-m3, rather
// than validating nomic-embed-text alone).
//
// It is double-gated so it can NEVER affect a normal `go test ./...` run or
// CI, and can never block a build without a live Ollama:
//
//  1. Build tag `ollama_live` — this file is invisible to `go build`/`go vet`/
//     `go test ./...` unless that tag is explicitly passed.
//  2. OLLAMA_LIVE_TEST=1 env var — an explicit opt-in even when built with
//     the tag, so a stray `-tags ollama_live` in a dev's shell doesn't
//     silently start hitting a real Ollama server.
//
// Run it once jina/jina-embeddings-v2-base-es and bge-m3 are pulled:
//
//	OLLAMA_LIVE_TEST=1 go test -tags ollama_live ./internal/embed/... -run TestModelAB -v
package embed

import (
	"context"
	"os"
	"testing"
)

func TestModelAB_JinaVsBGEM3(t *testing.T) {
	if os.Getenv("OLLAMA_LIVE_TEST") != "1" {
		t.Skip("OLLAMA_LIVE_TEST not set — skipping live-Ollama bilingual model A/B gate")
	}

	baseURL := os.Getenv("OLLAMA_BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}

	pairs, err := LoadABPairs("testdata/ab_pairs.json")
	if err != nil {
		t.Fatalf("LoadABPairs: %v", err)
	}

	const k = 10
	// design.md's bilingual gate: recall@10 >= 0.8 on the golden ES-query/
	// EN-memory set. Below this, fall back per engram obs #1401's runner-up.
	const recallGate = 0.8

	candidates := []struct {
		model string
		dim   int
	}{
		{"jina/jina-embeddings-v2-base-es", 768}, // primary (obs #1401)
		{"bge-m3", 1024},                          // runner-up (obs #1401)
	}

	for _, c := range candidates {
		c := c
		t.Run(c.model, func(t *testing.T) {
			client := New(baseURL, c.model, c.dim)
			result, err := RunModelAB(context.Background(), c.model, client, pairs, k)
			if err != nil {
				t.Fatalf("RunModelAB(%s): %v", c.model, err)
			}
			t.Logf("%s: recall@%d = %.3f (%d/%d hits)", c.model, k, result.RecallAtK, result.Hits, result.Total)
			if result.RecallAtK < recallGate {
				t.Errorf("%s: recall@%d = %.3f, want >= %.2f (design.md bilingual gate)", c.model, k, result.RecallAtK, recallGate)
			}
		})
	}
}
