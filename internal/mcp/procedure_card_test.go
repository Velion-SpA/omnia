package mcp

import (
	"context"
	"testing"
	"time"

	mcppkg "github.com/mark3labs/mcp-go/mcp"
	"github.com/velion/omnia/internal/store"
)

// ─── Phase 7 — Contrastive Retrieval ─────────────────────────────────────────

func mustUpsertTrustedProcedure(t *testing.T, s *store.Store, project, polarity, trigger string) string {
	t.Helper()
	syncID, err := s.UpsertProcedure(store.Procedure{
		Project:           project,
		Polarity:          polarity,
		Trigger:           trigger,
		Steps:             []store.ProcedureStep{{Order: 1, Template: "step for " + trigger}},
		PostconditionKind: store.PostconditionTestsPass,
		Confidence:        0.5,
	})
	if err != nil {
		t.Fatalf("UpsertProcedure(%q): %v", trigger, err)
	}
	// Promote to trusted via 3 ConfirmReuse calls (default trust_threshold).
	for i := 0; i < 3; i++ {
		if _, err := s.ConfirmReuse(syncID, "engram"); err != nil {
			t.Fatalf("ConfirmReuse(%q): %v", trigger, err)
		}
	}
	got, err := s.GetProcedure(syncID)
	if err != nil {
		t.Fatalf("GetProcedure(%q): %v", trigger, err)
	}
	if got.State != store.ProcedureStateTrusted {
		t.Fatalf("expected %q to be trusted after 3 ConfirmReuse calls; got state=%q", trigger, got.State)
	}
	return syncID
}

// TestBuildProcedureCard_MatchingQueryReturnsBothPolarities (tasks 7.1)
// verifies a query matching both a trusted playbook and a trusted
// anti_playbook returns one card naming both, with confidence values.
func TestBuildProcedureCard_MatchingQueryReturnsBothPolarities(t *testing.T) {
	s := newMCPTestStore(t)

	playbookID := mustUpsertTrustedProcedure(t, s, "engram", store.ProcedurePolarityPlaybook, "flaky test retry backoff strategy")
	antiID := mustUpsertTrustedProcedure(t, s, "engram", store.ProcedurePolarityAntiPlaybook, "flaky test retry sleep(5) anti pattern")

	card := BuildProcedureCard(s, "flaky test retry")
	if card == nil {
		t.Fatal("expected a non-nil procedure card")
	}
	if card.Playbook == nil || card.Playbook.SyncID != playbookID {
		t.Errorf("Playbook = %+v; want sync_id %q", card.Playbook, playbookID)
	}
	if card.AntiPlaybook == nil || card.AntiPlaybook.SyncID != antiID {
		t.Errorf("AntiPlaybook = %+v; want sync_id %q", card.AntiPlaybook, antiID)
	}
	if card.Playbook.Confidence <= 0 {
		t.Errorf("expected a positive playbook confidence; got %v", card.Playbook.Confidence)
	}
	if card.AntiPlaybook.Confidence <= 0 {
		t.Errorf("expected a positive anti_playbook confidence; got %v", card.AntiPlaybook.Confidence)
	}
}

// TestBuildProcedureCard_OnlyOnePolarityTrusted_NoFabrication (task 7.2)
// verifies that when only ONE polarity has a trusted match, the card
// contains only that polarity — the other side is never fabricated.
func TestBuildProcedureCard_OnlyOnePolarityTrusted_NoFabrication(t *testing.T) {
	s := newMCPTestStore(t)

	playbookID := mustUpsertTrustedProcedure(t, s, "engram", store.ProcedurePolarityPlaybook, "database connection pool exhaustion fix")

	card := BuildProcedureCard(s, "database connection pool exhaustion")
	if card == nil {
		t.Fatal("expected a non-nil procedure card")
	}
	if card.Playbook == nil || card.Playbook.SyncID != playbookID {
		t.Errorf("Playbook = %+v; want sync_id %q", card.Playbook, playbookID)
	}
	if card.AntiPlaybook != nil {
		t.Errorf("AntiPlaybook must be nil (no trusted anti_playbook match); got %+v", card.AntiPlaybook)
	}
}

// TestBuildProcedureCard_NoTrustedMatch_ReturnsNil verifies a query with no
// trusted match at all (only a candidate) returns nil — candidates never
// auto-inject as a card (spec: "Only trusted procedures auto-inject").
func TestBuildProcedureCard_NoTrustedMatch_ReturnsNil(t *testing.T) {
	s := newMCPTestStore(t)

	if _, err := s.UpsertProcedure(store.Procedure{
		Project:           "engram",
		Polarity:          store.ProcedurePolarityPlaybook,
		Trigger:           "candidate only, never confirmed",
		Steps:             []store.ProcedureStep{{Order: 1, Template: "step"}},
		PostconditionKind: store.PostconditionTestsPass,
		Confidence:        0.5,
	}); err != nil {
		t.Fatalf("UpsertProcedure: %v", err)
	}

	card := BuildProcedureCard(s, "candidate only never confirmed")
	if card != nil {
		t.Errorf("expected nil card for a candidate-only match; got %+v", card)
	}
}

// TestHandleSearch_AttachesProcedureCardWhenEnabled (task 7.4) verifies
// handleSearch attaches a procedure_card to the envelope when cfg.Procedural
// is configured and a trusted match exists.
func TestHandleSearch_AttachesProcedureCardWhenEnabled(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	if err := s.CreateSession("s-card", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-card", Type: "bugfix", Title: "Flaky test retry backoff",
		Content: "Added exponential backoff to the flaky retry loop.", Project: "engram", Scope: "project",
	}); err != nil {
		t.Fatalf("add observation: %v", err)
	}
	mustUpsertTrustedProcedure(t, s, "engram", store.ProcedurePolarityPlaybook, "flaky retry loop backoff")

	cfg := MCPConfig{Procedural: &ProceduralWiring{}}
	search := handleSearch(s, cfg, NewSessionActivity(10*time.Minute))

	res, err := search(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"query":   "flaky retry loop",
		"project": "engram",
	}}})
	if err != nil {
		t.Fatalf("search handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected search error: %s", callResultText(t, res))
	}

	body := callResultJSON(t, res)
	if _, ok := body["procedure_card"]; !ok {
		t.Fatalf("expected procedure_card in envelope; got keys %v", body)
	}
}

// TestHandleSearch_NoProcedureCardWhenDisabled verifies the default
// (cfg.Procedural nil) path never attaches a procedure_card, even when a
// trusted procedure would otherwise match — total backward compatibility.
func TestHandleSearch_NoProcedureCardWhenDisabled(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	if err := s.CreateSession("s-card2", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-card2", Type: "bugfix", Title: "Flaky test retry backoff",
		Content: "Added exponential backoff to the flaky retry loop.", Project: "engram", Scope: "project",
	}); err != nil {
		t.Fatalf("add observation: %v", err)
	}
	mustUpsertTrustedProcedure(t, s, "engram", store.ProcedurePolarityPlaybook, "flaky retry loop backoff")

	search := handleSearch(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	res, err := search(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"query":   "flaky retry loop",
		"project": "engram",
	}}})
	if err != nil {
		t.Fatalf("search handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected search error: %s", callResultText(t, res))
	}

	body := callResultJSON(t, res)
	if _, ok := body["procedure_card"]; ok {
		t.Fatalf("procedural.enabled=false must never attach procedure_card; got %v", body["procedure_card"])
	}
}
