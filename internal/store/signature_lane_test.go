package store

import "testing"

// ─── AddObservation: error_signature + outcome (design obs #1498 / audit #1497, slice 1) ───

func TestAddObservation_ComputesErrorSignatureForBugfixType(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	content := `TypeError: Cannot read properties of undefined (reading 'items')
    at processOrder (/Users/benja/dev/omnia/src/services/orderService.ts:47:19)`

	id, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "bugfix",
		Title:     "Fixed order service crash",
		Content:   content,
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if obs.ErrorSignature == nil || *obs.ErrorSignature == "" {
		t.Fatalf("expected non-empty error_signature for bugfix-type save, got %v", obs.ErrorSignature)
	}
	want := NormalizeErrorSignature(content)
	if *obs.ErrorSignature != want {
		t.Fatalf("error_signature = %q, want %q (derived from content)", *obs.ErrorSignature, want)
	}
}

func TestAddObservation_NonBugfixTypeGetsNoAutoErrorSignature(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Content deliberately looks error-shaped, but the type is not
	// bugfix-family, so no signature should be auto-derived.
	id, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "decision",
		Title:     "Chose to log errors instead of panicking",
		Content:   "TypeError: Cannot read properties of undefined (reading 'items')",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if obs.ErrorSignature != nil {
		t.Fatalf("expected nil error_signature for non-bugfix type, got %q", *obs.ErrorSignature)
	}
}

func TestAddObservation_ExplicitErrorSignatureOverridesAutoDerivation(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	rawErrorText := "panic: runtime error: index out of range [5] with length 3"

	// Even for a non-bugfix type, an explicit error_signature/error_text
	// should be respected — the caller is being explicit about it.
	id, err := s.AddObservation(AddObservationParams{
		SessionID:      "s1",
		Type:           "discovery",
		Title:          "Investigated a flaky test",
		Content:        "Some unrelated prose about test flakiness.",
		Project:        "engram",
		Scope:          "project",
		ErrorSignature: rawErrorText,
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	want := NormalizeErrorSignature(rawErrorText)
	if obs.ErrorSignature == nil || *obs.ErrorSignature != want {
		got := "<nil>"
		if obs.ErrorSignature != nil {
			got = *obs.ErrorSignature
		}
		t.Fatalf("error_signature = %q, want %q (explicit override)", got, want)
	}
}

func TestAddObservation_AcceptsOutcome(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	id, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "bugfix",
		Title:     "Fixed tokenizer panic",
		Content:   "Normalized tokenizer panic on edge case",
		Project:   "engram",
		Scope:     "project",
		Outcome:   "worked",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if obs.Outcome == nil || *obs.Outcome != OutcomeWorked {
		t.Fatalf("expected outcome=%q, got %v", OutcomeWorked, obs.Outcome)
	}
}

func TestAddObservation_RejectsInvalidOutcome(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "bugfix",
		Title:     "Fixed tokenizer panic",
		Content:   "Normalized tokenizer panic on edge case",
		Project:   "engram",
		Scope:     "project",
		Outcome:   "sort-of-worked",
	})
	if err == nil {
		t.Fatalf("expected error for invalid outcome value")
	}
}

func TestAddObservation_TopicKeyRevisionRefreshesSignaturePreservesOutcome(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	errA := "panic: runtime error: index out of range [5] with length 3"
	errB := "panic: runtime error: index out of range [99] with length 42 -- new occurrence with more context"

	firstID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "bugfix",
		Title:     "Fixed slice index panic",
		Content:   errA,
		Project:   "engram",
		Scope:     "project",
		TopicKey:  "bug/slice-index-panic",
		Outcome:   "worked",
	})
	if err != nil {
		t.Fatalf("AddObservation (first): %v", err)
	}

	secondID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "bugfix",
		Title:     "Fixed slice index panic",
		Content:   errB,
		Project:   "engram",
		Scope:     "project",
		TopicKey:  "bug/slice-index-panic",
		// No outcome provided on this revision — the previous "worked"
		// outcome must be preserved, not cleared.
	})
	if err != nil {
		t.Fatalf("AddObservation (revision): %v", err)
	}
	if firstID != secondID {
		t.Fatalf("expected topic_key upsert to reuse id, got %d and %d", firstID, secondID)
	}

	obs, err := s.GetObservation(secondID)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}

	wantSig := NormalizeErrorSignature(errB)
	if obs.ErrorSignature == nil || *obs.ErrorSignature != wantSig {
		t.Fatalf("expected error_signature refreshed to revision content %q, got %v", wantSig, obs.ErrorSignature)
	}
	if obs.Outcome == nil || *obs.Outcome != OutcomeWorked {
		t.Fatalf("expected outcome=%q to be preserved across topic_key revision, got %v", OutcomeWorked, obs.Outcome)
	}
}

// ─── UpdateObservation: outcome ──────────────────────────────────────────────

func TestUpdateObservation_SetsOutcome(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	id, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "bugfix",
		Title:     "Fixed tokenizer panic",
		Content:   "Normalized tokenizer panic on edge case",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	worked := OutcomeWorked
	updated, err := s.UpdateObservation(id, UpdateObservationParams{Outcome: &worked})
	if err != nil {
		t.Fatalf("UpdateObservation: %v", err)
	}
	if updated.Outcome == nil || *updated.Outcome != OutcomeWorked {
		t.Fatalf("expected outcome=%q after update, got %v", OutcomeWorked, updated.Outcome)
	}

	// Re-fetch to make sure it persisted, not just returned in-memory.
	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if obs.Outcome == nil || *obs.Outcome != OutcomeWorked {
		t.Fatalf("expected persisted outcome=%q, got %v", OutcomeWorked, obs.Outcome)
	}
}

func TestUpdateObservation_RejectsInvalidOutcome(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	id, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "bugfix",
		Title:     "Fixed tokenizer panic",
		Content:   "Normalized tokenizer panic on edge case",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	garbage := "totally-not-a-real-outcome"
	if _, err := s.UpdateObservation(id, UpdateObservationParams{Outcome: &garbage}); err == nil {
		t.Fatalf("expected error for invalid outcome value on update")
	}
}
