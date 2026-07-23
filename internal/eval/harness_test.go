package eval

import (
	"context"
	"errors"
	"testing"
)

// TestRunOnce_ScoresJudgeFreeAndContradictionCases exercises RunOnce's full
// dispatch: a plain Recall case goes through Score() (judge-free substring
// match), and a contradiction case (SupersedesOf set) goes through
// ScoreContradiction against a REAL judged store.RelationSupersedes relation
// — reusing contradiction_test.go's self-contained temp-store fixture rather
// than a mock, per spec EVAL-4's own fixture requirement.
func TestRunOnce_ScoresJudgeFreeAndContradictionCases(t *testing.T) {
	s := newContradictionTestStore(t)
	oldSyncID, newSyncID := seedSupersedesFixture(t, s)

	recallCase := EvalCase{
		ID:            "recall-1",
		ObservationID: "obs-recall-1",
		Capability:    CapabilityRecall,
		Query:         "what do we use for auth",
		Language:      LanguageEN,
		ExpectedFact:  "JWT auth",
	}
	contradictionCase := EvalCase{
		ID:            "contra-1",
		ObservationID: newSyncID,
		Capability:    CapabilityStateUpdate,
		Query:         "how do we do auth",
		Language:      LanguageEN,
		ExpectedFact:  "We switched to JWT auth",
		SupersedesOf:  &oldSyncID,
	}

	fetch := func(ctx context.Context, c EvalCase) (RetrievedCase, error) {
		switch c.ID {
		case "recall-1":
			return RetrievedCase{Retrieved: "we use JWT auth now", Tokens: TokenBreakdown{Retrieval: 10}}, nil
		case "contra-1":
			// Retrieval surfaced the CURRENT (superseding) observation -> hit.
			return RetrievedCase{SurfacedObservationID: newSyncID, Tokens: TokenBreakdown{Retrieval: 10}}, nil
		default:
			t.Fatalf("unexpected case %q", c.ID)
			return RetrievedCase{}, nil
		}
	}

	report, err := RunOnce(context.Background(), []EvalCase{recallCase, contradictionCase}, fetch, nil, s)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if seg := report.ByCapability[CapabilityRecall]; seg.Total != 1 || seg.Hits != 1 {
		t.Errorf("Recall segment = %+v, want Total=1 Hits=1", seg)
	}
	if seg := report.ByCapability[CapabilityStateUpdate]; seg.Total != 1 || seg.Hits != 1 {
		t.Errorf("StateUpdate segment = %+v, want Total=1 Hits=1", seg)
	}
}

// TestRunOnce_ContradictionSurfacingStaleScoresMiss confirms RunOnce routes
// contradiction cases through ScoreContradiction's stale-surfaced-is-a-miss
// rule end to end.
func TestRunOnce_ContradictionSurfacingStaleScoresMiss(t *testing.T) {
	s := newContradictionTestStore(t)
	oldSyncID, newSyncID := seedSupersedesFixture(t, s)

	c := EvalCase{
		ID:            "contra-stale",
		ObservationID: newSyncID,
		Capability:    CapabilityStateUpdate,
		Query:         "how do we do auth",
		Language:      LanguageEN,
		ExpectedFact:  "We switched to JWT auth",
		SupersedesOf:  &oldSyncID,
	}
	fetch := func(ctx context.Context, ec EvalCase) (RetrievedCase, error) {
		return RetrievedCase{SurfacedObservationID: oldSyncID}, nil // stale surfaced
	}

	report, err := RunOnce(context.Background(), []EvalCase{c}, fetch, nil, s)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if seg := report.ByCapability[CapabilityStateUpdate]; seg.Hits != 0 {
		t.Errorf("expected a miss when the stale observation is surfaced, got Hits=%d", seg.Hits)
	}
}

// TestRunOnce_FetchErrorPropagates ensures a retrieval failure aborts the
// pass rather than silently scoring the case as a miss.
func TestRunOnce_FetchErrorPropagates(t *testing.T) {
	c := EvalCase{ID: "x", Capability: CapabilityRecall, Language: LanguageEN, ExpectedFact: "y"}
	fetch := func(ctx context.Context, ec EvalCase) (RetrievedCase, error) {
		return RetrievedCase{}, errBoom
	}
	if _, err := RunOnce(context.Background(), []EvalCase{c}, fetch, nil, nil); err == nil {
		t.Fatal("expected RunOnce to propagate a fetch error")
	}
}

// TestRunOnce_ContradictionWithoutRelationsGetterErrors ensures a
// contradiction case can never silently skip the RelationsGetter check.
func TestRunOnce_ContradictionWithoutRelationsGetterErrors(t *testing.T) {
	older := "obs-old"
	c := EvalCase{ID: "contra", ObservationID: "obs-new", Capability: CapabilityStateUpdate, Language: LanguageEN, ExpectedFact: "y", SupersedesOf: &older}
	fetch := func(ctx context.Context, ec EvalCase) (RetrievedCase, error) { return RetrievedCase{}, nil }
	if _, err := RunOnce(context.Background(), []EvalCase{c}, fetch, nil, nil); err == nil {
		t.Fatal("expected RunOnce to error when a contradiction case has no RelationsGetter")
	}
}

// TestRunOnce_NilFetchErrors guards the RunOnce contract itself.
func TestRunOnce_NilFetchErrors(t *testing.T) {
	if _, err := RunOnce(context.Background(), nil, nil, nil, nil); err == nil {
		t.Fatal("expected RunOnce to error on a nil fetch")
	}
}

var errBoom = errors.New("boom")
