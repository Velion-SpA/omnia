package store

import (
	"context"
	"testing"
)

// ─── BackfillErrorSignatures (#1399 backfill) ────────────────────────────────
//
// RED: error_signature (signature.go, design obs #1498) is computed at SAVE
// time — bugfix-family observations saved BEFORE that feature existed have
// error_signature = NULL and never surface via Search's signature lane. These
// tests simulate "pre-existing" data by seeding through AddObservation (the
// normal path) and then clearing error_signature back to NULL directly via
// SQL — exactly what a row saved before slice 1 shipped looks like today.

// clearErrorSignature simulates "this row predates the error_signature
// feature" by nulling out whatever AddObservation auto-derived at insert
// time.
func clearErrorSignature(t *testing.T, s *Store, id int64) {
	t.Helper()
	if _, err := s.db.Exec(`UPDATE observations SET error_signature = NULL WHERE id = ?`, id); err != nil {
		t.Fatalf("clear error_signature: %v", err)
	}
}

func mustGetErrorSignature(t *testing.T, s *Store, id int64) string {
	t.Helper()
	var sig *string
	if err := s.db.QueryRow(`SELECT error_signature FROM observations WHERE id = ?`, id).Scan(&sig); err != nil {
		t.Fatalf("read error_signature: %v", err)
	}
	if sig == nil {
		return ""
	}
	return *sig
}

func TestBackfillErrorSignatures_BackfillsOnlyExtractableBugfixRows(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "backfillproj", "/tmp/backfillproj"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// A: bugfix-family, content quotes an error-shaped line, pre-existing
	// (signature cleared back to NULL after insert).
	errContent := "**What**: Fixed a crash in the checkout pipeline.\n" +
		"**Learned**: `panic: runtime error: index out of range [7] with length 2` happening in validateCartItems()."
	aID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1", Type: "bugfix", Title: "Fixed checkout cart crash",
		Content: errContent, Project: "backfillproj", Scope: "project",
	})
	if err != nil {
		t.Fatalf("AddObservation (A): %v", err)
	}
	clearErrorSignature(t, s, aID)

	// B: bugfix-family, PURE PROSE — no extractable error text at all.
	proseContent := "**What**: Refactored the checkout module for readability.\n" +
		"**Why**: The old code was hard to follow.\n" +
		"**Learned**: Smaller functions made the logic easier to review."
	bID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1", Type: "bugfix", Title: "Refactored checkout module",
		Content: proseContent, Project: "backfillproj", Scope: "project",
	})
	if err != nil {
		t.Fatalf("AddObservation (B): %v", err)
	}
	if got := mustGetErrorSignature(t, s, bID); got != "" {
		t.Fatalf("expected pure-prose bugfix to have NO auto-derived signature at save time, got %q", got)
	}

	// C: NON-bugfix type, content quotes an error-shaped line — must never be
	// touched by the backfill regardless of extractability.
	cID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1", Type: "discovery", Title: "Investigated a flaky panic in CI",
		Content: "Saw `panic: runtime error: index out of range [3] with length 1` once in CI, could not reproduce.",
		Project: "backfillproj", Scope: "project",
	})
	if err != nil {
		t.Fatalf("AddObservation (C): %v", err)
	}
	if got := mustGetErrorSignature(t, s, cID); got != "" {
		t.Fatalf("expected non-bugfix type to have NO auto-derived signature at save time, got %q", got)
	}

	scanned, updated, err := s.BackfillErrorSignatures(context.Background(), "backfillproj")
	if err != nil {
		t.Fatalf("BackfillErrorSignatures: %v", err)
	}
	if scanned != 2 {
		t.Fatalf("expected scanned=2 (A + B, the two bugfix-family rows lacking a signature), got %d", scanned)
	}
	if updated != 1 {
		t.Fatalf("expected updated=1 (only A had extractable error text), got %d", updated)
	}

	if got := mustGetErrorSignature(t, s, aID); got == "" {
		t.Fatalf("expected A to have a backfilled non-empty error_signature")
	}
	if want := NormalizeErrorSignature(extractErrorText(errContent)); mustGetErrorSignature(t, s, aID) != want {
		t.Fatalf("expected A's backfilled signature to equal the save-time derivation %q, got %q", want, mustGetErrorSignature(t, s, aID))
	}
	if got := mustGetErrorSignature(t, s, bID); got != "" {
		t.Fatalf("expected B (pure prose) to remain NULL after backfill, got %q", got)
	}
	if got := mustGetErrorSignature(t, s, cID); got != "" {
		t.Fatalf("expected C (non-bugfix) to remain untouched after backfill, got %q", got)
	}

	// Idempotency: a second run must update 0 rows. B still lacks a signature
	// (no extractable text — that's a permanent, correct state, not a bug),
	// so it may still be re-scanned, but nothing may be WRITTEN.
	_, updatedAgain, err := s.BackfillErrorSignatures(context.Background(), "backfillproj")
	if err != nil {
		t.Fatalf("BackfillErrorSignatures (second run): %v", err)
	}
	if updatedAgain != 0 {
		t.Fatalf("expected second run to update 0 rows (idempotent), got %d", updatedAgain)
	}
}

