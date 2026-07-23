package store

import (
	"strings"
	"testing"
	"time"
)

// ─── Phase 1 — Store Foundation ───────────────────────────────────────────────

// validProcedure returns a Procedure with every required field populated —
// tests mutate a copy of this to isolate the single field under test.
func validProcedure() Procedure {
	return Procedure{
		Project:  "testproject",
		Polarity: ProcedurePolarityPlaybook,
		Trigger:  "nil pointer dereference in handleRequest",
		Steps: []ProcedureStep{
			{Order: 1, Template: "check {{var}} for nil before dereferencing", Slots: []string{"var"}},
			{Order: 2, Template: "add an early return guard"},
		},
		ExpectedOutcome:   "handler no longer panics",
		PostconditionKind: PostconditionTestsPass,
		Confidence:        0.5,
		SourceObsSyncIDs:  []string{"obs-aaa111"},
	}
}

// TestUpsertProcedure_RejectsUnknownPolarity (task 1.1) verifies UpsertProcedure
// rejects a polarity outside {playbook, anti_playbook}.
func TestUpsertProcedure_RejectsUnknownPolarity(t *testing.T) {
	s := newTestStore(t)

	p := validProcedure()
	p.Polarity = "sideways"

	_, err := s.UpsertProcedure(p)
	if err == nil {
		t.Fatal("UpsertProcedure: expected error for unknown polarity, got nil")
	}
	if !strings.Contains(err.Error(), "polarity") {
		t.Errorf("UpsertProcedure: error should mention polarity; got %q", err.Error())
	}
}

// TestUpsertProcedure_RejectsUnknownPostconditionKind (task 1.1) verifies
// UpsertProcedure rejects a postcondition_kind outside the locked vocabulary.
func TestUpsertProcedure_RejectsUnknownPostconditionKind(t *testing.T) {
	s := newTestStore(t)

	p := validProcedure()
	p.PostconditionKind = "vibes_based"

	_, err := s.UpsertProcedure(p)
	if err == nil {
		t.Fatal("UpsertProcedure: expected error for unknown postcondition_kind, got nil")
	}
	if !strings.Contains(err.Error(), "postcondition_kind") {
		t.Errorf("UpsertProcedure: error should mention postcondition_kind; got %q", err.Error())
	}
}

// TestUpsertProcedure_RejectsEmptyTrigger guards the "trigger is required"
// validation path (empty trigger is neither a valid playbook nor a valid
// anti_playbook — there is nothing to match against at retrieval time).
func TestUpsertProcedure_RejectsEmptyTrigger(t *testing.T) {
	s := newTestStore(t)

	p := validProcedure()
	p.Trigger = "   "

	_, err := s.UpsertProcedure(p)
	if err == nil {
		t.Fatal("UpsertProcedure: expected error for empty trigger, got nil")
	}
}

