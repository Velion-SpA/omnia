package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeDeclaration(t *testing.T, dir, command string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, ".mcp.json")
	body, err := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			"omnia": map[string]any{"command": command, "args": []string{"mcp"}},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// The reported failure (#243): the declaration points at a binary that no longer
// exists after the install moved. Resolution must report it dead.
func TestLaunchersFromFileFlagsMissingAbsoluteCommand(t *testing.T) {
	dir := t.TempDir()
	path := writeDeclaration(t, dir, filepath.Join(dir, "nope", "omnia"))

	got := launchersFromFile(path)
	if len(got) != 1 {
		t.Fatalf("expected one launcher, got %d", len(got))
	}
	if !got[0].Absolute {
		t.Error("an absolute command must be marked absolute")
	}
	if got[0].Exists {
		t.Error("a nonexistent command must not be reported as existing")
	}
}

func TestLaunchersFromFileResolvesExecutableCommand(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "omnia")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write bin: %v", err)
	}
	path := writeDeclaration(t, dir, bin)

	got := launchersFromFile(path)
	if len(got) != 1 {
		t.Fatalf("expected one launcher, got %d", len(got))
	}
	if !got[0].Exists || !got[0].Executable {
		t.Errorf("expected an existing executable command, got %+v", got[0])
	}
}

// Present but not executable still fails to spawn — a path-only check would call
// this healthy.
func TestLaunchersFromFileDetectsNonExecutable(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "omnia")
	if err := os.WriteFile(bin, []byte("not executable"), 0o644); err != nil {
		t.Fatalf("write bin: %v", err)
	}
	path := writeDeclaration(t, dir, bin)

	got := launchersFromFile(path)
	if len(got) != 1 {
		t.Fatalf("expected one launcher, got %d", len(got))
	}
	if !got[0].Exists {
		t.Error("the file exists and must be reported as existing")
	}
	if got[0].Executable {
		t.Error("a non-executable file must not be reported as executable")
	}
}

// A relative command is deliberately left unresolved — see readMCPLaunchers.
func TestLaunchersFromFileLeavesRelativeCommandUnresolved(t *testing.T) {
	dir := t.TempDir()
	path := writeDeclaration(t, dir, "omnia")

	got := launchersFromFile(path)
	if len(got) != 1 {
		t.Fatalf("expected one launcher, got %d", len(got))
	}
	if got[0].Absolute {
		t.Error("a bare command name is not an absolute path")
	}
	if got[0].Exists || got[0].Executable {
		t.Error("a relative command must not be resolved against the filesystem")
	}
}

// A malformed declaration must not abort the scan of the files that are fine.
func TestLaunchersFromFileIgnoresMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".mcp.json")
	if err := os.WriteFile(path, []byte("{ not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := launchersFromFile(path); len(got) != 0 {
		t.Fatalf("malformed declaration must yield no launchers, got %d", len(got))
	}
}
