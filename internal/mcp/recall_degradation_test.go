package mcp

import (
	"context"
	"errors"
	"testing"
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
