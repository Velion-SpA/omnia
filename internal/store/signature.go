package store

import (
	"fmt"
	"regexp"
	"strings"
)

// ─── Recall reliability (#1399, slice 1) ────────────────────────────────────
//
// This file implements the deterministic error-signature normalizer and the
// outcome-feedback vocabulary used to make recurring bug fixes reliably
// re-findable via internal/store's plain FTS5/BM25 Search path (design obs
// #1498, audit obs #1497). It intentionally has NO dependency on the
// semantic/RRF recall path (internal/recall) — recall.enabled defaults to
// false, and this signature lane must work regardless of that flag.

// Outcome values accepted by mem_save/mem_update for bugfix-family
// observations. "unknown" (and empty) are stored as NULL — both mean "no
// verified outcome yet" and receive no ranking adjustment in Search.
const (
	OutcomeWorked     = "worked"
	OutcomeDidNotWork = "did_not_work"
	OutcomeUnknown    = "unknown"
)

// isBugfixFamilyType reports whether typ belongs to the bugfix family of
// observation types. This mirrors the switch in inferTopicFamily (used for
// topic_key suggestion) so "what counts as a bug" stays consistent across
// the codebase.
func isBugfixFamilyType(typ string) bool {
	switch strings.TrimSpace(strings.ToLower(typ)) {
	case "bug", "bugfix", "fix", "incident", "hotfix":
		return true
	default:
		return false
	}
}

// normalizeOutcome validates and canonicalizes a raw outcome string from an
// mem_save/mem_update caller. "" and "unknown" both normalize to "" (stored
// as NULL — no ranking adjustment). Anything other than the three known
// values is rejected so typos don't silently get swallowed.
func normalizeOutcome(raw string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(raw))
	switch v {
	case "", OutcomeUnknown:
		return "", nil
	case OutcomeWorked, OutcomeDidNotWork:
		return v, nil
	default:
		return "", fmt.Errorf("invalid outcome %q: must be one of %q, %q, %q (or empty)", raw, OutcomeWorked, OutcomeDidNotWork, OutcomeUnknown)
	}
}

// errorLineKeywords are case-insensitive substrings that mark a line as
// "error-shaped" for extractErrorText. Deliberately broad (covers Go, JS/TS,
// and generic log/exception phrasing) — a false positive here just means one
// extra line gets included in the signature source text, which is harmless;
// a false negative means a real bugfix never gets a signature at all.
var errorLineKeywords = []string{
	"panic:",
	"error:",
	"exception",
	"err:",
	"traceback",
	"fatal",
	"runtime error",
	"cannot read propert",
	"undefined",
	"nil pointer",
	"failed",
	"exit code",
	"exit status",
	"status code",
}

// reStackFrameAt matches a "at <fn> (...:NN" style stack frame line (Go/JS/TS
// convention), e.g. "at processOrder (/src/orderService.ts:47:19)".
var reStackFrameAt = regexp.MustCompile(`(?i)\bat\s+\S.*\(.*:\d+`)

// reStackFrameFile matches a bare "<file>.<ext>:NN" reference even without a
// leading "at ", e.g. a Go panic's "\t/path/to/file.go:3144 +0x1a4" frame.
var reStackFrameFile = regexp.MustCompile(`[\w./-]+\.[a-zA-Z]{1,6}:\d+`)

// lineLooksErrorShaped reports whether a single line of text looks like part
// of an error/panic/exception/stack-trace, as opposed to ordinary prose.
func lineLooksErrorShaped(line string) bool {
	lower := strings.ToLower(line)
	for _, kw := range errorLineKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return reStackFrameAt.MatchString(line) || reStackFrameFile.MatchString(line)
}

