package main

// procedure_test.go — CLI tests for `omnia procedure-induct` and
// `omnia procedure <list|inspect|retire>`.
// Follows the repo's stdlib-only, real-SQLite testing convention
// (mirrors conflicts_test.go's testConfig → seed → withArgs → captureOutput
// → assert pattern).

import (
	"strings"
	"testing"

	"github.com/velion/omnia/internal/store"
)

// seedBugfixObsCLI seeds a bugfix-family observation with an explicit
// outcome + error_signature via a direct store.New/AddObservation round
// trip (mirrors mustSeedObservation, but needs fields mustSeedObservation
// doesn't expose).
func seedBugfixObsCLI(t *testing.T, cfg store.Config, sessionID, project, title, errorText, outcome string) {
	t.Helper()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	if err := s.CreateSession(sessionID, project, "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID:      sessionID,
		Type:           "bugfix",
		Title:          title,
		Content:        "Fixed: " + title,
		Project:        project,
		Scope:          "project",
		ErrorSignature: errorText,
		Outcome:        outcome,
	}); err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
}

// ─── procedure-induct ─────────────────────────────────────────────────────────

// TestCmdProcedureInduct_DryRunDefault (task 6.3) verifies that without
// --apply, the command reports counts only and writes zero procedures.
func TestCmdProcedureInduct_DryRunDefault(t *testing.T) {
	cfg := testConfig(t)
	sharedErr := "panic: nil pointer dereference\n\tat run (/src/main.go:10)"
	seedBugfixObsCLI(t, cfg, "ses-cli-dry", "cliproj", "Fixed nil pointer #1", sharedErr, "worked")
	seedBugfixObsCLI(t, cfg, "ses-cli-dry", "cliproj", "Fixed nil pointer #2", sharedErr, "worked")

	withArgs(t, "omnia", "procedure-induct", "--project", "cliproj")
	stdout, stderr := captureOutput(t, func() { cmdProcedureInduct(cfg) })

	if stderr != "" {
		t.Errorf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "dry_run:              true") {
		t.Errorf("expected dry_run: true in output, got: %s", stdout)
	}
	if !strings.Contains(stdout, "clusters_found:       1") {
		t.Errorf("expected clusters_found: 1, got: %s", stdout)
	}
	if !strings.Contains(stdout, "procedures_induced:   0") {
		t.Errorf("expected procedures_induced: 0 in dry-run, got: %s", stdout)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	procedures, err := s.ListProcedures(store.ListProceduresOptions{Project: "cliproj", Limit: 10})
	if err != nil {
		t.Fatalf("ListProcedures: %v", err)
	}
	if len(procedures) != 0 {
		t.Fatalf("dry-run must write zero procedures rows; got %d", len(procedures))
	}
}

// TestCmdProcedureInduct_ApplyNoLLMConfigured_GracefulDegrade (task 6.6)
// verifies --apply with no OMNIA_AGENT_CLI configured never fails: it
// degrades to the deterministic fallback (SafeInduce) and still induces a
// (degraded) candidate procedure.
func TestCmdProcedureInduct_ApplyNoLLMConfigured_GracefulDegrade(t *testing.T) {
	t.Setenv("OMNIA_AGENT_CLI", "")
	t.Setenv("ENGRAM_AGENT_CLI", "")

	cfg := testConfig(t)
	seedBugfixObsCLI(t, cfg, "ses-cli-apply", "cliproj2", "Fixed slice panic", "panic: index out of range\n\tat h (/src/h.go:1)", "worked")

	withArgs(t, "omnia", "procedure-induct", "--project", "cliproj2", "--apply")
	stdout, stderr := captureOutput(t, func() { cmdProcedureInduct(cfg) })

	if stderr != "" {
		t.Errorf("unexpected stderr (--apply with no LLM must never fail): %s", stderr)
	}
	if !strings.Contains(stdout, "dry_run:              false") {
		t.Errorf("expected dry_run: false with --apply, got: %s", stdout)
	}
	if !strings.Contains(stdout, "procedures_induced:   1") {
		t.Errorf("expected 1 degraded-fallback procedure induced, got: %s", stdout)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	procedures, err := s.ListProcedures(store.ListProceduresOptions{Project: "cliproj2", Limit: 10})
	if err != nil {
		t.Fatalf("ListProcedures: %v", err)
	}
	if len(procedures) != 1 {
		t.Fatalf("expected 1 persisted procedure; got %d", len(procedures))
	}
	if procedures[0].Polarity != store.ProcedurePolarityPlaybook {
		t.Errorf("Polarity = %q; want %q", procedures[0].Polarity, store.ProcedurePolarityPlaybook)
	}
}

// TestCmdProcedureInduct_NoObservations_ZeroClusters covers the "nothing to
// induce" edge case: a project with no outcome-tagged bugfix observations
// reports zero clusters and never errors.
func TestCmdProcedureInduct_NoObservations_ZeroClusters(t *testing.T) {
	cfg := testConfig(t)

	withArgs(t, "omnia", "procedure-induct", "--project", "emptycliproj")
	stdout, stderr := captureOutput(t, func() { cmdProcedureInduct(cfg) })

	if stderr != "" {
		t.Errorf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "clusters_found:       0") {
		t.Errorf("expected clusters_found: 0, got: %s", stdout)
	}
}

// ─── procedure list/inspect/retire ────────────────────────────────────────────

func TestCmdProcedure_ListInspectRetire(t *testing.T) {
	cfg := testConfig(t)

	var syncID string
	{
		s, err := store.New(cfg)
		if err != nil {
			t.Fatalf("store.New: %v", err)
		}
		syncID, err = s.UpsertProcedure(store.Procedure{
			Project:           "cliproj3",
			Polarity:          store.ProcedurePolarityPlaybook,
			Trigger:           "cli inspect trigger",
			Steps:             []store.ProcedureStep{{Order: 1, Template: "do the cli thing"}},
			PostconditionKind: store.PostconditionTestsPass,
			Confidence:        0.5,
		})
		if err != nil {
			t.Fatalf("UpsertProcedure: %v", err)
		}
		s.Close()
	}

	withArgs(t, "omnia", "procedure", "list", "--project", "cliproj3")
	stdout, stderr := captureOutput(t, func() { cmdProcedure(cfg) })
	if stderr != "" {
		t.Errorf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(stdout, syncID) {
		t.Errorf("expected list output to contain sync_id %q, got: %s", syncID, stdout)
	}

	withArgs(t, "omnia", "procedure", "inspect", syncID)
	stdout, stderr = captureOutput(t, func() { cmdProcedure(cfg) })
	if stderr != "" {
		t.Errorf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "cli inspect trigger") {
		t.Errorf("expected inspect output to contain trigger text, got: %s", stdout)
	}

	withArgs(t, "omnia", "procedure", "retire", syncID)
	stdout, stderr = captureOutput(t, func() { cmdProcedure(cfg) })
	if stderr != "" {
		t.Errorf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "retired") {
		t.Errorf("expected retire confirmation, got: %s", stdout)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	got, err := s.GetProcedure(syncID)
	if err != nil {
		t.Fatalf("GetProcedure: %v", err)
	}
	if got.State != store.ProcedureStateRetired {
		t.Errorf("State = %q; want %q after retire", got.State, store.ProcedureStateRetired)
	}
}

func TestCmdProcedure_UnknownSubcommand(t *testing.T) {
	cfg := testConfig(t)
	oldExit := exitFunc
	var exitCode int
	exitFunc = func(code int) { exitCode = code }
	t.Cleanup(func() { exitFunc = oldExit })

	withArgs(t, "omnia", "procedure", "frobnicate")
	_, stderr := captureOutput(t, func() { cmdProcedure(cfg) })

	if exitCode != 1 {
		t.Errorf("exitCode = %d; want 1", exitCode)
	}
	if !strings.Contains(stderr, "unknown procedure subcommand") {
		t.Errorf("expected unknown-subcommand error, got: %s", stderr)
	}
}
