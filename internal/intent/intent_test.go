package intent

import "testing"

// TestClassify_RepresentativeCuesPerIntent is a smoke test: one clear ES
// and one clear EN example per intent, straight from the plan's own cue
// list (docs/conversational-retrieval-plan.md, P2 routing table). The
// precision/recall gate against the full labelled corpus lives in
// fixtures_test.go; this test exists so a regression in any single
// intent's table entry fails with an intent-specific message instead of
// only moving an aggregate percentage.
func TestClassify_RepresentativeCuesPerIntent(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  Intent
	}{
		{"identity/es", "¿qué es Omnia?", Identity},
		{"identity/en", "what is Omnia", Identity},
		{"status/es", "¿cómo va Workly?", Status},
		{"status/en", "how is Workly going", Status},
		{"delta/es", "¿cómo arreglamos el bug de staleness?", Delta},
		{"delta/en", "how did we fix the sync timeout", Delta},
		{"delta/raw-error", "panic: runtime error: invalid memory address", Delta},
		{"open_items/es", "¿qué falta para terminar P2?", OpenItems},
		{"open_items/en", "what's left before we ship P2", OpenItems},
		{"rationale/es", "¿por qué elegimos RRF en vez de un solo ranking?", Rationale},
		{"rationale/en", "why did we choose reciprocal rank fusion", Rationale},
		{"cross_project/es", "¿en qué estoy bloqueado?", CrossProject},
		{"cross_project/en", "what am I blocked on across everything", CrossProject},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.query)
			if got.Intent != tc.want {
				t.Errorf("Classify(%q) = %q (confidence %.2f, cue %q); want %q", tc.query, got.Intent, got.Confidence, got.Cue, tc.want)
			}
			if got.Confidence < DefaultConfidenceThreshold {
				t.Errorf("Classify(%q) confidence %.2f is below DefaultConfidenceThreshold %.2f despite a committed intent", tc.query, got.Confidence, DefaultConfidenceThreshold)
			}
		})
	}
}

// TestClassify_UnknownIsSafeDefault covers the plan's explicit safety
// property: "Unclassified -> today's behaviour, byte-for-byte." Neutral,
// off-topic, or empty queries must never be forced into one of the six
// real intents.
func TestClassify_UnknownIsSafeDefault(t *testing.T) {
	cases := []string{
		"",
		"hola",
		"gracias",
		"the quick brown fox jumps over the lazy dog",
		"list all observations from last month",
		"muéstrame las últimas notas",
	}
	for _, query := range cases {
		got := Classify(query)
		if got.Intent != Unknown {
			t.Errorf("Classify(%q) = %q (confidence %.2f, cue %q); want %q", query, got.Intent, got.Confidence, got.Cue, Unknown)
		}
	}
}

// TestClassify_HighestConfidenceWinsOverTableOrder is the worked example
// from ClassifyWithThreshold's doc comment: a query that lexically contains
// both identity's cue ("qué es") and open_items' cue ("que falta") must
// resolve to the higher-confidence rule (open_items, 0.9) rather than
// whichever rule happens to sit earlier in the signals table.
func TestClassify_HighestConfidenceWinsOverTableOrder(t *testing.T) {
	got := Classify("qué es lo que falta para terminar P2")
	if got.Intent != OpenItems {
		t.Fatalf("Classify(%q) = %q (confidence %.2f, cue %q); want %q (open_items' 0.9 confidence must outscore identity's 0.8, regardless of table order)",
			"qué es lo que falta para terminar P2", got.Intent, got.Confidence, got.Cue, OpenItems)
	}
}

// TestClassify_StatusOutscoresIdentityOnStatusOfPhrasing checks the other
// classic substring collision the signals table doc comment calls out:
// "what is the status of X" legitimately contains "what is" (identity's
// cue) as a substring, but the query is a status question.
func TestClassify_StatusOutscoresIdentityOnStatusOfPhrasing(t *testing.T) {
	got := Classify("what is the status of the embedding migration")
	if got.Intent != Status {
		t.Fatalf("Classify(%q) = %q; want %q", "what is the status of the embedding migration", got.Intent, Status)
	}
}

// TestClassifyWithThreshold_RaisingThresholdDowngradesWeakMatch exercises
// the threshold mechanism itself: Delta's raw-error-string heuristic is
// deliberately calibrated at 0.75 (below DefaultConfidenceThreshold's
// sibling rules, all >= 0.8), specifically so a caller that raises its own
// threshold above 0.75 sees that match downgraded to Unknown instead of a
// silently-unreachable code path.
func TestClassifyWithThreshold_RaisingThresholdDowngradesWeakMatch(t *testing.T) {
	query := "NullPointerException: cannot read property of undefined"

	got := ClassifyWithThreshold(query, 0.6)
	if got.Intent != Delta {
		t.Fatalf("ClassifyWithThreshold(%q, 0.6) = %q; want %q", query, got.Intent, Delta)
	}

	got = ClassifyWithThreshold(query, 0.8)
	if got.Intent != Unknown {
		t.Fatalf("ClassifyWithThreshold(%q, 0.8) = %q; want %q (0.75-confidence match must be downgraded above its own score)", query, got.Intent, Unknown)
	}
	if got.Confidence != 0.75 {
		t.Errorf("ClassifyWithThreshold(%q, 0.8).Confidence = %.2f; want 0.75 (raw best score preserved even when downgraded)", query, got.Confidence)
	}
}

// TestClassify_EqualsClassifyWithThresholdDefault pins Classify as a thin
// wrapper, so the two never silently drift apart.
func TestClassify_EqualsClassifyWithThresholdDefault(t *testing.T) {
	queries := []string{"qué es Omnia", "how is Workly going", "random unrelated text"}
	for _, q := range queries {
		a := Classify(q)
		b := ClassifyWithThreshold(q, DefaultConfidenceThreshold)
		if a != b {
			t.Errorf("Classify(%q) = %+v; ClassifyWithThreshold(%q, DefaultConfidenceThreshold) = %+v; must be equal", q, a, q, b)
		}
	}
}
