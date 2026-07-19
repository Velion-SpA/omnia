package main

import (
	"strconv"
	"strings"
	"testing"

	"github.com/velion/omnia/internal/store"
)

// ─── omnia recall-backfill (#1399 backfill) ──────────────────────────────────
//
// RED: a one-shot CLI command so the signature lane (#1399 slice 1) also
// works on bug-fix memories saved BEFORE the feature existed. Mirrors
// recall_fix_test.go's fixture/helper conventions.

// clearErrorSignatureViaCLI opens the store, nulls out the given
// observation's error_signature, and closes — simulating "this row predates
// the error_signature feature" (identical intent to
// internal/store's clearErrorSignature test helper, done from cmd/omnia via
// the exported store.Store.DB()).
func clearErrorSignatureViaCLI(t *testing.T, cfg store.Config, id int64) {
	t.Helper()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	if _, err := s.DB().Exec(`UPDATE observations SET error_signature = NULL WHERE id = ?`, id); err != nil {
		t.Fatalf("clear error_signature: %v", err)
	}
}

func readErrorSignatureViaCLI(t *testing.T, cfg store.Config, id int64) string {
	t.Helper()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	var sig *string
	if err := s.DB().QueryRow(`SELECT error_signature FROM observations WHERE id = ?`, id).Scan(&sig); err != nil {
		t.Fatalf("read error_signature: %v", err)
	}
	if sig == nil {
		return ""
	}
	return *sig
}

func TestCmdRecallBackfill_DryRunWritesNothing(t *testing.T) {
	cfg := testConfig(t)

	content := "**Learned**: `panic: runtime error: index out of range [7] with length 2` in validateCartItems()."
	id := mustSeedObservation(t, cfg, "s1", "recbackfill-dry", "bugfix", "Fixed checkout cart crash", content, "project")
	clearErrorSignatureViaCLI(t, cfg, id)

	withArgs(t, "omnia", "recall-backfill", "--project", "recbackfill-dry", "--dry-run")
	stdout, stderr := captureOutput(t, func() { cmdRecallBackfill(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, "scanned 1") {
		t.Fatalf("expected summary to report scanned 1, got: %q", stdout)
	}
	if !strings.Contains(stdout, "dry-run") {
		t.Fatalf("expected dry-run output to say so, got: %q", stdout)
	}

	if got := readErrorSignatureViaCLI(t, cfg, id); got != "" {
		t.Fatalf("expected --dry-run to write NOTHING, but error_signature is now %q", got)
	}
}

func TestCmdRecallBackfill_PersistsWithoutDryRun(t *testing.T) {
	cfg := testConfig(t)

	content := "**Learned**: `panic: runtime error: index out of range [7] with length 2` in validateCartItems()."
	id := mustSeedObservation(t, cfg, "s1", "recbackfill-real", "bugfix", "Fixed checkout cart crash", content, "project")
	clearErrorSignatureViaCLI(t, cfg, id)

	withArgs(t, "omnia", "recall-backfill", "--project", "recbackfill-real")
	stdout, stderr := captureOutput(t, func() { cmdRecallBackfill(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, "scanned 1 bugfix mem") {
		t.Fatalf("expected summary to report scanned 1 bugfix memories, got: %q", stdout)
	}
	if !strings.Contains(stdout, "backfilled 1 signature") {
		t.Fatalf("expected summary to report backfilled 1 signature, got: %q", stdout)
	}

	if got := readErrorSignatureViaCLI(t, cfg, id); got == "" {
		t.Fatalf("expected the observation to have a persisted error_signature after a real (non-dry-run) backfill")
	}
}

func TestCmdRecallBackfill_ReportsSkippedNonExtractableRows(t *testing.T) {
	cfg := testConfig(t)

	proseContent := "**What**: Refactored the checkout module for readability.\n**Learned**: Smaller functions made the logic easier to review."
	mustSeedObservation(t, cfg, "s1", "recbackfill-skip", "bugfix", "Refactored checkout module", proseContent, "project")

	withArgs(t, "omnia", "recall-backfill", "--project", "recbackfill-skip")
	stdout, stderr := captureOutput(t, func() { cmdRecallBackfill(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, "1 had no extractable error") {
		t.Fatalf("expected summary to report the skipped pure-prose row, got: %q", stdout)
	}
}

func TestCmdRecallBackfill_MakesPreExistingFixDiscoverableViaRecallFix(t *testing.T) {
	cfg := testConfig(t)

	content := "**What**: Fixed a crash in the checkout pipeline when the cart items slice was shorter than expected.\n" +
		"**Learned**: `panic: runtime error: index out of range [7] with length 2` happening in validateCartItems() when a coupon removed an item mid-request."
	fixID := mustSeedObservation(t, cfg, "s1", "recbackfill-e2e", "bugfix", "Fixed checkout cart crash", content, "project")
	clearErrorSignatureViaCLI(t, cfg, fixID)

	freshOccurrence := "panic: runtime error: index out of range [99] with length 4"

	// BEFORE backfill: recall-fix (signature-lane-only) finds nothing.
	withArgs(t, "omnia", "recall-fix", freshOccurrence, "--project", "recbackfill-e2e")
	before, _ := captureOutput(t, func() { cmdRecallFix(cfg) })
	if strings.TrimSpace(before) != "" {
		t.Fatalf("expected recall-fix to find NOTHING before backfill (this is the gap being fixed), got: %q", before)
	}

	withArgs(t, "omnia", "recall-backfill", "--project", "recbackfill-e2e")
	captureOutput(t, func() { cmdRecallBackfill(cfg) })

	// AFTER backfill: the SAME fresh occurrence now surfaces the pre-existing fix.
	withArgs(t, "omnia", "recall-fix", freshOccurrence, "--project", "recbackfill-e2e")
	after, _ := captureOutput(t, func() { cmdRecallFix(cfg) })
	wantID := "obs #" + strconv.FormatInt(fixID, 10)
	if !strings.Contains(after, wantID) {
		t.Fatalf("expected recall-fix to surface %s after backfill, got: %q", wantID, after)
	}
}
