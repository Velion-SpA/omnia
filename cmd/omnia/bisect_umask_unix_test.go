//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/velion/omnia/internal/store"
)

func TestBisectStateModeUnderRestrictiveUmask(t *testing.T) {
	if os.Getenv("OMNIA_UMASK_CHILD") == "" {
		cmd := exec.Command(os.Args[0], "-test.run", "^TestBisectStateModeUnderRestrictiveUmask$")
		cmd.Env = append(os.Environ(), "OMNIA_UMASK_CHILD=1")
		out, err := cmd.CombinedOutput()
		requireBisect(t, err == nil, string(out))
		return
	}
	dir, err := os.MkdirTemp("", "omnia-umask-*")
	requireBisect(t, err == nil && os.Chmod(dir, 0o700) == nil, "prepare umask fixture")
	path := filepath.Join(dir, "bisect-state.json")
	state := &bisectState{Version: bisectStateVersion, Generation: "generation", Candidates: []store.BisectEvent{{ID: 1, SyncID: "sync", CreatedAt: "2026-01-01T00:00:00Z"}}}
	oldUmask := syscall.Umask(0o777)
	defer syscall.Umask(oldUmask)
	requireBisect(t, writeBisectState(path, state) == nil, "write state under restrictive umask")
	info, err := os.Stat(path)
	requireBisect(t, err == nil && info.Mode().Perm() == 0o600, "restrictive umask changed state mode")
}
