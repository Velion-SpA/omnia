package embed

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeOllama returns a server that responds with the given embedding and records
// the last prompt it received.
func fakeOllama(t *testing.T, embedding []float32, status int, lastPrompt *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embeddings" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var req embedRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if lastPrompt != nil {
			*lastPrompt = req.Prompt
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		json.NewEncoder(w).Encode(embedResponse{Embedding: embedding})
	}))
}

func TestClient_Embed_DocumentPrefixAndUnitNorm(t *testing.T) {
	var prompt string
	srv := fakeOllama(t, []float32{3, 4, 0}, http.StatusOK, &prompt) // norm 5
	defer srv.Close()

	c := New(srv.URL, "test-model", 3)
	vec, err := c.Embed(context.Background(), "hello", TaskDocument)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if !strings.HasPrefix(prompt, "search_document: ") {
		t.Errorf("document prompt missing prefix: %q", prompt)
	}
	if prompt != "search_document: hello" {
		t.Errorf("prompt: got %q, want %q", prompt, "search_document: hello")
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

func TestClient_Embed_QueryPrefix(t *testing.T) {
	var prompt string
	srv := fakeOllama(t, []float32{1, 0, 0}, http.StatusOK, &prompt)
	defer srv.Close()

	c := New(srv.URL, "test-model", 3)
	if _, err := c.Embed(context.Background(), "find this", TaskQuery); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if prompt != "search_query: find this" {
		t.Errorf("query prompt: got %q, want %q", prompt, "search_query: find this")
	}
}

func TestClient_Embed_UnknownTask(t *testing.T) {
	c := New("http://127.0.0.1:0", "test-model", 3)
	if _, err := c.Embed(context.Background(), "x", "classification"); err == nil {
		t.Error("expected error for unknown task, got nil")
	}
}

func TestClient_Embed_DimMismatch(t *testing.T) {
	srv := fakeOllama(t, []float32{1, 0, 0}, http.StatusOK, nil) // 3 dims
	defer srv.Close()

	c := New(srv.URL, "test-model", 4) // expect 4
	if _, err := c.Embed(context.Background(), "x", TaskDocument); err == nil {
		t.Error("expected dim-mismatch error, got nil")
	}
}

func TestClient_Embed_ZeroVectorRejected(t *testing.T) {
	srv := fakeOllama(t, []float32{0, 0, 0}, http.StatusOK, nil)
	defer srv.Close()

	c := New(srv.URL, "test-model", 3)
	if _, err := c.Embed(context.Background(), "x", TaskDocument); err == nil {
		t.Error("expected zero-norm error, got nil")
	}
}

func TestClient_Embed_Non200(t *testing.T) {
	srv := fakeOllama(t, nil, http.StatusInternalServerError, nil)
	defer srv.Close()

	c := New(srv.URL, "test-model", 3)
	if _, err := c.Embed(context.Background(), "x", TaskDocument); err == nil {
		t.Error("expected error on 500, got nil")
	}
}
