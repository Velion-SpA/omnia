package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCmdConsolidateDisabledIsNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("consolidation:\n  enabled: false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out, _ := captureOutput(t, func() { cmdConsolidate([]string{"--config", path}) })
	if out != "" {
		t.Fatalf("unexpected output %q", out)
	}
}
