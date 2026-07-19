package embed

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// ABPair is one ES-query -> EN-memory validation pair for the bilingual
// recall@k model-selection gate (design.md "Bilingual via embeddings first,
// gated empirically", superseded to compare jina-embeddings-v2-base-es vs
// bge-m3 per engram obs #1401).
type ABPair struct {
	ID       string `json:"id"`
	QueryES  string `json:"query_es"`
	MemoryEN string `json:"memory_en"`
}

// LoadABPairs reads a JSON array of ABPair from path (see
// testdata/ab_pairs.json for the golden ~30-pair ES<->EN set).
func LoadABPairs(path string) ([]ABPair, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("embed: load ab pairs: %w", err)
	}
	var pairs []ABPair
	if err := json.Unmarshal(data, &pairs); err != nil {
		return nil, fmt.Errorf("embed: parse ab pairs %s: %w", path, err)
	}
	if len(pairs) == 0 {
		return nil, fmt.Errorf("embed: ab pairs file %s contains no pairs", path)
	}
	return pairs, nil
}

// ABResult is the recall@k outcome for one embedding model over a pair set.
type ABResult struct {
	Model     string
	K         int
	Total     int
	Hits      int
	RecallAtK float64
}

// RunModelAB measures cross-lingual recall@k for emb over pairs: it embeds
// every pair's EN memory once (the candidate "document" set), then for each
// pair's ES query checks whether that query's OWN paired memory ranks within
// the top k of the full memory set by cosine similarity. This is the same
// evaluation for any Embedder — call it once per candidate model (e.g. jina
// vs bge-m3) with a differently-configured client; the model comparison
// lives entirely in which Embedder the caller passes in.
func RunModelAB(ctx context.Context, model string, emb Embedder, pairs []ABPair, k int) (ABResult, error) {
	if len(pairs) == 0 {
		return ABResult{}, fmt.Errorf("embed: RunModelAB: no pairs supplied")
	}
	if k <= 0 {
		return ABResult{}, fmt.Errorf("embed: RunModelAB: k must be > 0, got %d", k)
	}

	memVecs := make([][]float32, len(pairs))
	for i, p := range pairs {
		vec, err := emb.Embed(ctx, p.MemoryEN)
		if err != nil {
			return ABResult{}, fmt.Errorf("embed: RunModelAB: embed memory %q: %w", p.ID, err)
		}
		memVecs[i] = vec
	}

	result := ABResult{Model: model, K: k, Total: len(pairs)}
	for i, p := range pairs {
		qvec, err := emb.Embed(ctx, p.QueryES)
		if err != nil {
			return ABResult{}, fmt.Errorf("embed: RunModelAB: embed query %q: %w", p.ID, err)
		}
		if rankOf(qvec, memVecs, i) < k {
			result.Hits++
		}
	}
	result.RecallAtK = float64(result.Hits) / float64(result.Total)
	return result, nil
}

// rankOf returns the 0-based rank of memVecs[targetIdx] among all memVecs
// when sorted by descending cosine similarity to qvec. Vectors are assumed
// unit-normalized (Client.Embed guarantees this), so dot product == cosine
// similarity; dot is the same primitive Store.Search already uses.
func rankOf(qvec []float32, memVecs [][]float32, targetIdx int) int {
	type scored struct {
		idx   int
		score float32
	}
	scores := make([]scored, len(memVecs))
	for i, v := range memVecs {
		scores[i] = scored{idx: i, score: dot(qvec, v)}
	}
	sort.SliceStable(scores, func(a, b int) bool { return scores[a].score > scores[b].score })
	for rank, s := range scores {
		if s.idx == targetIdx {
			return rank
		}
	}
	return len(memVecs)
}
