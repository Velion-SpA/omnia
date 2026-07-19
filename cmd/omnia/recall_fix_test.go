package main

import (
	"strconv"
	"strings"
	"testing"

	"github.com/velion/omnia/internal/store"
)

// ─── omnia recall-fix (#1399 slice 2 — forced activation) ────────────────────
//
// RED (design obs #1498 / audit #1497, slice 2): a compact, hook-safe CLI
// command that re-derives the SAME error-shaped-line extraction used at
// save time (store.ExtractErrorText) and hits ONLY the signature lane
// (store.SearchResult.SignatureMatch == true) — never loose BM25 text hits,
// which would be noise for a forced/automatic injection. Empty output means
// "no proven prior fix", so a PostToolUse hook can inject nothing.

// mustSeedRecallFixObservation seeds a bugfix-family observation with full
// control over Project/Outcome (mustSeedObservation in main_test.go doesn't
// expose Outcome), mirroring the store package's
// TestSearch_RecurringBugAutoFindsPastFixViaExtractedSignature fixture.
func mustSeedRecallFixObservation(t *testing.T, cfg store.Config, sessionID, project, title, content, outcome string) int64 {
	t.Helper()

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	if err := s.CreateSession(sessionID, project, "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	id, err := s.AddObservation(store.AddObservationParams{
		SessionID: sessionID,
		Type:      "bugfix",
		Title:     title,
		Content:   content,
		Project:   project,
		Scope:     "project",
		Outcome:   outcome,
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	return id
}

func TestCmdRecallFix_SignatureMatchReturnsCompactHit(t *testing.T) {
	cfg := testConfig(t)

	content := "**What**: Fixed a crash in the checkout pipeline when the cart items slice was shorter than expected.\n" +
		"**Why**: A customer reported the app crashing intermittently during checkout.\n" +
		"**Where**: internal/checkout/pipeline.go\n" +
		"**Learned**: `panic: runtime error: index out of range [7] with length 2` happening in validateCartItems() when a coupon removed an item mid-request."

	fixID := mustSeedRecallFixObservation(t, cfg, "s1", "reclfix", "Fixed checkout cart crash", content, "worked")

	// A FRESH occurrence of the SAME underlying bug — different incidental
	// numbers, shares ~no vocabulary with the memory's title/prose.
	freshOccurrence := "panic: runtime error: index out of range [99] with length 4"

	withArgs(t, "omnia", "recall-fix", freshOccurrence, "--project", "reclfix")
	stdout, stderr := captureOutput(t, func() { cmdRecallFix(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}

	wantID := "obs #" + strconv.FormatInt(fixID, 10)
	if !strings.Contains(stdout, wantID) {
		t.Fatalf("expected compact output to reference %s, got: %q", wantID, stdout)
	}
	if !strings.Contains(stdout, "[worked]") {
		t.Fatalf("expected compact output to include the recorded outcome [worked], got: %q", stdout)
	}
	if !strings.Contains(stdout, "Fixed checkout cart crash") {
		t.Fatalf("expected compact output to include the observation title, got: %q", stdout)
	}
	// Must NOT dump the full memory content — that defeats the anti-bloat
	// contract (compact top-K, lazy-expand via mem_get_observation).
	if strings.Contains(stdout, "A customer reported the app crashing intermittently") {
		t.Fatalf("expected compact output to EXCLUDE unrelated full-content prose, got: %q", stdout)
	}
}

func TestCmdRecallFix_NoSignatureMatchProducesEmptyOutput(t *testing.T) {
	cfg := testConfig(t)

	// Seed an unrelated bugfix whose signature will never contain-match a
	// completely different error.
	mustSeedRecallFixObservation(t, cfg, "s1", "reclfix", "Fixed unrelated auth bug",
		"**Learned**: `panic: nil pointer dereference in refreshToken()`", "worked")

	withArgs(t, "omnia", "recall-fix", "TypeError: Cannot read properties of undefined (reading 'items')", "--project", "reclfix")
	stdout, stderr := captureOutput(t, func() { cmdRecallFix(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("expected empty output when no signature match exists, got: %q", stdout)
	}
}

func TestCmdRecallFix_PureProseInputProducesEmptyOutput(t *testing.T) {
	cfg := testConfig(t)

	withArgs(t, "omnia", "recall-fix", "just a normal sentence with no error shape at all", "--project", "reclfix")
	stdout, stderr := captureOutput(t, func() { cmdRecallFix(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("expected empty output for input with no error-shaped lines (extractErrorText returns \"\"), got: %q", stdout)
	}
}

func TestCmdRecallFix_CapsToTopThreeHitsAndCharBudget(t *testing.T) {
	cfg := testConfig(t)

	longLearned := strings.Repeat("this fix required touching many files and reasoning about edge cases carefully ", 5)

	var ids []int64
	for i := 0; i < 5; i++ {
		content := "**Learned**: `panic: runtime error: index out of range [" + strconv.Itoa(i) + "] with length 2` " + longLearned
		id := mustSeedRecallFixObservation(t, cfg, "s1", "reclfix-cap",
			"Fixed cart crash variant "+strconv.Itoa(i), content, "")
		ids = append(ids, id)
	}

	withArgs(t, "omnia", "recall-fix", "panic: runtime error: index out of range [777] with length 9", "--project", "reclfix-cap")
	stdout, stderr := captureOutput(t, func() { cmdRecallFix(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}

	hitCount := strings.Count(stdout, "obs #")
	if hitCount == 0 {
		t.Fatalf("expected at least one signature-lane hit, got none: %q", stdout)
	}
	if hitCount > 3 {
		t.Fatalf("expected at most 3 compact hits (top-K cap), got %d: %q", hitCount, stdout)
	}
	if len(stdout) > 700 {
		t.Fatalf("expected compact output to respect the ~600 char hard cap (with slack for formatting), got %d chars: %q", len(stdout), stdout)
	}
}

func TestCmdRecallFix_ReadsErrorTextFromStdinWhenNoPositionalArgs(t *testing.T) {
	cfg := testConfig(t)

	content := "**Learned**: `panic: runtime error: index out of range [7] with length 2` happening in validateCartItems()."
	fixID := mustSeedRecallFixObservation(t, cfg, "s1", "reclfix-stdin", "Fixed checkout cart crash", content, "")

	oldReadStdin := readStdin
	readStdin = func() ([]byte, error) {
		return []byte("panic: runtime error: index out of range [42] with length 1"), nil
	}
	t.Cleanup(func() { readStdin = oldReadStdin })

	withArgs(t, "omnia", "recall-fix", "--project", "reclfix-stdin")
	stdout, stderr := captureOutput(t, func() { cmdRecallFix(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}

	wantID := "obs #" + strconv.FormatInt(fixID, 10)
	if !strings.Contains(stdout, wantID) {
		t.Fatalf("expected stdin-sourced error text to surface %s via the signature lane, got: %q", wantID, stdout)
	}
}

func TestCmdRecallFix_EmptyInputProducesEmptyOutput(t *testing.T) {
	cfg := testConfig(t)

	oldReadStdin := readStdin
	readStdin = func() ([]byte, error) { return []byte(""), nil }
	t.Cleanup(func() { readStdin = oldReadStdin })

	withArgs(t, "omnia", "recall-fix", "--project", "reclfix-empty")
	stdout, stderr := captureOutput(t, func() { cmdRecallFix(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr for empty input, got: %q", stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("expected empty output for empty input, got: %q", stdout)
	}
}

func TestCmdRecallFix_JSONModeOutputsStructuredHits(t *testing.T) {
	cfg := testConfig(t)

	content := "**Learned**: `panic: runtime error: index out of range [7] with length 2` in validateCartItems()."
	fixID := mustSeedRecallFixObservation(t, cfg, "s1", "reclfix-json", "Fixed checkout cart crash", content, "worked")

	withArgs(t, "omnia", "recall-fix", "panic: runtime error: index out of range [55] with length 2", "--project", "reclfix-json", "--json")
	stdout, stderr := captureOutput(t, func() { cmdRecallFix(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, `"id": `+strconv.FormatInt(fixID, 10)) {
		t.Fatalf("expected JSON output to contain the observation id, got: %q", stdout)
	}
	if !strings.Contains(stdout, `"outcome": "worked"`) {
		t.Fatalf("expected JSON output to contain the outcome field, got: %q", stdout)
	}
}

// ─── Pure formatting helpers (no store/DB needed) ─────────────────────────────

func TestFormatRecallFixCompact_EmptyHitsReturnsEmptyString(t *testing.T) {
	if got := formatRecallFixCompact(nil, maxRecallFixTotalChars); got != "" {
		t.Fatalf("expected empty string for zero hits, got: %q", got)
	}
}

func TestRecallFixSnippet_PrefersLearnedSectionAndCollapsesWhitespace(t *testing.T) {
	content := "**What**: Did a thing.\n**Why**: Because.\n**Learned**: The   root cause\nwas  a race condition."
	got := recallFixSnippet(content, 120)
	if strings.Contains(got, "Did a thing") {
		t.Fatalf("expected snippet to prefer the Learned section, got: %q", got)
	}
	if !strings.Contains(got, "root cause was a race condition") {
		t.Fatalf("expected snippet to collapse whitespace/newlines, got: %q", got)
	}
}

func TestRecallFixSnippet_FallsBackToContentWhenNoLearnedMarker(t *testing.T) {
	got := recallFixSnippet("plain content with no markdown sections at all", 120)
	if got == "" {
		t.Fatalf("expected non-empty fallback snippet")
	}
}
