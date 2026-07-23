package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// makeCases builds n minimal, individually-valid EvalCase entries (unique ID
// and observation_id, capability cycling through all four buckets) so size-
// range tests can isolate the count check from every other validation rule.
func makeCases(n int) []EvalCase {
	caps := []Capability{CapabilityRecall, CapabilityCausal, CapabilityStateUpdate, CapabilityStateAbstraction}
	cases := make([]EvalCase, n)
	for i := 0; i < n; i++ {
		cases[i] = EvalCase{
			ID:            fmt.Sprintf("case-%d", i),
			ObservationID: fmt.Sprintf("obs-%d", i),
			Capability:    caps[i%len(caps)],
			Query:         "query",
			Language:      LanguageEN,
			ExpectedFact:  "fact",
		}
	}
	return cases
}

func writeCasesFile(t *testing.T, cases []EvalCase) string {
	t.Helper()
	data, err := json.Marshal(cases)
	if err != nil {
		t.Fatalf("marshal cases: %v", err)
	}
	path := filepath.Join(t.TempDir(), "cases.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write temp corpus file: %v", err)
	}
	return path
}

func TestLoadCorpus_EnforcesSizeRange(t *testing.T) {
	tests := []struct {
		name    string
		count   int
		wantErr bool
	}{
		{"too few", 3, true},
		{"just below minimum", MinCorpusSize - 1, true},
		{"minimum boundary", MinCorpusSize, false},
		{"maximum boundary", MaxCorpusSize, false},
		{"just above maximum", MaxCorpusSize + 1, true},
		{"way too many", 200, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeCasesFile(t, makeCases(tt.count))
			_, err := LoadCorpus(path)
			if tt.wantErr && err == nil {
				t.Errorf("LoadCorpus with %d cases: expected error, got nil", tt.count)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("LoadCorpus with %d cases: unexpected error: %v", tt.count, err)
			}
		})
	}
}

func TestLoadCorpus_RequiresOneCapabilityTag(t *testing.T) {
	cases := makeCases(MinCorpusSize)
	cases[5].Capability = "" // untagged
	path := writeCasesFile(t, cases)
	if _, err := LoadCorpus(path); err == nil {
		t.Error("expected error for a case with no capability tag, got nil")
	}

	cases2 := makeCases(MinCorpusSize)
	cases2[5].Capability = Capability("not-a-real-capability")
	path2 := writeCasesFile(t, cases2)
	if _, err := LoadCorpus(path2); err == nil {
		t.Error("expected error for a case with an unknown capability tag, got nil")
	}
}

func TestLoadCorpus_RequiresQuery(t *testing.T) {
	cases := makeCases(MinCorpusSize)
	cases[5].Query = "" // missing query
	path := writeCasesFile(t, cases)
	if _, err := LoadCorpus(path); err == nil {
		t.Error("expected error for a case with an empty query, got nil")
	}
}

func TestLoadCorpus_RejectsDuplicateID(t *testing.T) {
	cases := makeCases(MinCorpusSize)
	cases[7].ID = cases[3].ID // duplicate ID
	path := writeCasesFile(t, cases)
	if _, err := LoadCorpus(path); err == nil {
		t.Error("expected error for two cases sharing the same ID, got nil")
	}
}

func TestLoadCorpus_MissingFile(t *testing.T) {
	if _, err := LoadCorpus(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("expected error for a missing corpus file, got nil")
	}
}

// TestCase_TracesToObservationID exercises spec EVAL-2's "case traces to a
// real observation" scenario against the real starter corpus
// (testdata/cases.json, seeded from actual dogfooded mem_save observations):
// every case must store the observation it came from plus an expected fact
// drawn from that observation's content. This is checked below LoadCorpus's
// size gate (via parseCorpus) because the starter corpus is intentionally a
// small seed, not yet the full 50-150 case set — see task 1.3.
func TestCase_TracesToObservationID(t *testing.T) {
	data, err := os.ReadFile("testdata/cases.json")
	if err != nil {
		t.Fatalf("read starter corpus: %v", err)
	}
	cases, err := parseCorpus(data, "testdata/cases.json")
	if err != nil {
		t.Fatalf("parseCorpus(testdata/cases.json): %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("starter corpus has no cases")
	}

	// NOTE: this starter corpus intentionally has no SupersedesOf-tagged case.
	// A genuine contradiction fixture needs a real `supersedes` relation, and
	// none exist in the DB today — inventing one here would be a fabricated
	// claim. That fixture is PR3's responsibility (spec EVAL-4); see
	// openspec/changes/omnia-eval-harness/tasks.md task 1.3. The field stays
	// supported in the schema (checked below when present) so PR3 can adopt
	// it without a schema change.
	for _, c := range cases {
		if c.ObservationID == "" {
			t.Errorf("case %q: empty ObservationID (must trace to a real observation)", c.ID)
		}
		if c.ExpectedFact == "" {
			t.Errorf("case %q: empty ExpectedFact (must be drawn from the observation's content)", c.ID)
		}
		if c.Query == "" {
			t.Errorf("case %q: empty Query", c.ID)
		}
		if !validCapabilities[c.Capability] {
			t.Errorf("case %q: invalid capability %q", c.ID, c.Capability)
		}
		if c.Language != LanguageEN && c.Language != LanguageES {
			t.Errorf("case %q: unexpected language %q", c.ID, c.Language)
		}
		if c.SupersedesOf != nil && *c.SupersedesOf == "" {
			t.Errorf("case %q: SupersedesOf is set but empty", c.ID)
		}
	}
}
