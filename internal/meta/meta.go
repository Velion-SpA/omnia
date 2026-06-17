package meta

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schema version — bump when fields are added (parser stays backward-tolerant).
const SchemaVersion = 1

// Meta holds the structured metadata appended to every Omnia observation.
// The block is machine-facing; it does NOT replace human-readable sections.
type Meta struct {
	SchemaVersion int       // always 1 for now
	Source        string    // github | discord | jira | whatsapp
	Kind          string    // pull_request | issue | commit_digest | message_digest
	Layer         string    // ingested (always for Omnia output; curated omits the block)
	Project       string    // Engram project name
	Repo          string    // github: owner/repo; discord: guild/channel; empty otherwise
	SourceID      string    // PR/issue number, message snowflake, etc.
	Status        string    // open | closed | merged | "" for digests
	Author        string    // primary author login/username
	Participants  []string  // all actors (deduplicated)
	URL           string    // canonical web URL; empty if not applicable
	CreatedAt     time.Time // zero if unknown
	UpdatedAt     time.Time // zero if unknown
	IngestedAt    time.Time // set by the caller to time.Now()
	// Chunk info — only set when an observation is part of a multi-chunk sequence.
	ChunkCurrent int // 0 means not chunked; ≥1 means "chunk N of Total"
	ChunkTotal   int // 0 means not chunked
}

const blockFence = "```omnia-meta"
const blockClose = "```"

// Render produces the fenced omnia-meta block to append to observation content.
// The block is always a complete, parseable unit — safe to append to any chunk.
func Render(m Meta) string {
	var sb strings.Builder
	sb.WriteString(blockFence)
	sb.WriteByte('\n')

	// Always-emitted fields.
	sb.WriteString(fmt.Sprintf("schema_version: %d\n", m.SchemaVersion))
	sb.WriteString(fmt.Sprintf("source: %s\n", m.Source))
	sb.WriteString(fmt.Sprintf("kind: %s\n", m.Kind))
	sb.WriteString(fmt.Sprintf("layer: %s\n", m.Layer))
	sb.WriteString(fmt.Sprintf("project: %s\n", m.Project))

	// Optional fields — only emit if non-zero/non-empty.
	if m.Repo != "" {
		sb.WriteString(fmt.Sprintf("repo: %s\n", m.Repo))
	}
	if m.SourceID != "" {
		sb.WriteString(fmt.Sprintf("source_id: %q\n", m.SourceID))
	}
	if m.Status != "" {
		sb.WriteString(fmt.Sprintf("status: %s\n", m.Status))
	}
	if m.Author != "" {
		sb.WriteString(fmt.Sprintf("author: %s\n", m.Author))
	}
	if len(m.Participants) > 0 {
		// FIX-3: JSON-encode participants so names containing ", " round-trip losslessly.
		// Format: participants: ["alice","bob"]
		encoded, err := json.Marshal(m.Participants)
		if err == nil {
			sb.WriteString(fmt.Sprintf("participants: %s\n", encoded))
		}
	}
	if m.URL != "" {
		sb.WriteString(fmt.Sprintf("url: %s\n", m.URL))
	}
	if !m.CreatedAt.IsZero() {
		sb.WriteString(fmt.Sprintf("created_at: %s\n", m.CreatedAt.UTC().Format(time.RFC3339)))
	}
	if !m.UpdatedAt.IsZero() {
		sb.WriteString(fmt.Sprintf("updated_at: %s\n", m.UpdatedAt.UTC().Format(time.RFC3339)))
	}
	if !m.IngestedAt.IsZero() {
		sb.WriteString(fmt.Sprintf("ingested_at: %s\n", m.IngestedAt.UTC().Format(time.RFC3339)))
	}
	if m.ChunkCurrent > 0 {
		sb.WriteString(fmt.Sprintf("chunk: %d/%d\n", m.ChunkCurrent, m.ChunkTotal))
	}

	sb.WriteString(blockClose)
	sb.WriteByte('\n')
	return sb.String()
}

