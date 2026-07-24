package mcp

// token_budget_wiring_test.go — handleSearch integration coverage for Omnia
// v0.3 "Context Economy" PR2 (design obs #1643, spec obs #1642
// injection-budget REQ6): confirms ApplyTokenBudget is actually wired into
// handleSearch's pipeline end-to-end, mirroring
// anchor_downrank_test.go's flag-off/flag-on integration pair for
// StructuralForgetting.

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	mcppkg "github.com/mark3labs/mcp-go/mcp"
	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/store"
)

// TestHandleSearch_InjectionBudgetDisabled_AllResultsReturned is the
// flag-OFF regression pin: with cfg.Injection.Budget.Enabled == false (the
// default), handleSearch returns every match untouched — byte-for-byte
// identical to pre-v0.3 behavior — regardless of how large the combined
// content is.
func TestHandleSearch_InjectionBudgetDisabled_AllResultsReturned(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s-tb-off", "tbproj-off", "/tmp/tbproj-off"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := s.AddObservation(store.AddObservationParams{
			SessionID: "s-tb-off", Type: "manual", Title: "Budget fixture " + strconv.Itoa(i),
			Content: "token budget wiring fixture content " + strconv.Itoa(i), Project: "tbproj-off", Scope: "project",
		}); err != nil {
			t.Fatalf("add observation: %v", err)
		}
	}

	search := handleSearch(s, MCPConfig{Injection: config.InjectionConfig{Budget: config.TokenBudgetConfig{Enabled: false}}}, NewSessionActivity(10*time.Minute))
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"query": "budget wiring fixture", "project": "tbproj-off", "scope": "project", "limit": 10.0,
	}}}
	res, err := search(context.Background(), req)
	if err != nil {
		t.Fatalf("handleSearch error: %v", err)
	}
	body := callResultJSON(t, res)
	results, ok := body["results"].([]any)
	if !ok || len(results) != 5 {
		t.Fatalf("expected all 5 results with the budget flag off, got %#v", body["results"])
	}
}

// TestHandleSearch_InjectionBudgetEnabled_TrimsToFitMaxTokens is the
// flag-ON happy path: a tiny max_tokens trims handleSearch's result set to
// however many complete rows fit, never producing an error.
func TestHandleSearch_InjectionBudgetEnabled_TrimsToFitMaxTokens(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s-tb-on", "tbproj-on", "/tmp/tbproj-on"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := s.AddObservation(store.AddObservationParams{
			SessionID: "s-tb-on", Type: "manual", Title: "Budget fixture " + strconv.Itoa(i),
			Content: "token budget wiring fixture content " + strconv.Itoa(i), Project: "tbproj-on", Scope: "project",
		}); err != nil {
			t.Fatalf("add observation: %v", err)
		}
	}

	// A tiny budget (1 token) should trim the result set down to well under
	// the 5 seeded rows.
	search := handleSearch(s, MCPConfig{Injection: config.InjectionConfig{Budget: config.TokenBudgetConfig{Enabled: true, MaxTokens: 1}}}, NewSessionActivity(10*time.Minute))
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"query": "budget wiring fixture", "project": "tbproj-on", "scope": "project", "limit": 10.0,
	}}}
	res, err := search(context.Background(), req)
	if err != nil {
		t.Fatalf("handleSearch error: %v", err)
	}
	body := callResultJSON(t, res)
	results, ok := body["results"].([]any)
	if !ok {
		t.Fatalf("expected a results array, got %#v", body["results"])
	}
	if len(results) >= 5 {
		t.Fatalf("expected the tiny token budget to trim below all 5 seeded rows, got %d", len(results))
	}
}

