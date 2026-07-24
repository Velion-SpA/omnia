package mcp

// type_lens_test.go — RED→GREEN tests for Omnia v0.3 "Context Economy" PR5
// (design obs #1643 section 3.5/ADR-6, spec obs #1642 type-as-lens domain,
// all 6 REQs). See preemption_invariant_test.go for the shared adversarial
// sentinel/signature-row invariant this pass must also satisfy — this file
// covers InferLensType's own signal-mapping table and ApplyTypeLens' own
// lift/no-op/neutral behavior.

import (
	"testing"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/store"
)

// ─── InferLensType ───────────────────────────────────────────────────────

// TestInferLensType_SignalMapping locks design section 3.5's ordered EN+ES
// signal table: first match wins, one query per row (plus a neutral
// no-match case landing on "" — spec: Neutral Context Leaves Ranking
// Unchanged).
func TestInferLensType_SignalMapping(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  string
	}{
		{"error EN", "why is this throwing an error", "bugfix"},
		{"panic EN", "goroutine panic on startup", "bugfix"},
		{"exception EN", "unhandled exception in handler", "bugfix"},
		{"crash EN", "the server crash happened at midnight", "bugfix"},
		{"stacktrace EN", "attach the stack trace please", "bugfix"},
		{"falla ES", "hay una falla en el login", "bugfix"},
		{"fallo ES", "el sistema tuvo un fallo", "bugfix"},
		{"fix EN", "how do I fix the broken build", "bugfix"},
		{"not working EN", "the button is not working", "bugfix"},
		{"no funciona ES", "el boton no funciona", "bugfix"},
		{"arregla ES", "necesito arreglar este bug", "bugfix"},
		{"decide EN", "help me decide between two approaches", "decision"},
		{"tradeoff EN", "what's the tradeoff here", "decision"},
		{"elegir ES", "tengo que elegir una opcion", "decision"},
		{"decidir ES", "como decidir la arquitectura", "decision"},
		{"architecture EN", "explain the system architecture", "architecture"},
		{"pattern EN", "which design pattern fits here", "architecture"},
		{"patron ES", "cual es el patrón recomendado", "architecture"},
		{"diseno ES", "revisemos el diseño del sistema", "architecture"},
		{"how to EN", "how to configure the server", "pattern"},
		{"steps EN", "what are the steps to deploy", "pattern"},
		{"procedure EN", "describe the procedure for onboarding", "pattern"},
		{"pasos ES", "cuales son los pasos a seguir", "pattern"},
		{"neutral", "what time is it in Buenos Aires", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := InferLensType(tc.query, "")
			if got != tc.want {
				t.Errorf("InferLensType(%q, \"\") = %q, want %q", tc.query, got, tc.want)
			}
		})
	}
}

// TestInferLensType_ExplicitTypeWins (spec: Explicit User Filter Always
// Wins): whenever the caller passed a non-empty explicitType, InferLensType
// ALWAYS returns "" — no lens — no matter how strong the query's own
// situational signal is.
func TestInferLensType_ExplicitTypeWins(t *testing.T) {
	got := InferLensType("there was a panic and a crash", "decision")
	if got != "" {
		t.Errorf("InferLensType with explicitType set = %q, want \"\" (explicit filter always wins)", got)
	}
}

// TestInferLensType_WordBoundaryNegatives pins the word-boundary fix
// (adversarial-review finding, v0.3): everyday dev vocabulary that merely
// CONTAINS a signal as a substring must stay neutral — "prefix"/"suffix"/
// "fixture"/"affix" must never trip the "fix" signal.
func TestInferLensType_WordBoundaryNegatives(t *testing.T) {
	for _, query := range []string{
		"test fixtures for the suite",
		"prefix matching algorithm",
		"suffix tree construction",
		"database fixture setup",
		"affix rules in linguistics",
	} {
		if got := InferLensType(query, ""); got != "" {
			t.Errorf("InferLensType(%q) = %q, want \"\" (substring must not fire inside unrelated words)", query, got)
		}
	}
}

// TestInferLensType_MultiSignalPrecedence pins first-match-wins TABLE order
// (bugfix > decision > architecture > pattern) for queries carrying more
// than one signal category — real production behavior that previously had
// no regression test (adversarial-review finding, v0.3): reordering
// lensSignals would silently change every one of these classifications.
func TestInferLensType_MultiSignalPrecedence(t *testing.T) {
	cases := []struct {
		query string
		want  string
	}{
		{"error handling patterns", "bugfix"},                  // bugfix beats architecture
		{"architecture decision for error recovery", "bugfix"}, // bugfix beats decision+architecture
		{"decide how to fix the pattern", "bugfix"},            // bugfix beats decision+pattern+architecture
		{"architecture decision record", "decision"},           // decision beats architecture
		{"how to document the architecture", "architecture"},   // architecture beats pattern
	}
	for _, tc := range cases {
		if got := InferLensType(tc.query, ""); got != tc.want {
			t.Errorf("InferLensType(%q) = %q, want %q (table-order precedence)", tc.query, got, tc.want)
		}
	}
}

