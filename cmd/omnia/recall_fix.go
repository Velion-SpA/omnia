package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/velion/omnia/internal/store"
	"github.com/velion/omnia/internal/token"
)

// ─── omnia recall-fix (#1399 slice 2 — forced activation) ────────────────────
//
// A compact, hook-safe recall command: given a fresh error occurrence (CLI
// arg(s) or stdin), it re-derives the SAME error-shaped-line extraction used
// at save time (store.ExtractErrorText) and returns ONLY signature-lane hits
// (store.SearchResult.SignatureMatch == true) from store.Search — never
// loose BM25 text hits, which would be noise for a forced/automatic
// injection (design obs #1498 / audit #1497).
//
// This is the confidence gate the PostToolUse hook relies on: empty output
// here means "no proven prior fix for this error", so the hook injects
// nothing. Output is capped to the top maxRecallFixHits hits and an overall
// maxRecallFixTokenBudget TOKEN budget (Omnia v0.3 Context Economy, design
// obs #1643 decision 2 / spec obs #1642 injection-budget REQ4) so a forced
// injection never crowds out the agent's context — lazy-expand via
// mem_get_observation is the intended follow-up, not a full memory dump
// here.
const (
	// maxRecallFixHits caps how many signature-matched hits are surfaced.
	maxRecallFixHits = 3

	// maxRecallFixTokenBudget is the hard cap, in estimated tokens (shared
	// internal/token.EstimateTokens primitive), on the whole compact block
	// (all hit lines combined), independent of the per-line snippet cap.
	// Migrated from the pre-v0.3 flat maxRecallFixTotalChars=600 char cap:
	// 150 tokens * ~4 chars/token lands in the same ballpark as the old
	// 600-char cap (anti-drift), while fixing that approach's mid-line
	// truncation — token.TrimToBudget keeps only COMPLETE hit lines
	// (top-N-complete), never cutting one in half.
	maxRecallFixTokenBudget = 150

	// recallFixSnippetLen caps the "first ~120 chars of the fix/Learned"
	// portion of each compact hit line.
	recallFixSnippetLen = 120

	// recallFixSearchFetchLimit is how many rows we ask store.Search for
	// before filtering down to SignatureMatch==true and maxRecallFixHits —
	// the signature lane itself is small/precise, so this only needs enough
	// headroom to not truncate away real matches before filtering.
	recallFixSearchFetchLimit = 20

	// recallFixForcedLineMaxRunes bounds the force-kept first line
	// (formatRecallFixCompact's "hits exist => never empty" guarantee) when
	// that single line alone exceeds maxRecallFixTokenBudget — e.g. an
	// observation Title, which has no length cap in the save path. Matches
	// the old pre-v0.3 truncate(out, 600) envelope, so a pathological title
	// can't crowd out the context.
	recallFixForcedLineMaxRunes = 600
)

func cmdRecallFix(cfg store.Config) {
	project := ""
	limit := maxRecallFixHits
	jsonOut := false
	var textParts []string

	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--project":
			if i+1 < len(os.Args) {
				project = os.Args[i+1]
				i++
			}
		case "--limit":
			if i+1 < len(os.Args) {
				if n, err := strconv.Atoi(os.Args[i+1]); err == nil && n > 0 {
					limit = n
				}
				i++
			}
		case "--json":
			jsonOut = true
		default:
			textParts = append(textParts, os.Args[i])
		}
	}

	// Anti-bloat contract: never surface more than maxRecallFixHits,
	// regardless of what a caller passes via --limit.
	if limit > maxRecallFixHits {
		limit = maxRecallFixHits
	}

	rawText := strings.Join(textParts, " ")
	if rawText == "" {
		if stdinBytes, err := readStdin(); err == nil {
			rawText = string(stdinBytes)
		}
	}

	// Fail-quiet + fast: no input at all means nothing to recall. This is a
	// normal, expected outcome for a hook-driven command (not a usage
	// error) — deliberately different from `omnia search`, which requires a
	// query. Exit 0, empty stdout.
	if strings.TrimSpace(rawText) == "" {
		return
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	hits, err := recallFixSearch(s, rawText, project, limit)
	if err != nil {
		fatal(err)
		return
	}

	if jsonOut {
		printRecallFixJSON(hits)
		return
	}

	out := formatRecallFixCompact(hits, maxRecallFixTokenBudget)
	if out == "" {
		return
	}
	fmt.Println(out)
}

