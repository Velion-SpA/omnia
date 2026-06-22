package main

import (
	"os"
	"testing"
)

// TestMain guards the entire cmd/omnia test package against ever touching the
// user's real ~/.engram or ~/.omnia. It pins OMNIA_DATA_DIR to an isolated temp
// directory, so any code path that resolves the default data directory — including
// the startup AutoMigrate inside main() and store.DefaultConfig() — stays inside
// the sandbox. Tests that specifically exercise data-dir resolution override this
// with t.Setenv.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "omnia-cmd-test-*")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("OMNIA_DATA_DIR", dir); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
