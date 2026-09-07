package eval

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
)

// This file defines the CONVERSATIONAL corpus profile (docs/
// conversational-retrieval-plan.md, "Baseline first: extend `omnia eval`
// with a conversational corpus"). It is a sibling of corpus.go's
// EvalCase/Capability profile, not a replacement: EvalCase stays the
// coding-agent recall corpus this package already scores; ConversationalCase
// is a second, independent corpus type for a conversational agent's
// questions, which do not fit EvalCase's shape — a conversational agent asks
// seven different KINDS of question (identity, status, delta, open_items,
// rationale, cross_project, absence) and collapsing them into one ranked
// list and one accuracy number is exactly the failure this profile exists
// to make visible.

// QuestionKind classifies a ConversationalCase by the SHAPE of question a
// conversational agent asks. Every ConversationalCase carries exactly one
// Kind, mirroring Capability's own "never untagged, never multi-tagged"
// rule (corpus.go).
type QuestionKind string

const (
	// KindIdentity asks "what IS this project" ("háblame sobre el proyecto
	// Omnia", "what is Omnia"). Scored primarily by Grounded (see
	// conversational_scoring.go), not ranking position — see
	// ConversationalCase.ObservationID's doc for why.
	KindIdentity QuestionKind = "identity"
	// KindStatus asks how something is going RIGHT NOW ("cómo va Workly",
	// "how is Workly going") — recency-sensitive, per the plan's P2
	// intent-routing table.
	KindStatus QuestionKind = "status"
	// KindDelta asks how something was fixed/changed ("cómo arreglamos X",
	// "how did we fix X") — the shape corpus.go's CapabilityRecall already
	// covers reasonably well for the coding-agent corpus; kept as its own
	// kind here so the per-kind report can show it does NOT regress when
	// identity/absence handling improves.
	KindDelta QuestionKind = "delta"
	// KindOpenItems asks what is still outstanding ("qué queda por hacer",
	// "próximos pasos", "pendiente", "what's next") — the P4
	// claim-lifecycle shape.
	KindOpenItems QuestionKind = "open_items"
	// KindRationale asks WHY a decision was made ("por qué elegimos", "why
	// did we choose").
	KindRationale QuestionKind = "rationale"
	// KindCrossProject asks a question that only a search spanning MORE
	// THAN ONE project can answer ("en qué estoy bloqueado", "esta
	// semana") — the P5 shape.
	KindCrossProject QuestionKind = "cross_project"
	// KindAbsence asks about something that does NOT exist in the store.
	// Its gold answer is literally "no evidence exists" (requirement 3 of
	// the baseline item) — see ConversationalCase.ExpectAbsence and
	// ScoreAbsence (conversational_scoring.go) for how that INVERTS normal
	// scoring: returning nothing is the PASS, returning something
	// confident is the FAIL.
	KindAbsence QuestionKind = "absence"
)

// AllQuestionKinds is the closed, ordered set BuildConversationalReport
// always seeds — see corpus.go's allCapabilities for the same "missing vs.
// zero" reasoning: a kind with no cases in a report and a kind that scored
// zero must never be visually indistinguishable.
var AllQuestionKinds = []QuestionKind{
	KindIdentity,
	KindStatus,
	KindDelta,
	KindOpenItems,
	KindRationale,
	KindCrossProject,
	KindAbsence,
}

var validQuestionKinds = map[QuestionKind]bool{
	KindIdentity:     true,
	KindStatus:       true,
	KindDelta:        true,
	KindOpenItems:    true,
	KindRationale:    true,
	KindCrossProject: true,
	KindAbsence:      true,
}

