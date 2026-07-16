package embed

import (
	"context"
	"errors"
	"testing"
)

// searcherFakeEmbedder is a minimal Embedder fake used to assert that Searcher
// forwards EmbedQuery to whatever Embedder it was built with — no live Ollama
// needed to prove delegation.
type searcherFakeEmbedder struct {
	calls int
	text  string
	vec   []float32
	err   error
}

func (f *searcherFakeEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	f.calls++
	f.text = text
	return f.vec, f.err
}

func TestSearcher_EmbedQuery_DelegatesToEmbedder(t *testing.T) {
	fake := &searcherFakeEmbedder{vec: []float32{1, 0, 0}}
	s := NewSearcher(nil, fake)

	vec, err := s.EmbedQuery(context.Background(), "hola")
	if err != nil {
		t.Fatalf("EmbedQuery: %v", err)
	}
	if fake.calls != 1 {
		t.Fatalf("Embedder.Embed calls: got %d, want 1", fake.calls)
	}
	if fake.text != "hola" {
		t.Errorf("Embedder.Embed text: got %q, want %q", fake.text, "hola")
	}
	if len(vec) != 3 || vec[0] != 1 {
		t.Errorf("EmbedQuery vec: got %v, want [1 0 0]", vec)
	}
}

func TestSearcher_EmbedQuery_PropagatesEmbedderError(t *testing.T) {
	fake := &searcherFakeEmbedder{err: errors.New("ollama down")}
	s := NewSearcher(nil, fake)

	if _, err := s.EmbedQuery(context.Background(), "hola"); err == nil {
		t.Fatal("EmbedQuery: expected error, got nil")
	}
}

func TestSearcher_Search_DelegatesToStore(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(t.TempDir() + "/emb.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	mustUpsert(t, store, unitRow("A", 1, []float32{1, 0, 0}))
	mustUpsert(t, store, unitRow("B", 2, []float32{0, 1, 0}))

	s := NewSearcher(store, &searcherFakeEmbedder{})
	hits, err := s.Search(ctx, []float32{1, 0, 0}, 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].SyncID != "A" {
		t.Fatalf("Search: got %+v, want a single hit for sync_id A", hits)
	}
}

func TestSearcher_Graph_DelegatesToStore(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/emb.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	mustUpsert(t, store, unitRow("A", 1, []float32{1, 0, 0}))
	mustUpsert(t, store, unitRow("B", 2, []float32{0.9999, 0.014, 0}))

	s := NewSearcher(store, &searcherFakeEmbedder{})
	nodes, _, err := s.Graph(5, 0.5)
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("Graph nodes: got %d, want 2", len(nodes))
	}
}
