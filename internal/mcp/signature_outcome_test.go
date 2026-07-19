package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	mcppkg "github.com/mark3labs/mcp-go/mcp"
	"github.com/velion/omnia/internal/store"
)

// ─── mem_save: error_signature + outcome (design obs #1498 / audit #1497, slice 1) ───

func TestHandleSave_AcceptsErrorSignatureAndOutcome(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	h := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	rawErrorText := "panic: runtime error: index out of range [5] with length 3"

	res, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":           "Fixed slice index panic",
		"content":         "Guarded the slice access with a length check before indexing.",
		"type":            "bugfix",
		"project":         "engram",
		"error_signature": rawErrorText,
		"outcome":         "worked",
	}}})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected save error: %s", callResultText(t, res))
	}

	body := callResultJSON(t, res)
	wantSig := store.NormalizeErrorSignature(rawErrorText)
	if body["error_signature"] != wantSig {
		t.Fatalf("expected envelope error_signature=%q, got %v", wantSig, body["error_signature"])
	}
	if body["outcome"] != "worked" {
		t.Fatalf("expected envelope outcome=%q, got %v", "worked", body["outcome"])
	}

	obs, err := s.RecentObservations("engram", "project", 5)
	if err != nil {
		t.Fatalf("recent observations: %v", err)
	}
	if len(obs) != 1 {
		t.Fatalf("expected 1 observation, got %d", len(obs))
	}
	if obs[0].ErrorSignature == nil || *obs[0].ErrorSignature != wantSig {
		t.Fatalf("expected persisted error_signature=%q, got %v", wantSig, obs[0].ErrorSignature)
	}
	if obs[0].Outcome == nil || *obs[0].Outcome != "worked" {
		t.Fatalf("expected persisted outcome=%q, got %v", "worked", obs[0].Outcome)
	}
}

func TestHandleSave_RejectsInvalidOutcome(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	h := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	res, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "Fixed slice index panic",
		"content": "Guarded the slice access with a length check before indexing.",
		"type":    "bugfix",
		"project": "engram",
		"outcome": "not-a-real-outcome",
	}}})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected invalid outcome to fail")
	}
	if !strings.Contains(callResultText(t, res), "outcome") {
		t.Fatalf("expected outcome validation error, got %q", callResultText(t, res))
	}

	obs, err := s.RecentObservations("engram", "project", 5)
	if err != nil {
		t.Fatalf("recent observations: %v", err)
	}
	if len(obs) != 0 {
		t.Fatalf("expected no observation to be written, got %#v", obs)
	}
}

// ─── mem_update: outcome ─────────────────────────────────────────────────────

func TestHandleUpdate_AcceptsOutcome(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s-outcome-update", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	id, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-outcome-update",
		Type:      "bugfix",
		Title:     "Fixed slice index panic",
		Content:   "Guarded the slice access with a length check before indexing.",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	res, err := handleUpdate(s)(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"id":      float64(id),
		"outcome": "worked",
	}}})
	if err != nil {
		t.Fatalf("update handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected update error: %s", callResultText(t, res))
	}

	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("get observation: %v", err)
	}
	if obs.Outcome == nil || *obs.Outcome != "worked" {
		t.Fatalf("expected persisted outcome=worked, got %v", obs.Outcome)
	}
}

// ─── mem_save -> mem_search round trip: signature + proven-fix flag ─────────

func TestHandleSearch_RoundTripsSignatureAndSurfacesProvenFixFlag(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	save := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	originalOccurrence := `TypeError: Cannot read properties of undefined (reading 'items')
    at processOrder (/home/ci/workspace/omnia/src/services/orderService.ts:99:3)`

	saveRes, err := save(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":           "Checkout flow stability improvement",
		"content":         "Added a guard clause before accessing the shopping list so incomplete checkouts no longer crash silently.",
		"type":            "bugfix",
		"project":         "engram",
		"error_signature": originalOccurrence,
		"outcome":         "worked",
	}}})
	if err != nil {
		t.Fatalf("save handler error: %v", err)
	}
	if saveRes.IsError {
		t.Fatalf("unexpected save error: %s", callResultText(t, saveRes))
	}

	search := handleSearch(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	// A NEW occurrence of the SAME bug (different path/line) — as if the
	// agent pasted the fresh stack trace it just hit.
	newOccurrence := `TypeError: Cannot read properties of undefined (reading 'items')
    at processOrder (/Users/benja/dev/omnia/src/services/orderService.ts:47:19)`

	searchRes, err := search(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"query":   newOccurrence,
		"project": "engram",
	}}})
	if err != nil {
		t.Fatalf("search handler error: %v", err)
	}
	if searchRes.IsError {
		t.Fatalf("unexpected search error: %s", callResultText(t, searchRes))
	}

	body := callResultJSON(t, searchRes)
	results, ok := body["results"].([]any)
	if !ok || len(results) != 1 {
		t.Fatalf("expected exactly 1 structured search result, got %v", body["results"])
	}
	entry, _ := results[0].(map[string]any)
	if entry["signature_match"] != true {
		t.Fatalf("expected signature_match=true on the round-tripped result, got %v", entry["signature_match"])
	}
	if entry["outcome"] != "worked" {
		t.Fatalf("expected outcome=%q surfaced on the round-tripped result, got %v", "worked", entry["outcome"])
	}
}
