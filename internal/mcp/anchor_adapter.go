package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/velion/omnia/internal/anchor"
	"github.com/velion/omnia/internal/store"
)

// ─── Stale-anchor recall downrank (omnia-structural-forgetting PR2) ─────────
//
// The functions below are the ONLY place memory-structural-forgetting's
// Requirement 6 ("Retrieval Downranks Stale Memories") is implemented. They
// are pure — no I/O, no git, no store access — living in internal/mcp so
// internal/recall and internal/store stay untouched leaves (design D6,
// mirrored from recall_ranking.go's own wiring-boundary placement). mcp.go's
// handleSearch is the only caller that batch-loads memory_anchors rows (via
// store.GetAnchorsForObservations) and feeds them into these functions.

// DefaultStalenessPenalty is the fixed score-breakdown deduction applied to
// a memory carrying at least one stale anchor. It is a plain constant (not
// proportional to how many anchors are stale, or how long ago they staled)
// — a single deterministic downrank fraction, mirroring ComputeRecency's
// "never hard-filter" contract: a stale memory always ranks below an
// equally-relevant fresh one, but is never excluded outright.
const DefaultStalenessPenalty = 0.5

// StalenessPenaltyFor scans anchors — every memory_anchors row linked to one
// observation, of ANY status (see store.GetAnchorsForObservations, which
// deliberately returns stale rows too, unlike ListActiveAnchors) — and
// returns DefaultStalenessPenalty if any row is anchor_status=stale, else 0.
// A memory with no anchors at all (the overwhelming majority) always gets 0
// (Regression: memory with no anchor unaffected).
func StalenessPenaltyFor(anchors []store.MemoryAnchor) float64 {
	for _, a := range anchors {
		if a.AnchorStatus == store.AnchorStatusStale {
			return DefaultStalenessPenalty
		}
	}
	return 0
}

// firstStaleAnchor returns the first stale-status row in anchors, if any.
func firstStaleAnchor(anchors []store.MemoryAnchor) (store.MemoryAnchor, bool) {
	for _, a := range anchors {
		if a.AnchorStatus == store.AnchorStatusStale {
			return a, true
		}
	}
	return store.MemoryAnchor{}, false
}

// BuildStaleReceipt renders Requirement 6's receipt line — "anchor
// <file>:<lines> changed <old->new sha>" — for a single stale anchor.
// Returns "" when a is not stale; callers must treat empty as "no receipt to
// show," never render a broken/partial line. new_blame_sha (the SHA
// discovered by ScanProject{Source:anchor}'s staling pass, persisted via
// store.UpdateAnchorStaleReceipt) supplies the "new" half; when it was never
// recorded (e.g. an anchor staled directly via MarkAnchorStale outside a
// scan), the originally-captured BlameSHA is shown on both sides rather than
// fabricating a value.
func BuildStaleReceipt(a store.MemoryAnchor) string {
	if a.AnchorStatus != store.AnchorStatusStale {
		return ""
	}
	oldSHA := a.BlameSHA
	newSHA := oldSHA
	if a.NewBlameSHA != nil && strings.TrimSpace(*a.NewBlameSHA) != "" {
		newSHA = *a.NewBlameSHA
	}
	return fmt.Sprintf("anchor %s:%d-%d changed %s->%s", a.FilePath, a.LineStart, a.LineEnd, shortSHA(oldSHA), shortSHA(newSHA))
}

// shortSHA renders a git SHA's first 8 hex characters for a compact receipt
// line, falling back to the full (possibly empty) value when it is shorter
// than that.
func shortSHA(sha string) string {
	if sha == "" {
		return "unknown"
	}
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// ApplyStalenessDownrank stable-partitions results so any row with at least
// one stale anchor sinks after every non-stale row, preserving each group's
// relative order (mirrors RankResults' own preempted/rest stable-partition
// shape in recall_ranking.go — a re-sort, never a full re-score). The
// topic_key exact-match sentinel and signature-match rows are pre-empted
// exactly like RankResults pre-empts them from ranking: a stale sentinel or
// signature-match row still surfaces first, since Requirement 6 is about
// relative ordering among otherwise-competing candidates, not about
// defeating an exact match.
//
// anchorsByObs is keyed by observation sync_id (store.GetAnchorsForObservations'
// shape). A result with no entry — no anchors captured at all, the
// overwhelming majority of memories — is treated as non-stale, so this is a
// complete no-op when structural forgetting has never captured an anchor for
// this project (Regression: memory with no anchor unaffected).
func ApplyStalenessDownrank(results []store.SearchResult, anchorsByObs map[string][]store.MemoryAnchor) []store.SearchResult {
	if len(anchorsByObs) == 0 || len(results) == 0 {
		return results
	}

	var preempted, fresh, stale []store.SearchResult
	for _, r := range results {
		if r.Rank == exactSentinelRank || r.SignatureMatch {
			preempted = append(preempted, r)
			continue
		}
		if StalenessPenaltyFor(anchorsByObs[r.SyncID]) > 0 {
			stale = append(stale, r)
		} else {
			fresh = append(fresh, r)
		}
	}
	if len(stale) == 0 {
		return results
	}

	out := make([]store.SearchResult, 0, len(results))
	out = append(out, preempted...)
	out = append(out, fresh...)
	out = append(out, stale...)
	return out
}

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
