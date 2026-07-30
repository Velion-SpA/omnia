package enforce

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestRunCommand_ExitZeroPasses (task 7.3, REQ-412) verifies a configured
// command that exits 0 is reported as a passing postcondition, and confirms
// no LLM/network call is anywhere on this path (the command itself is the
// only external process spawned).
func TestRunCommand_ExitZeroPasses(t *testing.T) {
	res := RunCommand(context.Background(), t.TempDir(), "exit 0", 5)
	if !res.Passed() {
		t.Fatalf("expected Passed()=true for exit 0, got %+v", res)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
}

// TestRunCommand_NonZeroExitFails (task 7.3) verifies a configured command
// that exits non-zero yields a failing (not erroring) postcondition result,
// with the exit code and output preserved for the caller.
func TestRunCommand_NonZeroExitFails(t *testing.T) {
	res := RunCommand(context.Background(), t.TempDir(), "echo boom 1>&2; exit 3", 5)
	if res.Passed() {
		t.Fatalf("expected Passed()=false for exit 3, got %+v", res)
	}
	if res.ExitCode != 3 {
		t.Fatalf("ExitCode = %d, want 3", res.ExitCode)
	}
	if !strings.Contains(res.Output, "boom") {
		t.Fatalf("expected output to capture stderr, got %q", res.Output)
	}
	if res.Err != nil {
		t.Fatalf("a non-zero exit is a FAILING postcondition, not a runner error; got Err=%v", res.Err)
	}
	if res.TimedOut {
		t.Fatalf("did not expect a timeout for a fast non-zero exit")
	}
}

// TestRunCommand_TimeoutIsNotAFailure (fail-safe: ADR-6/design.md — a runner
// timeout must be distinguishable from a normal postcondition failure so the
// caller can map it to `flag`, never `block`) verifies a hard-timeout command
// is reported as TimedOut, not as a plain non-zero-exit failure.
func TestRunCommand_TimeoutIsNotAFailure(t *testing.T) {
	start := time.Now()
	res := RunCommand(context.Background(), t.TempDir(), "sleep 5", 1)
	if !res.TimedOut {
		t.Fatalf("expected TimedOut=true, got %+v", res)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("expected the hard timeout to cut the command short, took %s", elapsed)
	}
}

// TestRunCommand_UnknownBinaryIsAFailureNotAnError verifies that when the
// shell itself starts fine but the configured command's binary does not
// exist, the shell's own non-zero exit status (not a Go-level start error)
// carries the result — a mechanically observed failure, never silently a
// pass.
func TestRunCommand_UnknownBinaryIsAFailureNotAnError(t *testing.T) {
	res := RunCommand(context.Background(), t.TempDir(), "definitely-not-a-real-omnia-enforce-binary --flag", 5)
	if res.Passed() {
		t.Fatalf("expected Passed()=false when the command's binary does not exist, got %+v", res)
	}
	if res.ExitCode == 0 {
		t.Fatalf("expected a non-zero exit code from the shell, got %+v", res)
	}
}

// TestRunCommand_TimeoutKillsBackgroundedGrandchild (SHOULD-FIX finding:
// exec.CommandContext's default cancellation only kills the direct sh -c
// child, not the whole process tree) reproduces a command that forks and
// backgrounds a child — exactly how real postcondition commands like "go
// test ./..." or "npm test" fan out per-package/per-worker subprocesses —
// and verifies the backgrounded child is actually gone, not orphaned and
// still running, once RunCommand returns its timeout verdict.
func TestRunCommand_TimeoutKillsBackgroundedGrandchild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group kill is unix-specific (see runner_windows.go); this scenario's POSIX shell syntax does not translate to cmd /C either")
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")

	// Background a real child ("sleep 30 &"), capture ITS pid (not the
	// shell's) via $!, then "wait" so the shell blocks until the hard
	// timeout fires. This mirrors the reviewer's exact repro.
	command := "sleep 30 & echo $! > " + pidFile + "; wait"

	start := time.Now()
	res := RunCommand(context.Background(), dir, command, 1)
	elapsed := time.Since(start)

	if !res.TimedOut {
		t.Fatalf("expected TimedOut=true, got %+v", res)
	}
	// The backgrounded child inherits the same stdout/stderr pipe Go uses
	// to capture output; if only the direct sh -c process is killed, that
	// child keeps the pipe's write end open and cmd.Wait() itself blocks
	// until it exits ~30s later on its own — silently defeating the whole
	// point of a 1s timeout, not just leaking a background process.
	if elapsed > 10*time.Second {
		t.Fatalf("RunCommand took %s to return after a 1s timeout — the backgrounded grandchild was never killed, so it (and the pipe it inherited) kept RunCommand blocked until it exited on its own", elapsed)
	}

	pidBytes, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read backgrounded child's pid file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatalf("parse child pid %q: %v", pidBytes, err)
	}

	// Poll for a short grace window instead of asserting instantaneously —
	// the kernel needs a moment to actually reap the killed process.
	deadline := time.Now().Add(2 * time.Second)
	for processExists(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("backgrounded child pid %d is still alive after RunCommand returned a timeout verdict — only the direct sh -c child was killed, not the whole process group", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestRunCommand_UnstartableProcessIsARunnerError verifies a genuine
// runner-level failure — the working directory itself does not exist, so
// exec cannot even start the shell — is reported via Err, never silently
// treated as a passing or failing postcondition.
func TestRunCommand_UnstartableProcessIsARunnerError(t *testing.T) {
	missingDir := t.TempDir() + "/does-not-exist"
	res := RunCommand(context.Background(), missingDir, "exit 0", 5)
	if res.Passed() {
		t.Fatalf("expected Passed()=false when the process cannot start, got %+v", res)
	}
	if res.Err == nil {
		t.Fatalf("expected a non-nil Err when the process cannot start")
	}
}
