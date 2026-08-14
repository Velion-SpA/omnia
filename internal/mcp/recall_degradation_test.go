package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/velion/omnia/internal/embed"
	"github.com/velion/omnia/internal/store"
)

// ─── #226 [RED]: mem_search must declare its own degradation ───
//
// When embeddings.db froze, semantic search went on answering — blind to every
// memory created since — and the response looked exactly like a healthy one.
// A caller (a hook, a script, another agent) had no way to tell a good result
// set from a blind one. The results themselves cannot carry that signal; the
// envelope must.

func TestRecallHealth_SemanticOffIsDegraded(t *testing.T) {
	h := EvaluateRecallHealth(context.Background(), false, nil)

	if !h.Degraded {
		t.Fatal("a lexical-only result set is degraded relative to what the caller expects from mem_search")
	}
	if h.Mode != "lexical" {
		t.Fatalf("mode: want lexical, got %q", h.Mode)
	}
}

func TestRecallHealth_SemanticOnAndCaughtUpIsHealthy(t *testing.T) {
	h := EvaluateRecallHealth(context.Background(), true, func(context.Context) (RecallWatermarks, error) {
		return RecallWatermarks{ObservationMaxID: 100, EmbeddingMaxObsID: 100}, nil
	})

	if h.Degraded {
		t.Fatalf("semantic recall over a caught-up index is not degraded: %+v", h)
	}
	if h.Mode != "semantic" {
		t.Fatalf("mode: want semantic, got %q", h.Mode)
	}
}

// The case that actually happened: semantic recall was ON and working, but the
// index it searched had stopped being written. Answers kept coming; they were
// simply blind to everything newer.
func TestRecallHealth_StaleIndexIsDegradedEvenWithSemanticOn(t *testing.T) {
	h := EvaluateRecallHealth(context.Background(), true, func(context.Context) (RecallWatermarks, error) {
		return RecallWatermarks{
			ObservationMaxID:  1890,
			EmbeddingMaxObsID: 1855,
			NewestEmbeddedAt:  "2026-07-31 09:22:14",
		}, nil
	})

	if !h.Degraded {
		t.Fatal("a stale embeddings index must be reported as degraded")
	}
	if h.Mode != "semantic" {
		t.Fatalf("the mode is still semantic — what is wrong is the index, got %q", h.Mode)
	}
	if !h.EmbeddingsStale {
		t.Fatal("EmbeddingsStale must be set so the caller knows WHY")
	}
	if h.NewestEmbeddedAt != "2026-07-31 09:22:14" {
		t.Fatalf("the freeze point must survive to the caller: got %q", h.NewestEmbeddedAt)
	}
}

// An unreadable embeddings store (the #228 shape) must degrade loudly, never
// quietly pass as healthy.
func TestRecallHealth_UnreadableWatermarksAreDegraded(t *testing.T) {
	h := EvaluateRecallHealth(context.Background(), true, func(context.Context) (RecallWatermarks, error) {
		return RecallWatermarks{}, errors.New("file is not a database (26)")
	})

	if !h.Degraded {
		t.Fatal("an unreadable embeddings store must degrade, not pass")
	}
	if h.Reason == "" {
		t.Fatal("the caller must be told why")
	}
}

// No watermark reader wired (embeddings off, or a caller that cannot supply
// one) must not manufacture a false staleness alarm — semantic-off is already
// reported on its own.
func TestRecallHealth_NoWatermarkReaderDoesNotClaimStaleness(t *testing.T) {
	h := EvaluateRecallHealth(context.Background(), true, nil)

	if h.EmbeddingsStale {
		t.Fatal("staleness must never be asserted without having looked")
	}
	if h.Degraded {
		t.Fatalf("semantic recall with no watermark reader is unknown, not degraded: %+v", h)
	}
}

// The envelope contract: healthy responses stay byte-for-byte as they were —
// the keys appear ONLY when there is something to say, matching this file's
// sibling conventions (fts_relaxed, budget_trimmed, recorded_time).
func TestRecallHealth_EnvelopeIsSilentWhenHealthy(t *testing.T) {
	env := map[string]any{"results": []any{}}
	AnnotateRecallHealth(env, RecallHealth{Degraded: false, Mode: "semantic"})

	if len(env) != 1 {
		t.Fatalf("a healthy response must add no envelope keys, got %v", env)
	}
}

