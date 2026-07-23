package eval

import (
	"os"
	"testing"
)

func TestSegment_Accuracy(t *testing.T) {
	tests := []struct {
		name string
		seg  Segment
		want float64
	}{
		{"zero cases is zero, not NaN", Segment{Total: 0, Hits: 0}, 0},
		{"half hits", Segment{Total: 4, Hits: 2}, 0.5},
		{"all hits", Segment{Total: 3, Hits: 3}, 1.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.seg.Accuracy(); got != tt.want {
				t.Errorf("Accuracy() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSegment_QualityPer1kTokens(t *testing.T) {
	seg := Segment{Total: 2, Hits: 1, TotalTokens: 500}
	want := QualityPer1kTokens(0.5, 500)
	if got := seg.QualityPer1kTokens(); got != want {
		t.Errorf("QualityPer1kTokens() = %v, want %v", got, want)
	}
}

func TestReport_AllFourCapabilityRowsPresent(t *testing.T) {
	// Even an empty run must produce a row for every capability — a missing
	// key would be indistinguishable from "not implemented yet".
	report := BuildReport(nil)

	for _, cap := range []Capability{CapabilityRecall, CapabilityCausal, CapabilityStateUpdate, CapabilityStateAbstraction} {
		seg, ok := report.ByCapability[cap]
		if !ok {
			t.Errorf("ByCapability missing row for capability %q", cap)
			continue
		}
		if seg.Label != string(cap) {
			t.Errorf("ByCapability[%q].Label = %q, want %q", cap, seg.Label, cap)
		}
	}
	if len(report.ByCapability) != 4 {
		t.Errorf("ByCapability has %d rows, want exactly 4", len(report.ByCapability))
	}
}

// TestReport_ENvsESRowsUseExistingABPairs verifies the language segmentation
// rides entirely on the harness's existing corpus (testdata/cases.json,
// which sources its ES cases from the same real dogfooded material the
// bilingual internal/embed/testdata/ab_pairs.json set already established —
// spec Purpose: "extends the existing recall-eval... rather than
// replacing"): no separate ES dataset is authored here, and both language
// rows are always present regardless of which cases a given run touches.
func TestReport_ENvsESRowsUseExistingABPairs(t *testing.T) {
	// The starter corpus (testdata/cases.json, task 1.3) is intentionally
	// below LoadCorpus's 50-case production floor, so parse it directly the
	// same way corpus_test.go's TestCase_TracesToObservationID does — this
	// is the ONE real, already-authored bilingual case set; nothing new is
	// authored here.
	parsed := readTestdataCases(t)

	results := make([]CaseResult, len(parsed))
	for i, c := range parsed {
		results[i] = CaseResult{Case: c, Hit: true, TotalTokens: 100}
	}
	report := BuildReport(results)

	en, ok := report.ByLanguage[LanguageEN]
	if !ok || en.Total == 0 {
		t.Error("expected a non-empty EN row sourced from the existing corpus")
	}
	es, ok := report.ByLanguage[LanguageES]
	if !ok || es.Total == 0 {
		t.Error("expected a non-empty ES row sourced from the existing corpus, not a new dataset")
	}
	if len(report.ByLanguage) != 2 {
		t.Errorf("ByLanguage has %d rows, want exactly 2 (en, es)", len(report.ByLanguage))
	}
}

// readTestdataCases parses testdata/cases.json without LoadCorpus's [50,150]
// size gate, mirroring corpus_test.go's own approach to the same starter
// file (see TestCase_TracesToObservationID).
func readTestdataCases(t *testing.T) []EvalCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/cases.json")
	if err != nil {
		t.Fatalf("read starter corpus: %v", err)
	}
	cases, err := parseCorpus(raw, "testdata/cases.json")
	if err != nil {
		t.Fatalf("parseCorpus(testdata/cases.json): %v", err)
	}
	return cases
}

func TestBuildReport_AggregatesHitsAndTokens(t *testing.T) {
	results := []CaseResult{
		{Case: EvalCase{Capability: CapabilityRecall, Language: LanguageEN}, Hit: true, TotalTokens: 50},
		{Case: EvalCase{Capability: CapabilityRecall, Language: LanguageES}, Hit: false, TotalTokens: 60},
		{Case: EvalCase{Capability: CapabilityCausal, Language: LanguageEN}, Hit: true, TotalTokens: 400},
	}
	report := BuildReport(results)

	recall := report.ByCapability[CapabilityRecall]
	if recall.Total != 2 || recall.Hits != 1 || recall.TotalTokens != 110 {
		t.Errorf("ByCapability[recall] = %+v, want Total=2 Hits=1 TotalTokens=110", recall)
	}
	causal := report.ByCapability[CapabilityCausal]
	if causal.Total != 1 || causal.Hits != 1 || causal.TotalTokens != 400 {
		t.Errorf("ByCapability[causal] = %+v, want Total=1 Hits=1 TotalTokens=400", causal)
	}
	// State-update and state-abstraction had zero cases in this run — the row
	// must still be present (per TestReport_AllFourCapabilityRowsPresent) but
	// with zero counts, not absent.
	stateUpdate := report.ByCapability[CapabilityStateUpdate]
	if stateUpdate.Total != 0 || stateUpdate.Hits != 0 {
		t.Errorf("ByCapability[state_update] = %+v, want a zeroed but present row", stateUpdate)
	}

	en := report.ByLanguage[LanguageEN]
	if en.Total != 2 || en.Hits != 2 {
		t.Errorf("ByLanguage[en] = %+v, want Total=2 Hits=2", en)
	}
	es := report.ByLanguage[LanguageES]
	if es.Total != 1 || es.Hits != 0 {
		t.Errorf("ByLanguage[es] = %+v, want Total=1 Hits=0", es)
	}
}