// Strip returns content with the trailing omnia-meta block removed. It only
// strips when a VALID block is present (same mandatory-field contract as Parse),
// so a curated human note that merely quotes the fence is returned untouched.
// Used to build clean embedding input: the identical boilerplate block would
// otherwise cluster all ingested memories spuriously in vector space.
func Strip(content string) string {
	if _, ok := Parse(content); !ok {
		return content
	}
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	// The block is always appended last; find the LAST opening fence and drop
	// everything from there to the end (the close fence and nothing follows it).
	startIdx := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == blockFence {
			startIdx = i
		}
	}
	if startIdx < 0 {
		return content
	}
	return strings.TrimRight(strings.Join(lines[:startIdx], "\n"), " \t\n")
}

// Parse extracts the omnia-meta block from observation content.
// Returns (Meta, true) if a valid, complete block is found; (Meta{}, false) otherwise.
//
// Separation contract: a block is only considered valid if it contains the mandatory
// fields schema_version, source, and kind. This prevents a curated human note that
// merely quotes an ```omnia-meta snippet from being misclassified as an ingested
// observation. Combined with scanning for the LAST fence occurrence, this ensures
// derived-vs-curated separation is robust even when PR bodies embed fake blocks.
//
// The parser is tolerant: unknown fields are silently ignored (forward compatibility).
func Parse(content string) (Meta, bool) {
	// Normalize CRLF to LF so Windows-encoded content parses correctly.
	content = strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(content, "\n")

	// FIX-1: Find the LAST opening fence line, not the first.
	// Omnia always appends the meta block at the end; an earlier occurrence in the
	// body (e.g. from a PR description quoting the format) must be ignored.
	startIdx := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == blockFence {
			startIdx = i // keep overwriting — we want the last match
		}
	}
	if startIdx < 0 {
		return Meta{}, false
	}

	// Find the closing fence line (must come after the opening fence).
	endIdx := -1
	for i := startIdx + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == blockClose {
			endIdx = i
			break
		}
	}
	if endIdx < 0 {
		return Meta{}, false
	}

	// Parse key: value lines between fences.
	var m Meta
	for _, line := range lines[startIdx+1 : endIdx] {
		idx := strings.Index(line, ": ")
		if idx < 0 {
			// Try key: with nothing after (shouldn't happen, but be tolerant).
			continue
		}
		key := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx+2:])

		switch key {
		case "schema_version":
			v, err := strconv.Atoi(value)
			if err == nil {
				m.SchemaVersion = v
			}
		case "source":
			m.Source = value
		case "kind":
			m.Kind = value
		case "layer":
			m.Layer = value
		case "project":
			m.Project = value
		case "repo":
			m.Repo = value
		case "source_id":
			// Strip surrounding double quotes.
			m.SourceID = strings.Trim(value, `"`)
		case "status":
			m.Status = value
		case "author":
			m.Author = value
		case "participants":
			// FIX-3: participants are JSON-encoded to support names containing ", ".
			// Format: ["alice","bob"] — fall back to empty slice on parse error.
			if value != "" {
				var parts []string
				if err := json.Unmarshal([]byte(value), &parts); err == nil {
					m.Participants = parts
				}
			}
		case "url":
			m.URL = value
		case "created_at":
			t, err := time.Parse(time.RFC3339, value)
			if err == nil {
				m.CreatedAt = t
			}
		case "updated_at":
			t, err := time.Parse(time.RFC3339, value)
			if err == nil {
				m.UpdatedAt = t
			}
		case "ingested_at":
			t, err := time.Parse(time.RFC3339, value)
			if err == nil {
				m.IngestedAt = t
			}
		case "chunk":
			// Parse N/T format.
			parts := strings.SplitN(value, "/", 2)
			if len(parts) == 2 {
				cur, err1 := strconv.Atoi(parts[0])
				tot, err2 := strconv.Atoi(parts[1])
				if err1 == nil && err2 == nil {
					m.ChunkCurrent = cur
					m.ChunkTotal = tot
				}
			}
			// Unknown keys are silently ignored (forward compatibility).
		}
	}

	// FIX-2: Mandatory field validation — separation contract.
	// A block missing schema_version, source, or kind is not a valid ingested block.
	// This makes it safe for the semantic index backfill to call Parse on all
	// Engram observations without misclassifying curated human notes.
	if m.SchemaVersion == 0 || m.Source == "" || m.Kind == "" {
		return Meta{}, false
	}

	return m, true
}
