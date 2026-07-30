//go:build windows

package enforce

import "os/exec"

// setProcessGroup is a documented gap on Windows: killing a full process
// tree there requires Job Objects (CreateJobObject/AssignProcessToJobObject
// via golang.org/x/sys/windows), a materially larger platform-specific
// feature than this fix's scope (this capability's dev/CI targets are
// macOS/Linux — see design.md). On Windows this package still relies on Go's
// default CommandContext cancellation behavior: killing only the direct
// child process.
func setProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup falls back to killing only the direct process on
// Windows — see setProcessGroup's doc comment for why a full tree kill is
// out of scope here.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

// processExists always reports false on Windows: no test exercises this
// path there (runner_test.go's process-group scenario skips on Windows,
// since its POSIX shell syntax does not translate to cmd /C either).
func processExists(pid int) bool { return false }
