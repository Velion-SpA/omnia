package eval

import (
	"context"
	"math"
	"testing"
)

func TestScoreConversationalCase_IdentityComputesGrounded(t *testing.T) {
	c := ConversationalCase{ID: "i1", Kind: KindIdentity, ExpectedFact: "Persistent memory for AI coding agents"}

	hit, err := ScoreConversationalCase(c, RetrievedCase{Retrieved: "Omnia: Persistent memory for AI coding agents, local-first."})
	if err != nil {
		t.Fatalf("ScoreConversationalCase: %v", err)
	}
	if hit.Grounded == nil || !*hit.Grounded {
		t.Fatalf("expected Grounded=true when the retrieved text contains the expected fact, got %+v", hit.Grounded)
	}

	miss, err := ScoreConversationalCase(c, RetrievedCase{Retrieved: "Omnia is a CLI tool for GitLab."})
	if err != nil {
		t.Fatalf("ScoreConversationalCase: %v", err)
	}
	if miss.Grounded == nil || *miss.Grounded {
		t.Fatalf("expected Grounded=false when the evidence is absent, got %+v", miss.Grounded)
	}
}

func TestScoreConversationalCase_NonIdentityLeavesGroundedNil(t *testing.T) {
	for _, k := range []QuestionKind{KindStatus, KindDelta, KindOpenItems, KindRationale, KindCrossProject} {
		k := k
		t.Run(string(k), func(t *testing.T) {
			c := ConversationalCase{ID: "c1", Kind: k, ObservationID: "obs-a", ExpectedFact: "fact"}
			result, err := ScoreConversationalCase(c, RetrievedCase{Retrieved: "fact", RankedObservationIDs: []string{"obs-a"}})
			if err != nil {
				t.Fatalf("ScoreConversationalCase: %v", err)
			}
			if result.Grounded != nil {
				t.Errorf("Grounded must stay nil for kind %q, got %v", k, *result.Grounded)
			}
			if result.HonestRefusal != nil {
				t.Errorf("HonestRefusal must stay nil for kind %q, got %v", k, *result.HonestRefusal)
			}
			if len(result.RankedObservationIDs) != 1 || result.RankedObservationIDs[0] != "obs-a" {
				t.Errorf("RankedObservationIDs must pass through unchanged, got %v", result.RankedObservationIDs)
			}
		})
	}
}

// TestScoreConversationalCase_AbsenceInvertsScoring is requirement 3's core
// property: an absence case PASSES when nothing is returned, and FAILS when
// something confident comes back — the opposite of every other kind.
func TestScoreConversationalCase_AbsenceInvertsScoring(t *testing.T) {
	c := ConversationalCase{ID: "a1", Kind: KindAbsence, ExpectAbsence: true}

	pass, err := ScoreConversationalCase(c, RetrievedCase{})
	if err != nil {
		t.Fatalf("ScoreConversationalCase: %v", err)
	}
	if pass.HonestRefusal == nil || !*pass.HonestRefusal {
		t.Fatalf("empty retrieval must PASS an absence case, got %+v", pass.HonestRefusal)
	}

	fail, err := ScoreConversationalCase(c, RetrievedCase{
		Retrieved:             "Omnia is a command-line client for GitLab.",
		RankedObservationIDs:  []string{"obs-wrong"},
		SurfacedObservationID: "obs-wrong",
	})
	if err != nil {
		t.Fatalf("ScoreConversationalCase: %v", err)
	}
	if fail.HonestRefusal == nil || *fail.HonestRefusal {
		t.Fatalf("a confident (non-empty) result must FAIL an absence case, got %+v", fail.HonestRefusal)
	}
}

// TestScoreAbsence_RetrievedTextAloneCountsAsConfident covers the edge case
// where a fetcher returns prose but no ranked ID list (e.g. a single-hit
// fetcher) — text alone is still "returned something", so it must still
// fail an absence case.
func TestScoreAbsence_RetrievedTextAloneCountsAsConfident(t *testing.T) {
	if ScoreAbsence(RetrievedCase{Retrieved: "some content"}) {
		t.Error("non-empty Retrieved text alone must count as a confident (failing) result")
	}
	if !ScoreAbsence(RetrievedCase{Retrieved: "   "}) {
		t.Error("whitespace-only Retrieved text must still count as an honest refusal (pass)")
	}
	if !ScoreAbsence(RetrievedCase{}) {
		t.Error("a completely empty RetrievedCase must count as an honest refusal (pass)")
	}
}

