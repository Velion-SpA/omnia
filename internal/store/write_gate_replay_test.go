package store

import (
	"testing"

	"github.com/velion/omnia/internal/similarity"
)

// ─── Task 3.7 — write-gate calibration against real obs #1662 clusters ──────
//
// These are replay-style fixtures MODELED ON the actual near-duplicate/
// duplicate shapes found in the real production DB during the omnia-0.3.1
// use-case battery (obs #1662, "1660 observations, 52 projects"):
//
//   (a) the #1360/#1359 pair: two write-ups of the SAME incident (same
//       underlying commit/fix), independently drafted from different
//       projects with very different wording — obs #1662 calls this "dedupe
//       by wording, not by fact" and explicitly says this pair "survives
//       intact" under v0.3's MMR-based read-time dedup. The write-gate MUST
//       NOT misclassify this as a duplicate at write-time either.
//   (b) a near-verbatim repeat (session-summary style), differing only in
//       whitespace/punctuation — MUST fire NOOP (save-normalization spec REQ
//       "Canonical Normalization For Comparison": such differences are
//       exactly what normalization treats as content-identical).
//   (c) the "4 near-identical embeddinggemma SDD artifacts" cluster obs #1662
//       found ("demoted" by MMR at read-time, -38.6% tokens) — modeled here
//       as 4 independently-drafted write-ups of the same SDD deliverable.
//       The write-gate MUST catch at least the closest pair at write-time
//       (AUTO-UPDATE), not let them silently pile up as obs #1662 observed
//       they do today.
//
// Each fixture's actual Jaccard score is asserted explicitly (not just
// inferred from the resulting decision) so a future similarity.Jaccard
// change that silently shifts these numbers fails loudly here instead of
// quietly breaking calibration. If noop_threshold=0.98 ever misfires on any
// of these fixtures, this file is where the value gets revisited (design
// obs #1668 "Open Questions" #1) — the ANSWER so far: 0.98/0.9 (the design
// defaults) classify all three cases correctly, see the recorded scores
// below.

// TestWriteGateReplay_SameIncidentDifferentWordingDoesNotMisfire is fixture
// (a). Observed: jaccard = 0.2458 (well below both thresholds) — the gate
// correctly falls through to RELATE, never NOOP/AUTO-UPDATE, preserving
// obs #1662's "dedupe by wording, not by fact" framing at write-time too.
func TestWriteGateReplay_SameIncidentDifferentWordingDoesNotMisfire(t *testing.T) {
	s := newWriteGateStore(t, gateEnabledDefaults)

	write1359 := `What: Fixed the omnia local install exit 137 crash by re-signing the binary with codesign -s - -f after every go build, since macOS Gatekeeper kills unsigned rebuilt binaries on launch.
Why: The locally built omnia binary was dying immediately with exit code 137 right after a fresh install, blocking all local testing of new features.
Where: Makefile install target, local dev workflow for the omnia CLI binary.
Learned: This only reproduces on macOS with Gatekeeper enabled; codesigning ad-hoc with the dash identity is enough to satisfy the OS without a real certificate.`

	write1360 := `What: Root-caused and resolved a Claude Code MCP integration failure where the local omnia server process terminated with signal 137 shortly after every reinstall.
Why: Benja could not use the newly built MCP tools locally because the daemon kept dying, and the failure looked like an OOM kill at first glance.
Where: internal build/install scripts that produce the omnia executable consumed by the Claude Code MCP client.
Learned: exit 137 here was Gatekeeper's unsigned-binary kill, NOT memory pressure — an ad-hoc codesign call on the freshly built artifact fixes it every time.`

	const wantJaccard = 0.2458
	if got := similarity.Jaccard(similarity.Tokenize(write1359), similarity.Tokenize(write1360)); diffAbs(got, wantJaccard) > 0.0005 {
		t.Fatalf("fixture drifted: expected jaccard ~%.4f, got %.4f", wantJaccard, got)
	}

	existing := mustSave(t, s, AddObservationParams{
		Title:   "omnia local install exit 137 crash (codesign)",
		Content: write1359,
		Type:    "bugfix",
	})
	got := mustSave(t, s, AddObservationParams{
		Title:   "Claude Code MCP omnia exit 137 crash root-cause",
		Content: write1360,
		Type:    "bugfix",
	})

	if got.Similarity >= 0.9 {
		t.Fatalf("calibration failed: same-incident-different-wording pair scored %.4f (>= 0.9) — noop_threshold/update_threshold would misfire on real obs #1360/#1359-shaped data", got.Similarity)
	}
	if got.Decision == WriteGateDecisionNoop || got.Decision == WriteGateDecisionUpdate {
		t.Fatalf("expected decision relate/save (below threshold), got %q against existing #%d", got.Decision, existing.ID)
	}
}

