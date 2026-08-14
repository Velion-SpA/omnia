package eval

import (
	"context"
	"fmt"
	"strings"
)

// ConversationalCaseResult is one scored ConversationalCase outcome. Unlike
// CaseResult's single Hit bool (built for a corpus where every case is
// scored the same way — scoring.go), a conversational case's verdict SHAPE
// depends on its Kind: requirement 2's per-kind breakdown and requirement
// 3's inverted absence scoring cannot share one boolean without collapsing
// straight back into the "one number" problem this corpus exists to fix.
// Each field below is populated only by the Kind(s) it applies to;
// BuildConversationalReport reads whichever fields are non-nil for a case's
// own Kind.
type ConversationalCaseResult struct {
	Case ConversationalCase

	// RankedObservationIDs is retrieval's ordered candidate list for this
	// case, best first — the exact same contract RetrievedCase (harness.go)
	// already defines, reused unchanged so accuracy@1/MRR can be computed
	// with the same primitive ranking.go's BuildRankingSection uses for the
	// coding-agent corpus. Always empty for KindAbsence (no gold
	// observation to rank against, by design) and MAY be empty for
	// KindIdentity today (see ConversationalCase.ObservationID) — an empty
	// list plus an empty Case.ObservationID means "unscoreable for
	// ranking", not a zero score.
	RankedObservationIDs []string

	// Grounded is the identity-only grounding verdict (requirement 2): did
	// the retrieved context contain the evidence needed to answer — a
	// presence test over the retrieved text, computed the same way
	// JudgeFreeScorer's factMatches already does for the coding-agent
	// corpus's Recall capability (scoring.go). nil for every kind except
	// identity.
	Grounded *bool

	// HonestRefusal is the absence-only verdict (requirement 3): true when
	// retrieval correctly returned NOTHING (PASS — honest refusal), false
	// when it confidently returned something for a question with no
	// evidence (FAIL). nil for every kind except absence. See ScoreAbsence
	// for why "the fetcher returned zero results" is what "nothing above
	// the relevance floor" means in this harness.
	HonestRefusal *bool
}

// ScoreConversationalCase scores one ConversationalCase against what
// retrieval returned (rc), dispatching by c.Kind. It never calls an LLM
// judge — every conversational metric (accuracy@1, MRR, grounding, honest
// refusal) is a presence or position test over text/IDs the fetcher already
// returned, matching the plan's "no LLM call inside the retrieval path"
// constraint (P2/P3 sections) extended to the eval harness itself.
func ScoreConversationalCase(c ConversationalCase, rc RetrievedCase) (ConversationalCaseResult, error) {
	if !validQuestionKinds[c.Kind] {
		return ConversationalCaseResult{}, fmt.Errorf("eval: ScoreConversationalCase: case %q has invalid kind %q", c.ID, c.Kind)
	}

	if c.Kind == KindAbsence {
		refusal := ScoreAbsence(rc)
		return ConversationalCaseResult{Case: c, HonestRefusal: &refusal}, nil
	}

	result := ConversationalCaseResult{Case: c, RankedObservationIDs: rc.RankedObservationIDs}
	if c.Kind == KindIdentity {
		grounded := factMatches(c.ExpectedFact, rc.Retrieved)
		result.Grounded = &grounded
	}
	return result, nil
}

