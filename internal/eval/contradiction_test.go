package eval

import (
	"testing"
	"time"

	"github.com/velion/omnia/internal/store"
)

// newContradictionTestStore builds a self-contained, temp-directory
// *store.Store for contradiction.go's tests — no dependency on (and no
// mutation of) the user's real ~/.omnia database. Mirrors the same
// bootstrap pattern internal/store's own tests use (store_test.go's
// newTestStore, internal/mcp's newMCPTestStore): DefaultConfig() +
// t.TempDir() DataDir + store.New.
func newContradictionTestStore(t *testing.T) *store.Store {
	t.Helper()
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatalf("store.DefaultConfig: %v", err)
	}
	cfg.DataDir = t.TempDir()
	cfg.DedupeWindow = time.Hour

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// seedSupersedesFixture creates two observations in s (an OLDER one and a
// NEWER one that supersedes it) and a real, judged store.RelationSupersedes
// relation between them, using only the store's public API — no direct SQL,
// no fixture file, no shared/user DB. This resolves the deferred fixture
// noted in PR1's apply-progress (obs #1609): testdata/cases.json has no
// SupersedesOf-backed case because no real supersedes relation existed
// anywhere until now. Returns the (older, newer) observation sync_ids.
//
// Direction convention (matches internal/mcp's established semantics — see
// mcp_test.go TestHandleSearch_SupersededAnnotation and mcp.go's
// "supersedes:"/"superseded_by:" annotation labels): SourceID is the
// CURRENT (newer) observation, TargetID is the STALE (older) one. A judged
// relation reads "source supersedes target".
func seedSupersedesFixture(t *testing.T, s *store.Store) (older, newer string) {
	t.Helper()
	const sessionID = "eval-contradiction-test"
	if err := s.CreateSession(sessionID, "eval-harness", "/tmp/eval-harness"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	oldID, err := s.AddObservation(store.AddObservationParams{
		SessionID: sessionID,
		Type:      "architecture",
		Title:     "Old auth design",
		Content:   "We use session-based auth",
		Project:   "eval-harness",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation(old): %v", err)
	}
	oldObs, err := s.GetObservation(oldID)
	if err != nil {
		t.Fatalf("GetObservation(old): %v", err)
	}

	newID, err := s.AddObservation(store.AddObservationParams{
		SessionID: sessionID,
		Type:      "architecture",
		Title:     "New auth design",
		Content:   "We switched to JWT auth",
		Project:   "eval-harness",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation(new): %v", err)
	}
	newObs, err := s.GetObservation(newID)
	if err != nil {
		t.Fatalf("GetObservation(new): %v", err)
	}

	relSyncID := "rel-eval-contradiction-test"
	if _, err := s.SaveRelation(store.SaveRelationParams{
		SyncID:   relSyncID,
		SourceID: newObs.SyncID,
		TargetID: oldObs.SyncID,
	}); err != nil {
		t.Fatalf("SaveRelation: %v", err)
	}
	if _, err := s.JudgeRelation(store.JudgeRelationParams{
		JudgmentID:    relSyncID,
		Relation:      store.RelationSupersedes,
		MarkedByActor: "agent:eval-harness-test",
		MarkedByKind:  "agent",
	}); err != nil {
		t.Fatalf("JudgeRelation: %v", err)
	}

	return oldObs.SyncID, newObs.SyncID
}

// TestContradiction_SupersededObservationScoresMiss is spec EVAL-4's core
// scenario: given a real store.RelationSupersedes relation linking an older
// observation to its superseding replacement, surfacing the OLDER
// observation must score a miss and surfacing the NEWER (current) one must
// score a hit.
func TestContradiction_SupersededObservationScoresMiss(t *testing.T) {
	s := newContradictionTestStore(t)
	oldSyncID, newSyncID := seedSupersedesFixture(t, s)

	c := EvalCase{
		ID:            "contra-1",
		ObservationID: newSyncID,
		Capability:    CapabilityStateUpdate,
		Query:         "how do we do auth",
		Language:      LanguageEN,
		ExpectedFact:  "We switched to JWT auth",
		SupersedesOf:  &oldSyncID,
	}

	// Retrieval surfaced the STALE (older) observation — must score a miss.
	hit, err := ScoreContradiction(s, c, oldSyncID)
	if err != nil {
		t.Fatalf("ScoreContradiction(stale surfaced): %v", err)
	}
	if hit {
		t.Error("surfacing the superseded (older) observation must score a miss")
	}

	// Retrieval surfaced the CURRENT (newer, superseding) observation — hit.
	hit, err = ScoreContradiction(s, c, newSyncID)
	if err != nil {
		t.Fatalf("ScoreContradiction(current surfaced): %v", err)
	}
	if !hit {
		t.Error("surfacing the CURRENT (superseding) observation must score a hit")
	}
}

// TestContradiction_MissingSupersedesRelationFailsFast is spec EVAL-4's
// second scenario: a case that CLAIMS a supersedes pair (via SupersedesOf)
// but has no real, judged store.RelationSupersedes relation backing it must
// fail fast rather than silently being scored as plain Recall.
func TestContradiction_MissingSupersedesRelationFailsFast(t *testing.T) {
	s := newContradictionTestStore(t)
	if err := s.CreateSession("eval-no-relation-test", "eval-harness", "/tmp/eval-harness"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	id, err := s.AddObservation(store.AddObservationParams{
		SessionID: "eval-no-relation-test",
		Type:      "architecture",
		Title:     "Standalone fact",
		Content:   "No supersedes relation backs this case",
		Project:   "eval-harness",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}

	claimedOlder := "obs-does-not-exist-as-a-relation"
	c := EvalCase{
		ID:            "contra-missing",
		ObservationID: obs.SyncID,
		Capability:    CapabilityStateUpdate,
		Query:         "irrelevant",
		Language:      LanguageEN,
		ExpectedFact:  "No supersedes relation backs this case",
		SupersedesOf:  &claimedOlder,
	}

	if _, err := ScoreContradiction(s, c, obs.SyncID); err == nil {
		t.Error("expected ScoreContradiction to fail fast when no store.RelationSupersedes relation backs the case (spec EVAL-4)")
	}
}

// TestContradiction_NoSupersedesOfFailsFast covers the non-contradiction
// input case: an EvalCase with no SupersedesOf at all is not a valid
// contradiction case and must not be silently scored.
func TestContradiction_NoSupersedesOfFailsFast(t *testing.T) {
	s := newContradictionTestStore(t)
	c := EvalCase{
		ID:            "not-a-contradiction-case",
		ObservationID: "obs-whatever",
		Capability:    CapabilityRecall,
		Query:         "irrelevant",
		Language:      LanguageEN,
		ExpectedFact:  "irrelevant",
	}

	if _, err := ScoreContradiction(s, c, "obs-whatever"); err == nil {
		t.Error("expected ScoreContradiction to fail fast for a case with no SupersedesOf")
	}
}
