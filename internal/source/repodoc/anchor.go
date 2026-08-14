package repodoc

import (
	"fmt"
	"strconv"
	"strings"
)

// AnchorInfo is the git staleness-detection carrier for a single doc chunk.
//
// Investigation note (plan P1's "Staleness anchor" requirement): a
// memory_anchors row (internal/store/anchors.go, UpsertAnchorParams) is
// shaped exactly like internal/anchor.Anchor — RepoRoot, File, Symbol,
// LineStart, LineEnd, BlameSHA, BlameAt, ContentHash — and
// internal/anchor.Probe.Capture(ctx, dir, file, symbol, start, end) already
// computes that entire shape via `git blame` + `git hash-object`, for an
// arbitrary line range. A doc chunk's heading section IS an arbitrary line
// range (Section.LineStart/LineEnd, see chunk.go), so this package reuses
// Capture directly — heading path as Symbol, section line range as
// (start, end) — instead of reinventing staleness detection.
//
// core.Item (internal/core/domain.go) has no structured metadata field to
// carry an Anchor through the Source -> Sink pipeline, so — following the
// same convention internal/source/github and internal/source/discord
// already use for their own provenance block (internal/meta.Render) — the
// captured Anchor is rendered as a second, separately-fenced block appended
// to the chunk's Content. AnchorInfo mirrors internal/anchor.Anchor's field
// names 1:1 so a later wiring step can map ParseAnchorBlock's result
// straight into store.UpsertAnchorParams once it has resolved the written
// item's obs_sync_id (this package deliberately does not do that wiring
// itself — see the "wiring request" in this change's final report; it
// requires internal/store and internal/mcp, both outside this package's
// ownership).
type AnchorInfo struct {
	RepoRoot    string
	File        string
	Symbol      string
	LineStart   int
	LineEnd     int
	BlameSHA    string
	BlameAt     string
	ContentHash string
}

const anchorFence = "```repodoc-anchor"
const anchorFenceClose = "```"

// renderAnchorBlock produces the fenced repodoc-anchor block appended to a
// chunk's content. Mandatory fields (file, line_start, line_end,
// content_hash) are always emitted; blame_sha/blame_at are omitted when
// anchor capture could not resolve them (e.g. an uncommitted file — git
// blame still succeeds there, but a repo with no commits at all might not).
func renderAnchorBlock(a AnchorInfo) string {
	var sb strings.Builder
	sb.WriteString(anchorFence)
	sb.WriteByte('\n')
	sb.WriteString(fmt.Sprintf("repo_root: %s\n", a.RepoRoot))
	sb.WriteString(fmt.Sprintf("file: %s\n", a.File))
	if a.Symbol != "" {
		sb.WriteString(fmt.Sprintf("symbol: %q\n", a.Symbol))
	}
	sb.WriteString(fmt.Sprintf("line_start: %d\n", a.LineStart))
	sb.WriteString(fmt.Sprintf("line_end: %d\n", a.LineEnd))
	if a.BlameSHA != "" {
		sb.WriteString(fmt.Sprintf("blame_sha: %s\n", a.BlameSHA))
	}
	if a.BlameAt != "" {
		sb.WriteString(fmt.Sprintf("blame_at: %s\n", a.BlameAt))
	}
	sb.WriteString(fmt.Sprintf("content_hash: %s\n", a.ContentHash))
	sb.WriteString(anchorFenceClose)
	sb.WriteByte('\n')
	return sb.String()
}

// ParseAnchorBlock extracts the repodoc-anchor block from an Item's
// rendered Content. Returns (AnchorInfo, true) only when a complete block
// with all mandatory fields (file, line_start, line_end, content_hash) is
// present — mirroring internal/meta.Parse's "last fence wins, mandatory
// fields required" contract, for the same reason: content is LLM/human
// facing and could otherwise contain a look-alike fenced block by
// coincidence (e.g. a doc that quotes this very format).
//
// Exported for the wiring layer (not owned by this package) that turns
// repodoc Items into memory_anchors rows via store.UpsertAnchor.
func ParseAnchorBlock(content string) (AnchorInfo, bool) {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(content, "\n")

	startIdx := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == anchorFence {
			startIdx = i // keep overwriting — last occurrence wins
		}
	}
	if startIdx < 0 {
		return AnchorInfo{}, false
	}
	endIdx := -1
	for i := startIdx + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == anchorFenceClose {
			endIdx = i
			break
		}
	}
	if endIdx < 0 {
		return AnchorInfo{}, false
	}

	var a AnchorInfo
	for _, line := range lines[startIdx+1 : endIdx] {
		idx := strings.Index(line, ": ")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx+2:])
		switch key {
		case "repo_root":
			a.RepoRoot = value
		case "file":
			a.File = value
		case "symbol":
			a.Symbol = strings.Trim(value, `"`)
		case "line_start":
			if n, err := strconv.Atoi(value); err == nil {
				a.LineStart = n
			}
		case "line_end":
			if n, err := strconv.Atoi(value); err == nil {
				a.LineEnd = n
			}
		case "blame_sha":
			a.BlameSHA = value
		case "blame_at":
			a.BlameAt = value
		case "content_hash":
			a.ContentHash = value
		}
	}

	if a.File == "" || a.LineStart == 0 || a.LineEnd == 0 || a.ContentHash == "" {
		return AnchorInfo{}, false
	}
	return a, true
}
