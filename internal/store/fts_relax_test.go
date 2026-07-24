package store

import (
	"testing"
	"time"
)

// ─── Search: zero-hit relaxation ladder (design obs #1668 D7, spec
// fts-recall, PR7) ───────────────────────────────────────────────────────
//
// Reproduces the real-data battery failure obs #1659/#1662 flagged: a
// natural-language query (often Spanish, function-word-heavy) whose strict
// AND-of-every-term FTS5 match finds nothing even though the document the
// caller is looking for plainly exists, because a stopword in the query
// ("cómo"/"lo"/"que"/"the"/"a"/...) simply never occurs verbatim in the
// stored title/content. The ladder never changes any non-zero-hit path
// (golden-test contract below) and is bounded to at most 2 extra retries.

// newTestStoreWithDisableFTSRelax mirrors newTestStore but sets
// Config.DisableFTSRelax — the PR7 follow-up kill-switch (design obs #1668
// D7) — to true, so callers can exercise the golden "byte-for-byte pre-PR7
// zero-hit behavior" path.
func newTestStoreWithDisableFTSRelax(t *testing.T) *Store {
	t.Helper()
	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()
	cfg.DedupeWindow = time.Hour
	cfg.DisableFTSRelax = true

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
	})
	return s
}

func TestSearch_StrictHitsSkipRelaxationLadder(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	fixID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "discovery",
		Title:     "Database connection pool exhausted",
		Content:   "The database connection pool exhausted under load during the nightly batch job.",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	// Every word here (including "the") is verbatim in the content above, so
	// the strict AND-of-all-terms pass already finds ≥1 hit — the golden-test
	// contract: this path must be byte-for-byte untouched by the ladder.
	var diag SearchDiag
	results, err := s.Search("the database connection pool exhausted", SearchOptions{
		Project: "engram",
		Limit:   10,
		Diag:    &diag,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].ID != fixID {
		t.Fatalf("expected exactly 1 result (id=%d), got %d: %+v", fixID, len(results), results)
	}
	if diag != (SearchDiag{}) {
		t.Fatalf("expected zero-value Diag when the strict pass already found hits (ladder must never fire), got %+v", diag)
	}
}

// TestSearch_ZeroHitStopwordRelaxationFindsResult reproduces the real
// battery failure shape: a natural-language Spanish query whose stopwords
// ("cómo", "el", "las") do not occur verbatim in the target document, so
// the strict pass returns 0 rows — dropping those stopwords (step 1) then
// finds the document via its remaining, real terms.
func TestSearch_ZeroHitStopwordRelaxationFindsResult(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	fixID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "discovery",
		Title:     "Limpieza automática de memorias duplicadas",
		Content:   "El sistema detecta y limpia las memorias duplicadas automáticamente antes de guardarlas en el almacenamiento.",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	// "cómo" never occurs verbatim in the title/content above, so the strict
	// AND-of-every-term pass (which also requires "cómo") returns 0 rows.
	query := "cómo limpia el sistema las memorias duplicadas"
	var diag SearchDiag
	results, err := s.Search(query, SearchOptions{
		Project: "engram",
		Limit:   10,
		Diag:    &diag,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].ID != fixID {
		t.Fatalf("expected the relaxed pass to surface exactly 1 result (id=%d), got %d: %+v", fixID, len(results), results)
	}
	if !diag.Relaxed || diag.Step != 1 || diag.Exhausted {
		t.Fatalf("expected Diag{Relaxed:true, Step:1, Exhausted:false}, got %+v", diag)
	}
}

// TestSearch_DisableFTSRelaxKillSwitchRestoresPrePR7BehaviorByteForByte is
// the golden test for the store.Config.DisableFTSRelax kill-switch (PR7
// follow-up, design obs #1668 D7): given the EXACT SAME fixture/query as
// TestSearch_ZeroHitStopwordRelaxationFindsResult above — which, with the
// ladder active, finds the document via step 1 — setting
// DisableFTSRelax=true must make Search behave byte-for-byte like the
// pre-PR7 strict-AND-only path: the ladder never fires at all (not even
// step 1's stopword drop), so the zero-hit result stays empty and Diag
// stays entirely zero-value (not even Exhausted=true — the ladder never
// runs, it doesn't "exhaust", it is simply switched off).
func TestSearch_DisableFTSRelaxKillSwitchRestoresPrePR7BehaviorByteForByte(t *testing.T) {
	s := newTestStoreWithDisableFTSRelax(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "discovery",
		Title:     "Limpieza automática de memorias duplicadas",
		Content:   "El sistema detecta y limpia las memorias duplicadas automáticamente antes de guardarlas en el almacenamiento.",
		Project:   "engram",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	query := "cómo limpia el sistema las memorias duplicadas"
	var diag SearchDiag
	results, err := s.Search(query, SearchOptions{
		Project: "engram",
		Limit:   10,
		Diag:    &diag,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected DisableFTSRelax=true to restore the pre-PR7 zero-hit result (0 rows), got %d: %+v", len(results), results)
	}
	if diag != (SearchDiag{}) {
		t.Fatalf("expected zero-value Diag when the kill-switch prevents the ladder from ever running, got %+v", diag)
	}
}

// TestSearch_ZeroHitORModeRelaxationFindsResult covers a query with NO
// stopwords at all (so step 1 would be byte-identical to the strict pass
// and is skipped as a guaranteed duplicate — see the "single-term"/"no
// stopwords dropped" skip-guard doc on zeroHitRelax) where only SOME of the
// terms occur in the target document — step 2's OR-of-terms is what
// surfaces it.
func TestSearch_ZeroHitORModeRelaxationFindsResult(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	fixID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "discovery",
		Title:     "Checkout webhook retry",
		Content:   "The payment gateway retried the checkout webhook automatically after a timeout.",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	// None of these 5 terms is a stopword, so step 1 (drop stopwords) would
	// be identical to the strict pass and must be skipped outright. Only
	// "checkout"/"webhook"/"automatically" occur in the content — "refund"
	// and "policy" do not — so the strict AND-of-all-5 pass returns 0 rows,
	// and only step 2's OR-of-terms finds it.
	query := "checkout webhook automatically refund policy"
	var diag SearchDiag
	results, err := s.Search(query, SearchOptions{
		Project: "engram",
		Limit:   10,
		Diag:    &diag,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].ID != fixID {
		t.Fatalf("expected the OR-mode pass to surface exactly 1 result (id=%d), got %d: %+v", fixID, len(results), results)
	}
	if !diag.Relaxed || diag.Step != 2 || diag.Exhausted {
		t.Fatalf("expected Diag{Relaxed:true, Step:2, Exhausted:false}, got %+v", diag)
	}
}

// TestSearch_AllLevelsExhaustedReturnsEmptyWithDiag covers a query that
// legitimately matches nothing at all, even after full relaxation: both
// steps run, both return 0 rows, and the ladder stops (bounded — no
// further/infinite retry), with Diag reflecting the exhaustion.
func TestSearch_AllLevelsExhaustedReturnsEmptyWithDiag(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "discovery",
		Title:     "Banana pancake syrup",
		Content:   "A completely unrelated breakfast note with no shared vocabulary at all.",
		Project:   "engram",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	query := "checkout webhook automatically refund policy"
	var diag SearchDiag
	results, err := s.Search(query, SearchOptions{
		Project: "engram",
		Limit:   10,
		Diag:    &diag,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d: %+v", len(results), results)
	}
	if diag.Relaxed || !diag.Exhausted || diag.Step != 2 {
		t.Fatalf("expected Diag{Relaxed:false, Step:2, Exhausted:true} once every level was tried, got %+v", diag)
	}
}

// TestSearch_AllStopwordQueryNoRelaxationAttempted pins the "all-stopword
// query" edge case: once every word is dropped as a stopword, there is no
// relaxed query left to construct at all — the ladder exhausts immediately
// without ever issuing a step 1 or step 2 retry.
func TestSearch_AllStopwordQueryNoRelaxationAttempted(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "discovery",
		Title:     "Unrelated title",
		Content:   "Unrelated content sharing nothing with the query below.",
		Project:   "engram",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	query := "el la de que" // every word is a stopword
	var diag SearchDiag
	results, err := s.Search(query, SearchOptions{
		Project: "engram",
		Limit:   10,
		Diag:    &diag,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results for an all-stopword query, got %d: %+v", len(results), results)
	}
	if diag.Relaxed || !diag.Exhausted || diag.Step != 0 {
		t.Fatalf("expected Diag{Relaxed:false, Step:0, Exhausted:true} (nothing left to relax with), got %+v", diag)
	}
}

// TestSearch_SingleTermQuerySkipsDuplicateRetry pins the single-term edge
// case: with exactly one non-stopword term, step 1 (drop stopwords) is a
// no-op (nothing changes) and step 2 (OR-of-terms) would be byte-identical
// to step 1/the strict pass (AND-of-one-term == OR-of-one-term) — so the
// ladder must recognize this and stop without ever issuing a duplicate
// retry query.
func TestSearch_SingleTermQuerySkipsDuplicateRetry(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "discovery",
		Title:     "Unrelated title",
		Content:   "Unrelated content sharing nothing with the query below.",
		Project:   "engram",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	query := "xylophone" // single, non-stopword term absent from every row
	var diag SearchDiag
	results, err := s.Search(query, SearchOptions{
		Project: "engram",
		Limit:   10,
		Diag:    &diag,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d: %+v", len(results), results)
	}
	if diag.Relaxed || !diag.Exhausted || diag.Step != 0 {
		t.Fatalf("expected Diag{Relaxed:false, Step:0, Exhausted:true} (single-term query has no distinct relaxed query to retry), got %+v", diag)
	}
}

// TestSearch_TopicKeySentinelUnaffectedWhenLadderNeverFires pins the
// composition contract with the pre-existing topic_key sentinel lane: it
// runs once, earlier in Search, against the ORIGINAL query only, entirely
// independent of the FTS relaxation ladder below it. Here the topic_key
// value equals the query text, so the topic_key lane finds it via plain
// equality AND (since topic_key is itself one of the FTS-indexed columns)
// the strict FTS pass also matches this same row via its own topic_key
// text — i.e. the strict pass is non-zero, so the ladder must never fire
// (Diag stays zero-value) while the sentinel result is still present
// exactly once (deduped, ranked as the topic_key sentinel — not the FTS
// row's own bm25 rank).
func TestSearch_TopicKeySentinelUnaffectedWhenLadderNeverFires(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// No whitespace, already lowercase — survives normalizeTopicKey
	// unchanged, so the stored topic_key is byte-identical to this literal
	// and to the query below (avoids the space→hyphen/lowercase
	// normalization that AddObservation applies to topic_key).
	topicKeyQuery := "proj/x"
	fixID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "discovery",
		Title:     "Alpha",
		Content:   "Beta gamma delta — unrelated to the query vocabulary.",
		Project:   "engram",
		Scope:     "project",
		TopicKey:  topicKeyQuery,
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	var diag SearchDiag
	results, err := s.Search(topicKeyQuery, SearchOptions{
		Project: "engram",
		Limit:   10,
		Diag:    &diag,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].ID != fixID {
		t.Fatalf("expected exactly 1 deduped result (id=%d) from the topic_key sentinel, got %d: %+v", fixID, len(results), results)
	}
	if results[0].Rank != topicKeySentinelRank {
		t.Fatalf("expected the topic_key sentinel rank %v, got %v", topicKeySentinelRank, results[0].Rank)
	}
	if diag != (SearchDiag{}) {
		t.Fatalf("expected zero-value Diag (topic_key's own text satisfies the strict FTS pass too, so the ladder must never fire), got %+v", diag)
	}
}

// TestSearch_RelaxedTermsNeverLeakIntoTopicKeyLane pins the other half of
// the composition contract: when the ladder DOES fire (no "/" in the
// query, so the topic_key block never even runs), an observation whose
// topic_key happens to equal what the relaxed (stopword-dropped) term set
// would look like if naively joined must NOT be surfaced via some
// accidental cross-wiring between the ladder and the topic_key lane.
func TestSearch_RelaxedTermsNeverLeakIntoTopicKeyLane(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// The document the relaxed pass is SUPPOSED to find.
	fixID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "discovery",
		Title:     "Limpieza automática de memorias duplicadas",
		Content:   "El sistema detecta y limpia las memorias duplicadas automáticamente antes de guardarlas en el almacenamiento.",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	// A decoy carrying an unrelated topic_key — since the query below
	// contains no "/", the topic_key exact-match block never runs at all
	// for this query (structurally independent of the ladder), so this
	// decoy must never surface via anything other than genuine FTS
	// vocabulary overlap (it has none).
	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "discovery",
		Title:     "Decoy",
		Content:   "Decoy content sharing nothing with the query.",
		Project:   "engram",
		Scope:     "project",
		TopicKey:  "unrelated/decoy-topic",
	}); err != nil {
		t.Fatalf("AddObservation (decoy): %v", err)
	}

	query := "cómo limpia el sistema las memorias duplicadas"
	var diag SearchDiag
	results, err := s.Search(query, SearchOptions{
		Project: "engram",
		Limit:   10,
		Diag:    &diag,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].ID != fixID {
		t.Fatalf("expected exactly the relaxed FTS hit (id=%d), no decoy leak, got %d: %+v", fixID, len(results), results)
	}
	if !diag.Relaxed || diag.Step != 1 {
		t.Fatalf("expected Diag{Relaxed:true, Step:1}, got %+v", diag)
	}
}
