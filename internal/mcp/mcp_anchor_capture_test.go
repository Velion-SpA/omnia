package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	mcppkg "github.com/mark3labs/mcp-go/mcp"
	"github.com/velion/omnia/internal/anchor"
)

// ─── 3.1: mem_save with code_anchors persists a linked memory_anchors row ───

func TestHandleSave_WithCodeAnchors_PersistsAnchor(t *testing.T) {
	s := newMCPTestStore(t)
	fake := &fakeAnchorCapturer{
		byFile: map[string]fakeCaptureResult{
			"internal/auth/middleware.go": {
				anc: anchor.Anchor{
					File: "internal/auth/middleware.go", Symbol: "Middleware",
					LineStart: 10, LineEnd: 20,
					BlameSHA: "d8d74ffa1489ad18fa062f76a5455cf41f22ea9d", BlameAt: "2024-01-01T00:00:00Z",
					ContentHash: "hash1", RepoRoot: "/repo",
				},
			},
		},
	}
	h := handleSave(s, MCPConfig{AnchorProbe: fake}, NewSessionActivity(10*time.Minute))

	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "Fixed auth middleware bug",
		"content": "**What**: fixed a bug\n**Where**: internal/auth/middleware.go",
		"type":    "bugfix",
		"code_anchors": []any{
			map[string]any{
				"file":       "internal/auth/middleware.go",
				"symbol":     "Middleware",
				"line_start": float64(10),
				"line_end":   float64(20),
			},
		},
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected save error: %s", callResultText(t, res))
	}

	anchors, err := s.ListActiveAnchors("")
	if err != nil {
		t.Fatalf("ListActiveAnchors: %v", err)
	}
	if len(anchors) != 1 {
		t.Fatalf("expected 1 persisted anchor, got %d", len(anchors))
	}
	if anchors[0].FilePath != "internal/auth/middleware.go" {
		t.Errorf("unexpected file_path: %q", anchors[0].FilePath)
	}
	if anchors[0].Symbol != "Middleware" {
		t.Errorf("unexpected symbol: %q", anchors[0].Symbol)
	}

	// Envelope should report the capture count.
	text := callResultText(t, res)
	var envelope map[string]any
	if err := json.Unmarshal([]byte(text), &envelope); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if got, ok := envelope["anchors_captured"].(float64); !ok || got != 1 {
		t.Errorf("expected anchors_captured=1 in envelope, got %v", envelope["anchors_captured"])
	}
}

// ─── 3.3: mem_save WITHOUT code_anchors is byte-identical to today ──────────

func TestHandleSave_WithoutCodeAnchors_EnvelopeUnchanged(t *testing.T) {
	s := newMCPTestStore(t)
	// No AnchorProbe injected — falls back to the lazily-constructed real
	// anchor.NewProbe(), which must never be invoked because no code_anchors
	// argument is supplied at all.
	h := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "Plain save with no anchors",
		"content": "**What**: a normal save\n**Why**: regression",
		"type":    "decision",
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected save error: %s", callResultText(t, res))
	}

	text := callResultText(t, res)
	var envelope map[string]any
	if err := json.Unmarshal([]byte(text), &envelope); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if _, exists := envelope["anchors_captured"]; exists {
		t.Errorf("did not expect anchors_captured key when code_anchors is omitted, got %v", envelope["anchors_captured"])
	}

	anchors, err := s.ListActiveAnchors("")
	if err != nil {
		t.Fatalf("ListActiveAnchors: %v", err)
	}
	if len(anchors) != 0 {
		t.Errorf("expected no anchors when code_anchors is omitted, got %d", len(anchors))
	}
}

// ─── 3.4: no-git / not-a-repo -> save succeeds, no anchor, no error surfaced ─

func TestHandleSave_CodeAnchorsCaptureFailure_SaveStillSucceeds(t *testing.T) {
	s := newMCPTestStore(t)
	fake := &fakeAnchorCapturer{fallback: fakeCaptureResult{err: anchor.ErrNotAGitRepo}}
	h := handleSave(s, MCPConfig{AnchorProbe: fake}, NewSessionActivity(10*time.Minute))

	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "Save in a non-repo directory",
		"content": "**What**: saved outside a git repo\n**Why**: regression",
		"type":    "bugfix",
		"code_anchors": []any{
			map[string]any{"file": "foo.go", "symbol": "Foo", "line_start": float64(1), "line_end": float64(3)},
		},
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("mem_save must never fail because of anchor capture: %s", callResultText(t, res))
	}

	text := callResultText(t, res)
	var envelope map[string]any
	if err := json.Unmarshal([]byte(text), &envelope); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if _, exists := envelope["error_code"]; exists {
		t.Errorf("expected no error_code surfaced in envelope, got %v", envelope["error_code"])
	}
	if got, ok := envelope["anchors_captured"].(float64); !ok || got != 0 {
		t.Errorf("expected anchors_captured=0, got %v", envelope["anchors_captured"])
	}

	anchors, err := s.ListActiveAnchors("")
	if err != nil {
		t.Fatalf("ListActiveAnchors: %v", err)
	}
	if len(anchors) != 0 {
		t.Errorf("expected no anchor persisted when capture degrades gracefully, got %d", len(anchors))
	}
}

// Malformed entries (missing file, bad line range) are skipped individually
// rather than failing the whole save.
func TestHandleSave_CodeAnchorsMalformedEntrySkipped(t *testing.T) {
	s := newMCPTestStore(t)
	fake := &fakeAnchorCapturer{
		byFile: map[string]fakeCaptureResult{
			"good.go": {anc: anchor.Anchor{File: "good.go", Symbol: "Good", LineStart: 1, LineEnd: 2, ContentHash: "h"}},
		},
	}
	h := handleSave(s, MCPConfig{AnchorProbe: fake}, NewSessionActivity(10*time.Minute))

	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "Mixed valid and malformed anchors",
		"content": "**What**: mixed anchors\n**Why**: regression",
		"type":    "bugfix",
		"code_anchors": []any{
			map[string]any{"file": "good.go", "symbol": "Good", "line_start": float64(1), "line_end": float64(2)},
			map[string]any{"symbol": "NoFile", "line_start": float64(1), "line_end": float64(2)},     // missing file
			map[string]any{"file": "bad-range.go", "line_start": float64(5), "line_end": float64(1)}, // end < start
		},
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected save error: %s", callResultText(t, res))
	}

	anchors, err := s.ListActiveAnchors("")
	if err != nil {
		t.Fatalf("ListActiveAnchors: %v", err)
	}
	if len(anchors) != 1 || anchors[0].FilePath != "good.go" {
		t.Fatalf("expected only the well-formed anchor to persist, got %+v", anchors)
	}
}
