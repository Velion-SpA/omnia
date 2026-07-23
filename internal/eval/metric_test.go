package eval

import "testing"

func TestQualityPer1kTokens_Formula(t *testing.T) {
	tests := []struct {
		name        string
		accuracy    float64
		totalTokens int
		want        float64
	}{
		{"typical run", 0.8, 4000, 0.2},
		{"perfect accuracy, 1k tokens", 1.0, 1000, 1.0},
		{"zero tokens is defined as zero, not +Inf", 0.9, 0, 0},
		{"negative tokens (malformed input) also zero", 0.9, -5, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := QualityPer1kTokens(tt.accuracy, tt.totalTokens)
			if got != tt.want {
				t.Errorf("QualityPer1kTokens(%v, %d) = %v, want %v", tt.accuracy, tt.totalTokens, got, tt.want)
			}
		})
	}
}

func TestParetoPoint_IncludesTokenBreakdown(t *testing.T) {
	breakdown := TokenBreakdown{QueryEmbed: 50, Retrieval: 200, InjectedContext: 700, Judge: 50}
	point := NewParetoPoint("jina-embeddings-v2-base-es", 0.75, breakdown)

	if point.Model != "jina-embeddings-v2-base-es" {
		t.Errorf("Model = %q, want jina-embeddings-v2-base-es", point.Model)
	}
	if point.Accuracy != 0.75 {
		t.Errorf("Accuracy = %v, want 0.75", point.Accuracy)
	}
	wantTokens := 50 + 200 + 700 + 50
	if point.Tokens != wantTokens {
		t.Errorf("Tokens = %d, want %d (derived from Breakdown.Total())", point.Tokens, wantTokens)
	}
	if point.Breakdown != breakdown {
		t.Errorf("Breakdown = %+v, want %+v (must preserve the itemized cost)", point.Breakdown, breakdown)
	}
	wantQPT := QualityPer1kTokens(0.75, wantTokens)
	if got := point.QualityPer1kTokens(); got != wantQPT {
		t.Errorf("QualityPer1kTokens() = %v, want %v", got, wantQPT)
	}
}

func TestTokenBreakdown_Total(t *testing.T) {
	b := TokenBreakdown{QueryEmbed: 1, Retrieval: 2, InjectedContext: 3, Judge: 4}
	if got := b.Total(); got != 10 {
		t.Errorf("Total() = %d, want 10", got)
	}
}

func TestReport_RejectsAggregateOnly(t *testing.T) {
	t.Run("empty frontier is rejected", func(t *testing.T) {
		f := Frontier{}
		if err := f.Validate(); err == nil {
			t.Error("expected error for a frontier with zero points, got nil")
		}
	})

	t.Run("point without a token breakdown is rejected", func(t *testing.T) {
		f := Frontier{Points: []ParetoPoint{{Model: "bare-scalar", Accuracy: 0.9, Tokens: 0}}}
		if err := f.Validate(); err == nil {
			t.Error("expected error for a point with no token breakdown (a bare aggregate ratio), got nil")
		}
	})

	t.Run("valid frontier with breakdown is accepted", func(t *testing.T) {
		f := Frontier{Points: []ParetoPoint{
			NewParetoPoint("jina", 0.8, TokenBreakdown{QueryEmbed: 10, Retrieval: 20, InjectedContext: 30, Judge: 0}),
			NewParetoPoint("bge-m3", 0.75, TokenBreakdown{QueryEmbed: 15, Retrieval: 25, InjectedContext: 40, Judge: 5}),
		}}
		if err := f.Validate(); err != nil {
			t.Errorf("expected a valid multi-point frontier to pass Validate(), got: %v", err)
		}
	})
}
