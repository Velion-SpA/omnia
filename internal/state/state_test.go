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
