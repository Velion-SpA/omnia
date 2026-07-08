// Package audit provides an append-only JSONL audit log for Omnia dashboard mutations.
// Each mutation (edit, soft-delete, hard-delete) is recorded here.
// This log is Omnia-owned and does NOT touch Engram.
package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Action represents the type of mutation recorded.
type Action string

const (
	ActionEdit       Action = "edit"
	ActionSoftDelete Action = "soft_delete"
	ActionHardDelete Action = "hard_delete"
)

// Entry is a single audit log record.
type Entry struct {
	Ts            string `json:"ts"`             // RFC3339
	Actor         string `json:"actor"`           // provisional identity
	Action        Action `json:"action"`          // edit|soft_delete|hard_delete
	ObservationID int    `json:"observation_id"`
	Project       string `json:"project"`
	Summary       string `json:"summary"`         // short before/after for edits, title for deletes; never full content
	Result        string `json:"result"`          // "ok" | "error"
}

// defaultLogPath returns ~/.local/state/omnia/audit.jsonl.
func defaultLogPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "omnia", "audit.jsonl")
}

// Append writes e as a JSONL line to the audit log. It uses O_APPEND for atomicity.
// If the write fails, it logs to stderr and returns WITHOUT blocking the caller action.
// The dir is created on first write.
func Append(e Entry) {
	path := defaultLogPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "audit: mkdir: %v\n", err)
		return
	}
	data, err := json.Marshal(e)
	if err != nil {
		fmt.Fprintf(os.Stderr, "audit: marshal: %v\n", err)
		return
	}
	data = append(data, '\n')

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "audit: open: %v\n", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		fmt.Fprintf(os.Stderr, "audit: write: %v\n", err)
	}
}

// Read returns the last n entries from the audit log, newest-first.
// Degrades gracefully if the file is absent (returns nil, nil).
func Read(n int) ([]Entry, error) {
	path := defaultLogPath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("audit read: %w", err)
	}

	lines := splitLines(data)
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}

	entries := make([]Entry, 0, len(lines))
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			continue // skip malformed lines
		}
		entries = append(entries, e)
	}

	// Reverse to newest-first.
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	return entries, nil
}

// EntriesForObservation returns all audit entries for a given observation ID, newest-first.
func EntriesForObservation(obsID int) ([]Entry, error) {
	all, err := Read(1000)
	if err != nil {
		return nil, err
	}
	var result []Entry
	for _, e := range all {
		if e.ObservationID == obsID {
			result = append(result, e)
		}
	}
	return result, nil
}

// Now returns the current time as RFC3339. Exported so tests can assert format.
func Now() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}
