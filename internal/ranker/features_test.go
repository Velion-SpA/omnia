package ranker

import (
	"testing"
	"time"

	"github.com/velion/omnia/internal/config"
)

func TestBuildFeaturesUsesOnlyExistingSignals(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	got := BuildFeatures(Candidate{
		LexicalRRF:     0.8,
		SemanticCosine: 0.75,
		UpdatedAt:      now.Add(-7 * 24 * time.Hour),
		Type:           "decision",
		Outcome:        "worked",
		Judgment:       "compatible",
	}, config.RankingConfig{RecencyHalfLifeDays: 7}, now)
	for name, value := range got.Named() {
		if value < 0 || value > 1 {
			t.Fatalf("%s = %v, want normalized [0,1]", name, value)
		}
	}
	if got.Importance != 1 {
		t.Fatalf("importance = %v, want DefaultImportanceWeight(decision) normalized to 1", got.Importance)
	}
	if got.OutcomeHistory != 1 {
		t.Fatalf("outcome/judgment history = %v, want worked/compatible positive signal", got.OutcomeHistory)
	}
}

func TestLabelUsesOnlyOutcomeAndJudgmentVocabulary(t *testing.T) {
	for _, tc := range []struct {
		c    Candidate
		want int
		ok   bool
	}{
		{Candidate{Outcome: "worked"}, 1, true},
		{Candidate{Judgment: "compatible"}, 1, true},
		{Candidate{Outcome: "did_not_work"}, 0, true},
		{Candidate{Judgment: "supersedes"}, 0, true},
		{Candidate{Judgment: "conflicts_with"}, 0, true},
		{Candidate{}, 0, false},
	} {
		got, ok := Label(tc.c)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("Label(%+v) = (%d,%v), want (%d,%v)", tc.c, got, ok, tc.want, tc.ok)
		}
	}
}
