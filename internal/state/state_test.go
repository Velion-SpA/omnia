package state_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/velion/omnia/internal/state"
)

func TestCursorRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	s, err := state.New(path)
	if err != nil {
		t.Fatal(err)
	}

	s.SetCursor("github", "velion/api", "2024-01-15T00:00:00Z")
	s.SetCursor("discord", "channel-123", "987654321")
	s.Flush()

	// Reload from disk.
	s2, err := state.New(path)
	if err != nil {
		t.Fatal(err)
	}

	if v, ok := s2.GetCursor("github", "velion/api"); !ok || v != "2024-01-15T00:00:00Z" {
		t.Errorf("github cursor = %q ok=%v, want 2024-01-15T00:00:00Z", v, ok)
	}
	if v, ok := s2.GetCursor("discord", "channel-123"); !ok || v != "987654321" {
		t.Errorf("discord cursor = %q ok=%v, want 987654321", v, ok)
	}

	// Missing key returns empty+false.
	if _, ok := s2.GetCursor("jira", "unknown"); ok {
		t.Error("expected missing key to return false")
	}

	// Clean up.
	os.Remove(path)
}

// TestAtomicFlushSurvivesPreexistingTmpFile verifies that Flush succeeds even when
// a stale .tmp file already exists at the target path (W3). This simulates a previous
// crash mid-write that left an orphaned tmp file behind.
func TestAtomicFlushSurvivesPreexistingTmpFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	tmpPath := path + ".tmp"

	// Pre-create a stale tmp file with invalid content.
	if err := os.WriteFile(tmpPath, []byte("stale garbage"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := state.New(path)
	if err != nil {
		t.Fatal(err)
	}

	s.SetCursor("github", "org/repo", "2024-03-01T00:00:00Z")

	if err := s.Flush(); err != nil {
		t.Fatalf("Flush failed with pre-existing tmp file: %v", err)
	}

	// Reload and verify the cursor survived.
	s2, err := state.New(path)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := s2.GetCursor("github", "org/repo"); !ok || v != "2024-03-01T00:00:00Z" {
		t.Errorf("cursor = %q ok=%v, want 2024-03-01T00:00:00Z true", v, ok)
	}

	// The tmp file must have been replaced or removed.
	if _, err := os.Stat(tmpPath); err == nil {
		// tmp file still exists — it should contain valid JSON (the rename succeeded).
		data, _ := os.ReadFile(tmpPath)
		// After a successful rename, the tmp file should not exist. If it does, it is
		// the old stale content and represents an error.
		t.Errorf("stale tmp file still present with content: %s", data)
	}
}
