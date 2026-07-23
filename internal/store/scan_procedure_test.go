package store

import (
	"context"
	"testing"
)

// ─── Phase 6 — Offline Batch Induction (spec: induction, ScanProject seam) ──

// seedBugfixObs seeds a bugfix-family observation with an explicit outcome
// and error_signature (raw text — AddObservation normalizes it via
// NormalizeErrorSignature, exactly as mem_save's real callers do).
func seedBugfixObs(t *testing.T, s *Store, sessionID, project, title, errorText, outcome string) string {
	t.Helper()
	id, err := s.AddObservation(AddObservationParams{
		SessionID:      sessionID,
		Type:           "bugfix",
		Title:          title,
		Content:        "Fixed: " + title,
		Project:        project,
		Scope:          "project",
		ErrorSignature: errorText,
		Outcome:        outcome,
	})
	if err != nil {
		t.Fatalf("seedBugfixObs AddObservation(%q): %v", title, err)
	}
	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("seedBugfixObs GetObservation(%d): %v", id, err)
	}
	return obs.SyncID
}

// fakeInduceFunc returns an InduceFunc that always yields a single-step
// InducedProcedureResult, so a test can assert a cluster produced a
// procedure without depending on a real LLM CLI.
func fakeInduceFunc() InduceFunc {
	return func(_ context.Context, trigger, _ string) InducedProcedureResult {
		return InducedProcedureResult{
			Trigger:           trigger,
			Steps:             []ProcedureStep{{Order: 1, Template: "induced step"}},
			PostconditionKind: PostconditionTestsPass,
		}
	}
}

