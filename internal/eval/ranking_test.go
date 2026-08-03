package eval

import (
	"math"
	"testing"
)

// ─── #236 [RED]: the harness must be able to measure ORDER, not just presence ───
//
// Every existing metric scores whether the retrieved text CONTAINS the expected
// fact. That is a presence test, and re-ranking a candidate pool changes the
// order of what is in it, not whether the answer is in it — measured: giving
// relevance weight ZERO and letting recency dominate 1000:1 left accuracy at
// exactly 0.550 across 3 runs.
//
// So the learned ranker's own promotion gate can prove "did not regress" and can
// never prove "improved". These tests add the metric that gives an ordering
// change somewhere to show up.

func caseWithRank(id string, ranked []string) CaseResult {
	return CaseResult{
		Case:                 EvalCase{ID: "c-" + id, ObservationID: id, Capability: CapabilityRecall},
		RankedObservationIDs: ranked,
	}
}

func TestBuildRankingSection_MRRRewardsHigherPositions(t *testing.T) {
	// correct answer at position 1, 2 and 4 → reciprocal ranks 1, 1/2, 1/4
	got := BuildRankingSection([]CaseResult{
		caseWithRank("a", []string{"a", "x", "y", "z"}),
		caseWithRank("b", []string{"x", "b", "y", "z"}),
		caseWithRank("c", []string{"x", "y", "z", "c"}),
	})
	if got == nil {
		t.Fatal("a scoreable batch must produce a section")
	}
	want := (1.0 + 0.5 + 0.25) / 3
	if math.Abs(got.MRR-want) > 1e-9 {
		t.Fatalf("MRR: want %.6f, got %.6f", want, got.MRR)
	}
	if got.Cases != 3 {
		t.Fatalf("Cases: want 3, got %d", got.Cases)
	}
}

// The whole point: the same set of results in a different ORDER must move the
// number. This is what accuracy cannot see.
func TestBuildRankingSection_OrderChangesTheNumber(t *testing.T) {
	good := BuildRankingSection([]CaseResult{caseWithRank("a", []string{"a", "x", "y"})})
	bad := BuildRankingSection([]CaseResult{caseWithRank("a", []string{"x", "y", "a"})})

	if good.MRR <= bad.MRR {
		t.Fatalf("a better ordering must score higher: good=%.4f bad=%.4f", good.MRR, bad.MRR)
	}
	if good.RecallAtK[1] != 1.0 || bad.RecallAtK[1] != 0.0 {
		t.Fatalf("recall@1 must separate them: good=%v bad=%v", good.RecallAtK[1], bad.RecallAtK[1])
	}
	// ...while recall@3 sees both as fine — exactly the presence-vs-position
	// distinction this metric exists to expose.
	if good.RecallAtK[3] != 1.0 || bad.RecallAtK[3] != 1.0 {
		t.Fatalf("recall@3 must find it in both: good=%v bad=%v", good.RecallAtK[3], bad.RecallAtK[3])
	}
}

// A miss is a miss: the correct observation is nowhere in the ranked list.
func TestBuildRankingSection_AbsentAnswerScoresZero(t *testing.T) {
	got := BuildRankingSection([]CaseResult{caseWithRank("a", []string{"x", "y", "z"})})

	if got.MRR != 0 {
		t.Fatalf("MRR must be 0 when the answer never appears, got %.4f", got.MRR)
	}
	if got.Cases != 1 {
		t.Fatalf("an absent answer is still a SCORED case, got Cases=%d", got.Cases)
	}
}

// The failure mode this whole issue is about: never report a number that was
// not actually measured. A fetcher that supplies no ranked list must show up as
// unmeasured, not as a zero that reads like a real result.
func TestBuildRankingSection_NoRankedListsIsUnmeasuredNotZero(t *testing.T) {
	got := BuildRankingSection([]CaseResult{
		{Case: EvalCase{ID: "c1", ObservationID: "a", Capability: CapabilityRecall}},
		{Case: EvalCase{ID: "c2", ObservationID: "b", Capability: CapabilityRecall}},
	})
	if got != nil {
		t.Fatalf("with no ranked list anywhere the section must be nil (unmeasured), got %+v", got)
	}
}

func TestBuildRankingSection_CountsUnscoreableCasesSeparately(t *testing.T) {
	got := BuildRankingSection([]CaseResult{
		caseWithRank("a", []string{"a", "x"}),
		{Case: EvalCase{ID: "c2", ObservationID: "b"}},                      // sin lista rankeada
		{Case: EvalCase{ID: "c3"}, RankedObservationIDs: []string{"x", "y"}}, // sin verdad de base
	})
	if got == nil {
		t.Fatal("one scoreable case is enough to produce a section")
	}
	if got.Cases != 1 {
		t.Fatalf("Cases must count only scoreable ones: got %d", got.Cases)
	}
	if got.Unscoreable != 2 {
		t.Fatalf("Unscoreable must be visible, not silently dropped: got %d", got.Unscoreable)
	}
}

func TestBuildRankingSection_RecallAtKIsMonotonic(t *testing.T) {
	got := BuildRankingSection([]CaseResult{
		caseWithRank("a", []string{"a", "x", "y", "z", "w"}),
		caseWithRank("b", []string{"x", "y", "b", "z", "w"}),
		caseWithRank("c", []string{"x", "y", "z", "w", "c"}),
	})
	prev := -1.0
	for _, k := range RankingKs {
		v := got.RecallAtK[k]
		if v < prev {
			t.Fatalf("recall@k must never decrease as k grows: recall@%d=%.3f after %.3f", k, v, prev)
		}
		prev = v
	}
	if got.RecallAtK[1] != 1.0/3 {
		t.Fatalf("recall@1: want 1/3, got %.4f", got.RecallAtK[1])
	}
	if got.RecallAtK[5] != 1.0 {
		t.Fatalf("recall@5: want 1, got %.4f", got.RecallAtK[5])
	}
}

// A report with no ranking section must stay distinguishable from one whose
// ranking happens to be zero — same contract Retrieval already follows.
func TestBuildReport_LeavesRankingNilWhenUnmeasured(t *testing.T) {
	rep := BuildReport([]CaseResult{
		{Case: EvalCase{ID: "c1", Capability: CapabilityRecall, Language: LanguageEN}, Hit: true},
	})
	if rep.Ranking != nil {
		t.Fatalf("no ranked lists → Report.Ranking must stay nil, got %+v", rep.Ranking)
	}
}

func TestBuildReport_AttachesRankingWhenMeasured(t *testing.T) {
	rep := BuildReport([]CaseResult{
		{
			Case:                 EvalCase{ID: "c1", ObservationID: "a", Capability: CapabilityRecall, Language: LanguageEN},
			Hit:                  true,
			RankedObservationIDs: []string{"a", "x"},
		},
	})
	if rep.Ranking == nil {
		t.Fatal("a run that supplied ranked lists must carry a Ranking section")
	}
	if rep.Ranking.MRR != 1.0 {
		t.Fatalf("MRR: want 1, got %.4f", rep.Ranking.MRR)
	}
}