// TestUpsertProcedure_ValidRecord_IsSearchableByTrigger (tasks 1.5/1.6)
// verifies a valid procedure is stored with a sync_id and can be found via
// SearchProcedures by a trigger-word match.
func TestUpsertProcedure_ValidRecord_IsSearchableByTrigger(t *testing.T) {
	s := newTestStore(t)

	syncID, err := s.UpsertProcedure(validProcedure())
	if err != nil {
		t.Fatalf("UpsertProcedure: unexpected error: %v", err)
	}
	if !strings.HasPrefix(syncID, "proc-") {
		t.Errorf("UpsertProcedure: sync_id should start with 'proc-'; got %q", syncID)
	}

	results, err := s.SearchProcedures("nil pointer dereference", "", "", 10)
	if err != nil {
		t.Fatalf("SearchProcedures: unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("SearchProcedures: want 1 result; got %d", len(results))
	}
	if results[0].SyncID != syncID {
		t.Errorf("SearchProcedures: sync_id = %q; want %q", results[0].SyncID, syncID)
	}
	if results[0].Trigger != "nil pointer dereference in handleRequest" {
		t.Errorf("SearchProcedures: trigger = %q; unexpected", results[0].Trigger)
	}
	if len(results[0].Steps) != 2 {
		t.Fatalf("SearchProcedures: want 2 steps; got %d", len(results[0].Steps))
	}
	if results[0].Steps[0].Template != "check {{var}} for nil before dereferencing" {
		t.Errorf("SearchProcedures: step[0].Template unexpected: %q", results[0].Steps[0].Template)
	}
	if results[0].State != ProcedureStateCandidate {
		t.Errorf("SearchProcedures: state = %q; want %q (default)", results[0].State, ProcedureStateCandidate)
	}
}

// TestSearchProcedures_FiltersByPolarityAndState triangulates
// SearchProcedures with a second, DIFFERENT setup: two procedures sharing an
// FTS-matchable word but differing polarity/state, proving the filters
// actually narrow the result set (not just the MATCH clause).
func TestSearchProcedures_FiltersByPolarityAndState(t *testing.T) {
	s := newTestStore(t)

	playbook := validProcedure()
	playbook.Trigger = "flaky test retry loop"
	if _, err := s.UpsertProcedure(playbook); err != nil {
		t.Fatalf("UpsertProcedure(playbook): %v", err)
	}

	antiPlaybook := validProcedure()
	antiPlaybook.Polarity = ProcedurePolarityAntiPlaybook
	antiPlaybook.Trigger = "flaky test retry with sleep(5)"
	antiPlaybook.State = ProcedureStateTrusted
	if _, err := s.UpsertProcedure(antiPlaybook); err != nil {
		t.Fatalf("UpsertProcedure(antiPlaybook): %v", err)
	}

	got, err := s.SearchProcedures("flaky test retry", ProcedurePolarityAntiPlaybook, ProcedureStateTrusted, 10)
	if err != nil {
		t.Fatalf("SearchProcedures: unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("SearchProcedures: want 1 filtered result; got %d", len(got))
	}
	if got[0].Polarity != ProcedurePolarityAntiPlaybook {
		t.Errorf("SearchProcedures: polarity = %q; want %q", got[0].Polarity, ProcedurePolarityAntiPlaybook)
	}
}

// TestUpsertProcedure_ConflictPreservesGovernanceState verifies that
// re-upserting an existing sync_id (e.g. re-running induction over the same
// cluster) refreshes content but does NOT reset governance-owned fields
// (state, reuse_confirmed) — those are owned exclusively by
// ConfirmReuse/Contradict/RetireProcedure.
func TestUpsertProcedure_ConflictPreservesGovernanceState(t *testing.T) {
	s := newTestStore(t)

	p := validProcedure()
	syncID, err := s.UpsertProcedure(p)
	if err != nil {
		t.Fatalf("UpsertProcedure (initial): %v", err)
	}
	if _, err := s.ConfirmReuse(syncID, "engram"); err != nil {
		t.Fatalf("ConfirmReuse: %v", err)
	}

	// Re-upsert same sync_id with different content.
	p.SyncID = syncID
	p.ExpectedOutcome = "updated expected outcome"
	if _, err := s.UpsertProcedure(p); err != nil {
		t.Fatalf("UpsertProcedure (re-apply): %v", err)
	}

	got, err := s.GetProcedure(syncID)
	if err != nil {
		t.Fatalf("GetProcedure: %v", err)
	}
	if got.ReuseConfirmed != 1 {
		t.Errorf("re-upsert must preserve reuse_confirmed; got %d, want 1", got.ReuseConfirmed)
	}
	if got.ExpectedOutcome != "updated expected outcome" {
		t.Errorf("re-upsert must refresh content; expected_outcome = %q", got.ExpectedOutcome)
	}
}

// TestGetProcedure_NotFound covers the "get" half of the CRUD surface.
func TestGetProcedure_NotFound(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.GetProcedure("proc-does-not-exist"); err == nil {
		t.Fatal("GetProcedure: expected error for unknown sync_id, got nil")
	}
}

// TestListProcedures_FiltersByProjectAndPolarity covers the "list" half of
// the CRUD surface with two different projects to prove filtering (not just
// a "return everything" stub).
func TestListProcedures_FiltersByProjectAndPolarity(t *testing.T) {
	s := newTestStore(t)

	a := validProcedure()
	a.Project = "projecta"
	if _, err := s.UpsertProcedure(a); err != nil {
		t.Fatalf("UpsertProcedure(a): %v", err)
	}

	b := validProcedure()
	b.Project = "projectb"
	b.Polarity = ProcedurePolarityAntiPlaybook
	if _, err := s.UpsertProcedure(b); err != nil {
		t.Fatalf("UpsertProcedure(b): %v", err)
	}

	got, err := s.ListProcedures(ListProceduresOptions{Project: "projecta"})
	if err != nil {
		t.Fatalf("ListProcedures: unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListProcedures: want 1 result for projecta; got %d", len(got))
	}
	if got[0].Project != "projecta" {
		t.Errorf("ListProcedures: project = %q; want %q", got[0].Project, "projecta")
	}
}

// ─── Phase 2 — Governance Gate (SSGM) ─────────────────────────────────────────

// TestConfirmReuse_PromotesToTrustedAtThreshold (task 2.1) verifies a third
// confirmed reuse (reuse_confirmed 2→3, threshold=3) promotes candidate→trusted.
func TestConfirmReuse_PromotesToTrustedAtThreshold(t *testing.T) {
	s := newTestStore(t)

	syncID, err := s.UpsertProcedure(validProcedure())
	if err != nil {
		t.Fatalf("UpsertProcedure: %v", err)
	}

	// First two confirmations: still candidate.
	for i := 0; i < 2; i++ {
		p, err := s.ConfirmReuse(syncID, "engram")
		if err != nil {
			t.Fatalf("ConfirmReuse (#%d): %v", i+1, err)
		}
		if p.State != ProcedureStateCandidate {
			t.Fatalf("ConfirmReuse (#%d): state = %q; want %q (not yet at threshold)", i+1, p.State, ProcedureStateCandidate)
		}
	}

	// Third confirmation crosses the default trust_threshold (3).
	p, err := s.ConfirmReuse(syncID, "engram")
	if err != nil {
		t.Fatalf("ConfirmReuse (#3): %v", err)
	}
	if p.ReuseConfirmed != 3 {
		t.Errorf("ConfirmReuse (#3): reuse_confirmed = %d; want 3", p.ReuseConfirmed)
	}
	if p.State != ProcedureStateTrusted {
		t.Errorf("ConfirmReuse (#3): state = %q; want %q", p.State, ProcedureStateTrusted)
	}
	if p.ReviewAfter == nil {
		t.Error("ConfirmReuse (#3): review_after must be set once trusted")
	}
}

// TestConfirmReuse_RequiresActor triangulates the validation path: an empty
// actor must be rejected (governance writes always carry provenance).
func TestConfirmReuse_RequiresActor(t *testing.T) {
	s := newTestStore(t)
	syncID, err := s.UpsertProcedure(validProcedure())
	if err != nil {
		t.Fatalf("UpsertProcedure: %v", err)
	}

	if _, err := s.ConfirmReuse(syncID, ""); err == nil {
		t.Fatal("ConfirmReuse: expected error for empty actor, got nil")
	}
}

// TestContradict_RetiresAtFloor_Idempotent (task 2.3) verifies enough
// contradictions push confidence to the floor and retire the procedure
// (without deleting it), and that re-contradicting an already-retired
// procedure is a safe, idempotent no-op that keeps it retired.
func TestContradict_RetiresAtFloor_Idempotent(t *testing.T) {
	s := newTestStore(t)

	p := validProcedure()
	p.Confidence = 0.3 // one decay step (0.20) above the default floor (0.15)
	syncID, err := s.UpsertProcedure(p)
	if err != nil {
		t.Fatalf("UpsertProcedure: %v", err)
	}

	got, err := s.Contradict(syncID, "engram")
	if err != nil {
		t.Fatalf("Contradict: %v", err)
	}
	if got.ContradictedCount != 1 {
		t.Errorf("Contradict: contradicted_count = %d; want 1", got.ContradictedCount)
	}
	if got.State != ProcedureStateRetired {
		t.Fatalf("Contradict: state = %q; want %q at/below confidence floor", got.State, ProcedureStateRetired)
	}
	if got.RetiredAt == nil {
		t.Error("Contradict: retired_at must be set once retired")
	}

	// Row must still be queryable — never hard-deleted.
	fetched, err := s.GetProcedure(syncID)
	if err != nil {
		t.Fatalf("GetProcedure after retire: %v", err)
	}
	if fetched.State != ProcedureStateRetired {
		t.Errorf("GetProcedure after retire: state = %q; want %q", fetched.State, ProcedureStateRetired)
	}

	// Idempotent re-contradict: still retired, count keeps incrementing, no error.
	again, err := s.Contradict(syncID, "engram")
	if err != nil {
		t.Fatalf("Contradict (re-retire): unexpected error: %v", err)
	}
	if again.State != ProcedureStateRetired {
		t.Errorf("Contradict (re-retire): state = %q; want %q (must not un-retire)", again.State, ProcedureStateRetired)
	}
	if again.ContradictedCount != 2 {
		t.Errorf("Contradict (re-retire): contradicted_count = %d; want 2", again.ContradictedCount)
	}
}

// TestContradict_AboveFloorStaysCandidate triangulates Contradict with a
// DIFFERENT starting confidence: high enough that one decay step does not
// cross the floor, so the procedure must remain non-retired.
func TestContradict_AboveFloorStaysCandidate(t *testing.T) {
	s := newTestStore(t)

	p := validProcedure()
	p.Confidence = 0.9
	syncID, err := s.UpsertProcedure(p)
	if err != nil {
		t.Fatalf("UpsertProcedure: %v", err)
	}

	got, err := s.Contradict(syncID, "engram")
	if err != nil {
		t.Fatalf("Contradict: %v", err)
	}
	if got.State != ProcedureStateCandidate {
		t.Errorf("Contradict: state = %q; want %q (well above floor)", got.State, ProcedureStateCandidate)
	}
	wantConfidence := 0.9 - contradictDecay
	if diff := got.Confidence - wantConfidence; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("Contradict: confidence = %v; want %v", got.Confidence, wantConfidence)
	}
}

// TestDecayProcedures_FlagsUnusedTrustedProcedure (task 2.5) verifies a
// trusted procedure whose review_after deadline is already in the past gets
// picked up and decayed by DecayProcedures.
func TestDecayProcedures_FlagsUnusedTrustedProcedure(t *testing.T) {
	s := newTestStore(t)

	p := validProcedure()
	p.State = ProcedureStateTrusted
	p.Confidence = 0.8
	syncID, err := s.UpsertProcedure(p)
	if err != nil {
		t.Fatalf("UpsertProcedure: %v", err)
	}

	// Force review_after into the past directly (bypassing ConfirmReuse,
	// which would push it into the future).
	past := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02 15:04:05")
	if _, err := s.db.Exec(`UPDATE procedures SET review_after = ? WHERE sync_id = ?`, past, syncID); err != nil {
		t.Fatalf("seed past review_after: %v", err)
	}

	decayed, err := s.DecayProcedures()
	if err != nil {
		t.Fatalf("DecayProcedures: unexpected error: %v", err)
	}
	if len(decayed) != 1 {
		t.Fatalf("DecayProcedures: want 1 flagged procedure; got %d", len(decayed))
	}
	if decayed[0].SyncID != syncID {
		t.Errorf("DecayProcedures: flagged sync_id = %q; want %q", decayed[0].SyncID, syncID)
	}
	wantConfidence := 0.8 - decayStepConfidence
	if diff := decayed[0].Confidence - wantConfidence; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("DecayProcedures: confidence = %v; want %v", decayed[0].Confidence, wantConfidence)
	}
	if decayed[0].State != ProcedureStateTrusted {
		t.Errorf("DecayProcedures: state = %q; want still %q (above floor)", decayed[0].State, ProcedureStateTrusted)
	}
}