// ─── ApplyTypeLens ────────────────────────────────────────────────────────

// TestApplyTypeLens_LiftsMatchingTypeStablePartition (spec: Situational Type
// Boost/Filter + Composes With Existing Importance Tiers): matching-type
// rows lift above non-matching rows, but relative order WITHIN each
// partition is preserved — a stable partition, never a full re-sort.
func TestApplyTypeLens_LiftsMatchingTypeStablePartition(t *testing.T) {
	results := []store.SearchResult{
		sr(1, "pattern", "2024-01-01", 4),
		sr(2, "bugfix", "2024-01-01", 3),
		sr(3, "decision", "2024-01-01", 2),
		sr(4, "bugfix", "2024-01-01", 1),
	}
	got := ApplyTypeLens(results, "bugfix", config.TypeLensConfig{Enabled: true})
	// bugfix rows (2, 4) lift first, keeping their own relative order; the
	// non-matching rest (1, 3) keep their own relative order after.
	wantIDs := []int64{2, 4, 1, 3}
	if !equalInt64s(idsOfResults(got), wantIDs) {
		t.Fatalf("ApplyTypeLens lift order = %v, want %v", idsOfResults(got), wantIDs)
	}
}

// TestApplyTypeLens_DisabledIsNoop is the backward-compat gate (spec:
// Disabled by Default, No-Op When Off): cfg.Enabled=false must return
// results completely untouched, same order, same values.
func TestApplyTypeLens_DisabledIsNoop(t *testing.T) {
	results := []store.SearchResult{sr(1, "bugfix", "2024-01-01", 2), sr(2, "decision", "2024-01-01", 1)}
	got := ApplyTypeLens(results, "bugfix", config.TypeLensConfig{Enabled: false})
	if !equalInt64s(idsOfResults(got), idsOfResults(results)) {
		t.Fatalf("ApplyTypeLens(disabled) reordered: got %v, want %v", idsOfResults(got), idsOfResults(results))
	}
}

// TestApplyTypeLens_EmptyLensTypeIsNoop covers the "no signal / explicit
// filter stood down" case: lensType=="" (whatever the reason InferLensType
// returned it) must never alter ranking, even when the gate is enabled.
func TestApplyTypeLens_EmptyLensTypeIsNoop(t *testing.T) {
	results := []store.SearchResult{sr(1, "bugfix", "2024-01-01", 2), sr(2, "decision", "2024-01-01", 1)}
	got := ApplyTypeLens(results, "", config.TypeLensConfig{Enabled: true})
	if !equalInt64s(idsOfResults(got), idsOfResults(results)) {
		t.Fatalf("ApplyTypeLens(lensType=\"\") reordered: got %v, want %v", idsOfResults(got), idsOfResults(results))
	}
}

// TestApplyTypeLens_NoMatchingRowsIsNoop: a lensType that matches nothing in
// the result set must not reorder anything (no accidental churn when the
// boost partition is empty).
func TestApplyTypeLens_NoMatchingRowsIsNoop(t *testing.T) {
	results := []store.SearchResult{sr(1, "decision", "2024-01-01", 2), sr(2, "architecture", "2024-01-01", 1)}
	got := ApplyTypeLens(results, "bugfix", config.TypeLensConfig{Enabled: true})
	if !equalInt64s(idsOfResults(got), idsOfResults(results)) {
		t.Fatalf("ApplyTypeLens(no matches) reordered: got %v, want %v", idsOfResults(got), idsOfResults(results))
	}
}

// TestApplyTypeLens_NeutralContextLeavesRankingUnchanged (spec: Neutral
// Context Leaves Ranking Unchanged) is the end-to-end pairing of
// InferLensType + ApplyTypeLens: a query with no situational signal at all
// produces lensType=="" from InferLensType, which ApplyTypeLens then treats
// as its own no-op — ranking stays byte-for-byte the pre-v0.3 baseline.
func TestApplyTypeLens_NeutralContextLeavesRankingUnchanged(t *testing.T) {
	results := []store.SearchResult{sr(1, "decision", "2024-01-01", 2), sr(2, "bugfix", "2024-01-01", 1)}
	lensType := InferLensType("what time is it", "")
	got := ApplyTypeLens(results, lensType, config.TypeLensConfig{Enabled: true})
	if !equalInt64s(idsOfResults(got), idsOfResults(results)) {
		t.Fatalf("neutral query context: got %v, want unchanged %v", idsOfResults(got), idsOfResults(results))
	}
}
