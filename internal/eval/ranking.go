package eval

// This file adds the harness's first ORDER-sensitive metric (#236).
//
// Every other metric here scores whether the retrieved text CONTAINS the
// expected fact. That is a presence test, and it is blind to ranking by
// construction: re-ranking a candidate pool changes the order of what is in
// it, not whether the answer is in it. Measured on a 60-case corpus, giving
// relevance weight ZERO and letting recency dominate 1000:1 — a configuration
// that scrambles the order completely — left accuracy at exactly 0.550 across
// three runs, unchanged to the case.
//
// That matters beyond curiosity: rank-train uses this harness as its promotion
// gate, so the learned ranker could only ever prove "did not regress" and never
// "improved". MRR and recall@k give an ordering change somewhere to land.
//
// The ground truth needed was already in the corpus: EvalCase.ObservationID,
// until now consulted only for contradiction cases.

// RankingKs are the cutoffs reported for recall@k. 1 is the one that separates
// a ranker that puts the answer FIRST from one that merely includes it; the
// larger cutoffs show how much room a better ordering would have to work with.
var RankingKs = []int{1, 3, 5, 10}

// RankingSection is the order-sensitive half of a Report.
//
// It is attached only when at least one case could actually be scored. A run
// whose fetcher supplies no ranked lists leaves Report.Ranking nil rather than
// reporting zeros — "we did not measure" must never be readable as "we measured
// and it was zero", which is the exact confusion this issue exists to remove.
type RankingSection struct {
	// Cases is how many cases were scored: those with BOTH a ranked list and a
	// ground-truth ObservationID.
	Cases int
	// Unscoreable is how many were skipped for lacking one or the other. It is
	// reported rather than silently dropped, so a shrinking denominator cannot
	// quietly inflate the score.
	Unscoreable int
	// MRR is the mean reciprocal rank: 1 when the correct observation is always
	// first, 1/2 when always second, 0 when it never appears.
	MRR float64
	// RecallAtK maps each k in RankingKs to the fraction of scored cases whose
	// correct observation appears within the first k results.
	RecallAtK map[int]float64
}

// BuildRankingSection computes MRR and recall@k over results, or returns nil
// when nothing could be scored.
func BuildRankingSection(results []CaseResult) *RankingSection {
	section := &RankingSection{RecallAtK: map[int]float64{}}
	hitsAtK := map[int]int{}
	sumReciprocal := 0.0

	for _, r := range results {
		if len(r.RankedObservationIDs) == 0 || r.Case.ObservationID == "" {
			section.Unscoreable++
			continue
		}
		section.Cases++

		// 1-based position of the correct observation; 0 when absent, which
		// contributes nothing to either metric but still counts as a scored
		// case — a miss is a result, not a gap in the sample.
		pos := 0
		for i, id := range r.RankedObservationIDs {
			if id == r.Case.ObservationID {
				pos = i + 1
				break
			}
		}
		if pos == 0 {
			continue
		}
		sumReciprocal += 1 / float64(pos)
		for _, k := range RankingKs {
			if pos <= k {
				hitsAtK[k]++
			}
		}
	}

	if section.Cases == 0 {
		return nil
	}
	section.MRR = sumReciprocal / float64(section.Cases)
	for _, k := range RankingKs {
		section.RecallAtK[k] = float64(hitsAtK[k]) / float64(section.Cases)
	}
	return section
}
