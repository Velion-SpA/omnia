package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/velion/omnia/internal/anchor"
	"github.com/velion/omnia/internal/store"
)

// AnchorCapturer is the subset of *anchor.Probe's behavior
// AnchorCaptureAdapter needs. Declaring it as an interface — rather than
// depending on the concrete *anchor.Probe type directly — lets tests in this
// package inject a fake capturer without needing anchor.Probe's unexported
// runGit field (mirrors this package's own StoreLexicalSearcher/recall
// port-at-the-wiring-layer style in recall_adapter.go). *anchor.Probe
// satisfies this interface as-is; see var _ AnchorCapturer below.
type AnchorCapturer interface {
	Capture(ctx context.Context, dir, file, symbol string, start, end int) (anchor.Anchor, error)
}

var _ AnchorCapturer = (*anchor.Probe)(nil)

// AnchorCaptureAdapter wraps an AnchorCapturer (real: *anchor.Probe) to
// resolve and persist code anchors for a single mem_save call, translating
// anchor.Anchor into store.UpsertAnchorParams. It lives in this wiring
// layer — never inside internal/store — so the store package stays
// git-agnostic (design decision D1/D4; mirrors StoreLexicalSearcher's
// wiring-layer placement in recall_adapter.go).
type AnchorCaptureAdapter struct {
	Probe AnchorCapturer
	Store *store.Store
}

// NewAnchorCaptureAdapter builds an AnchorCaptureAdapter backed by the real
// anchor.NewProbe() (real git shell-out).
func NewAnchorCaptureAdapter(s *store.Store) *AnchorCaptureAdapter {
	return &AnchorCaptureAdapter{Probe: anchor.NewProbe(), Store: s}
}

// CodeAnchorInput is one entry of mem_save's optional code_anchors argument.
type CodeAnchorInput struct {
	File      string
	Symbol    string
	LineStart int
	LineEnd   int
}

// Capture resolves each anchor input (via git shell-out) and persists it
// linked to obsSyncID. Capture failures are per-anchor and non-fatal by
// design: a single bad entry (bad line range, symbol not found, no git, not
// a repo, etc.) is skipped — this method NEVER returns a hard failure, so
// callers (handleSave) can call it unconditionally without risking mem_save
// itself (REQ-002 graceful degradation). dir is the working directory to
// resolve the git repo from (typically the current process cwd).
//
// Returns the number of anchors successfully captured and persisted, plus
// any per-input errors for logging (never for failing the caller).
func (a *AnchorCaptureAdapter) Capture(ctx context.Context, dir, obsSyncID string, inputs []CodeAnchorInput) (captured int, errs []error) {
	for _, in := range inputs {
		anc, err := a.Probe.Capture(ctx, dir, in.File, in.Symbol, in.LineStart, in.LineEnd)
		if err != nil {
			errs = append(errs, fmt.Errorf("anchor capture skipped for %s: %w", in.File, err))
			continue
		}
		if _, err := a.Store.UpsertAnchor(store.UpsertAnchorParams{
			ObsSyncID:   obsSyncID,
			RepoRoot:    anc.RepoRoot,
			FilePath:    anc.File,
			Symbol:      anc.Symbol,
			LineStart:   anc.LineStart,
			LineEnd:     anc.LineEnd,
			BlameSHA:    anc.BlameSHA,
			BlameAt:     anc.BlameAt,
			ContentHash: anc.ContentHash,
		}); err != nil {
			errs = append(errs, fmt.Errorf("anchor persist failed for %s: %w", in.File, err))
			continue
		}
		captured++
	}
	return captured, errs
}

// parseCodeAnchorsArg extracts the optional `code_anchors` argument from a
// mem_save request into []CodeAnchorInput. code_anchors is best-effort: a
// malformed entry (missing file, non-numeric or inverted line range) is
// skipped individually rather than failing the whole parse — mem_save MUST
// NEVER fail because of anchoring (REQ-002).
func parseCodeAnchorsArg(req mcp.CallToolRequest) []CodeAnchorInput {
	raw, ok := req.GetArguments()["code_anchors"].([]any)
	if !ok {
		return nil
	}

	var out []CodeAnchorInput
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		file, _ := m["file"].(string)
		if strings.TrimSpace(file) == "" {
			continue
		}
		symbol, _ := m["symbol"].(string)
		start, startOK := numArg(m["line_start"])
		end, endOK := numArg(m["line_end"])
		if !startOK || !endOK || start <= 0 || end <= 0 || end < start {
			continue
		}
		out = append(out, CodeAnchorInput{File: file, Symbol: symbol, LineStart: start, LineEnd: end})
	}
	return out
}

// numArg converts an MCP tool argument value (typically float64 from JSON
// decoding, but tolerant of int/int64 too) into an int.
func numArg(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	default:
		return 0, false
	}
}
