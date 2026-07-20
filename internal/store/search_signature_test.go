package store

import "testing"

// ─── Search: signature lane (design obs #1498 / audit #1497, slice 1) ───────

// TestSearch_SurfacesBugfixBySignatureWhenBM25TermsAloneWouldNotRankIt
// constructs a case where the stored fix's title/content share NO
// significant vocabulary with the query (so plain FTS5/BM25 would not
// surface it at all), but the query's normalized error signature exactly
// matches the observation's stored error_signature (captured from a
// DIFFERENT occurrence of the same bug — different path/line). Search must
// still surface it, flagged as a signature match.
func TestSearch_SurfacesBugfixBySignatureWhenBM25TermsAloneWouldNotRankIt(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// The ORIGINAL occurrence of the bug (different path/line from the query
	// below, but the SAME underlying error) — this is what gets normalized
	// and stored as error_signature.
	originalOccurrence := `TypeError: Cannot read properties of undefined (reading 'items')
    at processOrder (/home/ci/workspace/omnia/src/services/orderService.ts:99:3)`

	// Title/content deliberately share NO vocabulary with the query text
	// below, so plain FTS5/BM25 has nothing to match on.
	fixID, err := s.AddObservation(AddObservationParams{
		SessionID:      "s1",
		Type:           "bugfix",
		Title:          "Checkout flow stability improvement",
		Content:        "Added a guard clause before accessing the shopping list so incomplete checkouts no longer crash silently.",
		Project:        "engram",
		Scope:          "project",
		ErrorSignature: originalOccurrence,
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	// A NEW occurrence of the SAME bug (different path/line) is used as the
	// search query — as if the agent pasted the fresh stack trace.
	newOccurrence := `TypeError: Cannot read properties of undefined (reading 'items')
    at processOrder (/Users/benja/dev/omnia/src/services/orderService.ts:47:19)`

	results, err := s.Search(newOccurrence, SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected exactly 1 result (proves BM25 alone finds nothing and the signature lane is what surfaced it), got %d: %+v", len(results), results)
	}
	if results[0].ID != fixID {
		t.Fatalf("expected result id=%d, got id=%d", fixID, results[0].ID)
	}
	if !results[0].SignatureMatch {
		t.Fatalf("expected SignatureMatch=true for the surfaced result")
	}
}

// TestSearch_TopicKeySentinelRanksAboveSignatureMatch verifies an explicit
// topic_key hit still wins over a signature-lane hit for a query that
// exercises BOTH lanes at once.
//
// NOTE: an earlier version of this test tried to exercise topic_key +
// signature + BM25 in a single call using the topic_key STRING itself as the
// query, but under containment matching that query's normalized signature
// ("bug connection refused upstream") does NOT contain/get-contained-by an
// unrelated observation's stored signature unless deliberately constructed —
// the earlier test's signature assertion was silently skipped (never
// actually exercised) because sigOK was always false. This version
// constructs the topic_key value so its normalized form's SUFFIX exactly
// equals the target signature, so the containment check is guaranteed to
// fire and the assertion is no longer a silent no-op.
func TestSearch_TopicKeySentinelRanksAboveSignatureMatch(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	errorText := "connection refused dialing upstream service"

	sigID, err := s.AddObservation(AddObservationParams{
		SessionID:      "s1",
		Type:           "bugfix",
		Title:          "Retry upstream dial with backoff",
		Content:        "Added exponential backoff around the upstream dial call.",
		Project:        "engram",
		Scope:          "project",
		ErrorSignature: errorText,
	})
	if err != nil {
		t.Fatalf("AddObservation (signature): %v", err)
	}

	// The topic_key's normalized form ("bug connection refused dialing
	// upstream service") ends with exactly errText's normalized form, so
	// `querySig LIKE '%stored%'` containment holds and the signature lane
	// is guaranteed to fire alongside the topic_key sentinel.
	topicKeyValue := "bug/connection-refused-dialing-upstream-service"
	tkID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "bugfix",
		Title:     "Canonical fix record",
		Content:   "Canonical topic_key record for this recurring failure.",
		Project:   "engram",
		Scope:     "project",
		TopicKey:  topicKeyValue,
	})
	if err != nil {
		t.Fatalf("AddObservation (topic_key): %v", err)
	}

	results, err := s.Search(topicKeyValue, SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	rankByID := map[int64]float64{}
	for _, r := range results {
		rankByID[r.ID] = r.Rank
	}
	tkRank, tkOK := rankByID[tkID]
	sigRank, sigOK := rankByID[sigID]

	if !tkOK {
		t.Fatalf("expected topic_key result in results: %+v", results)
	}
	if !sigOK {
		t.Fatalf("expected signature-lane result in results (containment should have matched): %+v", results)
	}
	if !(tkRank < sigRank) {
		t.Fatalf("expected topic_key rank (%v) to beat signature rank (%v)", tkRank, sigRank)
	}
}

// TestSearch_SignatureMatchRanksAboveBM25ForSameQuery verifies a
// signature-lane hit beats a plain BM25 hit for the SAME query (no topic_key
// involved — the query has no "/").
func TestSearch_SignatureMatchRanksAboveBM25ForSameQuery(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	errorText := "connection refused dialing upstream service"

	// A plain BM25 hit: shares vocabulary with the query but has no
	// error_signature.
	bm25ID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "discovery",
		Title:     "Notes on upstream service reliability",
		Content:   "connection refused dialing upstream service happens under load, unrelated investigation notes.",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation (bm25): %v", err)
	}

	// A signature-lane hit: error_signature matches the query's normalized
	// signature exactly (equality is the trivial case of containment).
	sigID, err := s.AddObservation(AddObservationParams{
		SessionID:      "s1",
		Type:           "bugfix",
		Title:          "Retry upstream dial with backoff",
		Content:        "Added exponential backoff around the upstream dial call.",
		Project:        "engram",
		Scope:          "project",
		ErrorSignature: errorText,
	})
	if err != nil {
		t.Fatalf("AddObservation (signature): %v", err)
	}

	// A natural, space-separated query so sanitizeFTS's per-word quoting
	// yields an AND-of-independent-terms match against bm25ID's content.
	results, err := s.Search(errorText, SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	rankByID := map[int64]float64{}
	for _, r := range results {
		rankByID[r.ID] = r.Rank
	}
	sigRank, sigOK := rankByID[sigID]
	bm25Rank, bm25OK := rankByID[bm25ID]

	if !sigOK {
		t.Fatalf("expected signature-lane result in results: %+v", results)
	}
	if !bm25OK {
		t.Fatalf("expected plain BM25 result in results: %+v", results)
	}
	if !(sigRank < bm25Rank) {
		t.Fatalf("expected signature rank (%v) to beat plain BM25 rank (%v)", sigRank, bm25Rank)
	}
}

// ─── Search: outcome ranking (design obs #1498 / audit #1497, slice 1) ──────

// TestSearch_OutcomeWorkedRanksAboveUnknownForSameMatchStrength constructs two
// observations with byte-identical title/content (so BM25 match strength is
// effectively tied) — one with no recorded outcome, one explicitly marked
// "worked" — and asserts the proven fix ranks first. The "worked" row is
// inserted SECOND (higher rowid) specifically so a passing test can't be
// explained away by incidental insertion-order tie-breaking.
func TestSearch_OutcomeWorkedRanksAboveUnknownForSameMatchStrength(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "proj-a", "/tmp/a"); err != nil {
		t.Fatalf("create session a: %v", err)
	}
	if err := s.CreateSession("s2", "proj-b", "/tmp/b"); err != nil {
		t.Fatalf("create session b: %v", err)
	}

	title := "Fixed queue worker race condition"
	content := "Fixed queue worker race condition by adding a mutex around the shared counter."

	unknownID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "bugfix",
		Title:     title,
		Content:   content,
		Project:   "proj-a",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation (unknown outcome): %v", err)
	}

	workedID, err := s.AddObservation(AddObservationParams{
		SessionID: "s2",
		Type:      "bugfix",
		Title:     title,
		Content:   content,
		Project:   "proj-b",
		Scope:     "project",
		Outcome:   "worked",
	})
	if err != nil {
		t.Fatalf("AddObservation (worked outcome): %v", err)
	}

	results, err := s.Search("queue worker race condition mutex counter", SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	idxUnknown, idxWorked := -1, -1
	for i, r := range results {
		if r.ID == unknownID {
			idxUnknown = i
		}
		if r.ID == workedID {
			idxWorked = i
		}
	}
	if idxUnknown == -1 || idxWorked == -1 {
		t.Fatalf("expected both observations in results, got %d results: %+v", len(results), results)
	}
	if idxWorked >= idxUnknown {
		t.Fatalf("expected outcome=worked (id=%d, idx=%d) to rank ABOVE unknown outcome (id=%d, idx=%d)", workedID, idxWorked, unknownID, idxUnknown)
	}
}