// ConversationalCase is one case in the conversational eval corpus profile.
//
// HARD REQUIREMENT (memory #1898/#1912 — read before editing this corpus):
// every case here MUST be hand-authored and hand-labelled against real,
// VERBATIM text — either a real dogfooded Omnia observation (ObservationID
// + ExpectedFact copied byte-for-byte from that observation's actual stored
// content) or, for identity cases, a real excerpt from this repo's own
// README.md. #1898 published wrong recall numbers (48.3%/55.0%, retracted
// by #1912) because its expected-fact strings were sliced from NORMALIZED
// text and so never actually appeared verbatim in what was scored; #1912's
// rule was "slice it verbatim from the stored content and assert it
// appears literally before running" — apply that same discipline to every
// case added here.
//
// This corpus is a STATIC file (testdata/conversational_cases.json), not a
// generated artifact — see EmbeddedConversationalCorpus/
// LoadConversationalCorpus below. Nothing in this package mechanically
// derives it from a live store. If a scaffolding generator is ever added to
// help a human draft candidate cases faster, it MUST be clearly labelled as
// a draft-only aid whose output requires human review and hand-labelling
// before it can be added to testdata/conversational_cases.json — never
// wired to write that file directly.
type ConversationalCase struct {
	ID       string       `json:"id"`
	Kind     QuestionKind `json:"kind"`
	Query    string       `json:"query"`
	Language Language     `json:"language"`

	// ObservationID is the gold observation (sync ID) whose content should
	// surface — ideally ranked first — for this Query. It powers the same
	// accuracy@1/MRR primitive ranking.go's #236 metrics already compute
	// for EvalCase.ObservationID, generalized per QuestionKind (requirement
	// 2 of the baseline item).
	//
	// Left EMPTY for every identity case in this corpus (as of 2026-08): P1
	// (repodoc ingestion, docs/conversational-retrieval-plan.md — not yet
	// built) is what will eventually make "what is Omnia" answerable from a
	// real observation. Until P1 ships there is no gold observation to rank
	// against, so identity cases are scored on Grounded (presence of
	// evidence in whatever text retrieval returns) instead of ranking
	// position — see conversational_scoring.go. This is a deliberate,
	// verified property of today's store, not an oversight: a live
	// mem_search for "qué es Omnia" / "what is Omnia" during this corpus's
	// authoring surfaced no such observation (see the mem_save this file
	// shipped with for the search that confirmed it).
	//
	// ALWAYS empty for absence cases — ExpectAbsence's whole contract is
	// that no observation should surface at all, so there is no gold ID to
	// name.
	ObservationID string `json:"observation_id,omitempty"`

	// ExpectedFact is the verbatim evidence text that must appear in
	// retrieved context for this case to count as grounded/correct — copied
	// byte-for-byte from the real observation's content (or, for identity,
	// from README.md), never from a paraphrase or a normalized copy (see
	// the struct doc's hard requirement above). Required for every kind
	// except absence.
	ExpectedFact string `json:"expected_fact,omitempty"`

	// ExpectAbsence marks an absence-kind case (requirement 3): true means
	// the CORRECT behavior is that retrieval finds nothing above the
	// relevance floor for this Query — see ScoreAbsence
	// (conversational_scoring.go) for what "relevance floor" means
	// operationally in this harness. Only valid — and required to be true
	// — when Kind == KindAbsence; every other kind must leave it false.
	ExpectAbsence bool `json:"expect_absence,omitempty"`

	// Notes is optional free-text context for a human reviewer (e.g. which
	// real memory or plan section this case models) — never read by
	// scoring, purely so the corpus stays reviewable per the hard
	// requirement above.
	Notes string `json:"notes,omitempty"`
}

// MinCasesPerKind is the conversational corpus's fail-fast floor: every one
// of the seven AllQuestionKinds MUST have at least one case, or the harness
// refuses to load the corpus. A silently-empty kind would report as
// "unmeasured" everywhere downstream (BuildConversationalReport's seeded
// zero-Total segment) instead of as a loud load-time error — and an empty
// kind is exactly the "collapse into one number" failure mode this profile
// exists to prevent, just moved one step earlier.
//
// There is deliberately no [Min,Max] TOTAL case-count bound the way
// EvalCase has (spec EVAL-2, corpus.go's MinCorpusSize/MaxCorpusSize):
// MinCasesPerKind enforces that every kind is representable, which is the
// property that actually matters for a per-kind report. The brief's "~15
// cases per kind" is authoring guidance for testdata/
// conversational_cases.json, not a mechanically-enforced gate.
const MinCasesPerKind = 1

//go:embed testdata/conversational_cases.json
var embeddedConversationalCorpusJSON []byte