func TestRecallHealth_EnvelopeCarriesTheDegradation(t *testing.T) {
	env := map[string]any{"results": []any{}}
	AnnotateRecallHealth(env, RecallHealth{
		Degraded:         true,
		Mode:             "semantic",
		EmbeddingsStale:  true,
		BehindBy:         35,
		NewestEmbeddedAt: "2026-07-31 09:22:14",
		Reason:           "embeddings index is behind the store",
	})

	if env["recall_degraded"] != true {
		t.Fatalf("recall_degraded must be set: %v", env)
	}
	if env["recall_mode"] != "semantic" {
		t.Fatalf("recall_mode must be set: %v", env)
	}
	if env["embeddings_stale"] != true || env["embeddings_behind_by"] != 35 {
		t.Fatalf("staleness detail must reach the caller: %v", env)
	}
	if env["newest_embedded_at"] != "2026-07-31 09:22:14" {
		t.Fatalf("freeze point must reach the caller: %v", env)
	}
	if env["recall_degraded_reason"] == nil {
		t.Fatalf("the caller must be told why: %v", env)
	}
}

// ─── P0 (docs/conversational-retrieval-plan.md): NewWatermarkReader is the
// exported seam GET /search's cmd/omnia wiring (buildHTTPSearchFunc) reuses,
// so both consumers read the #226 watermark signal the exact same way ───

// noopWatermarkEmbedder is a minimal embed.Embedder stub — these tests only
// need a *embed.Worker with a real store attached, never an actual embed
// call.
type noopWatermarkEmbedder struct{}

func (noopWatermarkEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	return []float32{1, 0, 0}, nil
}

// TestNewWatermarkReader_NilStoreOrNilAutoEmbedReturnsNil pins the
// nil-means-unknown contract: with either argument nil, the reader itself
// must be nil (not a reader that errors), so EvaluateRecallHealth treats it
// as "we did not look," never "we looked and it is bad."
func TestNewWatermarkReader_NilStoreOrNilAutoEmbedReturnsNil(t *testing.T) {
	if got := NewWatermarkReader(nil, nil); got != nil {
		t.Fatal("expected nil reader when both store and autoEmbed are nil")
	}

	s := newMCPTestStore(t)
	if got := NewWatermarkReader(s, nil); got != nil {
		t.Fatal("expected nil reader when autoEmbed is nil (embeddings disabled)")
	}
}

// TestNewWatermarkReader_WiredWorkerReadsRealWatermarks proves the exported
// constructor produces a working reader over a real store + embeddings
// store pair — the same watermark data newWatermarkReader's MCPConfig-based
// wrapper has always produced, now reachable without an MCPConfig at all
// (cmd/omnia has none).
func TestNewWatermarkReader_WiredWorkerReadsRealWatermarks(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s-wm", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-wm", Type: "manual", Title: "t", Content: "c",
		Project: "engram", Scope: "project",
	}); err != nil {
		t.Fatalf("add observation: %v", err)
	}

	embStore, err := embed.OpenStore(t.TempDir() + "/emb.db")
	if err != nil {
		t.Fatalf("open embed store: %v", err)
	}
	defer embStore.Close()
	worker := embed.NewWorker(embStore, noopWatermarkEmbedder{}, "m", 3, 8, nil)

	reader := NewWatermarkReader(s, worker)
	if reader == nil {
		t.Fatal("expected a non-nil reader when both a store and a worker-with-store are provided")
	}

	wm, err := reader(context.Background())
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	if wm.ObservationMaxID <= 0 || wm.ObservationCount <= 0 {
		t.Fatalf("expected real watermark data reflecting the seeded observation, got %+v", wm)
	}
}

// TestNewWatermarkReader_IsTTLCached proves the exported constructor still
// wraps its reader in cachedWatermarkReader (watermarkCacheTTL) — a second
// call within the TTL window must not re-read the store, mirroring
// newWatermarkReader's own existing cache contract.
func TestNewWatermarkReader_IsTTLCached(t *testing.T) {
	s := newMCPTestStore(t)
	embStore, err := embed.OpenStore(t.TempDir() + "/emb.db")
	if err != nil {
		t.Fatalf("open embed store: %v", err)
	}
	defer embStore.Close()
	worker := embed.NewWorker(embStore, noopWatermarkEmbedder{}, "m", 3, 8, nil)

	reader := NewWatermarkReader(s, worker)
	if reader == nil {
		t.Fatal("expected a non-nil reader")
	}

	first, err := reader(context.Background())
	if err != nil {
		t.Fatalf("first read: %v", err)
	}

	if err := s.CreateSession("s-wm-cache", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-wm-cache", Type: "manual", Title: "t2", Content: "c2",
		Project: "engram", Scope: "project",
	}); err != nil {
		t.Fatalf("add observation: %v", err)
	}

	second, err := reader(context.Background())
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if second.ObservationMaxID != first.ObservationMaxID || second.ObservationCount != first.ObservationCount {
		t.Fatalf("expected the cached value within the TTL window, got first=%+v second=%+v", first, second)
	}
}
