package store

import (
	"strings"
	"testing"
	"time"
)

func TestFormatContextAsOfPreservesObservationPriorityAndOmitsUnversionedAdjuncts(t *testing.T) {
	s := newTimeTravelStore(t, true, 0)
	pinnedID := addTimeTravelObservation(t, s, "historical pinned", "")
	addTimeTravelObservation(t, s, "historical recent", "")
	if err := s.PinObservation(pinnedID); err != nil {
		t.Fatal(err)
	}
	var pinnedAt string
	if err := s.db.QueryRow(`
		SELECT MAX(valid_to) FROM observation_revisions r
		JOIN observations o ON o.sync_id = r.obs_sync_id
		WHERE o.id = ?`, pinnedID,
	).Scan(&pinnedAt); err != nil {
		t.Fatal(err)
	}
	pinnedBefore, err := s.GetObservation(pinnedID)
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.ContextTokenBudget = formatObservationLineTokens(*pinnedBefore)

	currentTitle := "current metadata must not leak"
	if _, err := s.UpdateObservation(pinnedID, UpdateObservationParams{Title: &currentTitle}); err != nil {
		t.Fatal(err)
	}
	if err := s.UnpinObservation(pinnedID); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession("future-session", "omnia", "/tmp/future"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPrompt(AddPromptParams{
		SessionID: "future-session",
		Content:   "future prompt must not leak",
		Project:   "omnia",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.FormatContextAsOf("omnia", "project", pinnedAt)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"### Pinned",
		"historical pinned",
		"sessions and prompts are omitted because they are not versioned",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("historical context missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{
		"### Recent Observations",
		"historical recent",
		"current metadata must not leak",
		"### Recent Sessions",
		"### Recent User Prompts",
		"future prompt must not leak",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("historical context leaked %q:\n%s", unwanted, got)
		}
	}
}

func TestFormatContextAsOfFutureMatchesLive(t *testing.T) {
	s := newTimeTravelStore(t, true, 0)
	addTimeTravelObservation(t, s, "live context", "")
	live, _ := s.FormatContext("omnia", "project")
	future, err := s.FormatContextAsOf("omnia", "project", time.Now().Add(time.Hour).Format(time.RFC3339Nano))
	if err != nil || future != live {
		t.Fatalf("future context differs from live:\n%s\n---\n%s (%v)", future, live, err)
	}
}

func TestPinnedRevisionRetentionGapAndUpdatedAt(t *testing.T) {
	s := newTimeTravelStore(t, true, 1)
	id := addTimeTravelObservation(t, s, "pin history", "")
	before, _ := s.GetObservation(id)
	if err := s.PinObservation(id); err != nil {
		t.Fatal(err)
	}
	pinned, _ := s.GetObservation(id)
	if before.UpdatedAt == pinned.UpdatedAt {
		t.Fatalf("pin did not advance updated_at: %q", pinned.UpdatedAt)
	}
	if err := s.UnpinObservation(id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StateAsOf(id, before.UpdatedAt); err == nil || !strings.Contains(err.Error(), "history unavailable before") {
		t.Fatalf("pruned pin interval returned %v, want history unavailable", err)
	}
}

func TestFormatContextAsOfNormalizesProjectAndSortsInstants(t *testing.T) {
	s := newTimeTravelStore(t, true, 0)
	ids := []int64{
		addTimeTravelObservation(t, s, "old instant", ""),
		addTimeTravelObservation(t, s, "new instant lower ID", ""),
		addTimeTravelObservation(t, s, "new instant higher ID", ""),
	}
	timestamps := []string{
		"2025-01-01T10:00:00+02:00",
		"2025-01-01T09:30:00Z",
		"2025-01-01T11:30:00+02:00",
	}
	for i, id := range ids {
		if _, err := s.db.Exec(`UPDATE observations SET project = 'OmNiA', created_at = ?, updated_at = ? WHERE id = ?`, timestamps[i], timestamps[i], id); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.FormatContextAsOf("OMNIA", "project", "2025-01-02T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	high, low, old := strings.Index(got, "higher ID"), strings.Index(got, "lower ID"), strings.Index(got, "old instant")
	if high < 0 || low < 0 || old < 0 || !(high < low && low < old) {
		t.Fatalf("historical context order/project normalization wrong:\n%s", got)
	}
}