// LoadConversationalCorpus reads a JSON array of ConversationalCase from
// path and validates it (see validateAndCountConversationalCases): every
// case must carry a valid Kind and Language, a non-empty Query, the
// right required/forbidden fields for its Kind, and IDs must be unique
// across the corpus.
func LoadConversationalCorpus(path string) ([]ConversationalCase, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("eval: load conversational corpus: %w", err)
	}
	return parseConversationalCorpus(data, path)
}

// EmbeddedConversationalCorpus returns the hand-authored seed corpus bundled
// into the binary via go:embed (testdata/conversational_cases.json),
// mirroring EmbeddedCorpus's cwd-independence fix (corpus.go, finding #1) —
// it works identically regardless of the process's current working
// directory.
func EmbeddedConversationalCorpus() ([]ConversationalCase, error) {
	return parseConversationalCorpus(embeddedConversationalCorpusJSON, "<embedded>")
}

// parseConversationalCorpus is LoadConversationalCorpus/
// EmbeddedConversationalCorpus's shared unmarshal-then-validate tail,
// mirroring parseCorpus's (corpus.go) shape for the coding-agent corpus.
func parseConversationalCorpus(data []byte, source string) ([]ConversationalCase, error) {
	var cases []ConversationalCase
	if err := json.Unmarshal(data, &cases); err != nil {
		return nil, fmt.Errorf("eval: parse conversational corpus %s: %w", source, err)
	}
	if err := validateConversationalCases(cases, source); err != nil {
		return nil, err
	}
	return cases, nil
}

// validateConversationalCases enforces every per-case rule plus the
// per-kind coverage floor (MinCasesPerKind) in one pass.
func validateConversationalCases(cases []ConversationalCase, source string) error {
	seenIDs := make(map[string]bool, len(cases))
	perKind := make(map[QuestionKind]int, len(AllQuestionKinds))

	for i, c := range cases {
		if seenIDs[c.ID] {
			return fmt.Errorf("eval: conversational corpus %s: duplicate case id %q (index %d)", source, c.ID, i)
		}
		seenIDs[c.ID] = true

		if !validQuestionKinds[c.Kind] {
			return fmt.Errorf("eval: conversational corpus %s: case %q (index %d) has invalid kind %q", source, c.ID, i, c.Kind)
		}
		if !validLanguages[c.Language] {
			return fmt.Errorf("eval: conversational corpus %s: case %q (index %d) has invalid language %q", source, c.ID, i, c.Language)
		}
		if c.Query == "" {
			return fmt.Errorf("eval: conversational corpus %s: case %q (index %d) missing query", source, c.ID, i)
		}

		if c.Kind == KindAbsence {
			if !c.ExpectAbsence {
				return fmt.Errorf("eval: conversational corpus %s: case %q (index %d) has kind %q but expect_absence is not true (spec: an absence case's gold answer IS \"no evidence exists\")", source, c.ID, i, KindAbsence)
			}
			if c.ObservationID != "" || c.ExpectedFact != "" {
				return fmt.Errorf("eval: conversational corpus %s: case %q (index %d) is kind %q but carries an observation_id/expected_fact — an absence case's gold answer is that NOTHING should surface, so neither field belongs here", source, c.ID, i, KindAbsence)
			}
		} else {
			if c.ExpectAbsence {
				return fmt.Errorf("eval: conversational corpus %s: case %q (index %d) has kind %q but expect_absence is true — only %q cases may set it", source, c.ID, i, c.Kind, KindAbsence)
			}
			if c.ExpectedFact == "" {
				return fmt.Errorf("eval: conversational corpus %s: case %q (index %d) missing expected_fact", source, c.ID, i)
			}
			// ObservationID is intentionally NOT required here: identity
			// cases legitimately have none until P1 ships (see the struct
			// doc above) and are scored on Grounded instead.
		}

		perKind[c.Kind]++
	}

	for _, k := range AllQuestionKinds {
		if perKind[k] < MinCasesPerKind {
			return fmt.Errorf("eval: conversational corpus %s: kind %q has %d case(s), want at least %d (every question kind must be measurable — see QuestionKind's doc)", source, k, perKind[k], MinCasesPerKind)
		}
	}

	return nil
}
