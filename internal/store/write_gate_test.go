package store

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// ─── Helpers ─────────────────────────────────────────────────────────────────

// newWriteGateStore builds a fresh store for write-gate tests. mutate lets a
// test override store.Config fields (WriteHygieneEnabled and the four
// threshold/limit fields) before the store opens; nil leaves the gate off
// (the store.Config zero value — see the Config.WriteHygieneEnabled doc
// comment in store.go).
func newWriteGateStore(t *testing.T, mutate func(*Config)) *Store {
	t.Helper()
	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()
	cfg.DedupeWindow = time.Hour
	if mutate != nil {
		mutate(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.CreateSession("ses-wg-test", "wgproject", "/tmp/wg"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return s
}

// gateEnabledDefaults sets the design-default thresholds (obs #1668 D5) on a
// store.Config: noop=0.98, update=0.9, shrink_guard=0.9, candidate_limit=10.
func gateEnabledDefaults(cfg *Config) {
	cfg.WriteHygieneEnabled = true
	cfg.NoopThreshold = 0.98
	cfg.UpdateThreshold = 0.9
	cfg.ShrinkGuard = 0.9
	cfg.CandidateLimit = 10
}

func countObservations(t *testing.T, s *Store, project string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM observations WHERE project = ?`, project).Scan(&n); err != nil {
		t.Fatalf("count observations: %v", err)
	}
	return n
}

func mustSave(t *testing.T, s *Store, p AddObservationParams) SaveResult {
	t.Helper()
	if p.SessionID == "" {
		p.SessionID = "ses-wg-test"
	}
	if p.Project == "" {
		p.Project = "wgproject"
	}
	if p.Scope == "" {
		p.Scope = "project"
	}
	if p.Type == "" {
		p.Type = "discovery"
	}
	result, err := s.SaveObservation(p)
	if err != nil {
		t.Fatalf("SaveObservation(%q): %v", p.Title, err)
	}
	return result
}

// ─── Task 3.1 — kill-switch off ──────────────────────────────────────────────

// TestSaveObservation_KillSwitchOffDifferentTitleSameContentCreatesTwoRows
// pins the write-gate's kill-switch (spec write-gate REQ "Default-On With
// Kill-Switch"): with WriteHygieneEnabled=false, two saves that share
// IDENTICAL content but DIFFERENT titles must both persist as brand-new rows
// — byte-for-byte pre-v0.3.1 AddObservation behavior. The pre-existing
// dedupe-hash-window step (untouched by this PR) requires an EXACT title
// match to fire, so different titles already sail through it in v0.3; this
// test locks in that the (new, disabled) ladder does NOT additionally catch
// what the hash-window never caught — the exact real-world gap write-hygiene
// exists to close (obs #1662: same substance, different title/wording).
func TestSaveObservation_KillSwitchOffDifferentTitleSameContentCreatesTwoRows(t *testing.T) {
	s := newWriteGateStore(t, nil) // WriteHygieneEnabled left at zero value (false)

	sharedContent := "Fixed the null pointer crash in the login handler by adding a nil check before dereferencing the session token."

	r1 := mustSave(t, s, AddObservationParams{Title: "Login crash fix", Content: sharedContent})
	r2 := mustSave(t, s, AddObservationParams{Title: "Session token nil check", Content: sharedContent})

	if r1.ID == r2.ID {
		t.Fatalf("expected two distinct rows, got the same ID twice: %d", r1.ID)
	}
	if r1.Decision != WriteGateDecisionSave || r2.Decision != WriteGateDecisionSave {
		t.Errorf("expected both decisions to be %q with the gate off, got %q and %q", WriteGateDecisionSave, r1.Decision, r2.Decision)
	}
	if got := countObservations(t, s, "wgproject"); got != 2 {
		t.Fatalf("expected 2 rows in observations table, got %d", got)
	}
}

// ─── Task 3.2 — full ladder ───────────────────────────────────────────────────

// TestSaveObservation_LadderNoop pins step 3 (design obs #1668 D3): a
// case/punctuation-only variant of existing content — Jaccard 1.0, comfortably
// >= the 0.98 noop_threshold — skips the insert, bumps duplicate_count on the
// existing row, and returns its ID.
func TestSaveObservation_LadderNoop(t *testing.T) {
	s := newWriteGateStore(t, gateEnabledDefaults)

	existing := mustSave(t, s, AddObservationParams{
		Title:   "Null pointer crash writeup",
		Content: "Fixed the null pointer crash in the login handler by adding a nil check before dereferencing the session token.",
	})

	// Same words, different case/punctuation/whitespace only — Tokenize
	// (lowercase + [\p{L}\p{N}]+ extraction) makes this Jaccard-identical to
	// the existing content (save-normalization spec REQ "Canonical
	// Normalization For Comparison").
	got := mustSave(t, s, AddObservationParams{
		Title:   "Null pointer crash notes",
		Content: "fixed the null pointer crash in the login handler by adding a nil check before dereferencing the session token",
	})

	if got.Decision != WriteGateDecisionNoop {
		t.Fatalf("expected decision %q, got %q (reason: %s)", WriteGateDecisionNoop, got.Decision, got.Reason)
	}
	if got.TargetID == nil || *got.TargetID != existing.ID {
		t.Fatalf("expected TargetID=%d, got %v", existing.ID, got.TargetID)
	}
	if got.ID != existing.ID {
		t.Fatalf("expected NOOP's ID to equal the existing row's ID %d, got %d", existing.ID, got.ID)
	}
	if got.Similarity < 0.98 {
		t.Errorf("expected similarity >= 0.98, got %.4f", got.Similarity)
	}
	if got := countObservations(t, s, "wgproject"); got != 1 {
		t.Fatalf("expected NOOP to skip the insert (1 row total), got %d", got)
	}
}

// TestSaveObservation_LadderAutoUpdate pins step 4 (design obs #1668 D3): a
// single-word variant scoring strictly > 0.9 and <= a well-under-0.98
// ceiling, with the replacement content the SAME length class as the
// original (shrink-guard trivially satisfied), triggers an in-place UPDATE.
func TestSaveObservation_LadderAutoUpdate(t *testing.T) {
	s := newWriteGateStore(t, gateEnabledDefaults)

	original := "Documented the full database migration runbook for the sqlite to postgres cutover including the pre-migration backup step the schema diff review the dry run validation pass the maintenance window announcement and the post-migration smoke test checklist covering every critical query path"
	revision := "Documented the full database migration runbook for the sqlite to postgres cutover including the pre-migration backup step the schema diff review the dry run validation pass the maintenance window announcement and the post-migration smoke test checklist covering every critical rollback path"

	existing := mustSave(t, s, AddObservationParams{Title: "DB migration runbook", Content: original, Type: "procedural"})
	got := mustSave(t, s, AddObservationParams{Title: "DB migration runbook v2", Content: revision, Type: "procedural"})

	if got.Decision != WriteGateDecisionUpdate {
		t.Fatalf("expected decision %q, got %q (reason: %s, similarity: %.4f)", WriteGateDecisionUpdate, got.Decision, got.Reason, got.Similarity)
	}
	if got.TargetID == nil || *got.TargetID != existing.ID || got.ID != existing.ID {
		t.Fatalf("expected update to target existing row %d, got ID=%d TargetID=%v", existing.ID, got.ID, got.TargetID)
	}
	if got.Similarity <= 0.9 || got.Similarity >= 0.98 {
		t.Errorf("expected similarity strictly between 0.9 and 0.98, got %.4f", got.Similarity)
	}
	updated, err := s.GetObservation(existing.ID)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if updated.Content != revision {
		t.Errorf("expected the existing row's content to be replaced with the revision, got %q", updated.Content)
	}
	if updated.RevisionCount != 2 {
		t.Errorf("expected revision_count=2 after one auto-update, got %d", updated.RevisionCount)
	}
	if got := countObservations(t, s, "wgproject"); got != 1 {
		t.Fatalf("expected AUTO-UPDATE to skip the insert (1 row total), got %d", got)
	}
}

// TestSaveObservation_LadderBoundaryExactPointNineDoesNotAutoUpdate pins the
// spec write-gate scenario "Similarity exactly at threshold does not
// auto-update": a pair constructed to score EXACTLY 0.9 Jaccard (19 unique
// tokens each, 18 shared, 1 exclusive per side => 18/20 = 0.9 exactly) must
// fall through to RELATE, not UPDATE — the business rule is strict ">0.9".
func TestSaveObservation_LadderBoundaryExactPointNineDoesNotAutoUpdate(t *testing.T) {
	s := newWriteGateStore(t, gateEnabledDefaults)

	existingContent := "alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima mike november oscar papa quebec romeo sierra"
	newContent := "alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima mike november oscar papa quebec romeo tango"

	existing := mustSave(t, s, AddObservationParams{Title: "NATO boundary fixture", Content: existingContent})
	got := mustSave(t, s, AddObservationParams{Title: "NATO boundary fixture v2", Content: newContent})

	const wantSimilarity = 0.9
	if diff := got.Similarity - wantSimilarity; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("expected the fixture to score exactly %.1f jaccard, got %.6f (fixture drifted — recheck token counts)", wantSimilarity, got.Similarity)
	}
	if got.Decision != WriteGateDecisionRelate {
		t.Fatalf("expected exactly-0.9 to fall through to %q, got %q", WriteGateDecisionRelate, got.Decision)
	}
	if got.TargetID == nil || *got.TargetID != existing.ID {
		t.Fatalf("expected RELATE's candidate to be the existing row %d, got %v", existing.ID, got.TargetID)
	}
	if got := countObservations(t, s, "wgproject"); got != 2 {
		t.Fatalf("expected RELATE to insert a new row (2 rows total), got %d", got)
	}
}

// TestSaveObservation_LadderShrinkGuardBlocksUpdateDowngradesToNoop pins the
// D3 superset/shrink-guard test: a candidate scores 0.9143 jaccard (in the
// AUTO-UPDATE band) but the new content is only ~78% of the existing
// content's length — well under the default shrink_guard (0.9) — so the
// ladder MUST downgrade to NOOP (keep the richer existing row) instead of
// silently discarding detail via an in-place update.
func TestSaveObservation_LadderShrinkGuardBlocksUpdateDowngradesToNoop(t *testing.T) {
	s := newWriteGateStore(t, gateEnabledDefaults)

	original := "Documented the full database migration runbook for the sqlite to postgres cutover including the pre-migration backup step the schema diff review the dry run validation pass the maintenance window announcement and the post-migration smoke test checklist covering every critical query path"
	// original's 35 unique tokens minus its 3 trailing ones (query/path/critical-ish tail),
	// reusing the SAME words in the SAME order but dropped — jaccard 0.9143, length ratio 0.777.
	shorter := "documented the full database migration runbook for sqlite to postgres cutover including pre-migration backup step schema diff review dry run validation pass maintenance window announcement and post-migration smoke test checklist covering every"

	existing := mustSave(t, s, AddObservationParams{Title: "DB migration runbook", Content: original, Type: "procedural"})
	got := mustSave(t, s, AddObservationParams{Title: "DB migration runbook short", Content: shorter, Type: "procedural"})

	if got.Similarity <= 0.9 || got.Similarity >= 0.98 {
		t.Fatalf("expected the fixture to land strictly between 0.9 and 0.98 jaccard, got %.4f (fixture drifted)", got.Similarity)
	}
	if len(shorter) >= int(0.9*float64(len(original))) {
		t.Fatalf("fixture drifted: shorter content must be < 90%% of original's length (got %.4f)", float64(len(shorter))/float64(len(original)))
	}
	if got.Decision != WriteGateDecisionNoop {
		t.Fatalf("expected the shrink-guard to downgrade to %q, got %q (reason: %s)", WriteGateDecisionNoop, got.Decision, got.Reason)
	}
	if got.TargetID == nil || *got.TargetID != existing.ID {
		t.Fatalf("expected the downgraded NOOP to target the existing row %d, got %v", existing.ID, got.TargetID)
	}
	if !strings.Contains(got.Reason, "shrink-guard") {
		t.Errorf("expected the reason to mention the shrink-guard, got %q", got.Reason)
	}
	kept, err := s.GetObservation(existing.ID)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if kept.Content != original {
		t.Errorf("expected the richer existing content to survive untouched, got %q", kept.Content)
	}
	if got := countObservations(t, s, "wgproject"); got != 1 {
		t.Fatalf("expected the shrink-guard-blocked NOOP to skip the insert (1 row total), got %d", got)
	}
}

// TestSaveObservation_LadderSaveRelate pins step 5: a candidate is found via
// the FTS title pre-filter, but its content scores well below the update
// threshold — the call MUST insert a new row (unchanged existing
// FindCandidates/judgment_required flow is untouched by this PR) and report
// decision "relate" naming the low-scoring candidate.
func TestSaveObservation_LadderSaveRelate(t *testing.T) {
	s := newWriteGateStore(t, gateEnabledDefaults)

	original := "Documented the full database migration runbook for the sqlite to postgres cutover including the pre-migration backup step the schema diff review the dry run validation pass the maintenance window announcement and the post-migration smoke test checklist covering every critical query path"
	unrelatedEnough := "Wrote a short note about the postgres migration effort mentioning a backup step and a validation pass, but skipping most of the runbook detail, window scheduling, and smoke test checklist entirely"

	existing := mustSave(t, s, AddObservationParams{Title: "DB migration runbook", Content: original, Type: "procedural"})
	got := mustSave(t, s, AddObservationParams{Title: "DB migration short note", Content: unrelatedEnough, Type: "procedural"})

	if got.Decision != WriteGateDecisionRelate {
		t.Fatalf("expected decision %q, got %q (similarity %.4f)", WriteGateDecisionRelate, got.Decision, got.Similarity)
	}
	if got.Similarity >= 0.9 {
		t.Fatalf("fixture drifted: expected similarity well under 0.9, got %.4f", got.Similarity)
	}
	if got.TargetID == nil || *got.TargetID != existing.ID {
		t.Fatalf("expected the RELATE candidate to be the existing row %d, got %v", existing.ID, got.TargetID)
	}
	if got.ID == existing.ID {
		t.Fatalf("expected RELATE to insert a NEW row (distinct ID), got the same ID %d", got.ID)
	}
	if got := countObservations(t, s, "wgproject"); got != 2 {
		t.Fatalf("expected RELATE to insert a new row (2 rows total), got %d", got)
	}
}

// TestSaveObservation_LadderSaveNoCandidate pins step 6: when the FTS title
// pre-filter finds no row at all (even though the corpus is non-empty), the
// call is a plain SAVE with no target.
func TestSaveObservation_LadderSaveNoCandidate(t *testing.T) {
	s := newWriteGateStore(t, gateEnabledDefaults)

	// Seed an unrelated distractor so the corpus isn't empty.
	mustSave(t, s, AddObservationParams{Title: "Weather forecast notes", Content: "Rain expected across the valley region tomorrow afternoon."})

	got := mustSave(t, s, AddObservationParams{Title: "Kilo Lima Mike November", Content: "Completely unrelated subject matter with its own distinct vocabulary."})

	if got.Decision != WriteGateDecisionSave {
		t.Fatalf("expected decision %q, got %q (reason: %s)", WriteGateDecisionSave, got.Decision, got.Reason)
	}
	if got.TargetID != nil {
		t.Fatalf("expected TargetID=nil for a plain SAVE, got %v", *got.TargetID)
	}
	if got.Similarity != 0 {
		t.Errorf("expected similarity=0 with no candidate, got %.4f", got.Similarity)
	}
	if got := countObservations(t, s, "wgproject"); got != 2 {
		t.Fatalf("expected 2 rows total (distractor + new save), got %d", got)
	}
}

// TestSaveObservation_LadderDeterministic pins the "determinism" case from
// task 3.2: given the SAME seed candidate and the SAME new-observation input
// (a non-hash/non-topic_key path — different titles, RELATE-band content),
// two independently built stores MUST classify identically. This isolates
// the ladder's own determinism from any state-mutation side effect a repeat
// call against the SAME store would introduce (the second call would then
// see its own first result as a fresh, even-closer candidate).
func TestSaveObservation_LadderDeterministic(t *testing.T) {
	seed := func(t *testing.T) (*Store, int64) {
		t.Helper()
		s := newWriteGateStore(t, gateEnabledDefaults)
		existing := mustSave(t, s, AddObservationParams{
			Title:   "DB migration runbook",
			Content: "Documented the full database migration runbook for the sqlite to postgres cutover including the pre-migration backup step the schema diff review the dry run validation pass the maintenance window announcement and the post-migration smoke test checklist covering every critical query path",
			Type:    "procedural",
		})
		return s, existing.ID
	}
	newParams := AddObservationParams{
		Title:   "DB migration short note",
		Content: "Wrote a short note about the postgres migration effort mentioning a backup step and a validation pass, but skipping most of the runbook detail, window scheduling, and smoke test checklist entirely",
		Type:    "procedural",
	}

	sA, existingA := seed(t)
	sB, existingB := seed(t)

	gotA := mustSave(t, sA, newParams)
	gotB := mustSave(t, sB, newParams)

	if gotA.Decision != gotB.Decision {
		t.Fatalf("expected identical decisions across independent stores, got %q vs %q", gotA.Decision, gotB.Decision)
	}
	if gotA.Similarity != gotB.Similarity {
		t.Fatalf("expected identical similarity scores, got %.6f vs %.6f", gotA.Similarity, gotB.Similarity)
	}
	if existingA != *gotA.TargetID || existingB != *gotB.TargetID {
		t.Fatalf("expected both calls to target their own seeded candidate")
	}
}

// TestSaveObservation_LadderCandidateLimitExcludesLowerRankedCandidate pins
// the "candidate-limit" edge: CandidateLimit caps candidate retrieval at the
// TOP-ranked (BM25) FTS matches BEFORE Jaccard scoring — it is never an
// all-pairs scan (design obs #1668 D2). Two candidates are seeded: A's title
// matches ALL of the new save's title words (strong multi-term BM25 match)
// but its CONTENT is unrelated; B's title matches only ONE word (weak match)
// but its CONTENT is a near-verbatim duplicate of the new save (jaccard
// ~1.0). With CandidateLimit=1, only the best-ranked candidate (A) is even
// considered, so the call MUST classify against A (RELATE, low similarity)
// — NOT against B's much higher Jaccard, proving the limit is applied before
// scoring rather than after (which would have picked B regardless of rank).
func TestSaveObservation_LadderCandidateLimitExcludesLowerRankedCandidate(t *testing.T) {
	newTitle := "Kilo Lima Mike November"
	newContent := "Quarterly infrastructure review notes covering budget, staffing, and vendor renewals for the coming cycle."

	seedCandidates := func(t *testing.T, s *Store) (aID, bID int64) {
		t.Helper()
		a := mustSave(t, s, AddObservationParams{
			Title:   newTitle + " Report", // shares all 4 words -> strong multi-term FTS match
			Content: "Completely unrelated weather forecast content with its own distinct vocabulary and nothing in common.",
		})
		b := mustSave(t, s, AddObservationParams{
			Title:   "November notes", // shares only 1 word -> weak FTS match
			Content: newContent,       // near-identical to the new save's content
		})
		return a.ID, b.ID
	}

	t.Run("limit=1 excludes the higher-jaccard, lower-ranked candidate", func(t *testing.T) {
		s := newWriteGateStore(t, func(cfg *Config) {
			gateEnabledDefaults(cfg)
			cfg.CandidateLimit = 1
		})
		aID, _ := seedCandidates(t, s)

		got := mustSave(t, s, AddObservationParams{Title: newTitle, Content: newContent})

		if got.TargetID == nil || *got.TargetID != aID {
			t.Fatalf("expected CandidateLimit=1 to restrict the ladder to the top-ranked candidate #%d, got target %v (decision %q, similarity %.4f)", aID, got.TargetID, got.Decision, got.Similarity)
		}
		if got.Decision == WriteGateDecisionNoop || got.Decision == WriteGateDecisionUpdate {
			t.Fatalf("expected the unrelated top-ranked candidate to score low (relate/save), got decision %q", got.Decision)
		}
	})

	t.Run("limit=10 (default) reaches the higher-jaccard candidate", func(t *testing.T) {
		s := newWriteGateStore(t, gateEnabledDefaults)
		_, bID := seedCandidates(t, s)

		got := mustSave(t, s, AddObservationParams{Title: newTitle, Content: newContent})

		if got.Decision != WriteGateDecisionNoop {
			t.Fatalf("expected the wider candidate pool to reach candidate B's near-duplicate content (NOOP), got %q (similarity %.4f)", got.Decision, got.Similarity)
		}
		if got.TargetID == nil || *got.TargetID != bID {
			t.Fatalf("expected the NOOP target to be candidate B (#%d), got %v", bID, got.TargetID)
		}
	})
}

// ─── Task 3.6 — AddObservation stays a thin wrapper ──────────────────────────

// TestAddObservation_ThinWrapperMatchesSaveObservationID confirms
// AddObservation(p) (int64, error) — the pre-existing signature every caller
// still uses — returns exactly SaveObservation(p).ID, for both a plain SAVE
// and a ladder-classified NOOP.
func TestAddObservation_ThinWrapperMatchesSaveObservationID(t *testing.T) {
	s := newWriteGateStore(t, gateEnabledDefaults)

	id, err := s.AddObservation(AddObservationParams{
		SessionID: "ses-wg-test",
		Project:   "wgproject",
		Scope:     "project",
		Type:      "discovery",
		Title:     "Plain save via AddObservation",
		Content:   "Some fresh content with no existing candidate to match against.",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	if id == 0 {
		t.Fatal("expected a non-zero ID from AddObservation")
	}

	// A second call with identical content but a different title, when the
	// gate is on, must NOOP back to the SAME id via the thin wrapper.
	id2, err := s.AddObservation(AddObservationParams{
		SessionID: "ses-wg-test",
		Project:   "wgproject",
		Scope:     "project",
		Type:      "discovery",
		Title:     "Plain save via AddObservation, take two",
		Content:   "some fresh content with no existing candidate to match against",
	})
	if err != nil {
		t.Fatalf("AddObservation (second call): %v", err)
	}
	if id2 != id {
		t.Fatalf("expected the NOOP'd second call to return the same ID %d via the thin wrapper, got %d", id, id2)
	}
}

// ─── PR3 review fixes (adversarial review) ───────────────────────────────────

// TestSaveObservation_EmbeddedQuoteTitleStillPersists pins the adversarial-
// review fix (CRITICAL): a title with an embedded (non-edge) double-quote
// used to build a malformed FTS5 phrase inside evaluateWriteGate's candidate
// query ("SQL logic error: unterminated string (1)"), and that error used to
// propagate straight out of SaveObservation — meaning the observation was
// NEVER PERSISTED. This violates the spec's absolute rule that hygiene never
// blocks a save. The save must now SUCCEED, persist the observation
// verbatim, and record the degraded gate in Reason.
func TestSaveObservation_EmbeddedQuoteTitleStillPersists(t *testing.T) {
	s := newWriteGateStore(t, gateEnabledDefaults)

	got := mustSave(t, s, AddObservationParams{
		Title:   `foo"bar baz`,
		Content: "Some ordinary content with no candidates to match against.",
	})

	if got.ID == 0 {
		t.Fatal("expected a non-zero ID — the save must persist despite the malformed FTS title")
	}
	obs, err := s.GetObservation(got.ID)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if obs.Title != `foo"bar baz` {
		t.Errorf("expected the title to persist verbatim, got %q", obs.Title)
	}
	if n := countObservations(t, s, "wgproject"); n != 1 {
		t.Fatalf("expected the observation to be persisted (1 row), got %d", n)
	}
}

// TestSaveObservation_FunctionCallTitleStillPersists is the
// getUserId("foo") variant of the same bug: a stray/unbalanced double-quote
// inside a function-call-style title (an ordinary bugfix title shape) must
// not abort the save either.
func TestSaveObservation_FunctionCallTitleStillPersists(t *testing.T) {
	s := newWriteGateStore(t, gateEnabledDefaults)

	got := mustSave(t, s, AddObservationParams{
		Title:   `Fixed getUserId("foo) returning null`,
		Content: "Some ordinary bugfix content with no candidates to match against.",
		Type:    "bugfix",
	})

	if got.ID == 0 {
		t.Fatal("expected a non-zero ID — the save must persist despite the malformed FTS title")
	}
	if n := countObservations(t, s, "wgproject"); n != 1 {
		t.Fatalf("expected the observation to be persisted (1 row), got %d", n)
	}
}

// TestSaveObservation_AutoUpdatePreservesIncomingTopicKey pins the
// adversarial-review fix (HIGH): a save that resolves via the similarity
// ladder's AUTO-UPDATE branch (NOT the topic_key-match path) used to
// silently DROP the caller's TopicKey — the UPDATE's SET list omitted
// topic_key entirely. The updated row must now carry the incoming
// (normalized) topic key.
func TestSaveObservation_AutoUpdatePreservesIncomingTopicKey(t *testing.T) {
	s := newWriteGateStore(t, gateEnabledDefaults)

	original := "Documented the full database migration runbook for the sqlite to postgres cutover including the pre-migration backup step the schema diff review the dry run validation pass the maintenance window announcement and the post-migration smoke test checklist covering every critical query path"
	revision := "Documented the full database migration runbook for the sqlite to postgres cutover including the pre-migration backup step the schema diff review the dry run validation pass the maintenance window announcement and the post-migration smoke test checklist covering every critical rollback path"

	existing := mustSave(t, s, AddObservationParams{Title: "DB migration runbook", Content: original, Type: "procedural"})
	got := mustSave(t, s, AddObservationParams{
		Title:    "DB migration runbook v2",
		Content:  revision,
		Type:     "procedural",
		TopicKey: "  Migration Runbook  ",
	})

	if got.Decision != WriteGateDecisionUpdate {
		t.Fatalf("expected decision %q, got %q (fixture drifted?)", WriteGateDecisionUpdate, got.Decision)
	}
	if got.TargetID == nil || *got.TargetID != existing.ID {
		t.Fatalf("expected the update to target the existing row %d, got %v", existing.ID, got.TargetID)
	}
	updated, err := s.GetObservation(existing.ID)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	const wantTopicKey = "migration-runbook"
	if updated.TopicKey == nil || *updated.TopicKey != wantTopicKey {
		t.Fatalf("expected the auto-updated row to carry the incoming (normalized) topic_key %q, got %v", wantTopicKey, updated.TopicKey)
	}
}

// TestSaveObservation_TypeMismatchDowngradesAutoUpdateToRelate pins the
// adversarial-review fix (MEDIUM): the AUTO-UPDATE branch used to be
// type-blind, letting a similarity-triggered update silently reclassify an
// existing observation across categories (decision -> bugfix, confirmed
// empirically). When the best candidate's type differs from the incoming
// save's type, the ladder must downgrade to RELATE instead — both rows
// persist, and the original row's type is never mutated.
func TestSaveObservation_TypeMismatchDowngradesAutoUpdateToRelate(t *testing.T) {
	s := newWriteGateStore(t, gateEnabledDefaults)

	original := "Documented the full database migration runbook for the sqlite to postgres cutover including the pre-migration backup step the schema diff review the dry run validation pass the maintenance window announcement and the post-migration smoke test checklist covering every critical query path"
	revision := "Documented the full database migration runbook for the sqlite to postgres cutover including the pre-migration backup step the schema diff review the dry run validation pass the maintenance window announcement and the post-migration smoke test checklist covering every critical rollback path"

	existing := mustSave(t, s, AddObservationParams{Title: "DB migration runbook", Content: original, Type: "decision"})
	got := mustSave(t, s, AddObservationParams{Title: "DB migration runbook v2", Content: revision, Type: "bugfix"})

	if got.Decision != WriteGateDecisionRelate {
		t.Fatalf("expected a type mismatch to downgrade to %q, got %q (reason: %s, similarity %.4f)", WriteGateDecisionRelate, got.Decision, got.Reason, got.Similarity)
	}
	if got.ID == existing.ID {
		t.Fatalf("expected RELATE to insert a NEW row, got the same ID %d", got.ID)
	}
	kept, err := s.GetObservation(existing.ID)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if kept.Type != "decision" {
		t.Fatalf("expected the original row's type to remain %q, got %q — AUTO-UPDATE must never mutate type across categories", "decision", kept.Type)
	}
	if kept.Content != original {
		t.Fatalf("expected the original row's content to remain untouched on a type-mismatch downgrade, got %q", kept.Content)
	}
	if n := countObservations(t, s, "wgproject"); n != 2 {
		t.Fatalf("expected both rows to exist (2 total), got %d", n)
	}
}

// TestSaveObservation_TypeMismatchDowngradesNoopToRelate covers the
// >=noop_threshold case with a mismatched type: an (almost) byte-identical
// duplicate declared under a DIFFERENT type is treated as a genuinely new
// fact-with-a-different-type rather than silently bumped as a duplicate of
// the wrong-typed row — it downgrades to RELATE (both rows persist) instead
// of NOOP.
func TestSaveObservation_TypeMismatchDowngradesNoopToRelate(t *testing.T) {
	s := newWriteGateStore(t, gateEnabledDefaults)

	existing := mustSave(t, s, AddObservationParams{
		Title:   "Null pointer crash writeup",
		Content: "Fixed the null pointer crash in the login handler by adding a nil check before dereferencing the session token.",
		Type:    "decision",
	})

	got := mustSave(t, s, AddObservationParams{
		Title:   "Null pointer crash notes",
		Content: "fixed the null pointer crash in the login handler by adding a nil check before dereferencing the session token",
		Type:    "bugfix",
	})

	if got.Decision != WriteGateDecisionRelate {
		t.Fatalf("expected a type mismatch at noop-level similarity to downgrade to %q, got %q (reason: %s)", WriteGateDecisionRelate, got.Decision, got.Reason)
	}
	if got.ID == existing.ID {
		t.Fatalf("expected RELATE to insert a NEW row, got the same ID %d", got.ID)
	}
	kept, err := s.GetObservation(existing.ID)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if kept.Type != "decision" {
		t.Fatalf("expected the original row's type to remain %q, got %q", "decision", kept.Type)
	}
	if kept.DuplicateCount != 1 {
		t.Fatalf("expected duplicate_count to remain 1 (not bumped) since the ladder downgraded away from noop, got %d", kept.DuplicateCount)
	}
	if n := countObservations(t, s, "wgproject"); n != 2 {
		t.Fatalf("expected both rows to exist (2 total), got %d", n)
	}
}

// ─── Real-data validation (obs #1683 battery I, issue #171 / PR13) ──────────

// TestSaveObservation_LadderNoopFiresAtRealisticCorpusScale pins the fix for
// battery I's flagship CRITICAL finding (obs #1683): evaluateWriteGate's BM25
// floor must stay permissive enough that NOOP/AUTO-UPDATE still fire once the
// FTS index holds a realistic number of rows, not just a handful of fixture
// rows. Every write-gate test above passes even with a tight floor because
// tiny fixture corpora yield bm25 scores well above -2.0 — the defect only
// surfaces once corpus size pushes BM25's IDF component far enough negative,
// exactly like the real ~1681-observation base battery I measured (-4.3 to
// -31, all more negative than the old -2.0 floor, so EVERY candidate was
// excluded and a near-verbatim re-save silently duplicated instead of
// NOOPing). This mirrors cmd/omnia/dedupe_test.go's own
// TestRunDedupeScanCandidatePreFilterBoundedAtScale fixture (1650 filler
// rows, the same real mechanism that led dedupe.go to its own
// dedupeCandidateBM25Floor=-1000 fix) — exercised here through the LIVE
// evaluateWriteGate path instead of the offline dedupe scan.
func TestSaveObservation_LadderNoopFiresAtRealisticCorpusScale(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large-scale write-gate test in -short mode")
	}

	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()
	cfg.DedupeWindow = time.Hour

	// Seed phase: gate OFF (Config zero value) so 1650 filler rows insert as
	// fast plain saves — the corpus-size effect on BM25 comes from FTS5's
	// global IDF statistics, not from the gate itself, so seeding never needs
	// the gate active. The store is reopened below (same DataDir/sqlite
	// file) with the gate ON for the actual assertion, mirroring
	// cmd/omnia/dedupe_test.go's own two-store-handle scale fixture.
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if err := s.CreateSession("wg-scale-sess", "wgscaleproj", "/tmp/wgscale"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	const fillerCount = 1650
	for i := 0; i < fillerCount; i++ {
		if _, err := s.AddObservation(AddObservationParams{
			SessionID: "wg-scale-sess",
			Type:      "discovery",
			Title:     fmt.Sprintf("Filler Entry %d", i),
			Content:   fmt.Sprintf("system log entry number %d for project telemetry batch", i),
			Project:   "wgscaleproj",
			Scope:     "project",
		}); err != nil {
			t.Fatalf("seed filler %d: %v", i, err)
		}
	}

	existingID, err := s.AddObservation(AddObservationParams{
		SessionID: "wg-scale-sess",
		Type:      "discovery",
		Title:     "Auth Module Nil Pointer Crash Writeup",
		Content:   "Fixed the null pointer crash in the login handler by adding a nil check before dereferencing the session token wg-scale-fixture-marker.",
		Project:   "wgscaleproj",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("seed existing observation: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close seeding store: %v", err)
	}

	// Reopen the SAME DataDir with the gate ON — this is the path a real
	// mem_save/omnia-save call takes in production.
	gateCfg := cfg
	gateEnabledDefaults(&gateCfg)
	s2, err := New(gateCfg)
	if err != nil {
		t.Fatalf("new store (gate on): %v", err)
	}
	defer s2.Close()

	// Same words, different case/punctuation only — Jaccard 1.0 against the
	// existing row, the exact "near-verbatim re-save" shape battery I
	// reproduced against the real DB (real memory #1451/#1360 duplicated
	// with the gate ON).
	got, err := s2.SaveObservation(AddObservationParams{
		SessionID: "wg-scale-sess",
		Type:      "discovery",
		Title:     "Auth Module Nil Pointer Crash Notes",
		Content:   "fixed the null pointer crash in the login handler by adding a nil check before dereferencing the session token wg-scale-fixture-marker",
		Project:   "wgscaleproj",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("SaveObservation: %v", err)
	}

	if got.Decision != WriteGateDecisionNoop {
		t.Fatalf("expected %q at realistic corpus scale (%d rows), got %q (reason: %q, similarity %.4f) — the write-gate's BM25 floor excluded the real candidate, exactly battery I's inert-gate finding (obs #1683)",
			WriteGateDecisionNoop, fillerCount+1, got.Decision, got.Reason, got.Similarity)
	}
	if got.TargetID == nil || *got.TargetID != existingID {
		t.Fatalf("expected TargetID=%d, got %v", existingID, got.TargetID)
	}
	if n := countObservations(t, s2, "wgscaleproj"); n != fillerCount+1 {
		t.Fatalf("expected NOOP to skip inserting a new row (total %d), got %d", fillerCount+1, n)
	}
}
