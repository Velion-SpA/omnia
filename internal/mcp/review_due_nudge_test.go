package mcp

// review_due_nudge_test.go — RED→GREEN tests for omnia-0.3.1-write-hygiene
// PR11 (spaced-review / Play G, design D11's mem_context due-count nudge).
// Spec requirement "Existing-Tool Output Changes Gated Default-OFF": gate
// off MUST stay byte-for-byte identical to pre-spaced-review mem_context
// output even when due observations exist; gate on MUST add exactly the
// due-count field + one text line, nothing else.

import (
	"context"
	"strings"
	"testing"
	"time"

	mcppkg "github.com/mark3labs/mcp-go/mcp"
	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/store"
)

// seedDueObservation creates a session + a "decision" observation whose
// review_after has already passed, matching TestHandleReviewListAndMarkReviewed's
// own backdating convention (s.DB().Exec against the real schema).
func seedDueObservation(t *testing.T, s *store.Store, sessionID, project string) int64 {
	t.Helper()
	if err := s.CreateSession(sessionID, project, "/tmp/review-nudge"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	id, err := s.AddObservation(store.AddObservationParams{SessionID: sessionID, Type: "decision", Title: "due decision", Content: "due content", Project: project})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	past := time.Now().UTC().Add(-time.Hour).Format("2006-01-02 15:04:05")
	if _, err := s.DB().Exec(`UPDATE observations SET review_after = ? WHERE id = ?`, past, id); err != nil {
		t.Fatalf("backdate review_after: %v", err)
	}
	return id
}

func TestHandleContextReviewDueNudgeGateOffByteIdentical(t *testing.T) {
	s := newMCPTestStore(t)
	seedDueObservation(t, s, "s-nudge-off", "nudge-off-proj")
	activity := NewSessionActivity(10 * time.Minute)
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"project": "nudge-off-proj"}}}

	implicitOff, err := handleContext(s, MCPConfig{}, activity)(context.Background(), req)
	if err != nil {
		t.Fatalf("context handler error: %v", err)
	}
	explicitOff, err := handleContext(s, MCPConfig{Review: config.ReviewConfig{DueNudge: false}}, activity)(context.Background(), req)
	if err != nil {
		t.Fatalf("context handler error: %v", err)
	}
	if callResultText(t, implicitOff) != callResultText(t, explicitOff) {
		t.Fatalf("zero-value Review.DueNudge must behave identically to explicit false")
	}
	if strings.Contains(callResultText(t, implicitOff), "due for review") {
		t.Fatalf("gate off must never emit the due-review nudge text, got: %s", callResultText(t, implicitOff))
	}
	if _, ok := callResultJSON(t, implicitOff)["review_due_count"]; ok {
		t.Fatalf("gate off must never surface review_due_count")
	}
}

func TestHandleContextReviewDueNudgeGateOnAddsCountAndText(t *testing.T) {
	s := newMCPTestStore(t)
	seedDueObservation(t, s, "s-nudge-on", "nudge-on-proj")
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"project": "nudge-on-proj"}}}

	off, err := handleContext(s, MCPConfig{}, NewSessionActivity(10*time.Minute))(context.Background(), req)
	if err != nil {
		t.Fatalf("context handler error (off): %v", err)
	}
	on, err := handleContext(s, MCPConfig{Review: config.ReviewConfig{DueNudge: true}}, NewSessionActivity(10*time.Minute))(context.Background(), req)
	if err != nil {
		t.Fatalf("context handler error (on): %v", err)
	}

	onBody := callResultJSON(t, on)
	count, ok := onBody["review_due_count"].(float64)
	if !ok || int(count) != 1 {
		t.Fatalf("expected review_due_count=1, got %v", onBody["review_due_count"])
	}

	offResult, _ := callResultJSON(t, off)["result"].(string)
	onResult, _ := onBody["result"].(string)
	wantSuffix := "\n\n1 memory due for review — run `omnia review-due` to see them."
	if onResult != offResult+wantSuffix {
		t.Fatalf("gate on must append ONLY the due-nudge suffix to the gate-off result\noff: %q\non:  %q", offResult, onResult)
	}
}