// extractErrorText scans content line by line and returns only the
// error-shaped lines (panics, exception messages, stack frames), joined by
// newlines — NOT the surrounding prose. This is the save-time source for an
// auto-derived error_signature: normalizing the FULL memory content (all of
// What/Why/Where/Learned) would bake in prose words that a future bare error
// occurrence never mentions, so the signature would (almost) never match
// again. Narrowing to just the error-shaped lines keeps the stored signature
// close in shape to what a fresh occurrence of the SAME bug will normalize
// to.
//
// Returns "" when no line looks error-shaped (e.g. a pure architecture/
// decision memory) — callers must NOT fall back to indexing the whole prose
// as a signature; an empty signature simply means this memory never
// participates in the signature lane, which is correct.
// ExtractErrorText is the exported form of extractErrorText for callers
// outside this package that need to derive the SAME error-shaped-line
// extraction used at save time, before running the result through
// NormalizeErrorSignature / Store.Search's signature lane — specifically
// #1399 slice 2's `omnia recall-fix` CLI command and its PostToolUse hook.
// Reusing this delegate (instead of re-implementing the keyword/stack-frame
// heuristics client-side) guarantees the query-time and save-time
// extraction never drift apart.
func ExtractErrorText(content string) string {
	return extractErrorText(content)
}

func extractErrorText(content string) string {
	lines := strings.Split(content, "\n")
	var matched []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if lineLooksErrorShaped(trimmed) {
			matched = append(matched, trimmed)
		}
	}
	if len(matched) == 0 {
		return ""
	}

	const maxExtractedLen = 400
	joined := strings.Join(matched, "\n")
	if len(joined) > maxExtractedLen {
		joined = joined[:maxExtractedLen]
	}
	return joined
}

