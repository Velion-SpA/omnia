package embed

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// fakeABEmbedder maps known text -> a preconfigured vector so RunModelAB's
// ranking/recall math can be exercised deterministically, with no HTTP or
// live Ollama involved.
type fakeABEmbedder struct {
	vectors map[string][]float32
	err     error
}

func (f *fakeABEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	vec, ok := f.vectors[text]
	if !ok {
		return nil, fmt.Errorf("fakeABEmbedder: no vector configured for %q", text)
	}
	return vec, nil
}

func TestLoadABPairs_GoldenSet(t *testing.T) {
	pairs, err := LoadABPairs("testdata/ab_pairs.json")
	if err != nil {
		t.Fatalf("LoadABPairs: %v", err)
	}
	if len(pairs) < 30 {
		t.Errorf("golden set size: got %d, want >= 30", len(pairs))
	}
	seen := map[string]bool{}
	for _, p := range pairs {
		if p.ID == "" || p.QueryES == "" || p.MemoryEN == "" {
			t.Errorf("pair %+v has an empty field", p)
		}
		if seen[p.ID] {
			t.Errorf("duplicate pair id %q", p.ID)
		}
		seen[p.ID] = true
	}
}

func TestLoadABPairs_MissingFile(t *testing.T) {
	if _, err := LoadABPairs(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("expected error for a missing ab-pairs file, got nil")
	}
}

func TestLoadABPairs_EmptyArray(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(path, []byte("[]"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	if _, err := LoadABPairs(path); err == nil {
		t.Error("expected error for an empty pairs array, got nil")
	}
}

func TestRunModelAB_PerfectRecallWhenQueryMatchesOwnMemory(t *testing.T) {
	pairs := []ABPair{
		{ID: "a", QueryES: "qa", MemoryEN: "ma"},
		{ID: "b", QueryES: "qb", MemoryEN: "mb"},
		{ID: "c", QueryES: "qc", MemoryEN: "mc"},
	}
	// Orthonormal basis: each pair's query and its OWN memory map to the same
	// basis vector, so cosine similarity is 1 for the correct pair and 0 for
	// every other pair — recall@1 must be perfect.
	fake := &fakeABEmbedder{vectors: map[string][]float32{
		"qa": {1, 0, 0}, "ma": {1, 0, 0},
		"qb": {0, 1, 0}, "mb": {0, 1, 0},
		"qc": {0, 0, 1}, "mc": {0, 0, 1},
	}}

	result, err := RunModelAB(context.Background(), "fake-model", fake, pairs, 1)
	if err != nil {
		t.Fatalf("RunModelAB: %v", err)
	}
	if result.Hits != 3 || result.Total != 3 || result.RecallAtK != 1.0 {
		t.Errorf("result: %+v, want Hits=3 Total=3 RecallAtK=1.0", result)
	}
	if result.Model != "fake-model" || result.K != 1 {
		t.Errorf("result metadata: got model=%q k=%d", result.Model, result.K)
	}
}

func TestRunModelAB_MissRanksBelowK(t *testing.T) {
	pairs := []ABPair{
		{ID: "a", QueryES: "qa", MemoryEN: "ma"},
		{ID: "b", QueryES: "qb", MemoryEN: "mb"},
	}
	// "qa" is deliberately closest to "mb" (the WRONG memory), so with k=1 it
	// must count as a miss while "qb" (closest to its own memory) still hits.
	fake := &fakeABEmbedder{vectors: map[string][]float32{
		"qa": {0, 1, 0}, "ma": {1, 0, 0},
		"qb": {0, 1, 0}, "mb": {0, 1, 0},
	}}

	result, err := RunModelAB(context.Background(), "fake-model", fake, pairs, 1)
	if err != nil {
		t.Fatalf("RunModelAB: %v", err)
	}
	if result.Hits != 1 {
		t.Errorf("hits: got %d, want 1 (qa misses, qb hits)", result.Hits)
	}
	if result.RecallAtK != 0.5 {
		t.Errorf("recall@1: got %v, want 0.5", result.RecallAtK)
	}
}

func TestRunModelAB_EmptyPairs(t *testing.T) {
	if _, err := RunModelAB(context.Background(), "m", &fakeABEmbedder{}, nil, 10); err == nil {
		t.Error("expected error for empty pairs, got nil")
	}
}

func TestRunModelAB_ZeroK(t *testing.T) {
	pairs := []ABPair{{ID: "a", QueryES: "qa", MemoryEN: "ma"}}
	fake := &fakeABEmbedder{vectors: map[string][]float32{"qa": {1}, "ma": {1}}}
	if _, err := RunModelAB(context.Background(), "m", fake, pairs, 0); err == nil {
		t.Error("expected error for k<=0, got nil")
	}
}

func TestRunModelAB_PropagatesEmbedError(t *testing.T) {
	pairs := []ABPair{{ID: "a", QueryES: "qa", MemoryEN: "ma"}}
	fake := &fakeABEmbedder{err: fmt.Errorf("simulated ollama down")}
	if _, err := RunModelAB(context.Background(), "m", fake, pairs, 10); err == nil {
		t.Error("expected the embed error to propagate, got nil")
	}
}
