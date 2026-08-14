package eval

import (
	"context"
	"reflect"
	"testing"
)

// deterministicConversationalRun returns a ConversationalRunFunc that scores
// a small, FIXED set of cases with no randomness, no wall-clock reads, and
// no map-iteration-dependent output — every call must produce byte-for-byte
// the same ConversationalReport.
func deterministicConversationalRun() ConversationalRunFunc {
	cases := []ConversationalCase{
		{ID: "s1", Kind: KindStatus, Query: "q1", Language: LanguageEN, ObservationID: "obs-1", ExpectedFact: "fact"},
		{ID: "s2", Kind: KindStatus, Query: "q2", Language: LanguageES, ObservationID: "obs-2", ExpectedFact: "fact"},
		{ID: "i1", Kind: KindIdentity, Query: "q3", Language: LanguageEN, ExpectedFact: "Persistent memory"},
		{ID: "a1", Kind: KindAbsence, Query: "q4", Language: LanguageEN, ExpectAbsence: true},
		{ID: "a2", Kind: KindAbsence, Query: "q5", Language: LanguageES, ExpectAbsence: true},
	}
	fetch := func(ctx context.Context, c ConversationalCase) (RetrievedCase, error) {
		switch c.ID {
		case "s1":
			return RetrievedCase{Retrieved: "fact", RankedObservationIDs: []string{"obs-1", "obs-x"}}, nil
		case "s2":
			return RetrievedCase{Retrieved: "fact", RankedObservationIDs: []string{"obs-x", "obs-2"}}, nil
		case "i1":
			return RetrievedCase{Retrieved: "Persistent memory for AI coding agents"}, nil
		case "a1":
			return RetrievedCase{}, nil // honest refusal
		default: // a2
			return RetrievedCase{Retrieved: "confident wrong answer"}, nil // false confidence
		}
	}
	return func(ctx context.Context) (ConversationalReport, error) {
		return RunOnceConversational(ctx, cases, fetch)
	}
}

// TestRunConversationalHarness_Deterministic is the baseline item's hard
// determinism requirement: "the existing harness reports ±0.000 across
// repeated runs. Yours must too." Two independent RunConversationalHarness
// calls over the SAME deterministic run function must produce field-for-
// field IDENTICAL summaries — not just equal means, but zero StdDev too,
// since nothing in the pipeline reads wall-clock time, randomness, or
// unordered map iteration in a way that could leak into the numbers.
func TestRunConversationalHarness_Deterministic(t *testing.T) {
	first, err := RunConversationalHarness(context.Background(), deterministicConversationalRun(), MinRuns)
	if err != nil {
		t.Fatalf("RunConversationalHarness (first): %v", err)
	}
	second, err := RunConversationalHarness(context.Background(), deterministicConversationalRun(), MinRuns)
	if err != nil {
		t.Fatalf("RunConversationalHarness (second): %v", err)
	}

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("two independent runs over the same fixed corpus/fetcher must be byte-for-byte identical:\nfirst:  %+v\nsecond: %+v", first, second)
	}

	for _, k := range AllQuestionKinds {
		stats := first.ByKind[k]
		if stats.AccuracyAt1.StdDev != 0 {
			t.Errorf("kind %q AccuracyAt1.StdDev = %v, want 0 (deterministic fetcher, no run-to-run variance)", k, stats.AccuracyAt1.StdDev)
		}
		if stats.MRR.StdDev != 0 {
			t.Errorf("kind %q MRR.StdDev = %v, want 0", k, stats.MRR.StdDev)
		}
		if stats.GroundingRate.StdDev != 0 {
			t.Errorf("kind %q GroundingRate.StdDev = %v, want 0", k, stats.GroundingRate.StdDev)
		}
		if stats.HonestRefusalRate.StdDev != 0 {
			t.Errorf("kind %q HonestRefusalRate.StdDev = %v, want 0", k, stats.HonestRefusalRate.StdDev)
		}
	}
}

// TestRunConversationalHarness_ComputesExpectedRates cross-checks the
// aggregated summary against hand-computed values for the fixed fixture
// above, so the determinism test's "identical" guarantee isn't vacuously
// passing on a corpus that scores all zeros.
func TestRunConversationalHarness_ComputesExpectedRates(t *testing.T) {
	summary, err := RunConversationalHarness(context.Background(), deterministicConversationalRun(), MinRuns)
	if err != nil {
		t.Fatalf("RunConversationalHarness: %v", err)
	}
	if summary.Runs != MinRuns {
		t.Fatalf("Runs: want %d, got %d", MinRuns, summary.Runs)
	}

	status := summary.ByKind[KindStatus]
	if status.AccuracyAt1.Mean != 0.5 {
		t.Errorf("status AccuracyAt1.Mean: want 0.5 (s1 ranks the gold ID first, s2 ranks it second), got %v", status.AccuracyAt1.Mean)
	}
	if status.MRR.Mean != 0.75 {
		t.Errorf("status MRR.Mean: want 0.75 ((1 + 1/2)/2), got %v", status.MRR.Mean)
	}

	identity := summary.ByKind[KindIdentity]
	if identity.GroundingRate.Mean != 1.0 {
		t.Errorf("identity GroundingRate.Mean: want 1.0, got %v", identity.GroundingRate.Mean)
	}

	absence := summary.ByKind[KindAbsence]
	if absence.HonestRefusalRate.Mean != 0.5 {
		t.Errorf("absence HonestRefusalRate.Mean: want 0.5 (a1 refuses, a2 does not), got %v", absence.HonestRefusalRate.Mean)
	}
}

func TestRunConversationalHarness_RejectsOutOfBoundsRuns(t *testing.T) {
	for _, runs := range []int{0, 1, 2, MaxRuns + 1, 100} {
		runs := runs
		if _, err := RunConversationalHarness(context.Background(), deterministicConversationalRun(), runs); err == nil {
			t.Errorf("RunConversationalHarness(runs=%d): expected error, got nil", runs)
		}
	}
}

func TestRunConversationalHarness_RejectsNilRunFunc(t *testing.T) {
	if _, err := RunConversationalHarness(context.Background(), nil, MinRuns); err == nil {
		t.Error("expected an error for a nil run func, got nil")
	}
}

func TestRunConversationalHarness_PropagatesRunError(t *testing.T) {
	run := func(ctx context.Context) (ConversationalReport, error) {
		return ConversationalReport{}, context.DeadlineExceeded
	}
	if _, err := RunConversationalHarness(context.Background(), run, MinRuns); err == nil {
		t.Error("expected the run function's error to propagate, got nil")
	}
}