func TestBackfillErrorSignatures_ScopesToProjectWhenGiven(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "proj-x", "/tmp/x"); err != nil {
		t.Fatalf("create session x: %v", err)
	}
	if err := s.CreateSession("s2", "proj-y", "/tmp/y"); err != nil {
		t.Fatalf("create session y: %v", err)
	}

	content := "**Learned**: `panic: nil pointer dereference in refreshToken()`"

	xID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1", Type: "bugfix", Title: "Fixed auth crash (x)",
		Content: content, Project: "proj-x", Scope: "project",
	})
	if err != nil {
		t.Fatalf("AddObservation (x): %v", err)
	}
	clearErrorSignature(t, s, xID)

	yID, err := s.AddObservation(AddObservationParams{
		SessionID: "s2", Type: "bugfix", Title: "Fixed auth crash (y)",
		Content: content, Project: "proj-y", Scope: "project",
	})
	if err != nil {
		t.Fatalf("AddObservation (y): %v", err)
	}
	clearErrorSignature(t, s, yID)

	scanned, updated, err := s.BackfillErrorSignatures(context.Background(), "proj-x")
	if err != nil {
		t.Fatalf("BackfillErrorSignatures: %v", err)
	}
	if scanned != 1 || updated != 1 {
		t.Fatalf("expected scanned=1, updated=1 for project-scoped backfill, got scanned=%d updated=%d", scanned, updated)
	}
	if got := mustGetErrorSignature(t, s, xID); got == "" {
		t.Fatalf("expected proj-x row to be backfilled")
	}
	if got := mustGetErrorSignature(t, s, yID); got != "" {
		t.Fatalf("expected proj-y row to be left untouched when project filter is proj-x, got %q", got)
	}
}

// TestBackfillErrorSignatures_MakesPreExistingBugSearchableViaSignatureLane
// is the key end-to-end proof: a bugfix memory saved BEFORE the
// error_signature feature (simulated by clearing the auto-derived value)
// does NOT surface via the signature lane for a fresh occurrence of the same
// bug — until the backfill runs, after which it does.
func TestBackfillErrorSignatures_MakesPreExistingBugSearchableViaSignatureLane(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	content := "**What**: Fixed a crash in the checkout pipeline when the cart items slice was shorter than expected.\n" +
		"**Why**: A customer reported the app crashing intermittently during checkout.\n" +
		"**Where**: internal/checkout/pipeline.go\n" +
		"**Learned**: `panic: runtime error: index out of range [7] with length 2` happening in validateCartItems() when a coupon removed an item mid-request."

	fixID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1", Type: "bugfix", Title: "Fixed checkout cart crash",
		Content: content, Project: "engram", Scope: "project",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	// Simulate: this memory predates the error_signature feature entirely.
	clearErrorSignature(t, s, fixID)

	freshOccurrence := "panic: runtime error: index out of range [99] with length 4"

	// BEFORE backfill: the pre-existing gap this change fixes — no
	// signature-lane match for a fresh occurrence of the same bug.
	before, err := s.Search(freshOccurrence, SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search (before): %v", err)
	}
	for _, r := range before {
		if r.ID == fixID && r.SignatureMatch {
			t.Fatalf("expected NO signature match before backfill (this is the gap being fixed), but id=%d matched", fixID)
		}
	}

	scanned, updated, err := s.BackfillErrorSignatures(context.Background(), "engram")
	if err != nil {
		t.Fatalf("BackfillErrorSignatures: %v", err)
	}
	if scanned < 1 || updated < 1 {
		t.Fatalf("expected at least 1 scanned/updated, got scanned=%d updated=%d", scanned, updated)
	}

	// AFTER backfill: the SAME fresh occurrence now surfaces the pre-existing
	// fix via the signature lane.
	after, err := s.Search(freshOccurrence, SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search (after): %v", err)
	}
	var found *SearchResult
	for i := range after {
		if after[i].ID == fixID {
			found = &after[i]
		}
	}
	if found == nil {
		t.Fatalf("expected the backfilled fix (id=%d) to surface for a fresh occurrence of the same bug, got %d results: %+v", fixID, len(after), after)
	}
	if !found.SignatureMatch {
		t.Fatalf("expected SignatureMatch=true after backfill, got %+v", found)
	}
	if found.Rank != signatureMatchRank {
		t.Fatalf("expected rank=%v (signature lane tier), got %v", signatureMatchRank, found.Rank)
	}
}

// ─── PreviewBackfillErrorSignatures (dry-run support) ────────────────────────

func TestPreviewBackfillErrorSignatures_ComputesWithoutWriting(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "previewproj", "/tmp/previewproj"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	content := "**Learned**: `panic: runtime error: index out of range [7] with length 2`"
	id, err := s.AddObservation(AddObservationParams{
		SessionID: "s1", Type: "bugfix", Title: "Fixed cart crash",
		Content: content, Project: "previewproj", Scope: "project",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	clearErrorSignature(t, s, id)

	scanned, wouldUpdate, err := s.PreviewBackfillErrorSignatures(context.Background(), "previewproj")
	if err != nil {
		t.Fatalf("PreviewBackfillErrorSignatures: %v", err)
	}
	if scanned != 1 || wouldUpdate != 1 {
		t.Fatalf("expected scanned=1, wouldUpdate=1, got scanned=%d wouldUpdate=%d", scanned, wouldUpdate)
	}

	// Nothing must have been written.
	if got := mustGetErrorSignature(t, s, id); got != "" {
		t.Fatalf("expected preview to write NOTHING, but error_signature is now %q", got)
	}

	// The real backfill should still find + update this row afterward.
	_, updated, err := s.BackfillErrorSignatures(context.Background(), "previewproj")
	if err != nil {
		t.Fatalf("BackfillErrorSignatures: %v", err)
	}
	if updated != 1 {
		t.Fatalf("expected the real backfill to still update the row previewed above, got updated=%d", updated)
	}
}
