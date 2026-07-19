//go:build ollama_live

// This file is the bilingual recall GATE described in design.md's "Bilingual
// via embeddings first, gated empirically" decision: a fixed set of
// ES-query -> EN-memory and EN-query -> ES-memory pairs must appear within
// top-k through the REAL recall.Service + embed.Searcher + Fuse pipeline
// (not just raw embedding similarity, which engram obs #1401 already
// confirmed via internal/embed's model A/B harness).
//
// It is double-gated exactly like internal/embed/model_ab_test.go, so it can
// NEVER affect a normal `go test ./...` run or CI, and can never block a
// build without a live Ollama:
//
//  1. Build tag `ollama_live` — invisible to `go build`/`go vet`/
//     `go test ./...` unless that tag is explicitly passed.
//  2. OLLAMA_LIVE_TEST=1 env var — an explicit opt-in even when built with
//     the tag.
//
// Per Phase 0's gate result (engram obs #1401: jina-embeddings-v2-base-es
// recall@10 = 1.000), this test is expected to pass with embeddings alone —
// recall.bilingual_expansion (design.md's flagged LLM-expansion fallback)
// stays deferred (task 2.10: N/A unless this gate fails).
//
// Run it once jina/jina-embeddings-v2-base-es is pulled:
//
//	OLLAMA_LIVE_TEST=1 go test -tags ollama_live ./internal/recall/... -run TestBilingualRecall -v
package recall

import (
	"context"
	"os"
	"testing"

	"github.com/velion/omnia/internal/embed"
)

// bilingualPair is one ES/EN memory pair used to validate cross-lingual
// recall in both directions through the real Service pipeline.
type bilingualPair struct {
	id string
	es string
	en string
}

var bilingualPairs = []bilingualPair{
	{id: "p1", es: "el usuario prefiere respuestas en español rioplatense", en: "the user prefers replies in Rioplatense Spanish"},
	{id: "p2", es: "usamos fusión por rango recíproco para combinar resultados", en: "we use reciprocal rank fusion to merge results"},
	{id: "p3", es: "el piso de relevancia se adapta según la cantidad de coincidencias fuertes", en: "the relevance floor adapts based on the number of strong matches"},
	{id: "p4", es: "las pruebas deben ser deterministas y no depender de una red en vivo", en: "tests must be deterministic and must not depend on a live network"},
	{id: "p5", es: "el paquete recall no debe importar el store ni el módulo cloud", en: "the recall package must not import store or the cloud module"},
}

// noLexical is a LexicalSearcher that always returns zero matches, isolating
// this gate to the semantic side of Service.Search (Phase 0's "bilingual via
// embeddings first" decision).
type noLexical struct{}

func (noLexical) Search(ctx context.Context, query string, opts LexicalSearchOptions) ([]LexicalHit, error) {
	return nil, nil
}

func TestBilingualRecall_ESQueryFindsENMemory_ENQueryFindsESMemory(t *testing.T) {
	if os.Getenv("OLLAMA_LIVE_TEST") != "1" {
		t.Skip("OLLAMA_LIVE_TEST not set — skipping live-Ollama bilingual recall gate")
	}

	baseURL := os.Getenv("OLLAMA_BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	const model = "jina/jina-embeddings-v2-base-es"
	const dim = 768
	const topK = 5

	client := embed.New(baseURL, model, dim)
	store, err := embed.OpenStore(t.TempDir() + "/bilingual.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// obsID encodes which pair + which language a row belongs to, so a query
	// in one language can assert the OTHER language's row (same pair) ranked
	// within top-k. EN rows: 100+i. ES rows: 200+i.
	for i, pr := range bilingualPairs {
		enVec, err := client.Embed(ctx, pr.en)
		if err != nil {
			t.Fatalf("embed EN %q: %v", pr.id, err)
		}
		if err := store.Upsert(ctx, embed.Row{
			SyncID: pr.id + "-en", ObsID: 100 + i, Project: "p1", Type: "decision",
			Title: pr.id + "-en", UpdatedAt: "2024-01-01 00:00:00", ContentHash: "h-" + pr.id + "-en",
			Model: model, Dim: dim, Vector: enVec, EmbeddedAt: "2024-01-01 00:00:00",
		}); err != nil {
			t.Fatalf("upsert EN %q: %v", pr.id, err)
		}

		esVec, err := client.Embed(ctx, pr.es)
		if err != nil {
			t.Fatalf("embed ES %q: %v", pr.id, err)
		}
		if err := store.Upsert(ctx, embed.Row{
			SyncID: pr.id + "-es", ObsID: 200 + i, Project: "p1", Type: "decision",
			Title: pr.id + "-es", UpdatedAt: "2024-01-01 00:00:00", ContentHash: "h-" + pr.id + "-es",
			Model: model, Dim: dim, Vector: esVec, EmbeddedAt: "2024-01-01 00:00:00",
		}); err != nil {
			t.Fatalf("upsert ES %q: %v", pr.id, err)
		}
	}

	svc := NewService(noLexical{}, embed.NewSearcher(store, client), FuseParams{
		RRFK: 60, DenseK: 5, MaxResults: topK, StrongFloor: 0.65, BaseFloor: 0.0,
	})

	for i, pr := range bilingualPairs {
		wantENObsID := int64(100 + i)
		wantESObsID := int64(200 + i)

		t.Run(pr.id+"/es-query-finds-en-memory", func(t *testing.T) {
			got, err := svc.Search(ctx, pr.es, LexicalSearchOptions{Limit: topK})
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if !containsID(got, wantENObsID) {
				t.Errorf("ES query %q: EN memory (obsID %d) not in top-%d: %v", pr.es, wantENObsID, topK, idsOf(got))
			}
		})

		t.Run(pr.id+"/en-query-finds-es-memory", func(t *testing.T) {
			got, err := svc.Search(ctx, pr.en, LexicalSearchOptions{Limit: topK})
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if !containsID(got, wantESObsID) {
				t.Errorf("EN query %q: ES memory (obsID %d) not in top-%d: %v", pr.en, wantESObsID, topK, idsOf(got))
			}
		})
	}
}

func containsID(results []Result, id int64) bool {
	for _, r := range results {
		if r.ID == id {
			return true
		}
	}
	return false
}
