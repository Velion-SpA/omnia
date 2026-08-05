package store

import (
	"math"
	"testing"
)

// TestAddObservation_PersistsSalience (Umbral bridge, Tanda T3): an explicit
// Salience persists on a new observation; an omitted one reads back nil, not
// a manufactured 0 — 0 is itself a meaningful value.
func TestAddObservation_PersistsSalience(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("sal-sess", "sal-proj", "/tmp/sal"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	v := 0.83
	id, err := s.AddObservation(AddObservationParams{
		SessionID: "sal-sess", Type: "manual", Title: "with salience",
		Content: "content", Project: "sal-proj", Salience: &v,
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if obs.Salience == nil || *obs.Salience != 0.83 {
		t.Fatalf("Salience = %v, want 0.83", obs.Salience)
	}

	id2, err := s.AddObservation(AddObservationParams{
		SessionID: "sal-sess", Type: "manual", Title: "no salience",
		Content: "content2", Project: "sal-proj",
	})
	if err != nil {
		t.Fatalf("AddObservation (no salience): %v", err)
	}
	obs2, err := s.GetObservation(id2)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if obs2.Salience != nil {
		t.Fatalf("Salience = %v, want nil for an omitted input", obs2.Salience)
	}
}

// TestAddObservation_RejectsSalienceOutOfRange: out-of-[0,1] values must be
// rejected, not clamped — silently clamping would let unit-confusion (e.g. a
// raw percentage, 83 instead of 0.83) pass as a wildly wrong ranking signal.
func TestAddObservation_RejectsSalienceOutOfRange(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("sal-bad", "sal-proj-bad", "/tmp/sal-bad"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	for _, bad := range []float64{-0.01, 1.01, 83, 1.0000001} {
		v := bad
		if _, err := s.AddObservation(AddObservationParams{
			SessionID: "sal-bad", Type: "manual", Title: "bad salience",
			Content: "content", Project: "sal-proj-bad", Salience: &v,
		}); err == nil {
			t.Errorf("AddObservation with Salience=%v: expected error, got nil", bad)
		}
	}
}

// TestNormalizeSalience_RejectsNaN is a regression test for the bug found in
// adversarial review of PR #259: IEEE-754 comparisons against NaN are always
// false, so a naive `raw < min || raw > max` range check lets NaN through as
// "in range" — normalizeSalience returned (NaN, nil) with no error before
// this fix. This must fail loudly, not silently: today the write path
// happens to be saved by sqlite3_bind_double rebinding NaN to NULL in
// SQLite's C implementation, but a data-integrity guarantee must not rest on
// an accident of a lower layer.
func TestNormalizeSalience_RejectsNaN(t *testing.T) {
	nan := math.NaN()
	got, err := normalizeSalience(&nan)
	if err == nil {
		t.Fatalf("normalizeSalience(NaN) = (%v, nil), want a non-nil error", got)
	}
}

// TestNormalizeSalience_RejectsInfinities covers +Inf/-Inf explicitly. Unlike
// NaN these are already caught by the existing `raw < min || raw > max`
// comparisons (Inf > salienceMax, -Inf < salienceMin), but the review asked
// for explicit coverage since they are the other IEEE-754 non-finite values.
func TestNormalizeSalience_RejectsInfinities(t *testing.T) {
	for _, bad := range []float64{math.Inf(1), math.Inf(-1)} {
		v := bad
		if got, err := normalizeSalience(&v); err == nil {
			t.Errorf("normalizeSalience(%v) = (%v, nil), want a non-nil error", bad, got)
		}
	}
}

// TestNormalizeSalience_AcceptsNegativeZero: -0.0 == 0.0 under IEEE-754, so
// it is a legitimate boundary value (the minimum of [0,1]), not a value to
// reject. This pins that -0.0 is accepted, not silently rejected by some
// future bitwise-equality change.
func TestNormalizeSalience_AcceptsNegativeZero(t *testing.T) {
	negZero := math.Copysign(0, -1)
	got, err := normalizeSalience(&negZero)
	if err != nil {
		t.Fatalf("normalizeSalience(-0.0): unexpected error %v", err)
	}
	if got == nil || *got != 0 {
		t.Fatalf("normalizeSalience(-0.0) = %v, want a pointer to 0", got)
	}
}

// TestSaveObservation_TopicKeyRevisionPreservesSalienceWhenAbsent mirrors
// TestAddObservation_TopicKeyRevision_PreservesSourceWhenAbsent: a topic_key
// revision that omits Salience preserves the prior value (SQL
// COALESCE(?, salience) — the nil-pointer equivalent of error_signature/
// outcome/source's COALESCE(NULLIF(?, ”), col)); one that supplies a new
// value overwrites it.
func TestSaveObservation_TopicKeyRevisionPreservesSalienceWhenAbsent(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("sal-rev", "sal-rev-proj", "/tmp/sal-rev"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	v1 := 0.2
	id, err := s.AddObservation(AddObservationParams{
		SessionID: "sal-rev", Type: "decision", Title: "v1",
		Content: "content v1", Project: "sal-rev-proj", TopicKey: "salience/revision-test", Salience: &v1,
	})
	if err != nil {
		t.Fatalf("AddObservation (initial): %v", err)
	}

	revisedID, err := s.AddObservation(AddObservationParams{
		SessionID: "sal-rev", Type: "decision", Title: "v2",
		Content: "content v2", Project: "sal-rev-proj", TopicKey: "salience/revision-test",
	})
	if err != nil || revisedID != id {
		t.Fatalf("AddObservation (revision, no salience): id=%d err=%v, want id=%d nil", revisedID, err, id)
	}
	if obs, err := s.GetObservation(id); err != nil || obs.Salience == nil || *obs.Salience != 0.2 {
		t.Fatalf("Salience after revision (no new value) = %+v err=%v, want preserved 0.2", obs, err)
	}

	v3 := 0.9
	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "sal-rev", Type: "decision", Title: "v3",
		Content: "content v3", Project: "sal-rev-proj", TopicKey: "salience/revision-test", Salience: &v3,
	}); err != nil {
		t.Fatalf("AddObservation (revision, new salience): %v", err)
	}
	if obs, err := s.GetObservation(id); err != nil || obs.Salience == nil || *obs.Salience != 0.9 {
		t.Fatalf("Salience after overwriting revision = %+v err=%v, want 0.9", obs, err)
	}
}
