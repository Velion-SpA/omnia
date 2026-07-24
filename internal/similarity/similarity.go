// Package similarity provides a deterministic, dependency-free token-set
// Jaccard similarity primitive shared by store, mcp, and cmd/omnia (spec
// write-gate, design obs #1668 D1 for Omnia v0.3.1 — Write Hygiene).
//
// Tokenize/Jaccard were promoted verbatim out of internal/mcp/mmr.go (Omnia
// v0.3 "Context Economy" PR4, design obs #1643 section 3.3, ADR-5), where
// they back ApplyMMR's read-time near-duplicate demotion. This package is
// the pure leaf both internal/store (write-time dedup gate) and
// internal/mcp (read-time MMR) build on: it imports only the Go standard
// library — no CGO, no external tokenizer, no dependency on any other
// internal/* package — so internal/store can import it without importing
// internal/mcp (store must never import mcp).
package similarity

import (
	"regexp"
	"strings"
)

// tokenRe extracts Unicode letter/digit runs as word tokens for Jaccard —
// punctuation and whitespace are separators, not content.
var tokenRe = regexp.MustCompile(`[\p{L}\p{N}]+`)

// Tokenize splits s into lowercased word tokens ("token-set Jaccard on
// lowercased word tokens"). Case and punctuation are deliberately
// insensitive: "Hello, World!" and "hello world" tokenize identically, so
// near-duplicate detection isn't fooled by capitalization or punctuation
// drift between two renderings of the same fact.
//
// KNOWN LIMITATION: scripts written without inter-word whitespace (Chinese,
// Japanese, ...) tokenize as one giant letter-run per sentence, so two
// near-duplicate CJK sentences look completely disjoint (Jaccard 0) unless
// byte-identical — dedup is effectively inert for such content. Accepted
// for v0.3/v0.3.1 (the corpus is ES/EN); a bigram fallback is the follow-up
// if CJK content ever matters.
func Tokenize(s string) []string {
	return tokenRe.FindAllString(strings.ToLower(s), -1)
}

// Jaccard returns the token-set Jaccard similarity |A∩B| / |A∪B| between two
// already-tokenized word-token slices: cheap, deterministic, O(n+m),
// allocation-light, no new embeddings or model dependency (spec: Cheap
// Lexical Similarity Only). Two token sets where either is empty return 0,
// not 1 — a deliberate conservative choice so empty/near-empty content never
// looks like a spurious "duplicate" of other empty/near-empty content and
// gets dropped (or NOOP'd/auto-updated) by accident.
func Jaccard(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}

	setA := make(map[string]struct{}, len(a))
	for _, t := range a {
		setA[t] = struct{}{}
	}
	setB := make(map[string]struct{}, len(b))
	for _, t := range b {
		setB[t] = struct{}{}
	}

	intersection := 0
	for t := range setA {
		if _, ok := setB[t]; ok {
			intersection++
		}
	}
	union := len(setA)
	for t := range setB {
		if _, ok := setA[t]; !ok {
			union++
		}
	}
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}
