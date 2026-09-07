package intent

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

// corpusCasesPath is a relative path, not a Go import of internal/eval:
// this package must stay a dependency-free leaf in its non-test code (see
// intent.go's package doc), and even in test code we deliberately avoid
// importing internal/eval as a package — reading its testdata JSON by path
// is a test-only, one-directional data dependency, not a code dependency.
const corpusCasesPath = "../eval/testdata/conversational_cases.json"

// corpusCase mirrors the fields of internal/eval/testdata/conversational_cases.json
// this test actually needs. Extra JSON fields (expected_fact,
// observation_id, notes, ...) are simply ignored by encoding/json.
type corpusCase struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	Query         string `json:"query"`
	Language      string `json:"language"`
	ExpectAbsence bool   `json:"expect_absence"`
}

// kindToIntent maps the corpus's JSON "kind" strings to this package's
// Intent values. "absence" has no entry: absence is a property of the
// ANSWER (whether grounding evidence exists), not of the query's
// grammatical form, so it has no corresponding intent to classify against
// — those cases are skipped entirely, not scored as Unknown.
var kindToIntent = map[string]Intent{
	"identity":      Identity,
	"status":        Status,
	"delta":         Delta,
	"open_items":    OpenItems,
	"rationale":     Rationale,
	"cross_project": CrossProject,
}

func loadCorpusCases(t *testing.T) []corpusCase {
	t.Helper()
	raw, err := os.ReadFile(corpusCasesPath)
	if err != nil {
		t.Fatalf("reading %s: %v (this is the independent corpus the ship gate below is measured against — it must exist and be readable)", corpusCasesPath, err)
	}
	var cases []corpusCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("unmarshaling %s: %v", corpusCasesPath, err)
	}
	if len(cases) == 0 {
		t.Fatalf("%s parsed to zero cases; corpus format may have changed", corpusCasesPath)
	}
	return cases
}

// TestClassify_BlindCorpusPrecisionGate_MustBeAtLeast0_90 is the REAL ship
// gate (plan P2: "Ship only if precision >= 0.9 ... measured against the
// hand-labelled corpus"), replacing the self-authored fixtures.go gate.
//
// The prior version of this package validated itself only against
// fixtures_test.go's fixtures, which were authored WITH the regex cue
// table in view — they proved self-consistency, not classifier quality,
// and reported precision 1.0000 while the classifier's real precision
// (measured here) was 0.4615 overall with identity precision at 0.400.
// That gap is exactly the failure mode this test exists to prevent from
// recurring: a classifier that only ever grades its own homework will
// report 1.0 forever.
//
// This test loads internal/eval/testdata/conversational_cases.json — a
// corpus authored independently of this package's cue table — classifies
// every non-absence case, and asserts precision >= 0.90 PER INTENT (not
// just in aggregate), matching the plan's own measurement note: "each of
// the seven classes scored separately — today they are all one number,
// which is why this problem was invisible."
//
// Precision, like fixtures_test.go's gate, is defined over COMMITTED
// (non-Unknown) predictions: an abstention (Unknown on a case whose true
// kind is one of the six real intents) costs recall, logged but not
// gated, because an abstention is not a misroute — it is this package's
// documented safe default (see intent.go's package doc).
func TestClassify_BlindCorpusPrecisionGate_MustBeAtLeast0_90(t *testing.T) {
	const minPrecision = 0.90

	cases := loadCorpusCases(t)

	type stats struct {
		tp, fp, fn int
	}
	byIntent := map[Intent]*stats{
		Identity:     {},
		Status:       {},
		Delta:        {},
		OpenItems:    {},
		Rationale:    {},
		CrossProject: {},
	}

	var scored, correct int
	var wrong []string

	for _, c := range cases {
		if c.Kind == "absence" || c.ExpectAbsence {
			// Absence is a property of the answer, not the query's
			// grammatical form (see kindToIntent's doc comment) — no
			// intent to classify against, so it is excluded from scoring,
			// exactly as the plan's cross-validation run excluded it.
			continue
		}
		want, ok := kindToIntent[c.Kind]
		if !ok {
			t.Fatalf("corpus case %q has unrecognized kind %q; kindToIntent needs an entry (or this case needs expect_absence handling)", c.ID, c.Kind)
		}
		scored++

		got := Classify(c.Query)
		if got.Intent == want {
			correct++
			byIntent[want].tp++
		} else {
			wrong = append(wrong, fmt.Sprintf("[%s] Classify(%q) = %q (confidence %.2f, cue %q); want %q", c.ID, c.Query, got.Intent, got.Confidence, got.Cue, want))
			if got.Intent != Unknown {
				if s, ok := byIntent[got.Intent]; ok {
					s.fp++
				}
			}
			if s, ok := byIntent[want]; ok {
				s.fn++
			}
		}
	}

	if scored == 0 {
		t.Fatal("zero non-absence cases scored from the corpus; precision is undefined")
	}

	t.Logf("OVERALL accuracy: %d/%d = %.4f", correct, scored, float64(correct)/float64(scored))

	var failures []string
	for _, intent := range []Intent{Identity, Status, Delta, OpenItems, Rationale, CrossProject} {
		s := byIntent[intent]
		committed := s.tp + s.fp
		var precision float64
		if committed > 0 {
			precision = float64(s.tp) / float64(committed)
		}
		total := s.tp + s.fn
		var recall float64
		if total > 0 {
			recall = float64(s.tp) / float64(total)
		}
		t.Logf("kind %-13s tp=%d fp=%d fn=%d precision=%.4f recall=%.4f", intent, s.tp, s.fp, s.fn, precision, recall)

		if committed == 0 {
			t.Logf("kind %s: classifier never committed to this intent on the corpus; precision undefined, not gated", intent)
			continue
		}
		if precision < minPrecision {
			failures = append(failures, fmt.Sprintf("%s precision %.4f (%d/%d) < %.2f", intent, precision, s.tp, committed, minPrecision))
		}
	}

	if len(failures) > 0 {
		t.Errorf("SHIP GATE FAILED against the blind corpus (%s): %v\nMisclassified cases:\n%s", corpusCasesPath, failures, joinLines(wrong))
	}
}