// ScoreAbsence implements requirement 3's inverted scoring: an absence case
// PASSES (returns true) when retrieval returned NOTHING, and FAILS (returns
// false) when it returned something — regardless of what that something is,
// because there is nothing correct it could have been.
//
// "Nothing above the relevance floor" (the plan's phrasing) and "the
// fetcher returned zero results" are the SAME event here, not two different
// checks: every fetcher this harness wires (cmd/omnia/eval.go's
// storeBackedFetcher, pipelineBackedFetcher, and their conversational
// siblings) already calls into store.Store.Search / recall.Service.Search,
// and both already apply their own relevance-floor/relaxation-ladder
// filtering BEFORE returning a result set. By the time a RetrievedCase
// reaches this function, floor filtering has already happened; a fetcher
// that still found something to return is, by definition, reporting a
// result IT considered above its own floor. Adding a SECOND, eval-side
// threshold here would just be a second hand-picked number to calibrate —
// exactly what the plan's P3 section warns against ("the existing floors
// ... were inherited from a different embedding model and starved recall
// until issue #83 caught it") and explicitly defers to P3's calibration
// work, not this baseline harness.
//
// This also predicts the correct baseline finding, not just a convenient
// one: today's store has no confidence signal at the HTTP boundary (plan
// section 0.1 / P3's "before" measurement — "how often does today's
// /search return something confident-looking for a question with no
// evidence? Expect: always"), and FTS5's relaxation ladder can surface a
// weak match for almost any query. So most absence cases are EXPECTED to
// score false (fail) against today's retrieval — that is the gap P3 exists
// to close, not a bug in this scorer.
func ScoreAbsence(rc RetrievedCase) bool {
	return len(rc.RankedObservationIDs) == 0 && strings.TrimSpace(rc.Retrieved) == ""
}

// ConversationalFetcher is the conversational harness's retrieval seam,
// mirroring RetrievedFetcher (harness.go) but keyed by ConversationalCase
// instead of EvalCase — the two corpora are siblings, not a shared type, so
// a fetcher for one can never be silently handed the other's cases. See
// cmd/omnia/eval.go for the production wiring (requirement 5): one
// implementation searches the real store in-process, another calls
// GET /search over HTTP, so the gap between the two paths can be quantified
// on the same corpus.
type ConversationalFetcher func(ctx context.Context, c ConversationalCase) (RetrievedCase, error)

// KindSegment is one QuestionKind's row in a ConversationalReport — the
// per-kind breakdown requirement 2 asks for ("never just one aggregate").
// Like Segment (report.go), it stores raw counts and exposes derived rates
// as methods rather than bare pre-computed scalars, so a report reader can
// always audit a rate back to its inputs.
type KindSegment struct {
	Kind QuestionKind
	// Total is every case of this Kind that was scored, regardless of
	// whether it was individually rankable — the segment's own case count.
	Total int

	// Ranking fields (accuracy@1 / MRR — requirement 2). RankedCases is the
	// subset of Total that had BOTH a ranked list and a gold ObservationID
	// to compare against — the same scoreable/unscoreable split
	// ranking.go's BuildRankingSection already uses, generalized per kind.
	// Always 0 for KindAbsence (no gold ID exists for that kind, by
	// construction) and MAY be less than Total for KindIdentity today (see
	// ConversationalCase.ObservationID's doc).
	RankedCases       int
	AccuracyAt1Hits   int
	SumReciprocalRank float64

	// Grounding fields (requirement 2's identity metric). Populated only
	// when Kind == KindIdentity; zero for every other kind.
	GroundingTotal int
	GroundingHits  int

	// HonestRefusals (requirement 3). Populated only when Kind ==
	// KindAbsence; Total above is this segment's own denominator for it.
	HonestRefusals int
}

// AccuracyAt1 returns the fraction of rankable cases whose top-ranked result
// was the correct observation. 0 when RankedCases is 0 — "unscoreable" and
// "scored zero" are different states; a caller that needs to distinguish
// them checks RankedCases, exactly like Segment.Accuracy's zero-Total
// convention (report.go).
func (s KindSegment) AccuracyAt1() float64 {
	if s.RankedCases == 0 {
		return 0
	}
	return float64(s.AccuracyAt1Hits) / float64(s.RankedCases)
}

// MRR returns this kind's mean reciprocal rank over its rankable cases.
func (s KindSegment) MRR() float64 {
	if s.RankedCases == 0 {
		return 0
	}
	return s.SumReciprocalRank / float64(s.RankedCases)
}

// GroundingRate returns identity's presence-of-evidence rate (requirement
// 2). 0 (not NaN) when GroundingTotal is 0 — e.g. for any non-identity
// kind, where grounding is never computed.
func (s KindSegment) GroundingRate() float64 {
	if s.GroundingTotal == 0 {
		return 0
	}
	return float64(s.GroundingHits) / float64(s.GroundingTotal)
}