// TestSearch_OutcomeDidNotWorkRanksBelowUnknownForSameMatchStrength is the
// mirror case: a "did_not_work" outcome should be penalized below an
// unknown-outcome result of similar match strength.
func TestSearch_OutcomeDidNotWorkRanksBelowUnknownForSameMatchStrength(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "proj-a", "/tmp/a"); err != nil {
		t.Fatalf("create session a: %v", err)
	}
	if err := s.CreateSession("s2", "proj-b", "/tmp/b"); err != nil {
		t.Fatalf("create session b: %v", err)
	}

	title := "Attempted fix for cache stampede"
	content := "Attempted fix for cache stampede by adding a mutex around the cache refill."

	didNotWorkID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "bugfix",
		Title:     title,
		Content:   content,
		Project:   "proj-a",
		Scope:     "project",
		Outcome:   "did_not_work",
	})
	if err != nil {
		t.Fatalf("AddObservation (did_not_work outcome): %v", err)
	}

	unknownID, err := s.AddObservation(AddObservationParams{
		SessionID: "s2",
		Type:      "bugfix",
		Title:     title,
		Content:   content,
		Project:   "proj-b",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation (unknown outcome): %v", err)
	}

	results, err := s.Search("cache stampede mutex refill", SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	idxDidNotWork, idxUnknown := -1, -1
	for i, r := range results {
		if r.ID == didNotWorkID {
			idxDidNotWork = i
		}
		if r.ID == unknownID {
			idxUnknown = i
		}
	}
	if idxDidNotWork == -1 || idxUnknown == -1 {
		t.Fatalf("expected both observations in results, got %d results: %+v", len(results), results)
	}
	if idxDidNotWork <= idxUnknown {
		t.Fatalf("expected did_not_work (id=%d, idx=%d) to rank BELOW unknown outcome (id=%d, idx=%d)", didNotWorkID, idxDidNotWork, unknownID, idxUnknown)
	}
}

// ─── The core recurring-bug use case (fix for the "almost never fires" defect) ─
//
// TestSearch_RecurringBugAutoFindsPastFixViaExtractedSignature is the test
// that proves the actual product use case works end-to-end: a bugfix memory
// saved as ordinary structured prose (What/Why/Where/Learned) that happens
// to QUOTE the underlying error, auto-derives a signature narrow enough that
// a LATER, bare occurrence of the SAME bug (different incidental numbers,
// sharing almost no vocabulary with the memory's title/prose) still finds
// it — via the signature lane, NOT BM25.
//
// This test FAILED before the fix: the old save path stored
// NormalizeErrorSignature(full content) (long, prose-polluted) and Search
// used EXACT equality against a bare query's short signature — the two
// could never match unless a caller passed an explicit error_signature AND
// searched the identical string.
func TestSearch_RecurringBugAutoFindsPastFixViaExtractedSignature(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	content := "**What**: Fixed a crash in the checkout pipeline when the cart items slice was shorter than expected.\n" +
		"**Why**: A customer reported the app crashing intermittently during checkout.\n" +
		"**Where**: internal/checkout/pipeline.go\n" +
		"**Learned**: `panic: runtime error: index out of range [7] with length 2` happening in validateCartItems() when a coupon removed an item mid-request."

	fixID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "bugfix",
		Title:     "Fixed checkout cart crash",
		Content:   content,
		Project:   "engram",
		Scope:     "project",
		// Deliberately NO explicit ErrorSignature — must be auto-derived
		// from content via extractErrorText.
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	// A FRESH occurrence of the SAME underlying bug: different incidental
	// numbers, and shares essentially no vocabulary with the memory's
	// title/prose ("checkout", "cart", "coupon", "customer", etc).
	newOccurrence := "panic: runtime error: index out of range [99] with length 4"

	results, err := s.Search(newOccurrence, SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	var found *SearchResult
	for i := range results {
		if results[i].ID == fixID {
			found = &results[i]
		}
	}
	if found == nil {
		t.Fatalf("expected the past fix (id=%d) to surface for a fresh occurrence of the same bug, got %d results: %+v", fixID, len(results), results)
	}
	if !found.SignatureMatch {
		t.Fatalf("expected the surfaced result to be flagged SignatureMatch=true (proven prior fix), got %+v", found)
	}
	if found.Rank != signatureMatchRank {
		t.Fatalf("expected rank=%v (signature lane tier), got %v", signatureMatchRank, found.Rank)
	}
}

// TestSearch_SignatureLaneNoFalsePositiveForDifferentErrors verifies two
// genuinely different errors (different message vocabulary) never
// cross-match via the signature lane's containment check.
func TestSearch_SignatureLaneNoFalsePositiveForDifferentErrors(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	id, err := s.AddObservation(AddObservationParams{
		SessionID:      "s1",
		Type:           "bugfix",
		Title:          "Fixed slice index panic",
		Content:        "Guarded the slice access with a length check before indexing.",
		Project:        "engram",
		Scope:          "project",
		ErrorSignature: "panic: runtime error: index out of range [5] with length 3",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	// A completely different error (different message, different runtime).
	results, err := s.Search("TypeError: Cannot read properties of undefined (reading 'items')", SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	for _, r := range results {
		if r.ID == id && r.SignatureMatch {
			t.Fatalf("expected NO signature-lane cross-match between genuinely different errors, but id=%d matched", id)
		}
	}
}

// TestSearch_SignatureLaneIgnoresTooShortQuerySignature verifies a short,
// low-signal query signature (below the minimum length/token guard) never
// triggers the signature lane, even if some stored signature happens to
// contain that short string as a substring.
func TestSearch_SignatureLaneIgnoresTooShortQuerySignature(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	id, err := s.AddObservation(AddObservationParams{
		SessionID:      "s1",
		Type:           "bugfix",
		Title:          "Fixed a deployment issue",
		Content:        "Root-caused an intermittent deployment failure.",
		Project:        "engram",
		Scope:          "project",
		ErrorSignature: "the deployment failed after three retries with a timeout",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	// "failed" alone normalizes to a single 6-char, 1-token signature — well
	// below both the minimum length (12) and minimum token (2) guards.
	results, err := s.Search("failed", SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	for _, r := range results {
		if r.ID == id && r.SignatureMatch {
			t.Fatalf("expected a too-short query signature to never trigger the signature lane, but id=%d matched", id)
		}
	}
}

// TestSearch_SignatureLaneIgnoresTooShortStoredSignature is the symmetric
// guard: a short/low-signal STORED signature must not broadly match a long,
// unrelated query that happens to contain it as a substring. This hardening
// goes slightly beyond the literal spec (which only gates the query side)
// but closes an obvious false-positive gap on the stored side too.
func TestSearch_SignatureLaneIgnoresTooShortStoredSignature(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	id, err := s.AddObservation(AddObservationParams{
		SessionID:      "s1",
		Type:           "bugfix",
		Title:          "Fixed something unrelated",
		Content:        "Some unrelated bugfix whose signature is deliberately short.",
		Project:        "engram",
		Scope:          "project",
		ErrorSignature: "failed",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	// Long, multi-token query that happens to CONTAIN the word "failed" —
	// should NOT match id via the signature lane just because "failed" is a
	// substring of the query's normalized signature.
	results, err := s.Search("the payment gateway integration failed during the checkout retry loop", SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	for _, r := range results {
		if r.ID == id && r.SignatureMatch {
			t.Fatalf("expected a too-short STORED signature to never trigger the signature lane, but id=%d matched", id)
		}
	}
}

// ─── Shared n-gram matching (recall #1399: recurring error, different location) ─
//
// RED (matches the primary real-world use case the two-way full-string
// containment check misses): obs #1332's stored error_signature is
// prose-polluted (derived from a memory paragraph quoting the error deep
// inside unrelated sentences), and a FRESH occurrence of the same
// underlying error happens in a different file/variable. Neither
// signature is a full substring of the other, so the old containment-only
// check returns nothing — but they share the distinctive error core
// "cannot read properties of", which ErrorSignatureProbes must surface as
// a matching n-gram.
const obs1332ErrorSignature = "root cause from hermes logs typeerror cannot read properties of null reading results at searchengram engram search returns json null not or when a query matches nothing common with garbled voice transcriptions e g alexa transcribed a request as query workly dime falta poder venderlo project null the code did array isarray data data data"

func TestSearch_SignatureLane_MatchesSameErrorDifferentLocation(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	fixID, err := s.AddObservation(AddObservationParams{
		SessionID:      "s1",
		Type:           "bugfix",
		Title:          "Root cause: Engram search returns JSON null instead of empty results",
		Content:        "Root cause from Hermes logs: TypeError: Cannot read properties of null (reading 'results') at searchEngram. Engram search returns JSON null (not []) when a query matches nothing, common with garbled voice transcriptions.",
		Project:        "engram",
		Scope:          "project",
		ErrorSignature: obs1332ErrorSignature,
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	// A fresh occurrence of the SAME underlying error in a completely
	// different file/variable ("value" vs "results", "ProductForm" vs
	// "searchEngram") — neither signature is a full substring of the
	// other, but they share the distinctive core "cannot read properties
	// of".
	newOccurrence := "Uncaught TypeError: Cannot read properties of null (reading 'value') at ProductForm (/src/modules/inventory/ProductForm.vue:88:14)"

	results, err := s.Search(newOccurrence, SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	var found *SearchResult
	for i := range results {
		if results[i].ID == fixID {
			found = &results[i]
		}
	}
	if found == nil {
		t.Fatalf("expected the recurring error (same core, different file/variable) to surface via the signature lane, got %d results: %+v", len(results), results)
	}
	if !found.SignatureMatch {
		t.Fatalf("expected SignatureMatch=true, got %+v", found)
	}
	if found.Rank != signatureMatchRank {
		t.Fatalf("expected rank=%v (signature lane tier), got %v", signatureMatchRank, found.Rank)
	}
}

func TestSearch_SignatureLane_DoesNotMatchUnrelatedError(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	fixID, err := s.AddObservation(AddObservationParams{
		SessionID:      "s1",
		Type:           "bugfix",
		Title:          "Root cause: Engram search returns JSON null instead of empty results",
		Content:        "Root cause from Hermes logs: TypeError: Cannot read properties of null (reading 'results') at searchEngram. Engram search returns JSON null (not []) when a query matches nothing, common with garbled voice transcriptions.",
		Project:        "engram",
		Scope:          "project",
		ErrorSignature: obs1332ErrorSignature,
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	// A totally unrelated error — shares no distinctive n-gram with the
	// stored signature, and must NOT be surfaced by the signature lane.
	results, err := s.Search("dial tcp 127.0.0.1:5432: connect: connection refused", SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	for _, r := range results {
		if r.ID == fixID && r.SignatureMatch {
			t.Fatalf("expected NO signature-lane match for an unrelated error, but id=%d matched", fixID)
		}
	}
}
