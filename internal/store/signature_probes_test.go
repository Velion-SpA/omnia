package store

import "testing"

// ─── ErrorSignatureProbes (recall #1399: shared n-gram matching) ────────────
//
// RED: the signature lane's containment match today requires the WHOLE
// query signature to be contained in (or contain) the stored signature.
// That fails the primary recurring-bug use case: the SAME error recurring
// in a DIFFERENT file/variable/prose context, where neither signature is a
// full substring of the other but they share a distinctive error CORE.
//
// ErrorSignatureProbes splits a normalized signature into contiguous
// n-token windows ("probes") that Search can OR together against
// error_signature, so a shared distinctive core (e.g. "cannot read
// properties of") is enough to match even when the surrounding tokens
// differ completely.

func TestErrorSignatureProbes_EmptyInputReturnsNil(t *testing.T) {
	if got := ErrorSignatureProbes("", 4); got != nil {
		t.Fatalf("expected nil for empty input, got %#v", got)
	}
}

// TestErrorSignatureProbes_WholeSignatureWhenAtOrBelowNGram covers the
// len(tokens) <= ngram path: the whole normalized signature is returned
// as a single probe when it passes the guards.
func TestErrorSignatureProbes_WholeSignatureWhenAtOrBelowNGram(t *testing.T) {
	sig := "cannot read properties of"
	got := ErrorSignatureProbes(sig, 4)

	if len(got) != 1 {
		t.Fatalf("expected exactly 1 probe (the whole signature), got %#v", got)
	}
	if got[0] != sig {
		t.Fatalf("expected probe %q, got %q", sig, got[0])
	}
}

// TestErrorSignatureProbes_WholeSignatureTooShortReturnsNil is the guard
// side of the <=ngram case: a short whole-signature (below
// minSignatureLength) must not be returned as a probe.
func TestErrorSignatureProbes_WholeSignatureTooShortReturnsNil(t *testing.T) {
	// 4 tokens, 11 chars total — below minSignatureLength (12).
	got := ErrorSignatureProbes("ab cd ef gh", 4)
	if got != nil {
		t.Fatalf("expected nil for a too-short whole signature, got %#v", got)
	}
}

// TestErrorSignatureProbes_WholeSignatureAllGenericReturnsNil covers the
// "all-generic" guard on the <=ngram whole-signature path: every token
// long enough to matter (>=5 chars) is in the minimal generic set, so the
// signature carries no distinctive content.
func TestErrorSignatureProbes_WholeSignatureAllGenericReturnsNil(t *testing.T) {
	// "error failed to load": error/failed are generic; "to"/"load" are
	// both <5 chars, so there is no qualifying non-generic token.
	got := ErrorSignatureProbes("error failed to load", 4)
	if got != nil {
		t.Fatalf("expected nil for an all-generic whole signature, got %#v", got)
	}
}