func TestScoreConversationalCase_RejectsInvalidKind(t *testing.T) {
	c := ConversationalCase{ID: "bad", Kind: QuestionKind("nonsense")}
	if _, err := ScoreConversationalCase(c, RetrievedCase{}); err == nil {
		t.Error("expected an error for an invalid Kind, got nil")
	}
}

func TestBuildConversationalReport_SeedsEveryKind(t *testing.T) {
	rep := BuildConversationalReport(nil)
	for _, k := range AllQuestionKinds {
		if _, ok := rep.ByKind[k]; !ok {
			t.Errorf("kind %q must be present (seeded) even with zero results", k)
		}
	}
}

func TestBuildConversationalReport_AccuracyAt1AndMRR(t *testing.T) {
	results := []ConversationalCaseResult{
		{Case: ConversationalCase{Kind: KindStatus, ObservationID: "a"}, RankedObservationIDs: []string{"a", "x", "y"}}, // pos 1
		{Case: ConversationalCase{Kind: KindStatus, ObservationID: "b"}, RankedObservationIDs: []string{"x", "b", "y"}}, // pos 2
		{Case: ConversationalCase{Kind: KindStatus, ObservationID: "c"}, RankedObservationIDs: []string{"x", "y", "z"}}, // absent, still ranked-scoreable
	}
	rep := BuildConversationalReport(results)
	seg := rep.ByKind[KindStatus]

	if seg.Total != 3 {
		t.Fatalf("Total: want 3, got %d", seg.Total)
	}
	if seg.RankedCases != 3 {
		t.Fatalf("RankedCases: want 3, got %d", seg.RankedCases)
	}
	wantAcc := 1.0 / 3
	if math.Abs(seg.AccuracyAt1()-wantAcc) > 1e-9 {
		t.Errorf("AccuracyAt1: want %.6f, got %.6f", wantAcc, seg.AccuracyAt1())
	}
	wantMRR := (1.0 + 0.5 + 0) / 3
	if math.Abs(seg.MRR()-wantMRR) > 1e-9 {
		t.Errorf("MRR: want %.6f, got %.6f", wantMRR, seg.MRR())
	}
}

func TestBuildConversationalReport_IdentityGroundingRate(t *testing.T) {
	yes, no := true, false
	results := []ConversationalCaseResult{
		{Case: ConversationalCase{Kind: KindIdentity}, Grounded: &yes},
		{Case: ConversationalCase{Kind: KindIdentity}, Grounded: &no},
		{Case: ConversationalCase{Kind: KindIdentity}, Grounded: &no},
	}
	seg := BuildConversationalReport(results).ByKind[KindIdentity]
	if seg.GroundingTotal != 3 {
		t.Fatalf("GroundingTotal: want 3, got %d", seg.GroundingTotal)
	}
	want := 1.0 / 3
	if math.Abs(seg.GroundingRate()-want) > 1e-9 {
		t.Errorf("GroundingRate: want %.6f, got %.6f", want, seg.GroundingRate())
	}
}

// TestBuildConversationalReport_AbsenceHonestRefusalAndFalseConfidence is
// requirement 3's headline pair of numbers: the plan names the "false
// confidence rate" explicitly, and it must be exactly 1-HonestRefusalRate.
func TestBuildConversationalReport_AbsenceHonestRefusalAndFalseConfidence(t *testing.T) {
	pass, fail := true, false
	results := []ConversationalCaseResult{
		{Case: ConversationalCase{Kind: KindAbsence}, HonestRefusal: &pass},
		{Case: ConversationalCase{Kind: KindAbsence}, HonestRefusal: &fail},
		{Case: ConversationalCase{Kind: KindAbsence}, HonestRefusal: &fail},
		{Case: ConversationalCase{Kind: KindAbsence}, HonestRefusal: &fail},
	}
	seg := BuildConversationalReport(results).ByKind[KindAbsence]
	if seg.Total != 4 || seg.HonestRefusals != 1 {
		t.Fatalf("Total=%d HonestRefusals=%d, want 4 and 1", seg.Total, seg.HonestRefusals)
	}
	if math.Abs(seg.HonestRefusalRate()-0.25) > 1e-9 {
		t.Errorf("HonestRefusalRate: want 0.25, got %.4f", seg.HonestRefusalRate())
	}
	if math.Abs(seg.FalseConfidenceRate()-0.75) > 1e-9 {
		t.Errorf("FalseConfidenceRate: want 0.75, got %.4f", seg.FalseConfidenceRate())
	}
	if math.Abs((seg.HonestRefusalRate()+seg.FalseConfidenceRate())-1.0) > 1e-9 {
		t.Error("HonestRefusalRate + FalseConfidenceRate must sum to 1")
	}
}