// TestDecayProcedures_IgnoresProceduresNotYetDue triangulates with a
// DIFFERENT setup: a trusted procedure whose review_after is still in the
// future must NOT be flagged.
func TestDecayProcedures_IgnoresProceduresNotYetDue(t *testing.T) {
	s := newTestStore(t)

	p := validProcedure()
	syncID, err := s.UpsertProcedure(p)
	if err != nil {
		t.Fatalf("UpsertProcedure: %v", err)
	}
	// Promote to trusted via 3 confirmed reuses — review_after lands in the future.
	for i := 0; i < 3; i++ {
		if _, err := s.ConfirmReuse(syncID, "engram"); err != nil {
			t.Fatalf("ConfirmReuse: %v", err)
		}
	}

	decayed, err := s.DecayProcedures()
	if err != nil {
		t.Fatalf("DecayProcedures: unexpected error: %v", err)
	}
	if len(decayed) != 0 {
		t.Fatalf("DecayProcedures: want 0 flagged (not yet due); got %d", len(decayed))
	}
}

// TestDecayProcedures_AutoRetiresBelowFloor triangulates decay's OTHER
// branch: a trusted procedure already close to the floor, past due, must
// auto-retire instead of just decaying in place.
func TestDecayProcedures_AutoRetiresBelowFloor(t *testing.T) {
	s := newTestStore(t)

	p := validProcedure()
	p.State = ProcedureStateTrusted
	p.Confidence = 0.20 // one decay step (0.10) drops this to 0.10, at/below the 0.15 floor
	syncID, err := s.UpsertProcedure(p)
	if err != nil {
		t.Fatalf("UpsertProcedure: %v", err)
	}
	past := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02 15:04:05")
	if _, err := s.db.Exec(`UPDATE procedures SET review_after = ? WHERE sync_id = ?`, past, syncID); err != nil {
		t.Fatalf("seed past review_after: %v", err)
	}

	decayed, err := s.DecayProcedures()
	if err != nil {
		t.Fatalf("DecayProcedures: unexpected error: %v", err)
	}
	if len(decayed) != 1 {
		t.Fatalf("DecayProcedures: want 1 flagged procedure; got %d", len(decayed))
	}
	if decayed[0].State != ProcedureStateRetired {
		t.Errorf("DecayProcedures: state = %q; want %q at/below floor", decayed[0].State, ProcedureStateRetired)
	}
}

// TestRetireProcedure_NeverDeletesRow covers RetireProcedure directly
// (design.md: "never hard-deletes").
func TestRetireProcedure_NeverDeletesRow(t *testing.T) {
	s := newTestStore(t)

	syncID, err := s.UpsertProcedure(validProcedure())
	if err != nil {
		t.Fatalf("UpsertProcedure: %v", err)
	}

	if _, err := s.RetireProcedure(syncID); err != nil {
		t.Fatalf("RetireProcedure: %v", err)
	}

	got, err := s.GetProcedure(syncID)
	if err != nil {
		t.Fatalf("GetProcedure after retire: %v", err)
	}
	if got.State != ProcedureStateRetired {
		t.Errorf("RetireProcedure: state = %q; want %q", got.State, ProcedureStateRetired)
	}

	// Idempotent: retiring again does not error and stays retired.
	if _, err := s.RetireProcedure(syncID); err != nil {
		t.Fatalf("RetireProcedure (again): unexpected error: %v", err)
	}
}
