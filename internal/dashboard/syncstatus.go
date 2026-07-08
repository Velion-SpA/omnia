package dashboard

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CursorEntry holds a single source cursor from state.json.
type CursorEntry struct {
	Key       string // e.g. "github:owner/repo"
	Source    string // "github" | "discord"
	Repo      string // the part after the first ":"
	Timestamp string // RFC3339
	Age       string // human-readable age
}

// SyncStatus is the parsed sync state shown in the status panel.
type SyncStatus struct {
	StateFile string
	Cursors   []CursorEntry
	LogLines  []string // last N lines of omnia.log, empty if absent
	LogFile   string
	Missing   bool // true when state.json doesn't exist
}

// loadSyncStatus reads ~/.local/state/omnia/state.json and the log tail.
// It degrades gracefully when files are absent.
func loadSyncStatus() SyncStatus {
	home, _ := os.UserHomeDir()
	stateFile := filepath.Join(home, ".local", "state", "omnia", "state.json")
	logFile := filepath.Join(home, "Library", "Logs", "omnia", "omnia.log")

	status := SyncStatus{
		StateFile: stateFile,
		LogFile:   logFile,
	}

	// Read state.json.
	data, err := os.ReadFile(stateFile)
	if err != nil {
		status.Missing = true
	} else {
		var cursors map[string]string
		if json.Unmarshal(data, &cursors) == nil {
			for k, v := range cursors {
				parts := strings.SplitN(k, ":", 2)
				source := k
				repo := ""
				if len(parts) == 2 {
					source = parts[0]
					repo = parts[1]
				}
				age := ""
				if t, err := time.Parse(time.RFC3339, v); err == nil {
					age = formatAge(t.Format("2006-01-02 15:04:05"))
				}
				status.Cursors = append(status.Cursors, CursorEntry{
					Key:       k,
					Source:    source,
					Repo:      repo,
					Timestamp: v,
					Age:       age,
				})
			}
			sort.Slice(status.Cursors, func(i, j int) bool {
				return status.Cursors[i].Key < status.Cursors[j].Key
			})
		}
	}

	// Tail the log file (last 30 lines). Degrade gracefully if absent.
	status.LogLines = tailFile(logFile, 30)

	return status
}

// SyncTargetView is a single row in the /sync page's health table: one cloud
// sync target with its live lifecycle, reason, and freshness — rendered so a
// degraded/blocked target is visible instead of silently absent (the gap
// OBL-12 closes: previously this data was only reachable via `omnia cloud
// status` or the HTTP /sync/status endpoint, never the dashboard).
type SyncTargetView struct {
	Cloud         string // display cloud alias, e.g. "work" (resolved via cloud.json)
	Project       string // canonical project segment of the target key ("" for a bare/legacy key)
	Lifecycle     string // "idle" | "pending" | "running" | "healthy" | "degraded"
	ReasonCode    string
	ReasonMessage string
	LastError     string
	Age           string // relative "last synced" time, via formatAge
}

// healthChipLabel returns the display label for a lifecycle value, defaulting
// to "unknown" for an empty value and echoing anything unrecognized verbatim
// (forward-compatible with a future lifecycle without hiding the row).
func healthChipLabel(lifecycle string) string {
	switch strings.TrimSpace(lifecycle) {
	case "":
		return "unknown"
	default:
		return lifecycle
	}
}

// syncTargetReasonText assembles the reason shown in the health table: prefer
// the human reason_message; fall back to the raw reason_code; fall back to
// last_error when neither is set (e.g. a transport failure recorded only
// there). Empty when none are present.
func syncTargetReasonText(t SyncTargetView) string {
	if t.ReasonMessage != "" {
		return t.ReasonMessage
	}
	if t.ReasonCode != "" {
		return t.ReasonCode
	}
	return t.LastError
}

// tailFile returns the last n lines of a file. Returns nil if the file can't be read.
func tailFile(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}
