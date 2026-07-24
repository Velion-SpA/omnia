package similarity

// similarity_test.go — RED→GREEN tests for Omnia v0.3.1 "Write Hygiene" PR1
// (design obs #1668 D1, tasks obs #1669 PR1, spec obs #1666): these cases are
// ported VERBATIM from internal/mcp/mmr_test.go's
// TestJaccardSimilarity_*/tokenizeForSimilarity coverage (Omnia v0.3 "Context
// Economy" PR4) — internal/mcp/mmr_test.go itself is left unchanged and is
// the regression net proving the move didn't alter behavior (kill-switch
// check, PR1 task 1.4).

import "testing"

// TestJaccard_Identical: identical token sets (even from differently-cased/
// punctuated source strings) must score exactly 1.0.
func TestJaccard_Identical(t *testing.T) {
	got := Jaccard(Tokenize("Hello World"), Tokenize("hello world"))
	if got != 1.0 {
		t.Errorf("Jaccard(identical) = %v, want 1.0", got)
	}
}

// TestJaccard_Disjoint: completely non-overlapping token sets must score
// exactly 0.0 (spec: Cheap Lexical Similarity Only, no partial credit for
// zero shared vocabulary).
func TestJaccard_Disjoint(t *testing.T) {
	got := Jaccard(Tokenize("alpha beta"), Tokenize("gamma delta"))
	if got != 0.0 {
		t.Errorf("Jaccard(disjoint) = %v, want 0.0", got)
	}
}

// TestJaccard_CaseAndPunctuationInsensitive: tokenization must lowercase and
// strip punctuation before comparing, so "Hello, World!" and "hello world"
// are recognized as the same two tokens, not four distinct ones.
func TestJaccard_CaseAndPunctuationInsensitive(t *testing.T) {
	got := Jaccard(Tokenize("Hello, World!"), Tokenize("hello world"))
	if got != 1.0 {
		t.Errorf("Jaccard(case/punctuation-insensitive) = %v, want 1.0", got)
	}
}

// TestJaccard_PartialOverlap locks the exact |A∩B|/|A∪B| formula with a
// hand-computed fixture: 4 shared tokens, 5 total in the union (1 unique to
// A) -> 4/5 = 0.8.
func TestJaccard_PartialOverlap(t *testing.T) {
	got := Jaccard(Tokenize("a b c d x"), Tokenize("a b c d"))
	if got != 0.8 {
		t.Errorf("Jaccard(partial overlap) = %v, want 0.8", got)
	}
}

// TestJaccard_BothEmpty pins the deliberate conservative choice documented on
// Jaccard: empty-vs-empty (and empty-vs-anything) is 0, NOT 1, so
// empty/near-empty content never looks like a spurious "duplicate" and gets
// hard-dropped by accident. This is the pinned empty/empty=0 rule carried
// over byte-for-byte from mmr_test.go's TestJaccardSimilarity_BothEmpty.
func TestJaccard_BothEmpty(t *testing.T) {
	if got := Jaccard(nil, nil); got != 0.0 {
		t.Errorf("Jaccard(empty, empty) = %v, want 0.0", got)
	}
	if got := Jaccard(Tokenize(""), Tokenize("alpha")); got != 0.0 {
		t.Errorf("Jaccard(empty, non-empty) = %v, want 0.0", got)
	}
}