// Regexes used by NormalizeErrorSignature. All compiled once at package init
// — normalization itself is pure string manipulation (no time/random
// dependency), which is what makes it deterministic.
var (
	// Unix-style file path immediately followed by :line or :line:col, e.g.
	// "internal/store/store.go:3144:5" or "/Users/x/src/index.js:12:5".
	// Requires at least one "segment/" before the final "name:digits" so we
	// don't accidentally eat unrelated "word:number" pairs (like "port:8080").
	reUnixPathWithLine = regexp.MustCompile(`(?:[\w.-]+/)+[\w.-]+:\d+(?::\d+)?`)

	// Bare file path (no trailing line/col) identified by a common source
	// extension, e.g. "./config/db.json" or "src/services/orderService.ts".
	reUnixPathNoLine = regexp.MustCompile(`(?:\.{1,2}/|/)?(?:[\w.-]+/)+[\w.-]+\.[a-z0-9]{1,6}`)

	// ISO-8601 timestamp, e.g. "2026-07-19T15:04:05Z" or "2026-07-19 15:04:05.123+00:00".
	reISOTimestamp = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[t ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:z|[+-]\d{2}:?\d{2})?`)

	// Bare clock time, e.g. "14:03:59".
	reClockTime = regexp.MustCompile(`\b\d{1,2}:\d{2}:\d{2}\b`)

	// Hex literals, e.g. memory addresses/offsets "0x1a2b3c".
	reHex = regexp.MustCompile(`0x[0-9a-f]+`)

	// UUIDs, e.g. "550e8400-e29b-41d4-a716-446655440000".
	reUUID = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

	// Bracketed/parenthesized numbers, e.g. "[5]" or "(12)".
	reBracketNum = regexp.MustCompile(`[\[(]\s*\d+\s*[\])]`)

	// Any remaining standalone digit run (line numbers, counts, ports, PIDs,
	// "length 3" style descriptors, etc). Deliberately unconditional: recall
	// is about the recurring SHAPE of a bug, and incidental numbers are the
	// most common thing that differs between two occurrences of the same bug.
	reDigits = regexp.MustCompile(`\d+`)

	// Anything left that isn't a lowercase letter or space (punctuation,
	// underscores, brackets, newlines, tabs, symbols) collapses to a space.
	reNonAlpha = regexp.MustCompile(`[^a-z ]+`)

	reWhitespace = regexp.MustCompile(`\s+`)
)

// signatureGenericProbeTokens is the MINIMAL set of overly-generic
// error-shaped words that must not, by themselves, count as "distinctive"
// enough to justify an n-gram probe (see ErrorSignatureProbes). Kept
// deliberately small: a broader generic list would start rejecting probes
// that ARE distinctive in context (e.g. "properties", "reading", "cannot"
// all carry real signal for a "cannot read properties of null" style
// error and must NOT be treated as generic).
var signatureGenericProbeTokens = map[string]bool{
	"error":     true,
	"failed":    true,
	"fatal":     true,
	"panic":     true,
	"runtime":   true,
	"exception": true,
}

// isDistinctiveSignatureProbe reports whether probe is specific enough to
// be worth matching against stored error_signature values: it must meet
// the same minimum length floor as the outer signature-lane guard
// (minSignatureLength), AND contain at least one token of length >= 5 that
// is NOT in signatureGenericProbeTokens. This blocks over-matching on
// generic runs like "error failed to load" (every token >=5 chars is
// generic) while still admitting short-but-specific phrases like "cannot
// read properties of" ("properties" alone already qualifies).
func isDistinctiveSignatureProbe(probe string) bool {
	if len(probe) < minSignatureLength {
		return false
	}
	for _, tok := range strings.Fields(probe) {
		if len(tok) >= 5 && !signatureGenericProbeTokens[tok] {
			return true
		}
	}
	return false
}

// ErrorSignatureProbes splits a normalized error signature into
// distinctive contiguous n-token windows ("probes") that Store.Search's
// signature lane can OR together against error_signature. This is the fix
// for the two-way full-string containment check's biggest blind spot: the
// SAME error recurring in a DIFFERENT file/variable/prose context, where
// neither signature is a full substring of the other but they clearly
// share a distinctive error core (e.g. "cannot read properties of").
//
// When normalizedSig has ngram tokens or fewer, the whole signature is
// returned as a single probe (if it passes isDistinctiveSignatureProbe) —
// windowing a signature shorter than the window size would be a no-op
// anyway. Otherwise every contiguous ngram-token window is a probe
// candidate, filtered by isDistinctiveSignatureProbe, deduplicated in
// first-seen order, and capped at 16 probes so a very long signature can't
// blow up the number of OR'd LIKE clauses in a single query.
//
// Returns nil when normalizedSig is empty or no candidate probe survives
// the guards.
func ErrorSignatureProbes(normalizedSig string, ngram int) []string {
	tokens := strings.Fields(normalizedSig)
	if len(tokens) == 0 {
		return nil
	}

	if len(tokens) <= ngram {
		if isDistinctiveSignatureProbe(normalizedSig) {
			return []string{normalizedSig}
		}
		return nil
	}

	const maxSignatureProbes = 16
	seen := make(map[string]bool)
	var probes []string
	for i := 0; i+ngram <= len(tokens); i++ {
		probe := strings.Join(tokens[i:i+ngram], " ")
		if !isDistinctiveSignatureProbe(probe) {
			continue
		}
		if seen[probe] {
			continue
		}
		seen[probe] = true
		probes = append(probes, probe)
		if len(probes) >= maxSignatureProbes {
			break
		}
	}
	return probes
}

// NormalizeErrorSignature deterministically reduces raw error text (a Go
// panic, a JS/TS stack trace, a log line, or a plain search query) to a
// stable signature string. Two occurrences of the SAME underlying bug —
// differing only in file paths, line/col numbers, memory addresses,
// timestamps, or incidental counters — normalize to the identical string.
// Genuinely different errors normalize to different strings (with the usual
// caveat of any lossy normalizer: it trades some precision for robustness
// against incidental noise).
//
// Empty or whitespace-only input returns "". Prose without any error-shaped
// tokens still returns a deterministic, stable (non-empty) value — it is
// never random or time-dependent.
func NormalizeErrorSignature(text string) string {
	s := strings.ToLower(text)

	s = reUnixPathWithLine.ReplaceAllString(s, " ")
	s = reUnixPathNoLine.ReplaceAllString(s, " ")
	s = reISOTimestamp.ReplaceAllString(s, " ")
	s = reClockTime.ReplaceAllString(s, " ")
	s = reUUID.ReplaceAllString(s, " ")
	s = reHex.ReplaceAllString(s, " ")
	s = reBracketNum.ReplaceAllString(s, " ")
	s = reDigits.ReplaceAllString(s, " ")
	s = reNonAlpha.ReplaceAllString(s, " ")

	s = strings.TrimSpace(reWhitespace.ReplaceAllString(s, " "))
	return s
}
