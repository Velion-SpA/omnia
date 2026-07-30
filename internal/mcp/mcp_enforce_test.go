package mcp

import (
	"context"
	"strings"
	"testing"

	mcppkg "github.com/mark3labs/mcp-go/mcp"
	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/store"
)

func upsertMCPEnforceTestProcedure(t *testing.T, s *store.Store, trigger string) {
	t.Helper()
	if _, err := s.UpsertProcedure(store.Procedure{
		Project:  "mcp-enforce-test",
		Polarity: store.ProcedurePolarityAntiPlaybook,
		Trigger:  trigger,
		Steps: []store.ProcedureStep{
			{Order: 1, Template: "run tests before declaring the change done"},
		},
		ExpectedOutcome:   "tests pass",
		PostconditionKind: store.PostconditionTestsPass,
		Confidence:        0.8,
		State:             store.ProcedureStateTrusted,
		SourceObsSyncIDs:  []string{"obs-fixture"},
	}); err != nil {
		t.Fatalf("UpsertProcedure: %v", err)
	}
}

// TestNewServerWithConfigDoesNotRegisterEnforceWhenDisabled (task 8.7/8.8,
// REQ-410) mirrors TestNewServerWithConfigDoesNotRegisterBlameWhenDisabled:
// mem_enforce must not exist on the server at all while
// enforcement.enabled is false — capability-disabled is total inertness,
// not a runtime check inside the handler.
func TestNewServerWithConfigDoesNotRegisterEnforceWhenDisabled(t *testing.T) {
	srv := NewServerWithConfig(newMCPTestStore(t), MCPConfig{Enforcement: config.EnforcementConfig{}}, nil)
	if _, found := srv.ListTools()["mem_enforce"]; found {
		t.Fatal("mem_enforce must not be registered while enforcement is disabled")
	}
}

// TestNewServerWithConfigRegistersEnforceWhenEnabled verifies mem_enforce IS
// registered once enforcement.enabled is true.
func TestNewServerWithConfigRegistersEnforceWhenEnabled(t *testing.T) {
	srv := NewServerWithConfig(newMCPTestStore(t), MCPConfig{Enforcement: config.EnforcementConfig{Enabled: true}}, nil)
	if _, found := srv.ListTools()["mem_enforce"]; !found {
		t.Fatal("mem_enforce must be registered while enforcement is enabled")
	}
}

// TestHandleEnforce_PassWhenNoTrustedProcedureMatches verifies the
// happy-path fail-safe: no matching trusted procedures returns pass.
func TestHandleEnforce_PassWhenNoTrustedProcedureMatches(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate the ActionEnforce audit write from the real user's audit.jsonl
	s := newMCPTestStore(t)
	cfg := MCPConfig{Enforcement: config.EnforcementConfig{Enabled: true, Mode: "flag"}}
	h := handleEnforce(s, cfg)

	res, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"project":       "mcp-enforce-test",
		"files_touched": []any{"internal/enforce/matcher.go"},
	}}})
	if err != nil {
		t.Fatalf("handleEnforce: %v", err)
	}
	text := callResultText(t, res)
	if !strings.Contains(text, `"verdict":"pass"`) {
		t.Fatalf("expected a pass verdict, got %s", text)
	}
}

// TestHandleEnforce_FlagsFailingTrustedProcedure verifies a matching trusted
// procedure whose configured command fails yields a flag verdict (default
// mode) via the MCP surface.
func TestHandleEnforce_FlagsFailingTrustedProcedure(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate the ActionEnforce audit write from the real user's audit.jsonl
	s := newMCPTestStore(t)
	upsertMCPEnforceTestProcedure(t, s, "changes touching internal/enforce/matcher.go must run go test before completion")

	cfg := MCPConfig{Enforcement: config.EnforcementConfig{
		Enabled:  true,
		Mode:     "flag",
		Commands: config.EnforcementCommandsConfig{Tests: "exit 1"},
	}}
	h := handleEnforce(s, cfg)

	res, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"project":       "mcp-enforce-test",
		"files_touched": []any{"internal/enforce/matcher.go"},
	}}})
	if err != nil {
		t.Fatalf("handleEnforce: %v", err)
	}
	text := callResultText(t, res)
	if !strings.Contains(text, `"verdict":"flag"`) {
		t.Fatalf("expected a flag verdict, got %s", text)
	}
}

// TestHandleEnforce_OverrideParamReturnsDistinctOverrideVerdict verifies the
// override/override_reason MCP params are plumbed through to the gate
// (REQ-415).
func TestHandleEnforce_OverrideParamReturnsDistinctOverrideVerdict(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate the ActionEnforce audit write from the real user's audit.jsonl
	s := newMCPTestStore(t)
	upsertMCPEnforceTestProcedure(t, s, "changes touching internal/enforce/matcher.go must run go test before completion")

	cfg := MCPConfig{Enforcement: config.EnforcementConfig{
		Enabled:  true,
		Mode:     "flag",
		Commands: config.EnforcementCommandsConfig{Tests: "exit 1"},
	}}
	h := handleEnforce(s, cfg)

	res, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"project":         "mcp-enforce-test",
		"files_touched":   []any{"internal/enforce/matcher.go"},
		"override":        true,
		"override_reason": "verified locally, known flaky in CI",
	}}})
	if err != nil {
		t.Fatalf("handleEnforce: %v", err)
	}
	text := callResultText(t, res)
	if !strings.Contains(text, `"verdict":"override"`) {
		t.Fatalf("expected an override verdict, got %s", text)
	}
}
