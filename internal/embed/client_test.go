package embed

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeOllama returns a server that responds with the given embedding and records
// the last request it received.
func fakeOllama(t *testing.T, embedding []float32, status int, lastReq *embedRequest) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("unexpected path %q, want /api/embed", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var req embedRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if lastReq != nil {
			*lastReq = req
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		json.NewEncoder(w).Encode(embedResponse{Embeddings: [][]float32{embedding}})
	}))
}

func TestClient_Embed_PostsToApiEmbedWithNoPrefix(t *testing.T) {
	var req embedRequest
	srv := fakeOllama(t, []float32{3, 4, 0}, http.StatusOK, &req) // norm 5
	defer srv.Close()

	c := New(srv.URL, "test-model", 3)
	vec, err := c.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	// jina-embeddings-v2-base-es has no search_document:/search_query: prefix
	// convention (unlike nomic-embed-text) — the raw text must be sent as-is.
	if req.Input != "hello" {
		t.Errorf("input: got %q, want %q (no prefix)", req.Input, "hello")
	}
	if req.Model != "test-model" {
		t.Errorf("model: got %q, want %q", req.Model, "test-model")
	}
	if !req.Truncate {
		t.Error("truncate: got false, want true (server-side truncation replaces the shrink-retry loop)")
	}
	if req.Options.NumCtx != 8192 {
		t.Errorf("options.num_ctx: got %d, want 8192 (fixes Ollama's 2048-token /api/embed 500 bug)", req.Options.NumCtx)
	}

	// [3,4,0] normalized → [0.6,0.8,0]
	want := []float32{0.6, 0.8, 0}
	for i := range want {
		if math.Abs(float64(vec[i]-want[i])) > 1e-6 {
			t.Errorf("vec[%d]: got %v, want %v", i, vec[i], want[i])
		}
	}
	var norm float64
	for _, x := range vec {
		norm += float64(x) * float64(x)
	}
	if math.Abs(math.Sqrt(norm)-1.0) > 1e-6 {
		t.Errorf("output not unit-norm: norm=%v", math.Sqrt(norm))
	}
}

func TestClient_Embed_DimMismatch(t *testing.T) {
	srv := fakeOllama(t, []float32{1, 0, 0}, http.StatusOK, nil) // 3 dims
	defer srv.Close()

	c := New(srv.URL, "test-model", 4) // expect 4
	if _, err := c.Embed(context.Background(), "x"); err == nil {
		t.Error("expected dim-mismatch error, got nil")
	}
}

func TestClient_Embed_ZeroVectorRejected(t *testing.T) {
	srv := fakeOllama(t, []float32{0, 0, 0}, http.StatusOK, nil)
	defer srv.Close()

	c := New(srv.URL, "test-model", 3)
	if _, err := c.Embed(context.Background(), "x"); err == nil {
		t.Error("expected zero-norm error, got nil")
	}
}

func TestClient_Embed_Non200(t *testing.T) {
	srv := fakeOllama(t, nil, http.StatusInternalServerError, nil)
	defer srv.Close()

	c := New(srv.URL, "test-model", 3)
	if _, err := c.Embed(context.Background(), "x"); err == nil {
		t.Error("expected error on 500, got nil")
	}
}

func TestClient_Embed_EmptyEmbeddingsRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(embedResponse{Embeddings: nil})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-model", 3)
	if _, err := c.Embed(context.Background(), "x"); err == nil {
		t.Error("expected error on empty embeddings array, got nil")
	}
}
