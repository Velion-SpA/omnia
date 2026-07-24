package main

// review_due_test.go — RED→GREEN CLI tests for `omnia review-due`
// (omnia-0.3.1-write-hygiene PR11, spaced-review / Play G, design D11).
// Mirrors forget_scan_test.go's withArgs/captureOutput/storeNew style.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/velion/omnia/internal/store"
)

// seedDueReviewObservation creates a session + observation whose
// review_after has already passed, mirroring mustSeedObservation's shape
// plus the backdating convention TestHandleReviewListAndMarkReviewed uses.
func seedDueReviewObservation(t *testing.T, cfg store.Config, sessionID, project, typ, title string) int64 {
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
		Type:      typ,
		Title:     title,
		Content:   title + " content",
		Project:   project,
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	past := time.Now().UTC().Add(-time.Hour).Format("2006-01-02 15:04:05")
	if _, err := s.DB().Exec(`UPDATE observations SET review_after = ? WHERE id = ?`, past, id); err != nil {
		t.Fatalf("backdate review_after: %v", err)
	}
	return id
}

func TestCmdReviewDueQuietWhenNothingDue(t *testing.T) {
	cfg := testConfig(t)
	withArgs(t, "omnia", "review-due", "--project", "quiet-review-proj")

	stdout, stderr := captureOutput(t, func() { cmdReviewDue(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "0 memories due for review") {
		t.Fatalf("expected quiet zero-due message, got: %s", stdout)
	}
}

func TestCmdReviewDueGroupsByProjectAndTypeNeverDumpsContent(t *testing.T) {
	cfg := testConfig(t)
	id := seedDueReviewObservation(t, cfg, "review-due-sess", "review-due-proj", "decision", "Secret Title")

	withArgs(t, "omnia", "review-due", "--project", "review-due-proj")
	stdout, stderr := captureOutput(t, func() { cmdReviewDue(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(stdout, fmt.Sprintf("#%d", id)) {
		t.Fatalf("expected observation id in output, got: %s", stdout)
	}
	if !strings.Contains(stdout, "Secret Title") {
		t.Fatalf("expected title in output, got: %s", stdout)
	}
	if !strings.Contains(stdout, "review-due-proj/decision: 1") {
		t.Fatalf("expected grouped count line, got: %s", stdout)
	}
	if strings.Contains(stdout, "Secret Title content") {
		t.Fatalf("must never dump observation content, got: %s", stdout)
	}
	if !strings.Contains(stdout, "mem_review mark_reviewed") {
		t.Fatalf("expected resolve hint, got: %s", stdout)
	}
}

func TestCmdReviewDueJSONOutput(t *testing.T) {
	cfg := testConfig(t)
	id := seedDueReviewObservation(t, cfg, "review-due-json-sess", "review-due-json-proj", "policy", "JSON Title")

	withArgs(t, "omnia", "review-due", "--project", "review-due-json-proj", "--json")
	stdout, stderr := captureOutput(t, func() { cmdReviewDue(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	var report reviewDueReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("unmarshal report: %v\noutput: %s", err, stdout)
	}
	if report.Count != 1 || len(report.Observations) != 1 || report.Observations[0].ID != id {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestCmdReviewDueDefaultsToAllProjectsWhenNoFilter(t *testing.T) {
	cfg := testConfig(t)
	seedDueReviewObservation(t, cfg, "review-due-all-a", "review-due-proj-a", "decision", "A")
	seedDueReviewObservation(t, cfg, "review-due-all-b", "review-due-proj-b", "decision", "B")

	withArgs(t, "omnia", "review-due")
	stdout, stderr := captureOutput(t, func() { cmdReviewDue(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "review-due-proj-a/decision: 1") || !strings.Contains(stdout, "review-due-proj-b/decision: 1") {
		t.Fatalf("expected both projects grouped, got: %s", stdout)
	}
}
