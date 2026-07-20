package store

import "testing"

// ─── Signature lane noise robustness (issue #84) ────────────────────────────
//
// These are the four scenarios issue #84 calls out by name for the recall-fix
// signature lane's QUERY-side matching, using the exact bug shape from the
// issue (a Go nil-pointer-dereference panic whose original text has the word
// "signal" threaded between "dereference" and "SIGSEGV" — that's the real
// insertion that used to break every fixed-size-4 n-gram window). All four
// exercise the same stored observation end-to-end via Store.Search, so this
// proves the fix at the same seam a real `omnia recall-fix` query hits.

// nilDerefPanicText is the original occurrence's raw content — the same
// shape the recall-eval harness (tools/eval/omnia_eval.py) seeds as its
// "nil_deref" fixture. Saved with Type "bugfix" and no explicit
// ErrorSignature, so error_signature is auto-derived via
// extractErrorText + NormalizeErrorSignature exactly as a real `mem_save`
// would.
const nilDerefPanicLine = "panic: runtime error: invalid memory address or nil pointer dereference [signal SIGSEGV: segmentation violation code=0x1 addr=0x0]"

const nilDerefPanicText = nilDerefPanicLine + "\n" +
	"goroutine 42 [running]:\n" +
	"main.(*Server).handleRequest(0x0)\n" +
	"\t/app/server.go:88 +0x2c\n" +
	"Fixed by nil-checking req.Session before deref"

func newNilDerefFixture(t *testing.T) (s *Store, fixID int64) {
	t.Helper()
	s = newTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	id, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "bugfix",
		Title:     "Fix nil deref in request handler",
		Content:   nilDerefPanicText,
		Project:   "engram",
		Scope:     "project",
		// Deliberately no explicit ErrorSignature — must be auto-derived,
		// same as a real mem_save of this bugfix.
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	return s, id
}

func searchSignatureMatch(t *testing.T, s *Store, query string, id int64) bool {
	t.Helper()
	results, err := s.Search(query, SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search(%q): %v", query, err)
	}
	for _, r := range results {
		if r.ID == id && r.SignatureMatch {
			return true
		}
	}
	return false
}

// TestSignatureLaneNoise_DistinctivePhraseHits is case (i): the bare
// distinctive error phrase alone must hit — this already worked before the
// #84 fix and must keep working after it (adding smaller n-gram windows
// must not regress the case that already worked).
func TestSignatureLaneNoise_DistinctivePhraseHits(t *testing.T) {
	s, id := newNilDerefFixture(t)
	if !searchSignatureMatch(t, s, "nil pointer dereference", id) {
		t.Fatal("expected a signature-lane hit for the bare distinctive phrase \"nil pointer dereference\"")
	}
}

// TestSignatureLaneNoise_DistinctivePhraseWithNoiseStillHits is case (ii),
// the primary regression this PR fixes: adding unrelated tokens after the
// distinctive phrase used to sink the match below what a single fixed-size
// n-gram window could find, because the stored signature has its own extra
// token ("signal") threaded between "dereference" and "sigsegv" — a
// 4-token window straddles that mismatch no matter where it starts, but a
// smaller window ("nil pointer dereference") stays entirely on the intact
// side of it.
func TestSignatureLaneNoise_DistinctivePhraseWithNoiseStillHits(t *testing.T) {
	s, id := newNilDerefFixture(t)
	if !searchSignatureMatch(t, s, "nil pointer dereference SIGSEGV in handleRequest", id) {
		t.Fatal("expected a signature-lane hit despite unrelated trailing tokens (SIGSEGV in handleRequest) diluting the query")
	}
}

// TestSignatureLaneNoise_GenericOnlyPrefixNeverHits is case (iii): a
// generic-only query (every token is a common word like "panic",
// "runtime", "error" — none individually distinctive) must NEVER match,
// regardless of how many n-gram window sizes are tried. This is the
// deliberate precision guard: distinctiveness is checked independently at
// every window size, so widening the window sizes tried must not widen
// what counts as a match.
func TestSignatureLaneNoise_GenericOnlyPrefixNeverHits(t *testing.T) {
	s, id := newNilDerefFixture(t)
	if searchSignatureMatch(t, s, "panic: runtime error", id) {
		t.Fatal("expected NO signature-lane hit for a generic-only query (\"panic: runtime error\") — every token is generic, so it must never match")
	}
}

// TestSignatureLaneNoise_FullStoredLineHits is case (iv): the exact
// original line the signature was derived from must still hit — the
// trivial, always-worked baseline this PR must not disturb.
func TestSignatureLaneNoise_FullStoredLineHits(t *testing.T) {
	s, id := newNilDerefFixture(t)
	if !searchSignatureMatch(t, s, nilDerefPanicLine, id) {
		t.Fatal("expected a signature-lane hit when the query is the exact original error line the signature was derived from")
	}
}
