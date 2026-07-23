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

// embedContextTokens is sent as options.num_ctx on every request. Ollama's
// /api/embed serves a hard-coded 2048-token context unless the caller
// overrides it per request, which surfaces as an HTTP 500 on longer prompts
// (a serving bug, not model-specific — see Ollama issues #7741/#7008/#13054).
// jina-embeddings-v2-base-es and bge-m3 both support real 8192-token context
// via ALiBi/RoPE, so this override is safe for either candidate model.
const embedContextTokens = 8192

// Embedder produces a unit-normalized embedding for text. It is an interface
// so Reconcile and tests can run without a live Ollama.
//
// Unlike the prior nomic-embed-text client, there is no asymmetric task
// parameter: jina-embeddings-v2-base-es (and bge-m3) have no
// search_document:/search_query: prefix convention, so the same call embeds
// both stored memories and interactive queries.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
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

type embedOptions struct {
	NumCtx int `json:"num_ctx"`
}

type embedRequest struct {
	Model    string       `json:"model"`
	Input    string       `json:"input"`
	Truncate bool         `json:"truncate"`
	Options  embedOptions `json:"options"`
}

// embedResponse mirrors Ollama's /api/embed shape, which batches: Embeddings
// is a list of vectors (one per Input element). We always send a single
// string Input, so we expect exactly one vector back.
type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

// Embed posts text to Ollama's /api/embed endpoint and returns a
// unit-normalized vector. truncate:true + options.num_ctx:8192 replace the
// former client-side shrinking-rune-budget retry loop: the server truncates
// over-long input itself instead of Omnia guessing a safe rune cap.
func (c *Client) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(embedRequest{
		Model:    c.model,
		Input:    text,
		Truncate: true,
		Options:  embedOptions{NumCtx: embedContextTokens},
	})
	if err != nil {
		return nil, fmt.Errorf("embed: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/embed", bytes.NewReader(body))
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
	if len(er.Embeddings) == 0 {
		return nil, fmt.Errorf("embed: ollama returned no embeddings")
	}
	vec := er.Embeddings[0]
	if c.dim > 0 && len(vec) != c.dim {
		// EMBM-4: a configured dim smaller than the model's native output is
		// only valid Matryoshka (MRL) truncation — keep the leading c.dim
		// components and let normalize() below re-establish unit length.
		// EMBM-3's guard: this is REJECTED for any model not flagged
		// MRL-capable in the registry (e.g. jina), and for an unknown model
		// (LookupModel's second return false), so truncation never silently
		// degrades a model that was never trained to support it.
		if info, ok := LookupModel(c.model); ok && info.MRL && c.dim < len(vec) {
			vec = vec[:c.dim]
		} else {
			return nil, fmt.Errorf("embed: expected dim %d, got %d", c.dim, len(vec))
		}
	}
	return normalize(vec)
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
