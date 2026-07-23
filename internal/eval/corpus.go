// Package eval implements the Omnia memory-quality eval harness (spec
// sdd/omnia-eval-harness): a token-cost-normalized measuring stick that
// extends, rather than forks, the existing recall-eval mechanism in
// internal/embed (ABPair/RunModelAB/testdata/ab_pairs.json).
package eval

import (
	"encoding/json"
	"fmt"
	"os"
)

// Capability tags every EvalCase with exactly one memory ability it probes
// (spec EVAL-2). A case is never untagged or multi-tagged.
type Capability string

const (
	CapabilityRecall           Capability = "recall"
	CapabilityCausal           Capability = "causal"
	CapabilityStateUpdate      Capability = "state_update"
	CapabilityStateAbstraction Capability = "state_abstraction"
)

// validCapabilities is the closed set of tags LoadCorpus accepts — an empty
// or unrecognized capability fails fast rather than silently defaulting to
// Recall (spec EVAL-2, EVAL-4 "missing supersedes relation fails fast" sibling
// rule: don't guess, reject).
var validCapabilities = map[Capability]bool{
	CapabilityRecall:           true,
	CapabilityCausal:           true,
	CapabilityStateUpdate:      true,
	CapabilityStateAbstraction: true,
}

// Language is the query language of an EvalCase. Spec EVAL-3 segments
// reporting by this field (EN-query vs. bilingual ES-query slices).
type Language string

const (
	LanguageEN Language = "en"
	LanguageES Language = "es"
)

// validLanguages is the closed set LoadCorpus accepts, mirroring
// validCapabilities: an empty or unrecognized (e.g. typo'd "En") language
// fails fast rather than silently landing report.go's accumulate in a stray
// third bucket keyed off the raw string value.
var validLanguages = map[Language]bool{
	LanguageEN: true,
	LanguageES: true,
}

// EvalCase is one eval-harness case: a query against a real, dogfooded Omnia
// memory (spec EVAL-2), tagged by capability and language, with the fact
// expected to surface. SupersedesOf is set only for adversarial contradiction
// cases (spec EVAL-4) and names the OLDER observation this case's
// ObservationID is expected to supersede; scoring that relation is
// contradiction.go's job (PR3), not the corpus loader's.
type EvalCase struct {
	ID            string     `json:"id"`
	ObservationID string     `json:"observation_id"`
	Capability    Capability `json:"capability"`
	Query         string     `json:"query"`
	Language      Language   `json:"language"`
	ExpectedFact  string     `json:"expected_fact"`
	SupersedesOf  *string    `json:"supersedes_of,omitempty"`
}

// MinCorpusSize and MaxCorpusSize are spec EVAL-2's hard bounds: the harness
// fails fast at load time if the corpus falls outside [50, 150] cases.
const (
	MinCorpusSize = 50
	MaxCorpusSize = 150
)

// LoadCorpus reads a JSON array of EvalCase from path and validates it
// against spec EVAL-2: case count MUST be in [MinCorpusSize, MaxCorpusSize],
// and every case MUST carry exactly one valid Capability, one valid
// Language, plus a non-empty ObservationID and ExpectedFact.
func LoadCorpus(path string) ([]EvalCase, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("eval: load corpus: %w", err)
	}
	cases, err := parseCorpus(data, path)
	if err != nil {
		return nil, err
	}
	if len(cases) < MinCorpusSize || len(cases) > MaxCorpusSize {
		return nil, fmt.Errorf("eval: corpus %s has %d cases, want between %d and %d (spec EVAL-2)", path, len(cases), MinCorpusSize, MaxCorpusSize)
	}
	return cases, nil
}

// parseCorpus unmarshals and validates per-case fields WITHOUT enforcing the
// overall size range, so callers (tests, and future tooling that inspects a
// growing corpus before it reaches the production floor) can check schema
// and traceability independently of LoadCorpus's fail-fast count gate.
func parseCorpus(data []byte, source string) ([]EvalCase, error) {
	var cases []EvalCase
	if err := json.Unmarshal(data, &cases); err != nil {
		return nil, fmt.Errorf("eval: parse corpus %s: %w", source, err)
	}
	seenIDs := make(map[string]bool, len(cases))
	for i, c := range cases {
		if seenIDs[c.ID] {
			return nil, fmt.Errorf("eval: corpus %s: duplicate case id %q (index %d)", source, c.ID, i)
		}
		seenIDs[c.ID] = true
		if !validCapabilities[c.Capability] {
			return nil, fmt.Errorf("eval: corpus %s: case %q (index %d) has invalid capability %q", source, c.ID, i, c.Capability)
		}
		if !validLanguages[c.Language] {
			return nil, fmt.Errorf("eval: corpus %s: case %q (index %d) has invalid language %q", source, c.ID, i, c.Language)
		}
		if c.ObservationID == "" {
			return nil, fmt.Errorf("eval: corpus %s: case %q (index %d) missing observation_id", source, c.ID, i)
		}
		if c.Query == "" {
			return nil, fmt.Errorf("eval: corpus %s: case %q (index %d) missing query", source, c.ID, i)
		}
		if c.ExpectedFact == "" {
			return nil, fmt.Errorf("eval: corpus %s: case %q (index %d) missing expected_fact", source, c.ID, i)
		}
	}
	return cases, nil
}