// TestErrorSignatureProbes_WindowsContiguousNGrams exercises the real
// motivating case from #1399: a fresh occurrence of "cannot read
// properties of null" in a different file/variable, confirming the
// distinctive 4-gram window "cannot read properties of" is produced.
func TestErrorSignatureProbes_WindowsContiguousNGrams(t *testing.T) {
	sig := "uncaught typeerror cannot read properties of null reading value at productform"

	got := ErrorSignatureProbes(sig, 4)

	// 11 tokens, ngram=4 -> 8 contiguous windows.
	if len(got) != 8 {
		t.Fatalf("expected 8 probes (11 tokens, ngram=4), got %d: %#v", len(got), got)
	}

	want := "cannot read properties of"
	found := false
	for _, p := range got {
		if p == want {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected probe %q among windows, got %#v", want, got)
	}
}

// TestErrorSignatureProbes_WindowFilteredWhenAllGeneric verifies a window
// that is entirely generic-or-too-short tokens is dropped even when other
// windows from the same signature survive.
func TestErrorSignatureProbes_WindowFilteredWhenAllGeneric(t *testing.T) {
	// Window "error failed to load" (all generic/too-short) must never
	// appear in the output; window "failed to load widgets" (has
	// "widgets", non-generic, >=5 chars) must survive.
	sig := "error failed to load widgets"
	got := ErrorSignatureProbes(sig, 4)

	for _, p := range got {
		if p == "error failed to load" {
			t.Fatalf("expected the all-generic window to be filtered out, got it in %#v", got)
		}
	}
	wantSurvivor := "failed to load widgets"
	found := false
	for _, p := range got {
		if p == wantSurvivor {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected surviving window %q in %#v", wantSurvivor, got)
	}
}

// TestErrorSignatureProbes_WindowFilteredWhenTooShort verifies short-token
// windows (below minSignatureLength even though not all-generic) are
// dropped in the windowing branch, not just the whole-signature branch.
func TestErrorSignatureProbes_WindowFilteredWhenTooShort(t *testing.T) {
	// 8 single-letter tokens: every 4-token window joins to 7 chars,
	// well below minSignatureLength (12), regardless of content.
	got := ErrorSignatureProbes("a b c d e f g h", 4)
	if got != nil {
		t.Fatalf("expected nil (all windows too short), got %#v", got)
	}
}

// TestErrorSignatureProbes_DeduplicatesPreservingOrder verifies repeated
// windows collapse to a single probe, and that the surviving probes retain
// first-seen order.
func TestErrorSignatureProbes_DeduplicatesPreservingOrder(t *testing.T) {
	// 8 tokens, the second half repeats the first half exactly, so the
	// windowing produces a duplicate of the very first window.
	sig := "alpha beta gamma delta alpha beta gamma delta"
	got := ErrorSignatureProbes(sig, 4)

	want := []string{
		"alpha beta gamma delta",
		"beta gamma delta alpha",
		"gamma delta alpha beta",
		"delta alpha beta gamma",
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d deduplicated probes, got %d: %#v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("probe[%d] = %q, want %q (full: %#v)", i, got[i], want[i], got)
		}
	}
}

// TestErrorSignatureProbes_CapsAtSixteen verifies the result never exceeds
// 16 probes even when the input would otherwise produce more distinct
// windows, and that the cap keeps the FIRST 16 in order.
func TestErrorSignatureProbes_CapsAtSixteen(t *testing.T) {
	tokens := []string{
		"wordaa", "wordbb", "wordcc", "wordde", "wordee", "wordff", "wordgg", "wordhh",
		"wordii", "wordjj", "wordkk", "wordll", "wordmm", "wordnn", "wordoo", "wordpp",
		"wordqq", "wordrr", "wordss", "wordtt", "wordxx", "wordyy", "wordzz", "wordab",
		"wordac", "wordad",
	}
	sig := ""
	for i, tok := range tokens {
		if i > 0 {
			sig += " "
		}
		sig += tok
	}

	got := ErrorSignatureProbes(sig, 4)
	if len(got) != 16 {
		t.Fatalf("expected exactly 16 probes (capped), got %d: %#v", len(got), got)
	}

	wantFirst := "wordaa wordbb wordcc wordde"
	if got[0] != wantFirst {
		t.Fatalf("expected first probe %q, got %q", wantFirst, got[0])
	}
}

// ─── ErrorSignatureProbesMulti (issue #84: noise-robust query matching) ─────
//
// A single fixed n-gram size is fragile to even one inserted/reordered
// token landing inside every window that would otherwise span the shared
// error core. ErrorSignatureProbesMulti unions probes across several sizes
// so a smaller window can still land on an intact slice of the core even
// when the largest size straddles a mismatch.

func TestErrorSignatureProbesMulti_EmptyInputReturnsNil(t *testing.T) {
	if got := ErrorSignatureProbesMulti("", []int{2, 3, 4}); got != nil {
		t.Fatalf("expected nil for empty input, got %#v", got)
	}
}

// TestErrorSignatureProbesMulti_SmallerWindowSurvivesWhenLargerStraddles is
// the exact issue #84 regression: a token inserted between two distinctive
// words breaks every 4-token window that spans it, but a 3-token window
// starting earlier stays entirely on the intact side.
func TestErrorSignatureProbesMulti_SmallerWindowSurvivesWhenLargerStraddles(t *testing.T) {
	// "signal" sits between "dereference" and "sigsegv" — every 4-token
	// window either includes both (and thus the gap) or excludes one of
	// them entirely.
	sig := "nil pointer dereference signal sigsegv segmentation violation"

	only4 := ErrorSignatureProbes(sig, 4)
	for _, p := range only4 {
		if p == "nil pointer dereference" {
			t.Fatalf("test assumption violated: ngram=4 alone should NOT produce the 3-word probe, got %#v", only4)
		}
	}

	multi := ErrorSignatureProbesMulti(sig, []int{2, 3, 4})
	found := false
	for _, p := range multi {
		if p == "nil pointer dereference" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected ErrorSignatureProbesMulti with sizes [2,3,4] to include the smaller-window probe \"nil pointer dereference\", got %#v", multi)
	}
}

// TestErrorSignatureProbesMulti_DedupesAcrossSizes verifies a probe
// produced identically by two different window sizes (e.g. the
// len(tokens)<=ngram whole-signature path firing for both 3 and 4 on a
// 3-token signature) appears only once in the combined result.
func TestErrorSignatureProbesMulti_DedupesAcrossSizes(t *testing.T) {
	sig := "connection refused upstream"
	got := ErrorSignatureProbesMulti(sig, []int{3, 4})

	count := 0
	for _, p := range got {
		if p == sig {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected the whole-signature probe to appear exactly once across sizes [3,4], got %d occurrences in %#v", count, got)
	}
}

// TestErrorSignatureProbesMulti_AllGenericAtEverySizeReturnsNil is the
// precision guard: widening the set of window sizes tried must never widen
// what counts as distinctive. A generic-only signature (issue #84's
// `panic: runtime error` case) must produce zero probes at every size.
func TestErrorSignatureProbesMulti_AllGenericAtEverySizeReturnsNil(t *testing.T) {
	got := ErrorSignatureProbesMulti("panic runtime error", []int{2, 3, 4})
	if got != nil {
		t.Fatalf("expected nil for an all-generic signature at every window size, got %#v", got)
	}
}

// TestErrorSignatureProbesMulti_CapsAtMaxSignatureProbesMulti verifies the
// combined result across sizes never exceeds maxSignatureProbesMulti, even
// though each individual size could otherwise contribute up to its own
// per-size cap.
func TestErrorSignatureProbesMulti_CapsAtMaxSignatureProbesMulti(t *testing.T) {
	tokens := []string{
		"wordaa", "wordbb", "wordcc", "wordde", "wordee", "wordff", "wordgg", "wordhh",
		"wordii", "wordjj", "wordkk", "wordll", "wordmm", "wordnn", "wordoo", "wordpp",
		"wordqq", "wordrr", "wordss", "wordtt", "wordxx", "wordyy", "wordzz", "wordab",
		"wordac", "wordad", "wordae", "wordaf", "wordag", "wordah",
	}
	sig := ""
	for i, tok := range tokens {
		if i > 0 {
			sig += " "
		}
		sig += tok
	}

	got := ErrorSignatureProbesMulti(sig, []int{2, 3, 4})
	if len(got) != maxSignatureProbesMulti {
		t.Fatalf("expected exactly %d probes (capped), got %d", maxSignatureProbesMulti, len(got))
	}
}
