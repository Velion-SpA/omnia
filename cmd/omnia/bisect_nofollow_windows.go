//go:build windows

package main

import "os"

// openBisectStateFileNoFollow opens path for reading. syscall.O_NOFOLLOW is
// not defined on Windows, so this platform relies on
// validateBisectStateFile's earlier os.Lstat symlink check (defense in
// depth) rather than an atomic no-follow open.
func openBisectStateFileNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY, 0)
}