// TestBuildConversationalReport_UnrankableCaseDoesNotCountAsRanked covers
// the "unscoreable, not zero" convention: a case with no ranked list and/or
// no gold ObservationID must not inflate RankedCases's denominator.
func TestBuildConversationalReport_UnrankableCaseDoesNotCountAsRanked(t *testing.T) {
	results := []ConversationalCaseResult{
		{Case: ConversationalCase{Kind: KindIdentity, ObservationID: ""}, RankedObservationIDs: nil}, // today's identity shape
		{Case: ConversationalCase{Kind: KindIdentity, ObservationID: "a"}, RankedObservationIDs: []string{"a"}},
	}
	seg := BuildConversationalReport(results).ByKind[KindIdentity]
	if seg.Total != 2 {
		t.Fatalf("Total: want 2, got %d", seg.Total)
	}
	if seg.RankedCases != 1 {
		t.Fatalf("RankedCases must only count cases with both a ranked list AND a gold ID: want 1, got %d", seg.RankedCases)
	}
}

func TestRunOnceConversational_EndToEnd(t *testing.T) {
	cases := []ConversationalCase{
		{ID: "s1", Kind: KindStatus, Query: "q1", Language: LanguageEN, ObservationID: "obs-1", ExpectedFact: "fact one"},
		{ID: "i1", Kind: KindIdentity, Query: "q2", Language: LanguageEN, ExpectedFact: "Persistent memory"},
		{ID: "a1", Kind: KindAbsence, Query: "q3", Language: LanguageEN, ExpectAbsence: true},
	}
	fetch := func(ctx context.Context, c ConversationalCase) (RetrievedCase, error) {
		switch c.ID {
		case "s1":
			return RetrievedCase{Retrieved: "fact one", SurfacedObservationID: "obs-1", RankedObservationIDs: []string{"obs-1"}}, nil
		case "i1":
			return RetrievedCase{Retrieved: "Persistent memory for AI coding agents"}, nil
		default: // a1: absence — honest refusal
			return RetrievedCase{}, nil
		}
	}

	rep, err := RunOnceConversational(context.Background(), cases, fetch)
	if err != nil {
		t.Fatalf("RunOnceConversational: %v", err)
	}

	if got := rep.ByKind[KindStatus].AccuracyAt1(); got != 1.0 {
		t.Errorf("status AccuracyAt1: want 1.0, got %.4f", got)
	}
	if got := rep.ByKind[KindIdentity].GroundingRate(); got != 1.0 {
		t.Errorf("identity GroundingRate: want 1.0, got %.4f", got)
	}
	if got := rep.ByKind[KindAbsence].HonestRefusalRate(); got != 1.0 {
		t.Errorf("absence HonestRefusalRate: want 1.0, got %.4f", got)
	}
}

func TestRunOnceConversational_RejectsNilFetcher(t *testing.T) {
	if _, err := RunOnceConversational(context.Background(), nil, nil); err == nil {
		t.Error("expected an error for a nil fetcher, got nil")
	}
}

func TestRunOnceConversational_PropagatesFetchError(t *testing.T) {
	cases := []ConversationalCase{{ID: "c1", Kind: KindStatus, Query: "q"}}
	fetch := func(ctx context.Context, c ConversationalCase) (RetrievedCase, error) {
		return RetrievedCase{}, context.DeadlineExceeded
	}
	if _, err := RunOnceConversational(context.Background(), cases, fetch); err == nil {
		t.Error("expected the fetcher's error to propagate, got nil")
	}
}
