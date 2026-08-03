package diagnostic

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Issue #243: the Claude Code plugin declares an absolute command path baked in at
// package time. When the install method changed (~/.local/bin -> Homebrew) that path
// went dead, every session lost its memory tools, and NOTHING surfaced it: the MCP
// server failed to spawn in ~1ms with ENOENT, and `claude mcp list` still reported
// "Connected" because it resolves the bare name through the shell PATH instead.
//
// A startup failure nobody sees is the worst failure mode for a memory product, so
// doctor has to be the thing that sees it.

func TestMCPLauncherCheckFlagsDeadAbsoluteCommand(t *testing.T) {
	check := MCPLauncherCheck{}
	scope := Scope{
		ReadMCPLaunchers: func(context.Context) ([]MCPLauncher, error) {
			return []MCPLauncher{{
				ConfigPath: "/Users/x/.claude/plugins/cache/omnia/omnia/0.1.0/.mcp.json",
				ServerName: "omnia",
				Command:    "/Users/x/.local/bin/omnia",
				Absolute:   true,
				Exists:     false,
			}}, nil
		},
	}

	result, err := check.Run(context.Background(), scope)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("expected exactly one finding for a dead declared command, got %d", len(result.Findings))
	}
	f := result.Findings[0]
	if f.Severity != SeverityWarning {
		t.Errorf("severity = %q, want %q", f.Severity, SeverityWarning)
	}
	if f.ReasonCode != "mcp_command_missing" {
		t.Errorf("reason_code = %q, want mcp_command_missing", f.ReasonCode)
	}
	// The operator must be able to act without opening the source.
	if !strings.Contains(f.Message, "/Users/x/.local/bin/omnia") {
		t.Errorf("message must name the dead path, got %q", f.Message)
	}
}

func TestMCPLauncherCheckFlagsNonExecutableCommand(t *testing.T) {
	check := MCPLauncherCheck{}
	scope := Scope{
		ReadMCPLaunchers: func(context.Context) ([]MCPLauncher, error) {
			return []MCPLauncher{{
				ConfigPath: "/cfg/.mcp.json",
				ServerName: "omnia",
				Command:    "/usr/local/bin/omnia",
				Absolute:   true,
				Exists:     true,
				Executable: false,
			}}, nil
		},
	}

	result, err := check.Run(context.Background(), scope)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("expected one finding for a present-but-not-executable command, got %d", len(result.Findings))
	}
	if got := result.Findings[0].ReasonCode; got != "mcp_command_not_executable" {
		t.Errorf("reason_code = %q, want mcp_command_not_executable", got)
	}
}

func TestMCPLauncherCheckHealthyWhenCommandResolves(t *testing.T) {
	check := MCPLauncherCheck{}
	scope := Scope{
		ReadMCPLaunchers: func(context.Context) ([]MCPLauncher, error) {
			return []MCPLauncher{{
				ConfigPath: "/cfg/.mcp.json",
				ServerName: "omnia",
				Command:    "/opt/homebrew/bin/omnia",
				Absolute:   true,
				Exists:     true,
				Executable: true,
			}}, nil
		},
	}

	result, err := check.Run(context.Background(), scope)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Findings) != 0 {
		t.Fatalf("a resolvable command must not produce findings, got %d", len(result.Findings))
	}
}

// A relative command cannot be judged from the declaration alone: whether it
// resolves depends on the PATH the harness hands the MCP process, which differs by
// launch origin. Measured on macOS: a terminal-launched session passes a 42-entry
// PATH including Homebrew, while a launchd-spawned process gets only
// /usr/bin:/bin:/usr/sbin:/sbin. Guessing either way would produce a finding the
// operator cannot trust, so the check reports it as unverifiable instead.
func TestMCPLauncherCheckDoesNotGuessAboutRelativeCommands(t *testing.T) {
	check := MCPLauncherCheck{}
	scope := Scope{
		ReadMCPLaunchers: func(context.Context) ([]MCPLauncher, error) {
			return []MCPLauncher{{
				ConfigPath: "/cfg/.mcp.json",
				ServerName: "omnia",
				Command:    "omnia",
				Absolute:   false,
			}}, nil
		},
	}

	result, err := check.Run(context.Background(), scope)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Findings) != 0 {
		t.Fatalf("a relative command must not be reported as broken, got %d finding(s)", len(result.Findings))
	}
}

// An unwired seam must abstain explicitly rather than claim health — the same
// contract EmbeddingLagCheck follows, so a reader can tell "we looked and it is
// fine" from "we did not look".
func TestMCPLauncherCheckAbstainsWithoutReader(t *testing.T) {
	result, err := MCPLauncherCheck{}.Run(context.Background(), Scope{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Findings) != 0 {
		t.Fatalf("missing reader must not produce findings, got %d", len(result.Findings))
	}
	if !strings.Contains(string(result.Evidence), "\"checked\":false") {
		t.Errorf("evidence must record that the check did not run, got %s", result.Evidence)
	}
}

func TestMCPLauncherCheckWarnsWhenDeclarationsUnreadable(t *testing.T) {
	check := MCPLauncherCheck{}
	scope := Scope{
		ReadMCPLaunchers: func(context.Context) ([]MCPLauncher, error) {
			return nil, errors.New("permission denied")
		},
	}

	result, err := check.Run(context.Background(), scope)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("expected one finding when declarations cannot be read, got %d", len(result.Findings))
	}
	if got := result.Findings[0].ReasonCode; got != "mcp_declarations_unreadable" {
		t.Errorf("reason_code = %q, want mcp_declarations_unreadable", got)
	}
}
