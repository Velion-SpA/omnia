package mcp

// preemption_invariant_test.go — the shared adversarial pre-emption
// invariant for Omnia v0.3's "Context Economy" family of injection passes
// (design obs #1643 section 5, spec obs #1642 injection-budget REQ5 /
// injection-diversity REQ4 / type-as-lens REQ5): every pass wired into
// handleSearch's pipeline AFTER RankResults/ApplyStalenessDownrank —
// ApplyTokenBudget (PR2, this file's only entry so far), ApplyMMR (PR4),
// ApplyTypeLens (PR5) — MUST keep the topic_key exact-match sentinel
// (Rank == exactSentinelRank) and any SignatureMatch row present, COMPLETE
// (never trimmed/altered), and ordered strictly before every non-pre-empted
// row, no matter how adversarially that pass is configured.
//
// This file is EXTENDED, never duplicated, by PR4 and PR5: each adds its own
// entry to preemptionInvariantCases below instead of writing a parallel
// invariant test file.

import (
	"strings"
	"testing"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/store"
)

// preemptionInvariantCase names one injection-economy pass under test,
// wrapped behind the most adversarial config that pass accepts — the worst
// case for the sentinel/signature guarantee.
type preemptionInvariantCase struct {
	name  string
	apply func([]store.SearchResult) []store.SearchResult
}

// preemptionInvariantCases is the shared table. PR4 (ApplyMMR) and PR5
// (ApplyTypeLens) append their own entries here.
var preemptionInvariantCases = []preemptionInvariantCase{
	{
		name: "ApplyTokenBudget",
		apply: func(results []store.SearchResult) []store.SearchResult {
			// MaxTokens=1 is the most adversarial budget possible: every
			// non-pre-empted row should be dropped, yet pre-empted rows
			// must still survive complete and first.
			return ApplyTokenBudget(results, config.TokenBudgetConfig{Enabled: true, MaxTokens: 1})
		},
	},
}

// preemptionFixture builds a result set with one topic_key sentinel row, one
// signature-match row, and several large "rest" rows whose content is big
// enough that any adversarial pass in preemptionInvariantCases would drop or
// alter them if the pre-emption guarantee were broken.
func preemptionFixture() []store.SearchResult {
	sentinel := sr(99, "manual", "2024-01-01", 0)
	sentinel.Rank = exactSentinelRank
	sentinel.Content = "sentinel row content"

	sig := sr(50, "bugfix", "2024-01-01", 0)
	sig.SignatureMatch = true
	sig.Content = "signature row content"

	big := strings.Repeat("adversarial filler content ", 200) // large enough to exceed any tiny budget
	rest := []store.SearchResult{
		tbSR(1, big, 1),
		tbSR(2, big, 2),
		tbSR(3, big, 3),
	}

	out := make([]store.SearchResult, 0, 2+len(rest))
	out = append(out, sentinel, sig)
	out = append(out, rest...)
	return out
}

// TestPreemptionInvariant_SentinelAndSignatureSurviveEveryPass runs every
// registered pass against preemptionFixture under adversarial params and
// asserts the sentinel/signature rows are always present, first, and
// unaltered — regardless of how aggressively the pass trims/reorders/drops
// everything else.
func TestPreemptionInvariant_SentinelAndSignatureSurviveEveryPass(t *testing.T) {
	for _, tc := range preemptionInvariantCases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.apply(preemptionFixture())

			if len(got) < 2 {
				t.Fatalf("%s: expected at least the 2 pre-empted rows to survive, got %d rows", tc.name, len(got))
			}
			if got[0].ID != 99 || got[0].Rank != exactSentinelRank {
				t.Errorf("%s: expected sentinel row (id 99) first, got id=%d rank=%v", tc.name, got[0].ID, got[0].Rank)
			}
			if got[0].Content != "sentinel row content" {
				t.Errorf("%s: sentinel row content altered/truncated: %q", tc.name, got[0].Content)
			}
			if got[1].ID != 50 || !got[1].SignatureMatch {
				t.Errorf("%s: expected signature row (id 50) second, got id=%d signatureMatch=%v", tc.name, got[1].ID, got[1].SignatureMatch)
			}
			if got[1].Content != "signature row content" {
				t.Errorf("%s: signature row content altered/truncated: %q", tc.name, got[1].Content)
			}
		})
	}
}
