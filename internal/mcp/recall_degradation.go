package mcp

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/velion/omnia/internal/store"
)

// This file makes mem_search declare its own degradation (#226).
//
// When embeddings.db froze, semantic search went on answering — blind to every
// memory created since — and the response looked exactly like a healthy one. A
// caller (a hook, a script, another agent) had no way to tell a good result set
// from a blind one, so it trusted the blind one. The results themselves cannot
// carry that signal; the envelope has to.
//
// The envelope keys follow this package's established present-only-when-notable
// convention (fts_relaxed, budget_trimmed, recorded_time): a healthy response is
// byte-for-byte what it was before this existed.

// RecallWatermarks pairs the two ids that answer "is the semantic index behind
// the store?" — the observation store's highest live id against the highest id
// any stored vector covers. Ids, not timestamps: both sides draw from the same
// monotonic sequence, so no clock agreement between the two databases is needed.
type RecallWatermarks struct {
	ObservationMaxID  int64
	ObservationCount  int
	EmbeddingMaxObsID int
	EmbeddingCount    int
	NewestEmbeddedAt  string
}

// WatermarkReader supplies RecallWatermarks. nil means the caller has no
// embeddings store to inspect — which yields "unknown", never "stale".
type WatermarkReader func(context.Context) (RecallWatermarks, error)

// RecallHealth is what mem_search learned about the quality of its own answer.
type RecallHealth struct {
	// Degraded is true when the caller should NOT treat this result set as a
	// full-quality semantic recall.
	Degraded bool
	// Mode is "semantic" or "lexical" — what actually ran.
	Mode string
	// EmbeddingsStale is asserted only after actually reading both
	// watermarks. Never inferred from an absent reader.
	EmbeddingsStale  bool
	BehindBy         int
	NewestEmbeddedAt string
	// Reason is a short human-readable explanation, present whenever Degraded.
	Reason string
}

// EvaluateRecallHealth decides whether this response should carry a
// degradation signal.
//
// Three distinct outcomes, deliberately kept apart:
//   - semantic off        → degraded, mode=lexical (the caller asked for
//     recall and got keyword matching)
//   - semantic on, stale  → degraded, mode=semantic (the mode is right; the
//     index it searched is behind) — the case that actually happened
//   - semantic on, no reader → NOT degraded, and staleness is not asserted.
//     "We did not look" must never be reported as "we looked and it is bad".
func EvaluateRecallHealth(ctx context.Context, semanticActive bool, readWatermarks WatermarkReader) RecallHealth {
	if !semanticActive {
		return RecallHealth{
			Degraded: true,
			Mode:     "lexical",
			Reason:   "semantic recall is not active; these results come from keyword matching only",
		}
	}

	h := RecallHealth{Mode: "semantic"}
	if readWatermarks == nil {
		return h
	}

	wm, err := readWatermarks(ctx)
	if err != nil {
		h.Degraded = true
		h.Reason = fmt.Sprintf("the embeddings index could not be read (%v); semantic coverage is unknown", err)
		return h
	}
	if wm.ObservationMaxID <= int64(wm.EmbeddingMaxObsID) {
		return h
	}

	behind := wm.ObservationCount - wm.EmbeddingCount
	if behind < 0 {
		behind = 0
	}
	h.Degraded = true
	h.EmbeddingsStale = true
	h.BehindBy = behind
	h.NewestEmbeddedAt = wm.NewestEmbeddedAt
	h.Reason = fmt.Sprintf("the embeddings index is behind the store (newest embedded observation %d, newest observation %d); memories saved since are not semantically searchable — run `omnia embed`",
		wm.EmbeddingMaxObsID, wm.ObservationMaxID)
	return h
}

// AnnotateRecallHealth attaches the degradation signal to a mem_search
// envelope. A healthy response adds NOTHING, matching this package's
// present-only-when-notable convention.
func AnnotateRecallHealth(envelope map[string]any, h RecallHealth) {
	if !h.Degraded {
		return
	}
	envelope["recall_degraded"] = true
	envelope["recall_mode"] = h.Mode
	envelope["recall_degraded_reason"] = h.Reason
	if h.EmbeddingsStale {
		envelope["embeddings_stale"] = true
		envelope["embeddings_behind_by"] = h.BehindBy
		if h.NewestEmbeddedAt != "" {
			envelope["newest_embedded_at"] = h.NewestEmbeddedAt
		}
	}
}

// newWatermarkReader builds this server's watermark reader, or nil when there
// is no embeddings store to inspect (auto-embed off) — in which case
// EvaluateRecallHealth reports "unknown", never "stale".
//
// It reads the auto-embed worker's OWN store — the very store that stopped
// being written in #226 — so the signal comes from the file that actually
// backs semantic recall, not from a separately resolved path that could drift
// from it.
//
// Call this ONCE, at handler-registration time (handleSearch's body, outside
// the returned closure): MCPConfig is passed by value, so memoising on the
// config would both defeat the cache and trip copylocks. The returned reader
// is already TTL-cached, so a long-lived server pays one read per window
// rather than one per search.
func newWatermarkReader(s *store.Store, cfg MCPConfig) WatermarkReader {
	if s == nil || cfg.AutoEmbed == nil {
		return nil
	}
	embStore := cfg.AutoEmbed.Store()
	if embStore == nil {
		return nil
	}
	return cachedWatermarkReader(func(ctx context.Context) (RecallWatermarks, error) {
		maxID, count, err := s.ObservationWatermark()
		if err != nil {
			return RecallWatermarks{}, err
		}
		lag, err := embStore.Lag(ctx)
		if err != nil {
			return RecallWatermarks{}, err
		}
		return RecallWatermarks{
			ObservationMaxID:  maxID,
			ObservationCount:  count,
			EmbeddingMaxObsID: lag.MaxObsID,
			EmbeddingCount:    lag.Count,
			NewestEmbeddedAt:  lag.NewestEmbeddedAt,
		}, nil
	})
}

// watermarkCacheTTL bounds how often a long-lived MCP server re-reads the
// watermarks. The read is two aggregates over the embeddings table, so it is
// cheap but not free at 100k rows — and staleness does not change
// meaningfully within seconds. A fresh spawn pays it exactly once.
const watermarkCacheTTL = 15 * time.Second

// cachedWatermarkReader wraps a reader with a short TTL so a burst of searches
// costs one read, not one per search. Errors are cached too: a store that
// cannot be opened will not be reopened on every query.
func cachedWatermarkReader(inner WatermarkReader) WatermarkReader {
	if inner == nil {
		return nil
	}
	var (
		mu       sync.Mutex
		cachedAt time.Time
		cached   RecallWatermarks
		cachedEr error
		nowFn    = time.Now
	)
	return func(ctx context.Context) (RecallWatermarks, error) {
		mu.Lock()
		defer mu.Unlock()
		if !cachedAt.IsZero() && nowFn().Sub(cachedAt) < watermarkCacheTTL {
			return cached, cachedEr
		}
		cached, cachedEr = inner(ctx)
		cachedAt = nowFn()
		return cached, cachedEr
	}
}
