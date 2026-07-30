package enforce

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"runtime"
	"time"
)

// defaultTimeoutSeconds is the runtime fail-safe floor: EnforcementConfig's
// TimeoutSeconds has no config.go default (see design.md ADR-6), so a
// zero-valued/unset config can never hang a postcondition command forever.
const defaultTimeoutSeconds = 60

// outputPreviewChars caps the amount of raw command output surfaced back to
// a caller (mem_enforce/omnia enforce) — never the full log, just enough to
// diagnose a failure.
const outputPreviewChars = 2000

// CommandResult is the mechanical outcome of running one postcondition
// command (ADR-6: "one sandboxed command runner, exit-code = verdict").
type CommandResult struct {
	// ExitCode is only meaningful when Err is nil and TimedOut is false.
	ExitCode int
	// Output is the combined stdout+stderr, truncated to outputPreviewChars.
	Output string
	// TimedOut is true when the hard timeout fired before the command
	// finished. A timeout is a DEGRADED result, not a postcondition failure
	// — the caller (gate.go) maps it to `flag`, never `block` (fail-safe).
	TimedOut bool
	// Err is non-nil only for a genuine runner error (the command could not
	// be started at all, e.g. an unknown binary). A non-zero exit code is
	// NOT an Err — it is a normal, mechanically-observed failing
	// postcondition (ExitCode holds the code).
	Err error
}

// Passed reports whether this command result represents a satisfied
// postcondition: it ran to completion with exit code 0.
func (r CommandResult) Passed() bool {
	return r.Err == nil && !r.TimedOut && r.ExitCode == 0
}

// RunCommand runs `command` as a shell command in repoRoot with a hard
// timeout; exit 0 = pass (ADR-6). timeoutSeconds <= 0 falls back to
// defaultTimeoutSeconds. The command is handed to the platform shell (`sh
// -c` / `cmd /C`) so operator-configured commands (e.g. "go test ./...")
// can use normal shell syntax without omnia parsing it itself.
func RunCommand(ctx context.Context, repoRoot, command string, timeoutSeconds int) CommandResult {
	if timeoutSeconds <= 0 {
		timeoutSeconds = defaultTimeoutSeconds
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	shell, flag := "sh", "-c"
	if runtime.GOOS == "windows" {
		shell, flag = "cmd", "/C"
	}

	cmd := exec.CommandContext(runCtx, shell, flag, command)
	if repoRoot != "" {
		cmd.Dir = repoRoot
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	err := cmd.Run()
	result := CommandResult{Output: truncateOutput(buf.String())}

	if runCtx.Err() == context.DeadlineExceeded {
		result.TimedOut = true
		return result
	}
	if err == nil {
		result.ExitCode = 0
		return result
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// A non-zero exit is a normal, mechanically observed failure — NOT
		// a runner error (never silently treated as pass or promoted to a
		// hard failure that hides the real exit code).
		result.ExitCode = exitErr.ExitCode()
		return result
	}

	// The command never started at all (unknown binary, permission denied,
	// etc.) — a genuine runner error. The caller (gate.go) maps this to
	// `flag`, never `block` (fail-safe: a wrong block is worse than a miss).
	result.Err = err
	return result
}

func truncateOutput(s string) string {
	runes := []rune(s)
	if len(runes) <= outputPreviewChars {
		return s
	}
	return string(runes[:outputPreviewChars]) + "..."
}