// TestInduceProject_ClustersBySignatureAndOutcome (task 6.1/6.2) verifies 3
// "worked" observations sharing one error_signature, plus 2 "did_not_work"
// observations sharing a DIFFERENT (but shared between themselves)
// error_signature, cluster into exactly 2 groups — one playbook, one
// anti_playbook — and --apply induces exactly 2 candidate procedures.
func TestInduceProject_ClustersBySignatureAndOutcome(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("ses-induce", "induceproj", "/tmp/induceproj"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	sharedWorkedErr := "panic: runtime error: index out of range [5] with length 3\n\tat handleRequest (/src/app/handler.go:42)"
	sharedFailErr := "TypeError: Cannot read properties of undefined (reading 'items')\n\tat processOrder (/src/services/orderService.ts:99:3)"

	seedBugfixObs(t, s, "ses-induce", "induceproj", "Fixed slice index panic #1", sharedWorkedErr, OutcomeWorked)
	seedBugfixObs(t, s, "ses-induce", "induceproj", "Fixed slice index panic #2", sharedWorkedErr, OutcomeWorked)
	seedBugfixObs(t, s, "ses-induce", "induceproj", "Fixed slice index panic #3", sharedWorkedErr, OutcomeWorked)

	seedBugfixObs(t, s, "ses-induce", "induceproj", "Attempted checkout guard #1", sharedFailErr, OutcomeDidNotWork)
	seedBugfixObs(t, s, "ses-induce", "induceproj", "Attempted checkout guard #2", sharedFailErr, OutcomeDidNotWork)

	result, err := s.InduceProject(InduceOptions{
		Project: "induceproj",
		Apply:   true,
		Inducer: fakeInduceFunc(),
	})
	if err != nil {
		t.Fatalf("InduceProject: unexpected error: %v", err)
	}

	if result.ObservationsScanned != 5 {
		t.Errorf("ObservationsScanned = %d; want 5", result.ObservationsScanned)
	}
	if result.ClustersFound != 2 {
		t.Errorf("ClustersFound = %d; want 2", result.ClustersFound)
	}
	if result.ProceduresInduced != 2 {
		t.Errorf("ProceduresInduced = %d; want 2", result.ProceduresInduced)
	}
	if result.DryRun {
		t.Error("DryRun should be false when Apply=true")
	}

	procedures, err := s.ListProcedures(ListProceduresOptions{Project: "induceproj", Limit: 10})
	if err != nil {
		t.Fatalf("ListProcedures: %v", err)
	}
	if len(procedures) != 2 {
		t.Fatalf("expected 2 persisted procedures; got %d", len(procedures))
	}

	var sawPlaybook, sawAntiPlaybook bool
	for _, p := range procedures {
		switch p.Polarity {
		case ProcedurePolarityPlaybook:
			sawPlaybook = true
			if len(p.SourceObsSyncIDs) != 3 {
				t.Errorf("playbook cluster: expected 3 source obs; got %d", len(p.SourceObsSyncIDs))
			}
		case ProcedurePolarityAntiPlaybook:
			sawAntiPlaybook = true
			if len(p.SourceObsSyncIDs) != 2 {
				t.Errorf("anti_playbook cluster: expected 2 source obs; got %d", len(p.SourceObsSyncIDs))
			}
		}
		if p.State != ProcedureStateCandidate {
			t.Errorf("induced procedure state = %q; want %q", p.State, ProcedureStateCandidate)
		}
	}
	if !sawPlaybook || !sawAntiPlaybook {
		t.Errorf("expected one playbook and one anti_playbook; sawPlaybook=%v sawAntiPlaybook=%v", sawPlaybook, sawAntiPlaybook)
	}
}

// TestInduceProject_ApplyRequiresInducer verifies --apply without an
// Inducer configured fails loudly rather than silently no-op'ing.
func TestInduceProject_ApplyRequiresInducer(t *testing.T) {
	s := newTestStore(t)
	_, err := s.InduceProject(InduceOptions{Project: "induceproj", Apply: true})
	if err == nil {
		t.Fatal("InduceProject: expected error when Apply=true with nil Inducer")
	}
}

// TestInduceProject_DryRunReportsCountsOnly (task 6.3) verifies that without
// --apply, InduceProject reports cluster counts but writes zero procedures
// rows, and never calls Inducer.
func TestInduceProject_DryRunReportsCountsOnly(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("ses-dry", "dryproj", "/tmp/dryproj"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	sharedErr := "panic: nil pointer dereference\n\tat run (/src/main.go:10)"
	seedBugfixObs(t, s, "ses-dry", "dryproj", "Fixed nil pointer #1", sharedErr, OutcomeWorked)
	seedBugfixObs(t, s, "ses-dry", "dryproj", "Fixed nil pointer #2", sharedErr, OutcomeWorked)

	inducerCalled := false
	result, err := s.InduceProject(InduceOptions{
		Project: "dryproj",
		Apply:   false,
		Inducer: func(_ context.Context, _, _ string) InducedProcedureResult {
			inducerCalled = true
			return InducedProcedureResult{}
		},
	})
	if err != nil {
		t.Fatalf("InduceProject dry-run: unexpected error: %v", err)
	}
	if !result.DryRun {
		t.Error("expected DryRun=true")
	}
	if result.ObservationsScanned != 2 {
		t.Errorf("ObservationsScanned = %d; want 2", result.ObservationsScanned)
	}
	if result.ClustersFound != 1 {
		t.Errorf("ClustersFound = %d; want 1", result.ClustersFound)
	}
	if result.ProceduresInduced != 0 {
		t.Errorf("ProceduresInduced = %d; want 0 in dry-run", result.ProceduresInduced)
	}
	if inducerCalled {
		t.Error("dry-run must NEVER call Inducer")
	}

	procedures, err := s.ListProcedures(ListProceduresOptions{Project: "dryproj", Limit: 10})
	if err != nil {
		t.Fatalf("ListProcedures: %v", err)
	}
	if len(procedures) != 0 {
		t.Fatalf("dry-run must write zero procedures rows; got %d", len(procedures))
	}
}

// TestInduceProject_SkipsClusterWhenInducerReturnsNoSteps verifies a cluster
// whose Inducer call yields zero steps (e.g. SafeInduce's own degraded
// fallback on totally empty trajectories) is counted as an error rather
// than silently persisted as an empty, useless procedure.
func TestInduceProject_SkipsClusterWhenInducerReturnsNoSteps(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("ses-empty", "emptyproj", "/tmp/emptyproj"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	seedBugfixObs(t, s, "ses-empty", "emptyproj", "Fixed something", "panic: boom\n\tat x (/a.go:1)", OutcomeWorked)

	result, err := s.InduceProject(InduceOptions{
		Project: "emptyproj",
		Apply:   true,
		Inducer: func(_ context.Context, _, _ string) InducedProcedureResult {
			return InducedProcedureResult{} // zero steps
		},
	})
	if err != nil {
		t.Fatalf("InduceProject: unexpected error: %v", err)
	}
	if result.ProceduresInduced != 0 {
		t.Errorf("ProceduresInduced = %d; want 0", result.ProceduresInduced)
	}
	if result.Errors != 1 {
		t.Errorf("Errors = %d; want 1", result.Errors)
	}
}
