package mcp

import (
	"context"
	"testing"
	"time"

	mcppkg "github.com/mark3labs/mcp-go/mcp"
	"github.com/velion/omnia/internal/store"
)

// fts_relax_envelope_test.go — RED->GREEN tests for omnia-0.3.1-write-hygiene
// PR7 task 7.5 (design obs #1668 D7, spec obs #1666 fts-recall domain REQ
// "Fallback Transparency"): handleSearch surfaces extra["fts_relaxed"] /
// extra["fts_relax_step"] whenever Store.Search's zero-hit relaxation ladder
// fired for a call, and adds NOTHING to the envelope otherwise — a pure
// no-op on every path where the strict pass already found results (or found
// nothing even after full relaxation), matching every other "byte-for-byte
// unless X" Context Economy convention in this file.

// TestHandleSearch_SurfacesFTSRelaxedWhenLadderFires reproduces the real
// battery failure shape end-to-end through the MCP tool boundary: a
// natural-language Spanish query whose stopwords ("cómo"/"el"/"las") are
// absent from the stored text fails the strict pass, but the relaxed
// (stopword-dropped) pass finds it — and the envelope must say so.
func TestHandleSearch_SurfacesFTSRelaxedWhenLadderFires(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("sess-fts-relax", "fts-relax-project", "/tmp"); err != nil {
		t.Fatal(err)
	}
	_, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess-fts-relax",
		Type:      "discovery",
		Title:     "Limpieza automática de memorias duplicadas",
		Content:   "El sistema detecta y limpia las memorias duplicadas automáticamente antes de guardarlas en el almacenamiento.",
		Project:   "fts-relax-project",
		Scope:     "project",
	})
	if err != nil {
		t.Fatal(err)
	}

	h := handleSearch(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	res, err := h(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			// "cómo" never occurs verbatim in the stored text above, so the
			// strict AND-of-every-term pass finds 0 rows.
			"query":   "cómo limpia el sistema las memorias duplicadas",
			"project": "fts-relax-project",
			"scope":   "project",
		}},
	})
	if err != nil || res.IsError {
		t.Fatalf("search: err=%v isError=%v text=%q", err, res.IsError, callResultText(t, res))
	}
	body := callResultJSON(t, res)
	relaxed, ok := body["fts_relaxed"].(bool)
	if !ok || !relaxed {
		t.Fatalf("expected extra[\"fts_relaxed\"]=true, got %v (body=%+v)", body["fts_relaxed"], body)
	}
	step, ok := body["fts_relax_step"].(float64)
	if !ok || step != 1 {
		t.Fatalf("expected extra[\"fts_relax_step\"]=1, got %v (body=%+v)", body["fts_relax_step"], body)
	}
}

// TestHandleSearch_NoFTSRelaxedFieldWhenStrictPassAlreadyHits is the
// byte-for-byte case: when the strict pass already finds the result, the
// ladder never runs (Diag stays zero-value), so the envelope must carry
// NEITHER key at all — not even a false/0 placeholder.
func TestHandleSearch_NoFTSRelaxedFieldWhenStrictPassAlreadyHits(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("sess-fts-strict", "fts-relax-project", "/tmp"); err != nil {
		t.Fatal(err)
	}
	_, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess-fts-strict",
		Type:      "manual",
		Title:     "envelope strict hit test",
		Content:   "envelope strict hit content",
		Project:   "fts-relax-project",
		Scope:     "project",
	})
	if err != nil {
		t.Fatal(err)
	}

	h := handleSearch(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	res, err := h(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"query":   "envelope strict hit test",
			"project": "fts-relax-project",
			"scope":   "project",
		}},
	})
	if err != nil || res.IsError {
		t.Fatalf("search: err=%v isError=%v text=%q", err, res.IsError, callResultText(t, res))
	}
	body := callResultJSON(t, res)
	if _, ok := body["fts_relaxed"]; ok {
		t.Errorf("expected NO fts_relaxed key when the strict pass already hit, got %+v", body["fts_relaxed"])
	}
	if _, ok := body["fts_relax_step"]; ok {
		t.Errorf("expected NO fts_relax_step key when the strict pass already hit, got %+v", body["fts_relax_step"])
	}
}

// TestHandleSearch_NoFTSRelaxedFieldWhenFullyExhausted covers a query that
// legitimately matches nothing even after full relaxation: the empty-result
// path stays exactly as it was before this PR (no fts_relaxed key either),
// since Diag.Relaxed is false even though the ladder DID run.
func TestHandleSearch_NoFTSRelaxedFieldWhenFullyExhausted(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("sess-fts-exhausted", "fts-relax-project", "/tmp"); err != nil {
		t.Fatal(err)
	}
	_, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess-fts-exhausted",
		Type:      "manual",
		Title:     "banana pancake syrup",
		Content:   "a completely unrelated breakfast note sharing no vocabulary",
		Project:   "fts-relax-project",
		Scope:     "project",
	})
	if err != nil {
		t.Fatal(err)
	}

	h := handleSearch(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	res, err := h(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"query":   "checkout webhook automatically refund policy",
			"project": "fts-relax-project",
			"scope":   "project",
		}},
	})
	if err != nil || res.IsError {
		t.Fatalf("search: err=%v isError=%v text=%q", err, res.IsError, callResultText(t, res))
	}
	body := callResultJSON(t, res)
	if _, ok := body["fts_relaxed"]; ok {
		t.Errorf("expected NO fts_relaxed key when every relaxation level was exhausted, got %+v", body["fts_relaxed"])
	}
}
