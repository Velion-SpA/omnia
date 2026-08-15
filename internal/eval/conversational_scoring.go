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
	// the relevance floor" means in this harness — UNLESS the fetcher
	// reported an explicit RetrievedCase.Confidence (P3's --target answer),
	// in which case "none" IS the honest-refusal signal directly (see
	// scoreAbsenceRefusal).
	HonestRefusal *bool

	// FalseRefusal is P3's OTHER failure mode, the mirror image of
	// HonestRefusal: for identity/status cases, true when the fetcher
	// reported Confidence "none" OR "off_topic" despite the corpus
	// guaranteeing real evidence exists for that case (every non-absence
	// ConversationalCase requires a non-empty ExpectedFact by construction
	// — see validateConversationalCases). Both values are refusal-shaped
	// from the consumer's perspective: "no tengo nada sobre eso" (none) and
	// "eso está fuera de lo que sé" (off_topic) both tell an identity/status
	// asker the system found nothing usable, when in fact it did.
	// "A memory system that says 'no sé' when it does know is a worse
	// product than one that guesses" (P3's own wording) — false_confidence
	// (HonestRefusal's mirror) and false_refusal are the two directions
	// that single sentence asks this harness to measure. nil for every kind
	// except identity/status, AND nil for those two kinds whenever the
	// fetcher supplied no Confidence signal at all (RetrievedCase.Confidence
	// == "" — --target inprocess/http have no such signal to score).
	FalseRefusal *bool
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
		refusal := scoreAbsenceRefusal(rc)
		return ConversationalCaseResult{Case: c, HonestRefusal: &refusal}, nil
	}

	result := ConversationalCaseResult{Case: c, RankedObservationIDs: rc.RankedObservationIDs}
	if c.Kind == KindIdentity {
		grounded := factMatches(c.ExpectedFact, rc.Retrieved)
		result.Grounded = &grounded
	}
	// P3's false-refusal check (requirement: "identity/status: a
	// confidence: 'none' on a question we DO have evidence for is a false
	// refusal. Target ≤0.05"). Only computed when the fetcher actually
	// reported a Confidence — --target inprocess/http have no such signal,
	// and an unset field must read as "unmeasured," not "scored zero" (see
	// FalseRefusal's own doc).
	if (c.Kind == KindIdentity || c.Kind == KindStatus) && rc.Confidence != "" {
		falseRefusal := rc.Confidence == confidenceNone || rc.Confidence == confidenceOffTopic
		result.FalseRefusal = &falseRefusal
	}
	return result, nil
}

// scoreAbsenceRefusal decides an absence case's honest-refusal verdict.
// When the fetcher reported an explicit Confidence (P3's --target answer,
// via GET /answer's own calibrated signal), "none" OR "off_topic" is used
// DIRECTLY — for an absence case, both values mean the endpoint correctly
// did not confidently hand back irrelevant or nonexistent evidence, so both
// count as an honest refusal. Re-deriving this from presence/absence of
// retrieved text would silently re-introduce a second, uncalibrated floor
// exactly like the one ScoreAbsence's own doc warns against. Fetchers with
// no confidence signal (--target inprocess/http, which predate P3 and
// measure the retrieval PATHS' raw honesty rather than the endpoint's
// calibrated one) fall back to ScoreAbsence's presence-based check,
// unchanged.
func scoreAbsenceRefusal(rc RetrievedCase) bool {
	if rc.Confidence != "" {
		return rc.Confidence == confidenceNone || rc.Confidence == confidenceOffTopic
	}
	return ScoreAbsence(rc)
}

// confidenceNone mirrors internal/mcp.AnswerConfidenceNone's string value
// ("none") without this package importing internal/mcp — the SAME
// cross-package "duplicate the tiny constant, document why" convention this
// file's own conversationalGroundingContextSeparator precedent (cmd/omnia/
// eval.go) already uses, applied here because internal/eval must stay free
// of a dependency on the mcp package it is busy evaluating.
const confidenceNone = "none"

// confidenceOffTopic mirrors internal/mcp.AnswerConfidenceOffTopic's string
// value ("off_topic") — same cross-package duplicate-and-document
// convention confidenceNone already uses.
const confidenceOffTopic = "off_topic"

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

	// FalseRefusalTotal/FalseRefusalHits (P3's false-refusal requirement).
	// Populated only for KindIdentity/KindStatus, and only among THOSE
	// cases whose fetcher reported an explicit Confidence (see
	// ConversationalCaseResult.FalseRefusal's own doc) — FalseRefusalTotal
	// is this narrower denominator, NOT the same as Total, so a kind with
	// no confidence-reporting fetcher wired reports FalseRefusalRate() as 0
	// via the "0 total" branch rather than silently mixing scored and
	// unscored cases.
	FalseRefusalTotal int
	FalseRefusalHits  int
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

// FalseRefusalRate returns P3's other failure-mode rate (requirement:
// "identity/status ... Target ≤0.05") — the fraction of confidence-scored
// identity/status cases where GET /answer refused (confidence "none")
// despite the corpus guaranteeing real evidence exists. 0 when
// FalseRefusalTotal is 0 (no confidence-reporting fetcher was used, or this
// kind is neither identity nor status) — "unmeasured" and "scored zero"
// stay distinguishable by checking FalseRefusalTotal directly, same
// convention as GroundingRate/HonestRefusalRate above.
func (s KindSegment) FalseRefusalRate() float64 {
	if s.FalseRefusalTotal == 0 {
		return 0
	}
	return float64(s.FalseRefusalHits) / float64(s.FalseRefusalTotal)
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

		if r.FalseRefusal != nil {
			seg.FalseRefusalTotal++
			if *r.FalseRefusal {
				seg.FalseRefusalHits++
			}
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
