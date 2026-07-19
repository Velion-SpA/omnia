package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/velion/omnia/internal/store"
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
// maxRecallFixTotalChars character budget so a forced injection never
// crowds out the agent's context — lazy-expand via mem_get_observation is
// the intended follow-up, not a full memory dump here.
const (
	// maxRecallFixHits caps how many signature-matched hits are surfaced.
	maxRecallFixHits = 3

	// maxRecallFixTotalChars is the hard cap on the whole compact block
	// (all hit lines combined), independent of the per-line snippet cap.
	maxRecallFixTotalChars = 600

	// recallFixSnippetLen caps the "first ~120 chars of the fix/Learned"
	// portion of each compact hit line.
	recallFixSnippetLen = 120

	// recallFixSearchFetchLimit is how many rows we ask store.Search for
	// before filtering down to SignatureMatch==true and maxRecallFixHits —
	// the signature lane itself is small/precise, so this only needs enough
	// headroom to not truncate away real matches before filtering.
	recallFixSearchFetchLimit = 20
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

	out := formatRecallFixCompact(hits, maxRecallFixTotalChars)
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
// joined by newlines, then hard-capped to maxTotalChars for the whole
// block. Returns "" for zero hits — callers must treat that as "inject
// nothing", not as an empty-but-present block.
func formatRecallFixCompact(hits []store.SearchResult, maxTotalChars int) string {
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

	out := strings.Join(lines, "\n")
	if len(out) > maxTotalChars {
		out = truncate(out, maxTotalChars)
	}
	return out
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
