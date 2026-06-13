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
