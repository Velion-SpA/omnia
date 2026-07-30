//go:build !windows

package enforce

import (
	"os/exec"
	"syscall"
)

// setProcessGroup makes cmd the leader of a brand-new process group
// (setpgid(0,0) in the child, before exec). This is what lets
// killProcessGroup later signal the WHOLE group — not just cmd's own PID —
// so a child the command forks and backgrounds (e.g. "sleep 30 &" inside a
// postcondition's "sh -c" invocation) dies too. Real postcondition commands
// (test runners, linters, build tools) routinely fan out per-package or
// per-worker subprocesses this way, so killing only the direct sh -c child
// leaves those grandchildren running past the timeout verdict.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup sends SIGKILL to cmd's entire process group: a negative
// PID targets the group (kill(2)), and setProcessGroup made cmd.Process.Pid
// the group's pgid. Falls back to killing only the direct process if the
// group-wide signal fails for any reason (e.g. Setpgid did not take effect),
// so the direct child still dies even in that degraded case.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}

// processExists reports whether pid is still alive. Used only by
// runner_test.go to confirm a backgrounded grandchild was actually reaped
// after RunCommand returns a timeout verdict — signal 0 performs no action
// but still reports ESRCH for a dead process.
func processExists(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
