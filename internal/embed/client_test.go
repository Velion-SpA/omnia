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

// TestClient_Embed_NonMRLDimMismatchStillErrors locks EMBM-3's guard at the
// client level for a REGISTERED non-MRL model (jina): even though the
// configured dim (2) is smaller than the returned vector's length (3) —
// exactly the shape that WOULD be truncated for an MRL-capable model — jina
// has no Matryoshka training, so this must still be a hard error, never a
// silent truncation.
func TestClient_Embed_NonMRLDimMismatchStillErrors(t *testing.T) {
	srv := fakeOllama(t, []float32{1, 0, 0}, http.StatusOK, nil) // 3 dims from Ollama
	defer srv.Close()

	c := New(srv.URL, "jina/jina-embeddings-v2-base-es", 2) // jina is NOT MRL-capable
	if _, err := c.Embed(context.Background(), "x"); err == nil {
		t.Error("expected dim-mismatch error for non-MRL model (jina), got nil — truncation must be rejected")
	}
}

// TestClient_Embed_TruncatesAndRenormalizes_MRLModel is EMBM-4's core
// scenario: for a Matryoshka-capable model (embeddinggemma:300m), a
// configured dim smaller than the returned vector's length truncates to the
// leading components and re-normalizes to unit length, rather than erroring.
func TestClient_Embed_TruncatesAndRenormalizes_MRLModel(t *testing.T) {
	srv := fakeOllama(t, []float32{3, 4, 0, 0}, http.StatusOK, nil) // native "4-dim", norm 5 over [3,4]
	defer srv.Close()

	c := New(srv.URL, "embeddinggemma:300m", 2) // configured dim 2 < native 4
	vec, err := c.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != 2 {
		t.Fatalf("vec dim: got %d, want 2 (truncated to configured dim)", len(vec))
	}
	// [3,4] truncated (drop trailing zeros) then re-normalized → [0.6, 0.8].
	want := []float32{0.6, 0.8}
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
		t.Errorf("truncated output not re-normalized to unit length: norm=%v", math.Sqrt(norm))
	}
}

// TestClient_Embed_FullDimensionLeavesVectorUntouched_MRLModel is EMBM-4's
// no-truncation scenario: when the configured dim equals the model's native
// output length, no truncation branch is taken at all — the vector is only
// normalized, exactly like today's non-Matryoshka path.
func TestClient_Embed_FullDimensionLeavesVectorUntouched_MRLModel(t *testing.T) {
	srv := fakeOllama(t, []float32{1, 0, 0}, http.StatusOK, nil) // already 3 dims
	defer srv.Close()

	c := New(srv.URL, "embeddinggemma:300m", 3) // configured dim matches native output
	vec, err := c.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != 3 {
		t.Fatalf("vec dim: got %d, want 3 (untouched, no truncation)", len(vec))
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
