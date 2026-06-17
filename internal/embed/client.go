// Package embed is Omnia's own local semantic-embeddings layer ("capa propia").
// It generates vectors via a local Ollama model, stores them in Omnia's own
// SQLite database (engram.db stays read-only), and serves brute-force cosine
// search. This package depends only on engramdb, meta, and the standard library
// — never on config — so it stays a reusable leaf.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
)

// Task types map to nomic-embed-text's asymmetric retrieval prefixes. Documents
// (stored memories) and queries are embedded with different prefixes; the
// trailing space is part of the prefix the model was trained on.
const (
	TaskDocument = "search_document"
	TaskQuery    = "search_query"
)

// Embedder produces a unit-normalized embedding for text under a task type.
// It is an interface so Reconcile and tests can run without a live Ollama.
type Embedder interface {
	Embed(ctx context.Context, text, task string) ([]float32, error)
}

// Client is an Ollama embeddings HTTP client.
type Client struct {
	baseURL    string
	model      string
	dim        int
	httpClient *http.Client
}

// New builds a Client. dim, when > 0, is asserted against the model's output.
func New(baseURL, model string, dim int) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		model:      model,
		dim:        dim,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

func prefixFor(task string) (string, error) {
	switch task {
	case TaskDocument:
		return "search_document: ", nil
	case TaskQuery:
		return "search_query: ", nil
	default:
		return "", fmt.Errorf("embed: unknown task %q", task)
	}
}

type embedRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type embedResponse struct {
	Embedding []float32 `json:"embedding"`
}

// Embed posts prefix+text to Ollama and returns a unit-normalized vector.
func (c *Client) Embed(ctx context.Context, text, task string) ([]float32, error) {
	prefix, err := prefixFor(task)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(embedRequest{Model: c.model, Prompt: prefix + text})
	if err != nil {
		return nil, fmt.Errorf("embed: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embed: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed: post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embed: ollama status %d", resp.StatusCode)
	}

	var er embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, fmt.Errorf("embed: decode: %w", err)
	}
	if c.dim > 0 && len(er.Embedding) != c.dim {
		return nil, fmt.Errorf("embed: expected dim %d, got %d", c.dim, len(er.Embedding))
	}
	return normalize(er.Embedding)
}

// normalize scales v to unit length. It errors on a zero-norm vector, which
// would otherwise yield NaNs that poison every future dot product.
func normalize(v []float32) ([]float32, error) {
	if len(v) == 0 {
		return nil, fmt.Errorf("embed: empty vector")
	}
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	norm := math.Sqrt(sum)
	if norm == 0 {
		return nil, fmt.Errorf("embed: zero-norm vector")
	}
	inv := 1.0 / norm
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(float64(x) * inv)
	}
	return out, nil
}
