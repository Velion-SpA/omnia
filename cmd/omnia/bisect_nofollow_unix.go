//go:build !windows

package main

import (
	"os"
	"syscall"
)

// openBisectStateFileNoFollow opens path for reading with O_NOFOLLOW folded
// into the open() syscall itself: if path is a symlink (planted there after
// validateBisectStateFile's earlier Lstat check), the open fails atomically
// with ELOOP instead of following it. This closes the TOCTOU race window
// that a separate Lstat-then-ReadFile pair would otherwise leave open.
func openBisectStateFileNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}
