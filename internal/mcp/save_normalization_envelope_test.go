package mcp

// save_normalization_envelope_test.go — RED->GREEN tests closing the
// save-normalization spec REQ "Warnings Are Itemized In The Envelope" at the
// handleSave layer (sdd-verify report obs #1682 CRITICAL finding #1). Reuses
// write_gate_envelope_test.go's helpers (newMCPTestStoreWithWriteHygiene,
// enrollProject, mustHandleSave, resultText).

import (
	"strings"
	"testing"
)

// TestHandleSaveSurfacesSaveWarningsWhenHygieneEnabled: junk content (missing
// a Keywords section here) produces extra["save_warnings"] plus an appended
// notice line in the result text, gated on cfg.WriteHygieneEnabled — mirrors
// write_gate's own gating exactly (design obs #1668 D4 precedent).
func TestHandleSaveSurfacesSaveWarningsWhenHygieneEnabled(t *testing.T) {
	s := newMCPTestStoreWithWriteHygiene(t)
	enrollProject(t, s, "wg-mcp-warnings")

	_, body := mustHandleSave(t, s, MCPConfig{WriteHygieneEnabled: true}, map[string]any{
		"title":   "Junk content note",
		"content": "This body has no keywords line at all, just plain prose.",
		"type":    "discovery",
		"project": "wg-mcp-warnings",
	})

	warningsRaw, ok := body["save_warnings"].([]any)
	if !ok {
		t.Fatalf("expected save_warnings array in envelope, got %v", body["save_warnings"])
	}
	if len(warningsRaw) != 1 || warningsRaw[0] != "missing Keywords section" {
		t.Fatalf("expected save_warnings=[\"missing Keywords section\"], got %v", warningsRaw)
	}
	text := resultText(t, body)
	if !strings.Contains(text, "missing Keywords section") {
		t.Errorf("expected result text to include a hygiene warning notice, got %q", text)
	}
}

// TestHandleSaveOmitsSaveWarningsWhenHygieneDisabled is the kill-switch
// byte-for-byte case: with WriteHygieneEnabled=false, the SAME junk content
// that triggers a warning in the test above (no Keywords section) produces
// no save_warnings key and no notice line at all.
func TestHandleSaveOmitsSaveWarningsWhenHygieneDisabled(t *testing.T) {
	s := newMCPTestStoreWithWriteHygiene(t)
	enrollProject(t, s, "wg-mcp-warnings-off")

	_, body := mustHandleSave(t, s, MCPConfig{WriteHygieneEnabled: false}, map[string]any{
		"title":   "Junk content note, gate off",
		"content": "This body has no keywords line at all, just plain prose.",
		"type":    "discovery",
		"project": "wg-mcp-warnings-off",
	})

	if _, ok := body["save_warnings"]; ok {
		t.Fatalf("expected save_warnings to be absent with the gate off, got %v", body["save_warnings"])
	}
	text := resultText(t, body)
	if strings.Contains(text, "Hygiene warning") {
		t.Errorf("expected no hygiene warning notice with the gate off, got %q", text)
	}
}
