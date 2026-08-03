package diagnostic

import (
	"context"
	"fmt"
)

// CheckMCPLauncher is the code for the MCP launcher-path check.
const CheckMCPLauncher = "mcp_launcher"

// MCPLauncher describes one MCP server declaration found on this machine, already
// resolved against the filesystem by the caller.
//
// internal/diagnostic keeps its single store dependency, so the filesystem walk
// lives behind Scope.ReadMCPLaunchers — the same injected-reader seam
// Scope.ReadEmbeddingSnapshot uses.
type MCPLauncher struct {
	// ConfigPath is the declaration file, reported as evidence so the operator
	// can open the exact file that is wrong.
	ConfigPath string
	// ServerName is the key under "mcpServers".
	ServerName string
	// Command is the declared launch command, verbatim.
	Command string
	// Absolute records whether Command is an absolute path. A relative command is
	// resolved through the PATH the harness hands the MCP process, which this
	// check cannot observe, so it is never judged.
	Absolute bool
	// Exists / Executable are only meaningful when Absolute is true.
	Exists     bool
	Executable bool
}

// MCPLauncherCheck reports MCP server declarations whose command cannot start.
//
// #243: the Claude Code plugin ships an absolute command path baked in at package
// time. When the install method changed the path went dead, the server failed to
// spawn in about a millisecond with ENOENT, and every session ran without memory
// tools. Nothing surfaced it — `claude mcp list` kept reporting "Connected"
// because it resolves the bare name through the shell PATH, a different resolution
// path than the one the session harness uses. For a memory product, a silent
// absence of the save path is the worst possible failure, so doctor must be the
// layer that notices.
type MCPLauncherCheck struct{}

func (MCPLauncherCheck) Code() string { return CheckMCPLauncher }

func (c MCPLauncherCheck) Run(ctx context.Context, scope Scope) (CheckResult, error) {
	// An unwired seam abstains EXPLICITLY rather than claiming health: evidence
	// carries checked=false so a reader can tell "we looked and it is fine" from
	// "we did not look". Callers without a filesystem reader (the server's doctor
	// endpoint) could never clear a warning here, and a permanent unfixable
	// warning is noise — noise is what trains people to ignore reports.
	if scope.ReadMCPLaunchers == nil {
		return okResult(c.Code(), map[string]any{
			"checked": false,
			"reason":  "this caller did not provide an MCP declaration reader; run `omnia doctor` from the CLI to check MCP launcher paths",
		}), nil
	}

	launchers, err := scope.ReadMCPLaunchers(ctx)
	if err != nil {
		return resultFromFindings(c.Code(), nil, []Finding{{
			CheckID:      c.Code(),
			Severity:     SeverityWarning,
			ReasonCode:   "mcp_declarations_unreadable",
			Message:      "MCP server declarations could not be read.",
			Why:          "If a declaration is unreadable, a broken launch path cannot be ruled out, and an MCP server that fails to spawn does so silently.",
			Evidence:     mustJSON(map[string]any{"error": err.Error()}),
			SafeNextStep: "Check the permissions on ~/.claude/plugins/cache and re-run `omnia doctor`.",
		}}), nil
	}

	var findings []Finding
	checked := 0
	for _, l := range launchers {
		// A relative command depends on the PATH the harness passes to the MCP
		// process, which differs by launch origin and is not observable from here.
		// Reporting either verdict would be a guess.
		if !l.Absolute {
			continue
		}
		checked++
		evidence := map[string]any{
			"config_path": l.ConfigPath,
			"server_name": l.ServerName,
			"command":     l.Command,
		}
		switch {
		case !l.Exists:
			findings = append(findings, Finding{
				CheckID:    c.Code(),
				Severity:   SeverityWarning,
				ReasonCode: "mcp_command_missing",
				Message: fmt.Sprintf("MCP server %q declares a command that does not exist: %s",
					l.ServerName, l.Command),
				Why:          "The server fails to spawn with ENOENT in about a millisecond, so it never appears as a timeout or an error in the session — the tools are simply absent. `claude mcp list` can still report it as connected, because that command resolves the name through the shell PATH instead of the declared absolute path.",
				Evidence:     mustJSON(evidence),
				SafeNextStep: fmt.Sprintf("Point %s at the installed binary, or restore the path it expects. Restart the client afterwards — MCP servers are registered at session start, not hot-reloaded.", l.ConfigPath),
			})
		case !l.Executable:
			findings = append(findings, Finding{
				CheckID:    c.Code(),
				Severity:   SeverityWarning,
				ReasonCode: "mcp_command_not_executable",
				Message: fmt.Sprintf("MCP server %q declares a command that is not executable: %s",
					l.ServerName, l.Command),
				Why:          "The file is present, so a path check alone looks healthy, but the spawn still fails and the tools never load.",
				Evidence:     mustJSON(evidence),
				SafeNextStep: fmt.Sprintf("Run `chmod +x %s`, then restart the client.", l.Command),
			})
		}
	}

	if len(findings) > 0 {
		return resultFromFindings(c.Code(), nil, findings), nil
	}
	return okResult(c.Code(), map[string]any{
		"checked":            true,
		"declarations_found": len(launchers),
		"absolute_commands":  checked,
	}), nil
}