// HonestRefusalRate returns absence's honest-refusal rate (requirement 3):
// the fraction of absence cases where retrieval correctly returned nothing.
// 0 when Total is 0.
func (s KindSegment) HonestRefusalRate() float64 {
	if s.Total == 0 {
		return 0
	}
	return float64(s.HonestRefusals) / float64(s.Total)
}

// FalseConfidenceRate is 1-HonestRefusalRate — the plan's P3 metric name
// ("false-confidence rate ... Target ≤ 0.1") for the same underlying count,
// exposed under both names so a caller matching the plan's vocabulary and a
// caller matching this package's "rate of the thing that happened"
// convention both find it without recomputing.
func (s KindSegment) FalseConfidenceRate() float64 {
	if s.Total == 0 {
		return 0
	}
	return 1 - s.HonestRefusalRate()
}

// ConversationalReport is the "never just one aggregate" report requirement
// 2 asks for: one KindSegment per QuestionKind, every kind always present
// (see BuildConversationalReport) so a missing kind and a kind that scored
// zero are never visually confusable — the same discipline Report/
// allCapabilities already apply (report.go).
type ConversationalReport struct {
	ByKind map[QuestionKind]KindSegment
}

// BuildConversationalReport aggregates a run's ConversationalCaseResults
// into the per-kind view, seeding every one of AllQuestionKinds first so an
// unrepresented kind reports as a present-but-empty segment, never a
// missing map key.
func BuildConversationalReport(results []ConversationalCaseResult) ConversationalReport {
	byKind := make(map[QuestionKind]KindSegment, len(AllQuestionKinds))
	for _, k := range AllQuestionKinds {
		byKind[k] = KindSegment{Kind: k}
	}

	for _, r := range results {
		seg := byKind[r.Case.Kind]
		seg.Total++

		if len(r.RankedObservationIDs) > 0 && r.Case.ObservationID != "" {
			seg.RankedCases++
			pos := indexOfID(r.RankedObservationIDs, r.Case.ObservationID) // 0 if absent
			if pos == 1 {
				seg.AccuracyAt1Hits++
			}
			if pos > 0 {
				seg.SumReciprocalRank += 1 / float64(pos)
			}
		}

		if r.Grounded != nil {
			seg.GroundingTotal++
			if *r.Grounded {
				seg.GroundingHits++
			}
		}

		if r.HonestRefusal != nil && *r.HonestRefusal {
			seg.HonestRefusals++
		}

		byKind[r.Case.Kind] = seg
	}

	return ConversationalReport{ByKind: byKind}
}

// indexOfID returns the 1-based position of target in ids, or 0 if absent —
// the same convention ranking.go's BuildRankingSection uses inline, factored
// out here since BuildConversationalReport needs the identical lookup.
func indexOfID(ids []string, target string) int {
	for i, id := range ids {
		if id == target {
			return i + 1
		}
	}
	return 0
}

// RunOnceConversational executes ONE full pass over the conversational
// corpus: for each case it fetches retrieval via fetch, scores it via
// ScoreConversationalCase, and folds every case into a ConversationalReport.
// Mirrors RunOnce's (harness.go) shape for the coding-agent corpus, kept as
// a separate function because the two corpora score differently (no LLM
// judge path here, and ConversationalCaseResult is not CaseResult).
func RunOnceConversational(ctx context.Context, cases []ConversationalCase, fetch ConversationalFetcher) (ConversationalReport, error) {
	if fetch == nil {
		return ConversationalReport{}, fmt.Errorf("eval: RunOnceConversational: fetch is nil")
	}

	results := make([]ConversationalCaseResult, 0, len(cases))
	for _, c := range cases {
		rc, err := fetch(ctx, c)
		if err != nil {
			return ConversationalReport{}, fmt.Errorf("eval: RunOnceConversational: case %q: fetch: %w", c.ID, err)
		}
		result, err := ScoreConversationalCase(c, rc)
		if err != nil {
			return ConversationalReport{}, fmt.Errorf("eval: RunOnceConversational: case %q: score: %w", c.ID, err)
		}
		results = append(results, result)
	}

	return BuildConversationalReport(results), nil
}