// recallFixSearch narrows rawErrorText to its error-shaped lines (the same
// derivation used at save time), runs it through store.Search, and returns
// only the hits surfaced by the signature lane (SignatureMatch == true),
// capped to limit. Returns (nil, nil) when the input has no error-shaped
// lines at all — mirrors store.ExtractErrorText's "no fallback to full-text
// indexing" contract.
func recallFixSearch(s *store.Store, rawErrorText string, project string, limit int) ([]store.SearchResult, error) {
	extracted := store.ExtractErrorText(rawErrorText)
	if extracted == "" {
		return nil, nil
	}

	results, err := storeSearch(s, extracted, store.SearchOptions{
		Project: project,
		Limit:   recallFixSearchFetchLimit,
	})
	if err != nil {
		return nil, err
	}

	var hits []store.SearchResult
	for _, r := range results {
		if !r.SignatureMatch {
			continue
		}
		hits = append(hits, r)
		if len(hits) >= limit {
			break
		}
	}
	return hits, nil
}

// formatRecallFixCompact renders hits as one line each:
//
//	obs #<id> [<outcome>] <title> — <snippet>
//
// then trims to maxTokenBudget estimated tokens via the shared
// internal/token.TrimToBudget primitive (Omnia v0.3 Context Economy, design
// obs #1643 decision 2 / spec obs #1642 injection-budget REQ2/REQ4): whole
// lines are kept in order until the next line would exceed the budget — no
// line is ever partially truncated, unlike the pre-v0.3 flat
// maxRecallFixTotalChars char cap this replaces.
//
// GUARANTEE: when hits is non-empty, the return value is NEVER "" — this is
// the confidence gate recall_fix.go:22-23 documents (empty output means "no
// proven prior fix exists"), so an empty string must never be produced just
// because a formatted line happened to be oversized. An observation's Title
// has no length cap in the save path, so token.TrimToBudget (which correctly
// never force-includes an over-budget item) can legitimately keep zero
// lines when even the FIRST line alone exceeds maxTokenBudget. In that case
// this function force-keeps that first line anyway, rune-truncated to
// recallFixForcedLineMaxRunes (matching the old truncate(out, 600)
// envelope) so a pathological title can't crowd out the context. Only
// len(hits) == 0 returns "".
func formatRecallFixCompact(hits []store.SearchResult, maxTokenBudget int) string {
	if len(hits) == 0 {
		return ""
	}

	lines := make([]string, 0, len(hits))
	for _, h := range hits {
		outcome := "unverified"
		if h.Outcome != nil && strings.TrimSpace(*h.Outcome) != "" {
			outcome = strings.TrimSpace(*h.Outcome)
		}
		snippet := recallFixSnippet(h.Content, recallFixSnippetLen)
		lines = append(lines, fmt.Sprintf("obs #%d [%s] %s — %s", h.ID, outcome, h.Title, snippet))
	}

	kept, _ := token.TrimToBudget(lines, token.EstimateTokens, maxTokenBudget)
	if len(kept) == 0 {
		// Hits exist but not even the first line fit the budget — force-keep
		// it, bounded, rather than silently returning "".
		kept = []string{truncate(lines[0], recallFixForcedLineMaxRunes)}
	}
	return strings.Join(kept, "\n")
}

// recallFixSnippet extracts a compact, single-line preview of the "fix"
// portion of an observation's content: it prefers the text following a
// **Learned**: marker (the project's What/Why/Where/Learned convention),
// falling back to the raw content when no such marker is present. Newlines
// are collapsed so the result is always a single display line, then
// truncated to maxLen.
func recallFixSnippet(content string, maxLen int) string {
	text := content
	lower := strings.ToLower(content)
	for _, marker := range []string{"**learned**:", "**learned**"} {
		if idx := strings.Index(lower, marker); idx >= 0 {
			text = content[idx+len(marker):]
			break
		}
	}

	text = strings.TrimSpace(text)
	text = strings.Join(strings.Fields(text), " ")
	return truncate(text, maxLen)
}

type recallFixJSONHit struct {
	ID      int64  `json:"id"`
	Outcome string `json:"outcome,omitempty"`
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
}

func printRecallFixJSON(hits []store.SearchResult) {
	out := make([]recallFixJSONHit, 0, len(hits))
	for _, h := range hits {
		outcome := ""
		if h.Outcome != nil {
			outcome = strings.TrimSpace(*h.Outcome)
		}
		out = append(out, recallFixJSONHit{
			ID:      h.ID,
			Outcome: outcome,
			Title:   h.Title,
			Snippet: recallFixSnippet(h.Content, recallFixSnippetLen),
		})
	}

	b, err := jsonMarshalIndent(out, "", "  ")
	if err != nil {
		fatal(err)
		return
	}
	fmt.Println(string(b))
}