// TestWriteGateReplay_NearVerbatimRepeatFiresNoop is fixture (b). Observed:
// jaccard = 1.0000 (whitespace/punctuation-only diff collapses to an
// identical token set) — comfortably >= noop_threshold (0.98), fires NOOP.
func TestWriteGateReplay_NearVerbatimRepeatFiresNoop(t *testing.T) {
	s := newWriteGateStore(t, gateEnabledDefaults)

	summary1 := "Session summary: completed the write-hygiene PR3 store core, ran full test suite, all green."
	summary2 := "Session summary:   completed the write-hygiene PR3 store core, ran full test suite,   all green.  "

	const wantJaccard = 1.0
	if got := similarity.Jaccard(similarity.Tokenize(summary1), similarity.Tokenize(summary2)); diffAbs(got, wantJaccard) > 0.0005 {
		t.Fatalf("fixture drifted: expected jaccard ~%.4f, got %.4f", wantJaccard, got)
	}

	existing := mustSave(t, s, AddObservationParams{Title: "Session wrap-up", Content: summary1})
	got := mustSave(t, s, AddObservationParams{Title: "Session wrap-up notes", Content: summary2})

	if got.Decision != WriteGateDecisionNoop {
		t.Fatalf("expected decision %q for a whitespace/punctuation-only repeat, got %q (similarity %.4f)", WriteGateDecisionNoop, got.Decision, got.Similarity)
	}
	if got.TargetID == nil || *got.TargetID != existing.ID {
		t.Fatalf("expected NOOP target to be the existing row %d, got %v", existing.ID, got.TargetID)
	}
}

// TestWriteGateReplay_EmbeddingGemmaClusterClosestPairAutoUpdates is fixture
// (c). Four independently-drafted write-ups of the same SDD deliverable (the
// shape obs #1662 found demoted -38.6% by read-time MMR). Observed pairwise
// jaccard: c1-c2=0.9535, c1-c3=0.8667, c1-c4=0.5370, c2-c3=0.8261,
// c2-c4=0.5091, c3-c4=0.5091 — the CLOSEST pair (c1, c2) lands in the
// AUTO-UPDATE band (strictly > 0.9, < 0.98), which this test locks in: with
// the gate on, saving c2 right after c1 must extend #1 in place, not
// silently create a second near-duplicate row the way v0.3 (or the gate
// disabled) would.
func TestWriteGateReplay_EmbeddingGemmaClusterClosestPairAutoUpdates(t *testing.T) {
	s := newWriteGateStore(t, gateEnabledDefaults)

	c1 := `Delivered the EmbeddingGemma evidence artifact for Omnia v0.2 mapping every new semantic recall feature to its real proof, comparing EmbeddingGemma against jina for accuracy and accessibility, and captured four live demos end to end covering semantic search, structural forgetting, procedural memory, and embedding quality across the whole test battery`
	c2 := `Delivered the EmbeddingGemma evidence artifact for Omnia v0.2 mapping every new semantic recall feature to its real proof, comparing EmbeddingGemma against jina for precision and accessibility, and captured four live demos end to end covering semantic search, structural forgetting, procedural memory, and embedding quality across the whole test battery`

	const wantJaccard = 0.9535
	if got := similarity.Jaccard(similarity.Tokenize(c1), similarity.Tokenize(c2)); diffAbs(got, wantJaccard) > 0.0005 {
		t.Fatalf("fixture drifted: expected jaccard ~%.4f, got %.4f", wantJaccard, got)
	}

	existing := mustSave(t, s, AddObservationParams{Title: "EmbeddingGemma v0.2 evidence artifact", Content: c1, Type: "discovery"})
	got := mustSave(t, s, AddObservationParams{Title: "EmbeddingGemma v0.2 evidence artifact (update)", Content: c2, Type: "discovery"})

	if got.Similarity <= 0.9 || got.Similarity >= 0.98 {
		t.Fatalf("calibration failed: closest embeddinggemma pair scored %.4f, expected strictly between 0.9 and 0.98", got.Similarity)
	}
	if got.Decision != WriteGateDecisionUpdate {
		t.Fatalf("calibration failed: closest embeddinggemma pair must AUTO-UPDATE (not silently duplicate), got decision %q", got.Decision)
	}
	if got.TargetID == nil || *got.TargetID != existing.ID {
		t.Fatalf("expected the auto-update to target the first artifact #%d, got %v", existing.ID, got.TargetID)
	}
	if got := countObservations(t, s, "wgproject"); got != 1 {
		t.Fatalf("expected the closest pair to collapse into 1 row via auto-update, got %d rows", got)
	}
}

func diffAbs(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}