// TestHandleSearch_InjectionBudgetEnabled_TrimTransparencyFooterAndEnvelope
// (PR2 review fix, WARNING): "Found <post-trim> memories" alone is
// indistinguishable from a genuine no-match/low-match response when the
// token budget silently drops real hits — "Found 0 memories" after a full
// drop reads identically to "nothing matched". When the budget pass
// actually trims rows, handleSearch must append a "---\n<N> more result(s)
// matched but were trimmed..." footer to the text block AND set
// budget_trimmed in the JSON envelope, so callers can tell "no matches"
// apart from "matches were cut by the budget".
func TestHandleSearch_InjectionBudgetEnabled_TrimTransparencyFooterAndEnvelope(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s-tb-footer", "tbproj-footer", "/tmp/tbproj-footer"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := s.AddObservation(store.AddObservationParams{
			SessionID: "s-tb-footer", Type: "manual", Title: "Budget fixture " + strconv.Itoa(i),
			Content: "token budget wiring fixture content " + strconv.Itoa(i), Project: "tbproj-footer", Scope: "project",
		}); err != nil {
			t.Fatalf("add observation: %v", err)
		}
	}

	search := handleSearch(s, MCPConfig{Injection: config.InjectionConfig{Budget: config.TokenBudgetConfig{Enabled: true, MaxTokens: 1}}}, NewSessionActivity(10*time.Minute))
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"query": "budget wiring fixture", "project": "tbproj-footer", "scope": "project", "limit": 10.0,
	}}}
	res, err := search(context.Background(), req)
	if err != nil {
		t.Fatalf("handleSearch error: %v", err)
	}

	body := callResultJSON(t, res)
	results, ok := body["results"].([]any)
	if !ok {
		t.Fatalf("expected a results array, got %#v", body["results"])
	}
	wantTrimmed := 5 - len(results)
	if wantTrimmed <= 0 {
		t.Fatalf("expected the tiny token budget to trim at least one row, got %d results", len(results))
	}

	trimmed, ok := body["budget_trimmed"].(float64)
	if !ok || int(trimmed) != wantTrimmed {
		t.Fatalf("expected envelope budget_trimmed == %d, got %#v", wantTrimmed, body["budget_trimmed"])
	}

	text := callResultText(t, res)
	wantFooter := strconv.Itoa(wantTrimmed) + " more result(s) matched but were trimmed by the injection token budget (injection.budget.max_tokens)"
	if !strings.Contains(text, wantFooter) {
		t.Fatalf("expected trim-transparency footer containing %q, got text: %q", wantFooter, text)
	}
}

// TestHandleSearch_InjectionBudgetDisabled_NoTrimFooterOrEnvelopeField pins
// the flag-OFF case: no footer, no budget_trimmed field at all.
func TestHandleSearch_InjectionBudgetDisabled_NoTrimFooterOrEnvelopeField(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s-tb-off-footer", "tbproj-off-footer", "/tmp/tbproj-off-footer"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-tb-off-footer", Type: "manual", Title: "Budget fixture",
		Content: "token budget wiring fixture content", Project: "tbproj-off-footer", Scope: "project",
	}); err != nil {
		t.Fatalf("add observation: %v", err)
	}

	search := handleSearch(s, MCPConfig{Injection: config.InjectionConfig{Budget: config.TokenBudgetConfig{Enabled: false}}}, NewSessionActivity(10*time.Minute))
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"query": "budget wiring fixture", "project": "tbproj-off-footer", "scope": "project", "limit": 10.0,
	}}}
	res, err := search(context.Background(), req)
	if err != nil {
		t.Fatalf("handleSearch error: %v", err)
	}

	body := callResultJSON(t, res)
	if _, present := body["budget_trimmed"]; present {
		t.Fatalf("expected no budget_trimmed field when the budget flag is off, got %#v", body["budget_trimmed"])
	}
	text := callResultText(t, res)
	if strings.Contains(text, "were trimmed by the injection token budget") {
		t.Fatalf("expected no trim-transparency footer when the budget flag is off, got text: %q", text)
	}
}

// TestHandleSearch_InjectionBudgetEnabled_NothingTrimmedNoFooterOrField pins
// the enabled-but-nothing-trimmed case: a generous budget that fits every
// row must not produce a footer or envelope field either.
func TestHandleSearch_InjectionBudgetEnabled_NothingTrimmedNoFooterOrField(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s-tb-fit", "tbproj-fit", "/tmp/tbproj-fit"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-tb-fit", Type: "manual", Title: "Budget fixture",
		Content: "token budget wiring fixture content", Project: "tbproj-fit", Scope: "project",
	}); err != nil {
		t.Fatalf("add observation: %v", err)
	}

	search := handleSearch(s, MCPConfig{Injection: config.InjectionConfig{Budget: config.TokenBudgetConfig{Enabled: true, MaxTokens: 100000}}}, NewSessionActivity(10*time.Minute))
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"query": "budget wiring fixture", "project": "tbproj-fit", "scope": "project", "limit": 10.0,
	}}}
	res, err := search(context.Background(), req)
	if err != nil {
		t.Fatalf("handleSearch error: %v", err)
	}

	body := callResultJSON(t, res)
	results, ok := body["results"].([]any)
	if !ok || len(results) != 1 {
		t.Fatalf("expected the generous budget to keep the single seeded row, got %#v", body["results"])
	}
	if _, present := body["budget_trimmed"]; present {
		t.Fatalf("expected no budget_trimmed field when nothing was trimmed, got %#v", body["budget_trimmed"])
	}
	text := callResultText(t, res)
	if strings.Contains(text, "were trimmed by the injection token budget") {
		t.Fatalf("expected no trim-transparency footer when nothing was trimmed, got text: %q", text)
	}
}
