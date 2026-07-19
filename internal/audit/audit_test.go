package audit_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/velion/omnia/internal/audit"
)

func TestAppendAndRead(t *testing.T) {
	// Point audit at a temp dir.
	dir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	defer os.Setenv("HOME", origHome)

	e1 := audit.Entry{
		Ts:            audit.Now(),
		Actor:         "testuser",
		Action:        audit.ActionEdit,
		ObservationID: 42,
		Project:       "myproject",
		Summary:       "title: old → new",
		Result:        "ok",
	}
	e2 := audit.Entry{
		Ts:            audit.Now(),
		Actor:         "testuser",
		Action:        audit.ActionSoftDelete,
		ObservationID: 99,
		Project:       "myproject",
		Summary:       "My observation title",
		Result:        "ok",
	}

	audit.Append(e1)
	audit.Append(e2)

	entries, err := audit.Read(200)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	// Newest-first: e2 should be first.
	if entries[0].ObservationID != 99 {
		t.Errorf("expected newest entry first (id=99), got id=%d", entries[0].ObservationID)
	}
	if entries[1].ObservationID != 42 {
		t.Errorf("expected second entry id=42, got id=%d", entries[1].ObservationID)
	}

	// EntriesForObservation filters correctly.
	obs42, err := audit.EntriesForObservation(42)
	if err != nil {
		t.Fatalf("EntriesForObservation: %v", err)
	}
	if len(obs42) != 1 {
		t.Fatalf("expected 1 entry for obs 42, got %d", len(obs42))
	}
}

func TestRead_GracefulWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	defer os.Setenv("HOME", origHome)

	entries, err := audit.Read(200)
	if err != nil {
		t.Fatalf("Read on absent file should not error: %v", err)
	}
	if entries != nil {
		t.Errorf("expected nil entries when file absent, got %v", entries)
	}
}

func TestAppend_CreatesDir(t *testing.T) {
	dir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	defer os.Setenv("HOME", origHome)

	audit.Append(audit.Entry{
		Ts:            audit.Now(),
		Actor:         "bot",
		Action:        audit.ActionHardDelete,
		ObservationID: 1,
		Project:       "p",
		Summary:       "title",
		Result:        "ok",
	})

	path := filepath.Join(dir, ".local", "state", "omnia", "audit.jsonl")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("audit.jsonl not created: %v", err)
	}
}
